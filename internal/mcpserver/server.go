// Package mcpserver exposes the mesh to language-model assistants over the
// Model Context Protocol. It is a read-only view plus a reachability probe,
// served on the dashboard's own listener at /mcp, so any MCP client on the LAN
// can point at one URL — there is nothing to install per machine.
//
// The tools deliberately stop short of the network writes (form, join, leave,
// enable/disable, restore). Those go through the UI's confirm modal, and a tool
// call is a single click by another name. Credentials are never exposed here.
//
// The tool results are shaped for reasoning rather than mirroring the REST
// payloads: names are resolved from the user's labels, parents are named rather
// than given as RLOC16s, ages are seconds rather than timestamps, and the
// topology is nested by router. The join key throughout is the extended
// address, which is what every other part of this app keys devices on.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// Snapshots is the subset of the monitor the tools read. It is satisfied by
// service.Monitor and mirrors api.Snapshotter without importing it.
type Snapshots interface {
	Snapshot() model.Overview
	DeviceSnapshot() model.DeviceInventory
	TopologySnapshot() model.Topology
	ScanNetworks(context.Context) (*model.NetworkScan, error)
}

// Labels supplies user-assigned device names keyed by lower-case extended address.
type Labels interface {
	Snapshot() map[string]string
}

// Pinger tests reachability from the border router; optional.
type Pinger interface {
	Ping(ctx context.Context, address string, count int) (*model.PingResult, error)
}

// HistoryProvider reads OpenThread's event recorder; optional.
type HistoryProvider interface {
	History(ctx context.Context) (*model.History, error)
}

// quietAfter is how long a device may go unheard before get_network counts it
// as quiet. Sleepy children poll their parent well inside this on any sane
// configuration, so a device past it is worth a look.
const quietAfter = 10 * time.Minute

// pingTimeout must accommodate a sleepy device, which answers only when it next
// wakes; the REST route uses the same figure.
const pingTimeout = 60 * time.Second

type server struct {
	data    Snapshots
	labels  Labels
	pinger  Pinger
	history HistoryProvider
	logger  *slog.Logger
}

// Handler returns the Streamable HTTP MCP endpoint. provider is the OTBR
// client; the ping and history tools are registered only when it implements
// them, which mirrors how the REST routes appear.
func Handler(data Snapshots, labels Labels, provider any, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	s := &server{data: data, labels: labels, logger: logger}
	s.pinger, _ = provider.(Pinger)
	s.history, _ = provider.(HistoryProvider)

	// The SDK logs every session at INFO, and in stateless mode every request is
	// a session, so it only gets to speak up about problems.
	quiet := slog.New(&minLevelHandler{Handler: logger.Handler(), min: slog.LevelWarn})
	impl := &mcp.Implementation{Name: "otbr-insight", Title: "OTBR Insight", Version: buildVersion()}
	mcpServer := mcp.NewServer(impl, &mcp.ServerOptions{Instructions: instructions, Logger: quiet})
	s.register(mcpServer)

	// Stateless with plain JSON responses: every tool call is one POST and one
	// reply, so there is no session to time out and no SSE stream to hold open
	// past the HTTP server's write timeout. Localhost protection is off because
	// the documented reverse-proxy setup forwards the LAN Host header to a
	// loopback upstream, which that check would refuse; the app's trust model is
	// already "anyone on the LAN", stated in the README.
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, Logger: quiet, DisableLocalhostProtection: true,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A ping against a sleepy device outlives the server's write timeout, so
		// extend the deadline for this request alone rather than raising it globally.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(pingTimeout + 30*time.Second))
		handler.ServeHTTP(w, r)
	})
}

const instructions = `OTBR Insight monitors a Thread mesh through its OpenThread Border Router (OTBR).
Start with get_network for orientation; it includes device counts so you can decide whether list_devices is worth calling.
Devices are identified by name (the user's label when one is set) and joined by extended address, a 16-hex-digit hardware identifier. RLOC16s change when a device roams; extended addresses do not.
Signal: RSSI below about -85 dBm is weak; link quality is 0-3 where 3 is best. Error rates are fractions over roughly the last 64 transmissions, so a high rate with a strong RSSI points at interference rather than range.
Sleepy end devices are battery powered and only wake to poll their parent; a ping to one can take several seconds and "last seen" of a minute or two is normal.
These tools are read-only apart from ping_device, which makes the border router transmit but changes nothing. Network changes (form, join, leave) are not available here by design.`

func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func (s *server) register(m *mcp.Server) {
	readOnly := func(title string) *mcp.ToolAnnotations {
		return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, OpenWorldHint: boolPtr(false)}
	}
	mcp.AddTool(m, &mcp.Tool{
		Name:        "get_network",
		Description: "Summarise the Thread network: name, channel, PAN ID, the border router's role and state, leader and partition, and device counts including any device not heard from recently. Call this first. Credentials are never included.",
		Annotations: readOnly("Network summary"),
	}, s.getNetwork)
	mcp.AddTool(m, &mcp.Tool{
		Name:        "list_devices",
		Description: "List the devices in the mesh with their role, parent, how many seconds ago each was heard, signal (RSSI, link quality) and error rates, and addresses. Filter by role or by a name/address substring to keep the result small.",
		Annotations: readOnly("List devices"),
	}, s.listDevices)
	mcp.AddTool(m, &mcp.Tool{
		Name:        "get_topology",
		Description: "The mesh structure: each router with the children attached to it, plus router-to-router links with link quality, path cost and RSSI. Children whose parent could not be resolved are listed separately.",
		Annotations: readOnly("Mesh topology"),
	}, s.getTopology)
	mcp.AddTool(m, &mcp.Tool{
		Name:        "scan_networks",
		Description: "Actively scan for other Thread networks on the air (name, PAN ID, channel, signal). Takes several seconds. Useful for checking channel overlap or whether a neighbour's network is present.",
		Annotations: readOnly("Scan for networks"),
	}, s.scanNetworks)
	if s.history != nil {
		mcp.AddTool(m, &mcp.Tool{
			Name:        "get_history",
			Description: "OpenThread's own event log, newest first: role and partition changes for the border router, and devices attaching or detaching with the signal at the time. This is the only source for when something happened. Optionally filter to one device and cap the number of entries.",
			Annotations: readOnly("Event history"),
		}, s.getHistory)
	}
	if s.pinger != nil {
		mcp.AddTool(m, &mcp.Tool{
			Name:        "ping_device",
			Description: "Test whether a device answers, from the border router itself. Accepts a device name, extended address, RLOC16 or IPv6 address. Can take up to a minute against a sleepy device. This transmits on the radio but changes nothing.",
			Annotations: &mcp.ToolAnnotations{Title: "Ping device", ReadOnlyHint: false, DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(false)},
		}, s.pingDevice)
	}
}

// --- get_network -----------------------------------------------------------

type borderRouterSummary struct {
	ExtendedAddress   string `json:"extendedAddress,omitempty"`
	RLOC16            string `json:"rloc16,omitempty"`
	RouterID          *int   `json:"routerId,omitempty"`
	MeshLocalAddress  string `json:"meshLocalAddress,omitempty"`
	OMRAddress        string `json:"omrAddress,omitempty"`
	OpenThreadVersion string `json:"openThreadVersion,omitempty"`
	RCPVersion        string `json:"rcpVersion,omitempty"`
	TxPower           string `json:"txPower,omitempty"`
}

type deviceCounts struct {
	Total          int      `json:"total"`
	Routers        int      `json:"routers"`
	EndDevices     int      `json:"endDevices"`
	Unnamed        int      `json:"unnamed"`
	Quiet          int      `json:"quiet"`
	QuietDevices   []string `json:"quietDevices,omitempty"`
	InventoryStale bool     `json:"inventoryStale"`
	Source         string   `json:"source,omitempty"`
}

type networkSummary struct {
	Status                string              `json:"status"`
	Stale                 bool                `json:"stale"`
	NetworkName           string              `json:"networkName,omitempty"`
	Channel               string              `json:"channel,omitempty"`
	PANID                 string              `json:"panId,omitempty"`
	ExtendedPANID         string              `json:"extendedPanId,omitempty"`
	MeshLocalPrefix       string              `json:"meshLocalPrefix,omitempty"`
	Role                  string              `json:"role,omitempty"`
	State                 string              `json:"state,omitempty"`
	PartitionID           *uint32             `json:"partitionId,omitempty"`
	LeaderRouterID        *int                `json:"leaderRouterId,omitempty"`
	RouterCount           *int                `json:"routerCount,omitempty"`
	BorderRouter          borderRouterSummary `json:"borderRouter"`
	Devices               deviceCounts        `json:"devices"`
	LastSuccessfulRefresh *time.Time          `json:"lastSuccessfulRefresh,omitempty"`
	Error                 string              `json:"error,omitempty"`
	Note                  string              `json:"note,omitempty"`
}

func (s *server) getNetwork(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, networkSummary, error) {
	overview := s.data.Snapshot()
	inventory := s.data.DeviceSnapshot()
	labels := s.labels.Snapshot()
	now := time.Now()

	counts := deviceCounts{Total: len(inventory.Items), InventoryStale: inventory.Stale, Source: inventory.Source}
	for _, device := range inventory.Items {
		if isRouter(device) {
			counts.Routers++
		} else {
			counts.EndDevices++
		}
		if labelFor(labels, device.ExtendedAddress, device.ID) == "" && device.Name == "" {
			counts.Unnamed++
		}
		if device.LastSeen != nil && now.Sub(*device.LastSeen) > quietAfter {
			counts.Quiet++
			counts.QuietDevices = append(counts.QuietDevices, displayName(labels, device))
		}
	}

	summary := networkSummary{
		Status: overview.Status, Stale: overview.Stale, NetworkName: overview.NetworkName,
		Channel: overview.RCPChannel, PANID: overview.PANID, ExtendedPANID: overview.ExtendedPANID,
		MeshLocalPrefix: overview.MeshLocalPrefix, Role: overview.Role, State: overview.State,
		PartitionID: overview.PartitionID, LeaderRouterID: overview.LeaderRouterID, RouterCount: overview.RouterCount,
		BorderRouter: borderRouterSummary{
			ExtendedAddress: overview.ExtendedAddress, RLOC16: overview.RLOC16, RouterID: overview.RouterID,
			MeshLocalAddress: overview.MeshLocalAddress, OMRAddress: overview.OMRIPv6Address,
			OpenThreadVersion: overview.OpenThreadVersion, RCPVersion: overview.RCPVersion, TxPower: overview.RCPTxPower,
		},
		Devices: counts, LastSuccessfulRefresh: overview.LastSuccessfulRefresh, Error: overview.Error,
	}
	switch {
	case overview.Status == "offline" && !overview.HasData:
		summary.Note = "OTBR has not answered since start-up; the other fields are unknown."
	case overview.Status == "online" && strings.EqualFold(overview.State, "disabled"):
		summary.Note = "OTBR is reachable but the Thread interface is disabled; the mesh is down."
	case overview.Stale:
		summary.Note = "OTBR is not answering; values are the last ones received."
	}
	return nil, summary, nil
}

// --- list_devices ----------------------------------------------------------

type listDevicesInput struct {
	Role  string `json:"role,omitempty" jsonschema:"Keep only this kind of device: router (routers, the leader and the border router) or end-device (children, including sleepy ones)."`
	Query string `json:"query,omitempty" jsonschema:"Case-insensitive substring matched against the name, extended address and RLOC16."`
}

type deviceSummary struct {
	Name             string   `json:"name"`
	ExtendedAddress  string   `json:"extendedAddress,omitempty"`
	Role             string   `json:"role,omitempty"`
	IsBorderRouter   bool     `json:"isBorderRouter,omitempty"`
	RLOC16           string   `json:"rloc16,omitempty"`
	RouterID         *int     `json:"routerId,omitempty"`
	Parent           string   `json:"parent,omitempty"`
	LastSeenSeconds  *int64   `json:"lastSeenSeconds,omitempty"`
	RSSI             *int     `json:"rssi,omitempty"`
	LinkQuality      *int     `json:"linkQuality,omitempty"`
	LinkMargin       *int     `json:"linkMargin,omitempty"`
	FrameErrorRate   *float64 `json:"frameErrorRate,omitempty"`
	MessageErrorRate *float64 `json:"messageErrorRate,omitempty"`
	MeshLocalAddress string   `json:"meshLocalAddress,omitempty"`
	OMRAddress       string   `json:"omrAddress,omitempty"`
	ThreadVersion    string   `json:"threadVersion,omitempty"`
}

type deviceList struct {
	Status  string          `json:"status"`
	Stale   bool            `json:"stale"`
	Source  string          `json:"source,omitempty"`
	Count   int             `json:"count"`
	Devices []deviceSummary `json:"devices"`
	Error   string          `json:"error,omitempty"`
}

func (s *server) listDevices(_ context.Context, _ *mcp.CallToolRequest, in listDevicesInput) (*mcp.CallToolResult, deviceList, error) {
	role := strings.ToLower(strings.TrimSpace(in.Role))
	switch role {
	case "", "router", "end-device":
	default:
		return nil, deviceList{}, fmt.Errorf("role must be \"router\" or \"end-device\", got %q", in.Role)
	}
	query := strings.ToLower(strings.TrimSpace(in.Query))

	overview := s.data.Snapshot()
	inventory := s.data.DeviceSnapshot()
	labels := s.labels.Snapshot()
	meshLocal := meshLocalPrefix(overview.MeshLocalPrefix)
	now := time.Now()

	out := deviceList{Status: inventory.Status, Stale: inventory.Stale, Source: inventory.Source, Error: inventory.Error, Devices: []deviceSummary{}}
	for _, device := range inventory.Items {
		if role == "router" && !isRouter(device) || role == "end-device" && isRouter(device) {
			continue
		}
		name := displayName(labels, device)
		if query != "" && !matchesQuery(query, name, device.Name, device.ExtendedAddress, device.RLOC16) {
			continue
		}
		summary := deviceSummary{
			Name: name, ExtendedAddress: device.ExtendedAddress, Role: device.Role, IsBorderRouter: device.IsBorderRouter,
			RLOC16: device.RLOC16, RouterID: device.RouterID, RSSI: device.RSSI, LinkQuality: device.LinkQuality,
			LinkMargin: device.LinkMargin, FrameErrorRate: device.FrameErrorRate, MessageErrorRate: device.MessageErrorRate,
			OMRAddress: device.OMRIPv6Address, ThreadVersion: device.ThreadVersion,
			MeshLocalAddress: meshLocalAddress(device, meshLocal, overview),
		}
		if device.LastSeen != nil {
			age := int64(now.Sub(*device.LastSeen).Seconds())
			if age < 0 {
				age = 0
			}
			summary.LastSeenSeconds = &age
		}
		if !isRouter(device) {
			if parent := findParent(device, inventory.Items); parent != nil {
				summary.Parent = displayName(labels, *parent)
			}
		}
		out.Devices = append(out.Devices, summary)
	}
	out.Count = len(out.Devices)
	return nil, out, nil
}

// --- get_topology ----------------------------------------------------------

type topologyChild struct {
	Name            string `json:"name"`
	ExtendedAddress string `json:"extendedAddress,omitempty"`
	RLOC16          string `json:"rloc16,omitempty"`
	LinkQuality     *int   `json:"linkQuality,omitempty"`
	Sleepy          *bool  `json:"sleepy,omitempty"`
	Parent          string `json:"parent,omitempty"`
}

type routerCluster struct {
	Name            string          `json:"name"`
	ExtendedAddress string          `json:"extendedAddress,omitempty"`
	Role            string          `json:"role,omitempty"`
	RLOC16          string          `json:"rloc16,omitempty"`
	RouterID        *int            `json:"routerId,omitempty"`
	IsBorderRouter  bool            `json:"isBorderRouter,omitempty"`
	IsLeader        bool            `json:"isLeader,omitempty"`
	Children        []topologyChild `json:"children"`
}

type routerLink struct {
	From             string   `json:"from"`
	To               string   `json:"to"`
	LinkQuality      *int     `json:"linkQuality,omitempty"`
	LinkQualityIn    *int     `json:"linkQualityIn,omitempty"`
	LinkQualityOut   *int     `json:"linkQualityOut,omitempty"`
	RouteCost        *int     `json:"routeCost,omitempty"`
	AverageRSSI      *int     `json:"averageRssi,omitempty"`
	LastRSSI         *int     `json:"lastRssi,omitempty"`
	FrameErrorRate   *float64 `json:"frameErrorRate,omitempty"`
	MessageErrorRate *float64 `json:"messageErrorRate,omitempty"`
}

type topologyView struct {
	Status      string          `json:"status"`
	Stale       bool            `json:"stale"`
	Source      string          `json:"source,omitempty"`
	Routers     []routerCluster `json:"routers"`
	RouterLinks []routerLink    `json:"routerLinks"`
	Unattached  []topologyChild `json:"unattached"`
	Error       string          `json:"error,omitempty"`
}

func (s *server) getTopology(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, topologyView, error) {
	topology := s.data.TopologySnapshot()
	overview := s.data.Snapshot()
	labels := s.labels.Snapshot()

	out := topologyView{Status: topology.Status, Stale: topology.Stale, Source: topology.Source, Error: topology.Error,
		Routers: []routerCluster{}, RouterLinks: []routerLink{}, Unattached: []topologyChild{}}

	nodeName := map[string]string{}
	clusters := map[string]*routerCluster{}
	var routerOrder []string
	for _, node := range topology.Nodes {
		name := nodeDisplayName(labels, node)
		nodeName[node.ID] = name
		if !isRouterRole(node.Role) && !node.IsBorderRouter {
			continue
		}
		leader := strings.EqualFold(node.Role, "leader") ||
			(node.RouterID != nil && overview.LeaderRouterID != nil && *node.RouterID == *overview.LeaderRouterID)
		clusters[node.ID] = &routerCluster{
			Name: name, ExtendedAddress: node.ExtendedAddress, Role: node.Role, RLOC16: node.RLOC16, RouterID: node.RouterID,
			IsBorderRouter: node.IsBorderRouter, IsLeader: leader, Children: []topologyChild{},
		}
		routerOrder = append(routerOrder, node.ID)
	}
	for _, node := range topology.Nodes {
		if _, isRouter := clusters[node.ID]; isRouter {
			continue
		}
		child := topologyChild{Name: nodeName[node.ID], ExtendedAddress: node.ExtendedAddress, RLOC16: node.RLOC16, LinkQuality: node.LinkQuality}
		if node.RxOnWhenIdle != nil {
			sleepy := !*node.RxOnWhenIdle
			child.Sleepy = &sleepy
		}
		if parent, ok := clusters[node.ParentID]; ok {
			parent.Children = append(parent.Children, child)
			continue
		}
		child.Parent = node.ParentID
		out.Unattached = append(out.Unattached, child)
	}
	// Border router first, then the leader, then the provider's order — the same
	// anchoring the map uses, so a reader can line the two up.
	sort.SliceStable(routerOrder, func(i, j int) bool {
		return routerRank(clusters[routerOrder[i]]) < routerRank(clusters[routerOrder[j]])
	})
	for _, id := range routerOrder {
		out.Routers = append(out.Routers, *clusters[id])
	}
	for _, link := range topology.Links {
		if link.Type != "router" {
			continue
		}
		from, to := nodeName[link.Source], nodeName[link.Target]
		if from == "" || to == "" {
			continue
		}
		out.RouterLinks = append(out.RouterLinks, routerLink{
			From: from, To: to, LinkQuality: link.LinkQuality, LinkQualityIn: link.LinkQualityIn, LinkQualityOut: link.LinkQualityOut,
			RouteCost: link.RouteCost, AverageRSSI: link.AverageRSSI, LastRSSI: link.LastRSSI,
			FrameErrorRate: link.FrameErrorRate, MessageErrorRate: link.MessageErrorRate,
		})
	}
	return nil, out, nil
}

func routerRank(c *routerCluster) int {
	switch {
	case c.IsBorderRouter:
		return 0
	case c.IsLeader:
		return 1
	default:
		return 2
	}
}

// --- scan_networks ---------------------------------------------------------

func (s *server) scanNetworks(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, *model.NetworkScan, error) {
	scan, err := s.data.ScanNetworks(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("scan unavailable: %w", err)
	}
	return nil, scan, nil
}

// --- get_history -----------------------------------------------------------

type historyInput struct {
	Device string `json:"device,omitempty" jsonschema:"Keep only neighbour events for this device: a name, extended address or RLOC16."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum entries to return in each list, newest first. Default 30."`
}

type networkEvent struct {
	At          time.Time `json:"at"`
	AgeSeconds  int64     `json:"ageSeconds"`
	Role        string    `json:"role,omitempty"`
	Mode        string    `json:"mode,omitempty"`
	RLOC16      string    `json:"rloc16,omitempty"`
	PartitionID *uint32   `json:"partitionId,omitempty"`
}

type neighborEvent struct {
	At              time.Time `json:"at"`
	AgeSeconds      int64     `json:"ageSeconds"`
	Device          string    `json:"device"`
	ExtendedAddress string    `json:"extendedAddress,omitempty"`
	Type            string    `json:"type,omitempty"`
	Event           string    `json:"event,omitempty"`
	RLOC16          string    `json:"rloc16,omitempty"`
	AverageRSSI     *int      `json:"averageRssi,omitempty"`
}

type historyView struct {
	Status    string          `json:"status"`
	Source    string          `json:"source,omitempty"`
	Error     string          `json:"error,omitempty"`
	Network   []networkEvent  `json:"network"`
	Neighbors []neighborEvent `json:"neighbors"`
}

func (s *server) getHistory(ctx context.Context, _ *mcp.CallToolRequest, in historyInput) (*mcp.CallToolResult, historyView, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 30
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	history, err := s.history.History(ctx)
	if err != nil {
		return nil, historyView{}, fmt.Errorf("history unavailable: %w", err)
	}
	labels := s.labels.Snapshot()
	now := time.Now()

	var filter *model.Device
	filterText := strings.ToLower(strings.TrimSpace(in.Device))
	if filterText != "" {
		filter = s.resolveDevice(filterText, labels)
	}

	// Without the socket the provider answers with status "unsupported" and the
	// reason in Error rather than failing, so the reason must travel too.
	out := historyView{Status: history.Status, Source: history.Source, Error: history.Error, Network: []networkEvent{}, Neighbors: []neighborEvent{}}
	network := append([]model.NetworkHistoryEntry(nil), history.Network...)
	sort.SliceStable(network, func(i, j int) bool { return network[i].At.After(network[j].At) })
	for _, entry := range network {
		if len(out.Network) == limit {
			break
		}
		out.Network = append(out.Network, networkEvent{At: entry.At, AgeSeconds: ageSeconds(now, entry.At), Role: entry.Role, Mode: entry.Mode, RLOC16: entry.RLOC16, PartitionID: entry.PartitionID})
	}
	neighbors := append([]model.NeighborHistoryEntry(nil), history.Neighbors...)
	sort.SliceStable(neighbors, func(i, j int) bool { return neighbors[i].At.After(neighbors[j].At) })
	for _, entry := range neighbors {
		if len(out.Neighbors) == limit {
			break
		}
		if filterText != "" && !historyMatches(entry, filterText, filter) {
			continue
		}
		device := entry.CustomName
		if device == "" {
			device = labelFor(labels, entry.ExtendedAddress, "")
		}
		if device == "" && filter != nil {
			device = displayName(labels, *filter)
		}
		if device == "" {
			device = entry.ExtendedAddress
		}
		out.Neighbors = append(out.Neighbors, neighborEvent{
			At: entry.At, AgeSeconds: ageSeconds(now, entry.At), Device: device, ExtendedAddress: entry.ExtendedAddress,
			Type: entry.Type, Event: entry.Event, RLOC16: entry.RLOC16, AverageRSSI: entry.AverageRSSI,
		})
	}
	return nil, out, nil
}

func historyMatches(entry model.NeighborHistoryEntry, text string, device *model.Device) bool {
	if device != nil {
		if device.ExtendedAddress != "" && strings.EqualFold(entry.ExtendedAddress, device.ExtendedAddress) {
			return true
		}
		if device.RLOC16 != "" && sameRLOC(entry.RLOC16, device.RLOC16) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(entry.ExtendedAddress), text) || sameRLOC(entry.RLOC16, text)
}

// --- ping_device -----------------------------------------------------------

type pingInput struct {
	Device string `json:"device" jsonschema:"The device to reach: its name, extended address, RLOC16, or an IPv6 address."`
	Count  int    `json:"count,omitempty" jsonschema:"Number of echo requests, 1-10. Default 3."`
}

type pingView struct {
	Device    string   `json:"device"`
	Address   string   `json:"address"`
	Reachable bool     `json:"reachable"`
	Sent      int      `json:"sent"`
	Received  int      `json:"received"`
	MinMs     *float64 `json:"minMs,omitempty"`
	AverageMs *float64 `json:"averageMs,omitempty"`
	MaxMs     *float64 `json:"maxMs,omitempty"`
	Note      string   `json:"note,omitempty"`
}

func (s *server) pingDevice(ctx context.Context, _ *mcp.CallToolRequest, in pingInput) (*mcp.CallToolResult, pingView, error) {
	target := strings.TrimSpace(in.Device)
	if target == "" {
		return nil, pingView{}, errors.New("device is required")
	}
	count := in.Count
	if count <= 0 {
		count = 3
	}
	if count > 10 {
		count = 10
	}
	labels := s.labels.Snapshot()
	address, name, err := s.resolveAddress(target, labels)
	if err != nil {
		return nil, pingView{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	result, err := s.pinger.Ping(ctx, address, count)
	if err != nil {
		return nil, pingView{}, fmt.Errorf("ping failed: %w", err)
	}
	view := pingView{Device: name, Address: result.Address, Reachable: result.Reachable, Sent: result.Sent, Received: result.Received,
		MinMs: result.MinMs, AverageMs: result.AverageMs, MaxMs: result.MaxMs}
	if !result.Reachable {
		view.Note = "No reply. A sleepy device may simply not have woken during the test; a device that is also absent from list_devices has most likely left the mesh."
	}
	return nil, view, nil
}

// resolveAddress turns whatever the caller named into something the border
// router can ping. An IPv6 literal passes through; anything else must match a
// device in the inventory, whose mesh-local address is preferred because it
// survives roaming where the RLOC16 does not.
func (s *server) resolveAddress(target string, labels map[string]string) (address, name string, err error) {
	if addr, parseErr := netip.ParseAddr(target); parseErr == nil && addr.Is6() {
		name = target
		if device := s.deviceByAddress(addr); device != nil {
			name = displayName(labels, *device)
		}
		return target, name, nil
	}
	device := s.resolveDevice(strings.ToLower(target), labels)
	if device == nil {
		return "", "", fmt.Errorf("no device matches %q; use list_devices to see names and addresses", target)
	}
	overview := s.data.Snapshot()
	address = meshLocalAddress(*device, meshLocalPrefix(overview.MeshLocalPrefix), overview)
	if address == "" {
		address = device.OMRIPv6Address
	}
	if address == "" && len(device.IPv6Addresses) > 0 {
		address = device.IPv6Addresses[0]
	}
	if address == "" {
		return "", "", fmt.Errorf("%s has no known IPv6 address to ping", displayName(labels, *device))
	}
	return address, displayName(labels, *device), nil
}

func (s *server) deviceByAddress(addr netip.Addr) *model.Device {
	inventory := s.data.DeviceSnapshot()
	for i := range inventory.Items {
		for _, candidate := range append([]string{inventory.Items[i].OMRIPv6Address}, inventory.Items[i].IPv6Addresses...) {
			if parsed, err := netip.ParseAddr(candidate); err == nil && parsed == addr {
				return &inventory.Items[i]
			}
		}
	}
	return nil
}

// resolveDevice matches lower-cased free text against the inventory: an exact
// name or address wins, then a unique substring match.
func (s *server) resolveDevice(text string, labels map[string]string) *model.Device {
	inventory := s.data.DeviceSnapshot()
	var partial []*model.Device
	for i := range inventory.Items {
		device := &inventory.Items[i]
		keys := []string{strings.ToLower(displayName(labels, *device)), strings.ToLower(device.Name), strings.ToLower(device.ExtendedAddress)}
		for _, key := range keys {
			if key != "" && key == text {
				return device
			}
		}
		if sameRLOC(device.RLOC16, text) {
			return device
		}
		if matchesQuery(text, keys[0], keys[1], keys[2], "") {
			partial = append(partial, device)
		}
	}
	if len(partial) == 1 {
		return partial[0]
	}
	return nil
}

// --- helpers ---------------------------------------------------------------

func isRouterRole(role string) bool {
	switch strings.ToLower(strings.NewReplacer("_", "-", " ", "-").Replace(role)) {
	case "router", "leader", "border-router":
		return true
	}
	return false
}

// isRouter mirrors the frontend's test: role, border-router flag, or an RLOC16
// whose child bits are zero (firmware that reports no role for routers).
func isRouter(device model.Device) bool {
	if device.IsBorderRouter || isRouterRole(device.Role) {
		return true
	}
	if value, ok := parseRLOC(device.RLOC16); ok && value&0x03ff == 0 {
		return true
	}
	return false
}

func parseRLOC(text string) (uint64, bool) {
	clean := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(text)), "0x")
	if clean == "" || len(clean) > 4 {
		return 0, false
	}
	value, err := strconv.ParseUint(clean, 16, 16)
	return value, err == nil
}

func sameRLOC(a, b string) bool {
	x, okA := parseRLOC(a)
	y, okB := parseRLOC(b)
	return okA && okB && x == y
}

// findParent resolves a child's parent the way the map does: the reported
// parent matched against router identifiers, else the router id encoded in the
// child's own RLOC16.
func findParent(child model.Device, items []model.Device) *model.Device {
	parent := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(child.Parent), "0x"))
	routerID := -1
	if value, ok := parseRLOC(child.RLOC16); ok {
		routerID = int(value >> 10)
	}
	var byRouterID *model.Device
	for i := range items {
		candidate := &items[i]
		if !isRouter(*candidate) {
			continue
		}
		if parent != "" {
			for _, key := range []string{candidate.ID, candidate.ExtendedAddress, candidate.RLOC16} {
				if key != "" && strings.EqualFold(strings.TrimPrefix(key, "0x"), parent) {
					return candidate
				}
			}
			if candidate.RouterID != nil && strconv.Itoa(*candidate.RouterID) == parent {
				return candidate
			}
		}
		if routerID >= 0 && byRouterID == nil {
			id := -1
			if candidate.RouterID != nil {
				id = *candidate.RouterID
			} else if value, ok := parseRLOC(candidate.RLOC16); ok {
				id = int(value >> 10)
			}
			if id == routerID {
				byRouterID = candidate
			}
		}
	}
	return byRouterID
}

func labelFor(labels map[string]string, ext, id string) string {
	if ext != "" {
		return labels[strings.ToLower(ext)]
	}
	return labels[strings.ToLower(id)]
}

// displayName prefers the user's label, then the provider's name, then the
// extended address, falling back to whatever identifies the device at all.
func displayName(labels map[string]string, device model.Device) string {
	if device.CustomName != "" {
		return device.CustomName
	}
	if name := labelFor(labels, device.ExtendedAddress, device.ID); name != "" {
		return name
	}
	if device.Name != "" {
		return device.Name
	}
	if device.IsBorderRouter {
		return "Border Router"
	}
	if device.ExtendedAddress != "" {
		return device.ExtendedAddress
	}
	return device.ID
}

func nodeDisplayName(labels map[string]string, node model.TopologyNode) string {
	if node.CustomName != "" {
		return node.CustomName
	}
	if node.ExtendedAddress != "" {
		if name := labels[strings.ToLower(node.ExtendedAddress)]; name != "" {
			return name
		}
	}
	if node.Name != "" {
		return node.Name
	}
	if node.IsBorderRouter {
		return "Border Router"
	}
	if node.ExtendedAddress != "" {
		return node.ExtendedAddress
	}
	return node.ID
}

func matchesQuery(query string, candidates ...string) bool {
	for _, candidate := range candidates {
		if candidate != "" && strings.Contains(strings.ToLower(candidate), query) {
			return true
		}
	}
	return false
}

// meshLocalPrefix parses the overview's prefix, which arrives as "fdde:ad00:beef:0::/64"
// or without a length; a bare address is taken as a /64.
func meshLocalPrefix(text string) netip.Prefix {
	text = strings.TrimSpace(text)
	if text == "" {
		return netip.Prefix{}
	}
	if prefix, err := netip.ParsePrefix(text); err == nil {
		return prefix.Masked()
	}
	if addr, err := netip.ParseAddr(text); err == nil {
		return netip.PrefixFrom(addr, 64).Masked()
	}
	return netip.Prefix{}
}

func meshLocalAddress(device model.Device, prefix netip.Prefix, overview model.Overview) string {
	if prefix.IsValid() {
		for _, candidate := range device.IPv6Addresses {
			if addr, err := netip.ParseAddr(candidate); err == nil && prefix.Contains(addr) {
				return candidate
			}
		}
	}
	if device.IsBorderRouter {
		return overview.MeshLocalAddress
	}
	return ""
}

func ageSeconds(now, at time.Time) int64 {
	age := int64(now.Sub(at).Seconds())
	if age < 0 {
		return 0
	}
	return age
}

func boolPtr(v bool) *bool { return &v }

// minLevelHandler drops records below min before they reach the wrapped handler.
type minLevelHandler struct {
	slog.Handler
	min slog.Level
}

func (h *minLevelHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.min && h.Handler.Enabled(ctx, level)
}

func (h *minLevelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &minLevelHandler{Handler: h.Handler.WithAttrs(attrs), min: h.min}
}

func (h *minLevelHandler) WithGroup(name string) slog.Handler {
	return &minLevelHandler{Handler: h.Handler.WithGroup(name), min: h.min}
}
