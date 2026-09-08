package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

type fakeProvider struct {
	overview *model.Overview
	err      error
}

func (f *fakeProvider) Overview(context.Context) (*model.Overview, error) { return f.overview, f.err }
func (f *fakeProvider) Devices(context.Context) (*model.DeviceInventory, error) {
	return &model.DeviceInventory{Status: "unsupported", Items: []model.Device{}, Source: "node overview"}, nil
}
func (f *fakeProvider) Topology(context.Context) (*model.Topology, error) {
	return &model.Topology{Status: "unsupported", Nodes: []model.TopologyNode{}, Links: []model.TopologyLink{}, Source: "diagnostics"}, nil
}
func (f *fakeProvider) ScanNetworks(context.Context) (*model.NetworkScan, error) {
	return &model.NetworkScan{Status: "available", Items: []model.AvailableNetwork{}, Source: "scan"}, nil
}
func (f *fakeProvider) Capabilities(context.Context) (*model.Capabilities, error) {
	return &model.Capabilities{}, nil
}

func TestMonitorKeepsValidDataAndMarksItStale(t *testing.T) {
	provider := &fakeProvider{overview: &model.Overview{NetworkName: "test-mesh", HasData: true}}
	monitor := NewMonitor(provider, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	monitor.RefreshNow(context.Background())
	first := monitor.Snapshot()
	if first.Status != "online" || first.Stale {
		t.Fatalf("first snapshot = %+v", first)
	}

	provider.err = errors.New("connection refused")
	provider.overview = nil
	monitor.RefreshNow(context.Background())
	stale := monitor.Snapshot()
	if stale.NetworkName != "test-mesh" {
		t.Errorf("lost cached network name: %q", stale.NetworkName)
	}
	if stale.Status != "offline" || !stale.Stale || !stale.HasData {
		t.Errorf("stale snapshot = %+v", stale)
	}
	if stale.Error == "" {
		t.Error("expected refresh error")
	}
}

func TestMonitorOfflineBeforeFirstDataIsNotStale(t *testing.T) {
	provider := &fakeProvider{err: errors.New("connection refused")}
	monitor := NewMonitor(provider, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	monitor.RefreshNow(context.Background())
	snapshot := monitor.Snapshot()
	if snapshot.Status != "offline" || snapshot.Stale || snapshot.HasData {
		t.Errorf("snapshot = %+v", snapshot)
	}
}

func TestMonitorFallsBackToBorderRouterWhenDeviceCollectionUnsupported(t *testing.T) {
	provider := &fakeProvider{overview: &model.Overview{
		NetworkName: "test-mesh", ExtendedAddress: "0011223344556677",
		Role: "router", RLOC16: "0x7400", HasData: true,
	}}
	monitor := NewMonitor(provider, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	monitor.RefreshNow(context.Background())
	monitor.refreshDevices(context.Background())
	inventory := monitor.DeviceSnapshot()
	if inventory.Status != "partial" || inventory.CollectionSupported {
		t.Fatalf("inventory = %+v", inventory)
	}
	if len(inventory.Items) != 1 || !inventory.Items[0].IsBorderRouter {
		t.Fatalf("items = %+v", inventory.Items)
	}
}

// discoveringProvider is a provider that can also trigger a mesh sweep.
type discoveringProvider struct {
	fakeProvider
	mu      sync.Mutex
	calls   int
	routers []string
	result  error
}

func (d *discoveringProvider) RefreshMesh(_ context.Context, routers []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	d.routers = append([]string(nil), routers...)
	return d.result
}

func (d *discoveringProvider) lastRouters() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.routers...)
}

func (d *discoveringProvider) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

type unsupportedDiscovery struct{}

func (unsupportedDiscovery) Error() string              { return "unsupported" }
func (unsupportedDiscovery) DiscoveryUnsupported() bool { return true }

func TestMonitorSweepsOnStartupSoStaleCollectionsRefresh(t *testing.T) {
	provider := &discoveringProvider{fakeProvider: fakeProvider{overview: &model.Overview{HasData: true}}}
	monitor := NewMonitor(provider, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), WithDiscoveryInterval(time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	go monitor.Run(ctx)
	waitFor(t, func() bool { return provider.callCount() >= 1 })
	cancel()
}

func TestMonitorStopsAskingWhenDiscoveryIsUnsupported(t *testing.T) {
	provider := &discoveringProvider{
		fakeProvider: fakeProvider{overview: &model.Overview{HasData: true}},
		result:       unsupportedDiscovery{},
	}
	// A tick every 10ms would hammer a border router that can never answer.
	monitor := NewMonitor(provider, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), WithDiscoveryInterval(30*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	go monitor.Run(ctx)
	waitFor(t, func() bool { return provider.callCount() >= 1 })
	time.Sleep(200 * time.Millisecond)
	cancel()
	if got := provider.callCount(); got != 1 {
		t.Errorf("RefreshMesh called %d times, want 1 — an unsupported endpoint must not be retried", got)
	}
}

func TestMonitorSkipsDiscoveryWhenIntervalIsZero(t *testing.T) {
	provider := &discoveringProvider{fakeProvider: fakeProvider{overview: &model.Overview{HasData: true}}}
	monitor := NewMonitor(provider, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	go monitor.Run(ctx)
	time.Sleep(150 * time.Millisecond)
	cancel()
	if got := provider.callCount(); got != 0 {
		t.Errorf("RefreshMesh called %d times with discovery disabled, want 0", got)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}

func TestMonitorAsksForTheLocalBorderRoutersChildTable(t *testing.T) {
	// Children attach only via a router's childTable, and the local BR is a router
	// before any inventory has arrived — so it must be queried from the first sweep.
	provider := &discoveringProvider{fakeProvider: fakeProvider{
		overview: &model.Overview{HasData: true, ExtendedAddress: "1a2b3c4d5e6f7a8b"},
	}}
	monitor := NewMonitor(provider, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), WithDiscoveryInterval(time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	go monitor.Run(ctx)
	waitFor(t, func() bool { return provider.callCount() >= 1 })
	cancel()
	if got := provider.lastRouters(); len(got) != 1 || got[0] != "1a2b3c4d5e6f7a8b" {
		t.Errorf("routers = %v, want the local border router", got)
	}
}
