package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// Fixture values follow the documentation conventions used elsewhere in the
// tree: sequential extended addresses and the fdde:ad00:beef / fd11:2233:4455
// prefixes. Nothing here came from live hardware.

func intPtr(v int) *int { return &v }

type fakeData struct {
	overview  model.Overview
	inventory model.DeviceInventory
	topology  model.Topology
	scanErr   error
}

func (f *fakeData) Snapshot() model.Overview              { return f.overview }
func (f *fakeData) DeviceSnapshot() model.DeviceInventory { return f.inventory }
func (f *fakeData) TopologySnapshot() model.Topology      { return f.topology }
func (f *fakeData) ScanNetworks(context.Context) (*model.NetworkScan, error) {
	if f.scanErr != nil {
		return nil, f.scanErr
	}
	return &model.NetworkScan{Status: "available", Items: []model.AvailableNetwork{{Name: "Neighbour", PANID: "0x1234", Channel: intPtr(20)}}}, nil
}

type fakeLabels map[string]string

func (f fakeLabels) Snapshot() map[string]string { return f }

// fakeProvider implements ping and history; a provider without them is plain struct{}.
type fakeProvider struct {
	pinged  []string
	history *model.History
}

func (f *fakeProvider) Ping(_ context.Context, address string, count int) (*model.PingResult, error) {
	f.pinged = append(f.pinged, address)
	return &model.PingResult{Address: address, Reachable: true, Sent: count, Received: count}, nil
}

func (f *fakeProvider) History(context.Context) (*model.History, error) {
	if f.history == nil {
		return nil, errors.New("socket unavailable")
	}
	return f.history, nil
}

func fixture(now time.Time) *fakeData {
	recent := now.Add(-5 * time.Second)
	old := now.Add(-45 * time.Minute)
	leaderID := 28
	return &fakeData{
		overview: model.Overview{
			Status: "online", HasData: true, NetworkName: "OpenThreadDemo", Role: "leader", State: "leader",
			RLOC16: "0x7000", RouterID: &leaderID, ExtendedAddress: "0102030405060708", PANID: "0x1234",
			ExtendedPANID: "1122334455667788", LeaderRouterID: &leaderID, RouterCount: intPtr(2), RCPChannel: "15",
			MeshLocalPrefix: "fdde:ad00:beef:0::/64", MeshLocalAddress: "fdde:ad00:beef:0:1111:2222:3333:4444",
			OMRIPv6Address: "fd11:2233:4455:0:1111:2222:3333:4444",
		},
		inventory: model.DeviceInventory{Status: "available", Source: "otctl", Items: []model.Device{
			{ID: "0102030405060708", ExtendedAddress: "0102030405060708", Role: "leader", RLOC16: "0x7000", RouterID: &leaderID, IsBorderRouter: true},
			{ID: "1112131415161718", ExtendedAddress: "1112131415161718", Role: "router", RLOC16: "0x0400", RouterID: intPtr(1), LastSeen: &recent, RSSI: intPtr(-60),
				IPv6Addresses: []string{"fd11:2233:4455:0:aaaa:bbbb:cccc:dddd", "fdde:ad00:beef:0:aaaa:bbbb:cccc:dddd"}},
			{ID: "2122232425262728", ExtendedAddress: "2122232425262728", Role: "child", RLOC16: "0x0401", Parent: "1112131415161718", LastSeen: &recent, RSSI: intPtr(-70),
				IPv6Addresses: []string{"fdde:ad00:beef:0:1:2:3:4", "fd11:2233:4455:0:1:2:3:4"}, OMRIPv6Address: "fd11:2233:4455:0:1:2:3:4"},
			{ID: "3132333435363738", ExtendedAddress: "3132333435363738", Role: "child", RLOC16: "0x7001", LastSeen: &old, RSSI: intPtr(-88)},
		}},
		topology: model.Topology{Status: "available", Source: "otctl", Nodes: []model.TopologyNode{
			{ID: "r1", Role: "router", RLOC16: "0x0400", RouterID: intPtr(1), ExtendedAddress: "1112131415161718"},
			{ID: "br", Role: "leader", RLOC16: "0x7000", RouterID: &leaderID, ExtendedAddress: "0102030405060708", IsBorderRouter: true},
			{ID: "c1", Role: "child", RLOC16: "0x0401", ExtendedAddress: "2122232425262728", ParentID: "r1", LinkQuality: intPtr(3)},
			{ID: "c2", Role: "child", RLOC16: "0x7001", ExtendedAddress: "3132333435363738", ParentID: "br"},
			{ID: "orphan", Role: "child", RLOC16: "0x0c01", ExtendedAddress: "4142434445464748", ParentID: "r3"},
		}, Links: []model.TopologyLink{
			{Source: "br", Target: "r1", Type: "router", LinkQuality: intPtr(3), RouteCost: intPtr(1)},
			{Source: "r1", Target: "c1", Type: "child"},
		}},
	}
}

func connect(t *testing.T, data *fakeData, labels fakeLabels, provider any) *mcp.ClientSession {
	t.Helper()
	srv := httptest.NewServer(Handler(data, labels, provider, nil))
	t.Cleanup(srv.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func call(t *testing.T, session *mcp.ClientSession, name string, args any, out any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if result.IsError || out == nil {
		return result
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("%s: marshal structured content: %v", name, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s: decode %s: %v", name, raw, err)
	}
	return result
}

func TestToolsRegisteredByCapability(t *testing.T) {
	names := func(session *mcp.ClientSession) []string {
		tools, err := session.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tool := range tools.Tools {
			out = append(out, tool.Name)
		}
		return out
	}
	full := names(connect(t, fixture(time.Now()), fakeLabels{}, &fakeProvider{}))
	if got := strings.Join(full, ","); got != "get_history,get_network,get_topology,list_devices,ping_device,scan_networks" {
		t.Fatalf("tools with socket provider: %s", got)
	}
	restOnly := names(connect(t, fixture(time.Now()), fakeLabels{}, struct{}{}))
	if got := strings.Join(restOnly, ","); got != "get_network,get_topology,list_devices,scan_networks" {
		t.Fatalf("tools without socket provider: %s", got)
	}
}

func TestGetNetworkSummarisesDevicesWithoutListingThem(t *testing.T) {
	session := connect(t, fixture(time.Now()), fakeLabels{"3132333435363738": "Porch sensor"}, struct{}{})
	var out networkSummary
	result := call(t, session, "get_network", nil, &out)
	if out.NetworkName != "OpenThreadDemo" || out.Channel != "15" || out.PANID != "0x1234" {
		t.Fatalf("identity: %+v", out)
	}
	if out.Devices.Total != 4 || out.Devices.Routers != 2 || out.Devices.EndDevices != 2 {
		t.Fatalf("counts: %+v", out.Devices)
	}
	if out.Devices.Quiet != 1 || len(out.Devices.QuietDevices) != 1 || out.Devices.QuietDevices[0] != "Porch sensor" {
		t.Fatalf("quiet: %+v", out.Devices)
	}
	if out.Devices.Unnamed != 3 {
		t.Fatalf("unnamed = %d", out.Devices.Unnamed)
	}
	// The summary must not carry the device list or any credential field.
	text := textOf(result)
	for _, forbidden := range []string{"ipv6Addresses", "networkKey", "pskc", "1112131415161718"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("summary leaks %q: %s", forbidden, text)
		}
	}
}

func TestGetNetworkNotesDisabledInterface(t *testing.T) {
	data := fixture(time.Now())
	data.overview.State = "disabled"
	data.overview.Role = "disabled"
	var out networkSummary
	call(t, connect(t, data, fakeLabels{}, struct{}{}), "get_network", nil, &out)
	if !strings.Contains(out.Note, "disabled") {
		t.Fatalf("note = %q", out.Note)
	}
}

func TestListDevicesNamesParentsAndAges(t *testing.T) {
	session := connect(t, connectFixture(t), fakeLabels{"1112131415161718": "Hallway plug", "2122232425262728": "Kitchen sensor"}, struct{}{})
	var out deviceList
	call(t, session, "list_devices", nil, &out)
	if out.Count != 4 {
		t.Fatalf("count = %d", out.Count)
	}
	byName := map[string]deviceSummary{}
	for _, d := range out.Devices {
		byName[d.Name] = d
	}
	kitchen, ok := byName["Kitchen sensor"]
	if !ok {
		t.Fatalf("custom name not applied: %+v", out.Devices)
	}
	if kitchen.Parent != "Hallway plug" {
		t.Fatalf("parent resolved to %q, want the router's label", kitchen.Parent)
	}
	if kitchen.LastSeenSeconds == nil || *kitchen.LastSeenSeconds > 10 {
		t.Fatalf("lastSeenSeconds = %v", kitchen.LastSeenSeconds)
	}
	if kitchen.MeshLocalAddress != "fdde:ad00:beef:0:1:2:3:4" || kitchen.OMRAddress != "fd11:2233:4455:0:1:2:3:4" {
		t.Fatalf("addresses: %+v", kitchen)
	}
	// The child with no reported parent derives it from its RLOC16 (0x7001 → router 28).
	if byName["3132333435363738"].Parent != "Border Router" {
		t.Fatalf("derived parent = %q", byName["3132333435363738"].Parent)
	}
	if byName["Border Router"].MeshLocalAddress != "fdde:ad00:beef:0:1111:2222:3333:4444" {
		t.Fatalf("border router mesh-local from overview: %+v", byName["Border Router"])
	}
}

func TestListDevicesFilters(t *testing.T) {
	session := connect(t, connectFixture(t), fakeLabels{"2122232425262728": "Kitchen sensor"}, struct{}{})
	var out deviceList
	call(t, session, "list_devices", map[string]any{"role": "router"}, &out)
	if out.Count != 2 {
		t.Fatalf("routers = %d", out.Count)
	}
	call(t, session, "list_devices", map[string]any{"role": "end-device", "query": "kitchen"}, &out)
	if out.Count != 1 || out.Devices[0].Name != "Kitchen sensor" {
		t.Fatalf("query: %+v", out.Devices)
	}
	call(t, session, "list_devices", map[string]any{"query": "0x0400"}, &out)
	if out.Count != 1 || out.Devices[0].RLOC16 != "0x0400" {
		t.Fatalf("rloc query: %+v", out.Devices)
	}
	result := call(t, session, "list_devices", map[string]any{"role": "sleepy"}, nil)
	if !result.IsError {
		t.Fatal("unknown role should be a tool error")
	}
}

func TestGetTopologyNestsChildrenUnderRouters(t *testing.T) {
	session := connect(t, connectFixture(t), fakeLabels{"1112131415161718": "Hallway plug"}, struct{}{})
	var out topologyView
	call(t, session, "get_topology", nil, &out)
	if len(out.Routers) != 2 || out.Routers[0].Name != "Border Router" || !out.Routers[0].IsLeader {
		t.Fatalf("router order/leader: %+v", out.Routers)
	}
	if len(out.Routers[0].Children) != 1 || out.Routers[0].Children[0].ExtendedAddress != "3132333435363738" {
		t.Fatalf("border router children: %+v", out.Routers[0].Children)
	}
	if out.Routers[1].Name != "Hallway plug" || len(out.Routers[1].Children) != 1 || out.Routers[1].Children[0].LinkQuality == nil {
		t.Fatalf("router children: %+v", out.Routers[1])
	}
	if len(out.Unattached) != 1 || out.Unattached[0].Parent != "r3" {
		t.Fatalf("unattached: %+v", out.Unattached)
	}
	if len(out.RouterLinks) != 1 || out.RouterLinks[0].From != "Border Router" || out.RouterLinks[0].To != "Hallway plug" {
		t.Fatalf("router links: %+v", out.RouterLinks)
	}
}

func TestPingResolvesNamesToMeshLocalAddress(t *testing.T) {
	provider := &fakeProvider{}
	session := connect(t, connectFixture(t), fakeLabels{"2122232425262728": "Kitchen sensor"}, provider)
	var out pingView
	call(t, session, "ping_device", map[string]any{"device": "kitchen sensor"}, &out)
	if out.Address != "fdde:ad00:beef:0:1:2:3:4" || out.Device != "Kitchen sensor" || !out.Reachable || out.Sent != 3 {
		t.Fatalf("by name: %+v", out)
	}
	call(t, session, "ping_device", map[string]any{"device": "0x0401", "count": 1}, &out)
	if out.Address != "fdde:ad00:beef:0:1:2:3:4" || out.Sent != 1 {
		t.Fatalf("by rloc16: %+v", out)
	}
	call(t, session, "ping_device", map[string]any{"device": "fd11:2233:4455:0:1:2:3:4"}, &out)
	if out.Address != "fd11:2233:4455:0:1:2:3:4" || out.Device != "Kitchen sensor" {
		t.Fatalf("by address: %+v", out)
	}
	call(t, session, "ping_device", map[string]any{"device": "Border Router"}, &out)
	if out.Address != "fdde:ad00:beef:0:1111:2222:3333:4444" {
		t.Fatalf("border router: %+v", out)
	}
	result := call(t, session, "ping_device", map[string]any{"device": "garage"}, nil)
	if !result.IsError || !strings.Contains(textOf(result), "list_devices") {
		t.Fatalf("unknown device: %+v", result)
	}
	if len(provider.pinged) != 4 {
		t.Fatalf("pings sent: %v", provider.pinged)
	}
}

func TestHistoryFiltersAndNames(t *testing.T) {
	now := time.Now()
	provider := &fakeProvider{history: &model.History{Status: "available", Source: "OpenThread history tracker",
		Network: []model.NetworkHistoryEntry{{At: now.Add(-time.Hour), Role: "leader"}, {At: now.Add(-2 * time.Hour), Role: "detached"}},
		Neighbors: []model.NeighborHistoryEntry{
			{At: now.Add(-10 * time.Minute), Event: "Added", ExtendedAddress: "2122232425262728", RLOC16: "0x0401"},
			{At: now.Add(-20 * time.Minute), Event: "Removed", ExtendedAddress: "3132333435363738", RLOC16: "0x7001"},
			{At: now.Add(-30 * time.Minute), Event: "Added", ExtendedAddress: "2122232425262728", RLOC16: "0x0c01"},
		}}}
	session := connect(t, fixture(now), fakeLabels{"2122232425262728": "Kitchen sensor"}, provider)
	var out historyView
	call(t, session, "get_history", map[string]any{"device": "kitchen sensor"}, &out)
	if len(out.Neighbors) != 2 || out.Neighbors[0].Event != "Added" || out.Neighbors[0].Device != "Kitchen sensor" || out.Neighbors[1].RLOC16 != "0x0c01" {
		t.Fatalf("filtered neighbours: %+v", out.Neighbors)
	}
	if len(out.Network) != 2 || out.Network[0].Role != "leader" || out.Network[0].AgeSeconds < 3590 {
		t.Fatalf("network events newest first: %+v", out.Network)
	}
	call(t, session, "get_history", map[string]any{"limit": 1}, &out)
	if len(out.Neighbors) != 1 || len(out.Network) != 1 {
		t.Fatalf("limit: %+v", out)
	}
	result := call(t, session, "get_history", nil, nil)
	if result.IsError {
		t.Fatalf("unexpected error: %s", textOf(result))
	}
	provider.history = nil
	if result := call(t, session, "get_history", nil, nil); !result.IsError {
		t.Fatal("history failure should surface as a tool error")
	}
}

func TestScanNetworks(t *testing.T) {
	data := connectFixture(t)
	session := connect(t, data, fakeLabels{}, struct{}{})
	var out model.NetworkScan
	call(t, session, "scan_networks", nil, &out)
	if len(out.Items) != 1 || out.Items[0].Name != "Neighbour" {
		t.Fatalf("scan: %+v", out)
	}
	data.scanErr = errors.New("otbr-web not running")
	if result := call(t, session, "scan_networks", nil, nil); !result.IsError {
		t.Fatal("scan failure should be a tool error")
	}
}

func connectFixture(t *testing.T) *fakeData {
	t.Helper()
	return fixture(time.Now())
}

func textOf(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}
