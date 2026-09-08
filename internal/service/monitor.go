package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

type ThreadProvider interface {
	Overview(context.Context) (*model.Overview, error)
	Devices(context.Context) (*model.DeviceInventory, error)
	Topology(context.Context) (*model.Topology, error)
	ScanNetworks(context.Context) (*model.NetworkScan, error)
	Capabilities(context.Context) (*model.Capabilities, error)
}

// FastDeviceSource is implemented by providers whose device inventory is cheap to
// read. Polling REST's collections quickly is pointless — they only change after a
// discovery sweep — but a live source tracks the mesh second by second.
type FastDeviceSource interface {
	FastDevices() bool
}

// DeviceDiscoverer is the optional half of a provider: OTBR only refreshes its
// device and diagnostics collections when asked, so a provider that can ask keeps
// them current. A provider without it just reads whatever the collections hold.
type DeviceDiscoverer interface {
	RefreshMesh(ctx context.Context, routers []string) error
}

type Monitor struct {
	provider     ThreadProvider
	pollEvery    time.Duration
	logger       *slog.Logger
	mu           sync.RWMutex
	overview     model.Overview
	devices      model.DeviceInventory
	topology     model.Topology
	capabilities model.Capabilities
	lastOnline   bool

	discoverEvery      time.Duration
	discoveryOff       bool
	discoveryUnhealthy bool
}

// MonitorOption tunes a Monitor at construction.
type MonitorOption func(*Monitor)

// WithDiscoveryInterval sets how often the monitor asks the provider to
// rediscover the mesh. Zero disables it.
func WithDiscoveryInterval(d time.Duration) MonitorOption {
	return func(m *Monitor) { m.discoverEvery = d }
}

func NewMonitor(provider ThreadProvider, pollEvery time.Duration, logger *slog.Logger, opts ...MonitorOption) *Monitor {
	if logger == nil {
		logger = slog.Default()
	}
	monitor := &Monitor{
		provider: provider, pollEvery: pollEvery, logger: logger,
		overview: model.Overview{Status: "connecting", APIHealth: "unknown", Stale: true},
		devices:  model.DeviceInventory{Status: "connecting", Items: []model.Device{}, Source: "detecting"},
		topology: model.Topology{Status: "connecting", Nodes: []model.TopologyNode{}, Links: []model.TopologyLink{}, Source: "detecting"},
	}
	for _, opt := range opts {
		opt(monitor)
	}
	return monitor
}

// knownRouters lists the routers whose child tables are worth querying: the local
// border router (always a router, and known before any inventory arrives) plus any
// router seen in the last inventory. Children are reachable only through a parent's
// childTable, so missing a router here means its children float unattached.
func (m *Monitor) knownRouters() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := map[string]bool{}
	routers := make([]string, 0, len(m.devices.Items)+1)
	add := func(ext string) {
		if ext == "" || seen[ext] {
			return
		}
		seen[ext] = true
		routers = append(routers, ext)
	}
	add(m.overview.ExtendedAddress)
	for _, device := range m.devices.Items {
		switch strings.ToLower(device.Role) {
		case "router", "leader":
			add(device.ExtendedAddress)
		}
	}
	return routers
}

// refreshDeviceCollection asks OTBR to rediscover the mesh. It is fire-and-forget:
// the sweep runs asynchronously and the next device poll reads the result.
func (m *Monitor) refreshDeviceCollection(ctx context.Context) {
	if m.discoveryOff {
		return
	}
	discoverer, ok := m.provider.(DeviceDiscoverer)
	if !ok {
		m.discoveryOff = true
		return
	}
	if err := discoverer.RefreshMesh(ctx, m.knownRouters()); err != nil {
		var unsupported interface{ DiscoveryUnsupported() bool }
		if errors.As(err, &unsupported) && unsupported.DiscoveryUnsupported() {
			// Nothing will change on a retry, so stop asking.
			m.discoveryOff = true
			m.logger.Info("OTBR device discovery unsupported; device list reflects whatever OTBR already cached")
			return
		}
		if !m.discoveryUnhealthy {
			m.discoveryUnhealthy = true
			m.logger.Warn("OTBR device discovery failed", "error", err)
		}
		return
	}
	m.discoveryUnhealthy = false
}

func (m *Monitor) Run(ctx context.Context) {
	discoverEvery := m.discoverEvery
	if discoverEvery <= 0 {
		m.discoveryOff = true
		discoverEvery = time.Hour // never observed; the ticker is drained but ignored
	}
	// Overview first: it supplies the local border router's extended address, and a
	// sweep without it cannot ask for the childTable that attaches this router's
	// children. Then kick a sweep — OTBR's collections may be stale by days, and the
	// result lands in time for the device ticker rather than this first read.
	m.refreshOverview(ctx)
	m.refreshDeviceCollection(ctx)
	m.refreshDevices(ctx)
	m.refreshTopology(ctx)
	m.refreshCapabilities(ctx)
	overviewTicker := time.NewTicker(m.pollEvery)
	deviceEvery := 3 * m.pollEvery
	if deviceEvery < 15*time.Second {
		deviceEvery = 15 * time.Second
	}
	// With a live source the inventory is as cheap as the overview, and polling it
	// slowly is what made "last seen" lag by up to a full interval.
	if fast, ok := m.provider.(FastDeviceSource); ok && fast.FastDevices() {
		deviceEvery = m.pollEvery
	}
	deviceTicker := time.NewTicker(deviceEvery)
	capabilityTicker := time.NewTicker(time.Minute)
	defer overviewTicker.Stop()
	defer deviceTicker.Stop()
	defer capabilityTicker.Stop()
	discoveryTicker := time.NewTicker(discoverEvery)
	defer discoveryTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-overviewTicker.C:
			m.refreshOverview(ctx)
		case <-deviceTicker.C:
			m.refreshDevices(ctx)
			m.refreshTopology(ctx)
		case <-capabilityTicker.C:
			m.refreshCapabilities(ctx)
		case <-discoveryTicker.C:
			m.refreshDeviceCollection(ctx)
		}
	}
}

func (m *Monitor) DeviceSnapshot() model.DeviceInventory {
	m.mu.RLock()
	defer m.mu.RUnlock()
	copy := m.devices
	copy.Items = append([]model.Device(nil), m.devices.Items...)
	for i := range copy.Items {
		copy.Items[i].IPv6Addresses = append([]string(nil), copy.Items[i].IPv6Addresses...)
	}
	return copy
}

func (m *Monitor) TopologySnapshot() model.Topology {
	m.mu.RLock()
	defer m.mu.RUnlock()
	copy := m.topology
	copy.Nodes = append([]model.TopologyNode(nil), m.topology.Nodes...)
	copy.Links = append([]model.TopologyLink(nil), m.topology.Links...)
	return copy
}

func (m *Monitor) ScanNetworks(ctx context.Context) (*model.NetworkScan, error) {
	return m.provider.ScanNetworks(ctx)
}

func (m *Monitor) Snapshot() model.Overview {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.overview
}

func (m *Monitor) CapabilitySnapshot() model.Capabilities {
	m.mu.RLock()
	defer m.mu.RUnlock()
	copy := m.capabilities
	copy.Items = append([]model.Capability(nil), m.capabilities.Items...)
	return copy
}

func (m *Monitor) RefreshNow(ctx context.Context) {
	m.refreshOverview(ctx)
}

func (m *Monitor) refreshOverview(ctx context.Context) {
	attempt := time.Now().UTC()
	overview, err := m.provider.Overview(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		wasOnline := m.lastOnline
		m.overview.Status = "offline"
		m.overview.APIHealth = "unavailable"
		m.overview.Stale = m.overview.HasData
		m.overview.LastAttempt = attempt
		m.overview.Error = err.Error()
		m.lastOnline = false
		if wasOnline {
			m.logger.Warn("OTBR connection lost", "error", err)
		}
		return
	}
	now := time.Now().UTC()
	overview.Status = "online"
	overview.Stale = false
	overview.HasData = true
	overview.APIHealth = "healthy"
	overview.LastAttempt = attempt
	overview.LastSuccessfulRefresh = &now
	overview.Error = ""
	if !m.lastOnline {
		m.logger.Info("OTBR connection established")
	}
	m.lastOnline = true
	m.overview = *overview
}

func (m *Monitor) refreshCapabilities(ctx context.Context) {
	capabilities, err := m.provider.Capabilities(ctx)
	if err != nil {
		m.logger.Warn("OTBR capability detection failed", "error", err)
		return
	}
	m.mu.Lock()
	previous := m.capabilities
	m.capabilities = *capabilities
	m.mu.Unlock()
	if len(previous.Items) > 0 && capabilitiesChanged(previous, *capabilities) {
		m.logger.Info("OTBR capabilities changed")
	}
}

func (m *Monitor) refreshDevices(ctx context.Context) {
	attempt := time.Now().UTC()
	inventory, err := m.provider.Devices(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.devices.Status = "unavailable"
		m.devices.Stale = len(m.devices.Items) > 0
		m.devices.LastAttempt = attempt
		m.devices.Error = err.Error()
		m.logger.Warn("OTBR device refresh failed", "error", err)
		return
	}
	mergeBorderRouter(inventory, m.overview)
	now := time.Now().UTC()
	inventory.LastAttempt = attempt
	inventory.LastSuccessfulRefresh = &now
	inventory.Stale = false
	inventory.NetworkMismatch = devicesMismatchNetwork(inventory, m.overview)
	if inventory.Status == "unsupported" {
		inventory.Status = "partial"
	}
	m.devices = *inventory
}

func (m *Monitor) refreshTopology(ctx context.Context) {
	attempt := time.Now().UTC()
	topology, err := m.provider.Topology(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.topology.Status = "unavailable"
		m.topology.Stale = len(m.topology.Nodes) > 0
		m.topology.LastAttempt = attempt
		m.topology.Error = err.Error()
		m.logger.Warn("OTBR topology refresh failed", "error", err)
		return
	}
	for i := range topology.Nodes {
		node := &topology.Nodes[i]
		if node.ExtendedAddress != "" && node.ExtendedAddress == m.overview.ExtendedAddress {
			node.IsBorderRouter = true
		}
		for _, device := range m.devices.Items {
			if node.ExtendedAddress == "" || (device.ID != node.ExtendedAddress && device.ExtendedAddress != node.ExtendedAddress) {
				continue
			}
			node.Name = device.Name
			node.IsBorderRouter = node.IsBorderRouter || device.IsBorderRouter
			break
		}
	}
	now := time.Now().UTC()
	topology.LastAttempt = attempt
	topology.LastSuccessfulRefresh = &now
	topology.Stale = false
	topology.Error = ""
	m.topology = *topology
}

// devicesMismatchNetwork reports whether the device collection appears to
// belong to a different network than the one the border router is currently on.
// Some firmware serves a stale device cache after a network change; the
// authoritative router count (from /node) is compared against the routers in
// the inventory. A mismatch means the list should not be trusted as "current".
func devicesMismatchNetwork(inventory *model.DeviceInventory, overview model.Overview) bool {
	if !overview.HasData || overview.RouterCount == nil {
		return false
	}
	routers := 0
	for _, device := range inventory.Items {
		switch device.Role {
		case "router", "leader", "border-router":
			routers++
		}
	}
	return routers != *overview.RouterCount
}

func mergeBorderRouter(inventory *model.DeviceInventory, overview model.Overview) {
	if !overview.HasData || overview.ExtendedAddress == "" {
		return
	}
	for i := range inventory.Items {
		if inventory.Items[i].ID == overview.ExtendedAddress || inventory.Items[i].ExtendedAddress == overview.ExtendedAddress {
			inventory.Items[i].IsBorderRouter = true
			if inventory.Items[i].RLOC16 == "" {
				inventory.Items[i].RLOC16 = overview.RLOC16
			}
			if inventory.Items[i].RouterID == nil {
				inventory.Items[i].RouterID = overview.RouterID
			}
			// Already listed by a live source, which knows more about the mesh than
			// the overview does — but the overview is the only place the local
			// router's own OMR address comes from, so fill just that.
			// A router cannot report its own age, so the local one has no "last heard"
			// from the mesh. The last successful poll is the honest equivalent.
			if inventory.Items[i].LastSeen == nil {
				inventory.Items[i].LastSeen = overview.LastSuccessfulRefresh
			}
			if inventory.Items[i].OMRIPv6Address == "" && overview.OMRIPv6Address != "" {
				inventory.Items[i].OMRIPv6Address = overview.OMRIPv6Address
				if len(inventory.Items[i].IPv6Addresses) == 0 {
					inventory.Items[i].IPv6Addresses = []string{overview.OMRIPv6Address}
				}
			}
			return
		}
	}
	addresses := []string{}
	if overview.OMRIPv6Address != "" {
		addresses = append(addresses, overview.OMRIPv6Address)
	}
	inventory.Items = append([]model.Device{{
		ID: overview.ExtendedAddress, Name: "Border Router", Role: overview.Role,
		RLOC16: overview.RLOC16, RouterID: overview.RouterID,
		ExtendedAddress: overview.ExtendedAddress, IPv6Addresses: addresses,
		OMRIPv6Address: overview.OMRIPv6Address, LastSeen: overview.LastSuccessfulRefresh,
		IsBorderRouter: true,
	}}, inventory.Items...)
	if inventory.CollectionSupported {
		// Append rather than replace: the inventory may have come from the live mesh
		// reader, and relabelling it "OTBR device collection" would be a lie.
		inventory.Source += " + node overview"
	}
}

func capabilitiesChanged(a, b model.Capabilities) bool {
	if len(a.Items) != len(b.Items) {
		return true
	}
	for i := range a.Items {
		if a.Items[i].Endpoint != b.Items[i].Endpoint || a.Items[i].Supported != b.Items[i].Supported {
			return true
		}
	}
	return false
}
