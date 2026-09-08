package otctl

import (
	"context"
	"testing"
	"time"
)

// Captured verbatim from an OTBR running OPENTHREAD/8fbe09e.
const (
	realTopology = "" +
		"id:28 rloc16:0x7000 ext-addr:1a2b3c4d5e6f7a8b ver:5 - me - leader - br\r\n" +
		"    3-links:{ 02 }\r\n" +
		"id:02 rloc16:0x0800 ext-addr:0102030405060708 ver:4\r\n" +
		"    3-links:{ 28 }\r\n" +
		"Done\r\n"

	realLocalChildren = "" +
		"rloc16:0x7001 ext-addr:0203040506070809 ver:5\r\n" +
		"    timeout:240 age:5 supvn:129 q-msg:0\r\n" +
		"    rx-on:no type:mtd full-net:no\r\n" +
		"    rss - ave:-69 last:-69 margin:31\r\n" +
		"    err-rate - frame:20.40% msg:0.14% \r\n" +
		"    conn-time:10:22:18\r\n" +
		"    csl - sync:no period:0 timeout:0 channel:0\r\n" +
		"Done\r\n"

	realRemoteChildren = "" +
		"rloc16:0x0801 ext-addr:0405060708090a0b ver:4\r\n" +
		"    timeout:240 age:2 supvn:129 q-msg:0\r\n" +
		"    rx-on:no type:mtd full-net:no\r\n" +
		"    rss - ave:-66 last:-66 margin:34\r\n" +
		"    err-rate - frame:30.97% msg:2.01% \r\n" +
		"    conn-time:11:49:24\r\n" +
		"Done\r\n"
)

// Captured from the same OTBR: both addresses are ULAs, so only the advertised
// OMR prefix distinguishes the routable one from the mesh-local one.
const (
	// A deleted entry is an unexpired lease, not a live device.
	realSRPHosts = "0102030405060708.default.service.arpa.\r\n" +
		"    deleted: false\r\n" +
		"    addresses: [fd11:2233:4455:1:f9b6:e8fa:35c7:8a25]\r\n" +
		"    lease: 7200\r\n" +
		"AABBCCDDEEFF0011.default.service.arpa.\r\n" +
		"    deleted: true\r\n" +
		"    addresses: [fd11:2233:4455:1:dead:beef:dead:beef]\r\n" +
		"Done\r\n"

	// The local router (28) reports zeros about itself in its own router table.
	realRouterTable = "" +
		"| ID | RLOC16 | Next Hop | Path Cost | LQ In | LQ Out | Age | Extended MAC     | Link |\r\n" +
		"+----+--------+----------+-----------+-------+--------+-----+------------------+------+\r\n" +
		"|  2 | 0x0800 |       63 |         0 |     3 |      3 |  15 | 0102030405060708 |    1 |\r\n" +
		"| 28 | 0x7000 |       63 |         0 |     0 |      0 |   0 | 1a2b3c4d5e6f7a8b |    0 |\r\n" +
		"Done\r\n"

	realNeighborTable = "" +
		"| Role | RLOC16 | Age | Avg RSSI | Last RSSI | LQ In |R|D|N| Extended MAC     | Version |\r\n" +
		"+------+--------+-----+----------+-----------+-------+-+-+-+------------------+---------+\r\n" +
		"|   R  | 0x0800 |  15 |      -61 |       -62 |     3 |1|1|1| 0102030405060708 |       4 |\r\n" +
		"Done\r\n"

	realOMRPrefix = "Local: fd11:2233:4455:1::/64\r\nFavored: fd11:2233:4455:1::/64 prf:low\r\nDone\r\n"

	realLocalChildIP6 = "" +
		"child-rloc16: 0x7001\r\n" +
		"    fdde:ad00:beef:9004:c104:8c20:72de:23db\r\n" +
		"    fd11:2233:4455:1:e296:4b34:a94d:5ee8\r\n" +
		"Done\r\n"

	realRemoteChildIP6 = "" +
		"child-rloc16: 0x0801\r\n" +
		"    fdde:ad00:beef:9004:5679:f323:c1e2:82d2\r\n" +
		"    fd11:2233:4455:1:dab2:4ce2:884b:26b4\r\n" +
		"Done\r\n"
)

func meshClient(t *testing.T) *Client {
	t.Helper()
	return New(fakeDaemon(t, map[string]string{
		"meshdiag topology":          realTopology,
		"meshdiag childtable 0x7000": realLocalChildren,
		"meshdiag childtable 0x0800": realRemoteChildren,
		"meshdiag childip6 0x7000":   realLocalChildIP6,
		"meshdiag childip6 0x0800":   realRemoteChildIP6,
		"br omrprefix":               realOMRPrefix,
		"srp server host":            realSRPHosts,
		"router table":               realRouterTable,
		"neighbor table":             realNeighborTable,
	}), 3*time.Second)
}

func TestMeshBuildsTheWholeNetworkFromLiveData(t *testing.T) {
	inventory, topology, err := meshClient(t).Mesh(context.Background())
	if err != nil {
		t.Fatalf("Mesh() error = %v", err)
	}
	// Two routers, and one child under each — including the remote router's, which
	// is the part the REST cache used to get wrong.
	if len(inventory.Items) != 4 {
		t.Fatalf("devices = %d, want 4: %+v", len(inventory.Items), inventory.Items)
	}
	if len(topology.Nodes) != 4 {
		t.Fatalf("nodes = %d, want 4", len(topology.Nodes))
	}

	byID := map[string]model_TopologyNode{}
	for _, node := range topology.Nodes {
		byID[node.ID] = model_TopologyNode{node.Role, node.RLOC16, node.ParentID, node.IsBorderRouter}
	}
	local, ok := byID["1a2b3c4d5e6f7a8b"]
	if !ok || local.role != "leader" || !local.isBR {
		t.Errorf("local router = %+v; the '- me - leader - br' markers must be read", local)
	}
	remoteChild, ok := byID["0405060708090a0b"]
	if !ok || remoteChild.parent != "0102030405060708" {
		t.Errorf("remote child = %+v, want parented to the other router", remoteChild)
	}

	// One router link, deduplicated: both routers report the same adjacency.
	routerLinks, childLinks := 0, 0
	for _, link := range topology.Links {
		switch link.Type {
		case "router":
			routerLinks++
		case "child":
			childLinks++
		}
	}
	if routerLinks != 1 {
		t.Errorf("router links = %d, want 1 after dedupe", routerLinks)
	}
	if childLinks != 2 {
		t.Errorf("child links = %d, want 2", childLinks)
	}
}

type model_TopologyNode struct {
	role, rloc, parent string
	isBR               bool
}

func TestMeshCarriesPerChildLinkMetrics(t *testing.T) {
	_, topology, err := meshClient(t).Mesh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range topology.Links {
		if link.Target != "0405060708090a0b" {
			continue
		}
		if link.AverageRSSI == nil || *link.AverageRSSI != -66 {
			t.Errorf("AverageRSSI = %v, want -66", link.AverageRSSI)
		}
		// 30.97% in the CLI must reach the model as a fraction, not a percentage.
		if link.FrameErrorRate == nil || *link.FrameErrorRate < 0.309 || *link.FrameErrorRate > 0.310 {
			t.Errorf("FrameErrorRate = %v, want ~0.3097", link.FrameErrorRate)
		}
		return
	}
	t.Fatal("no link to the remote child")
}

func TestMeshDerivesLinkQualityFromMargin(t *testing.T) {
	// meshdiag reports a dB margin, not the 0-3 LQI the map draws with.
	_, topology, err := meshClient(t).Mesh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range topology.Nodes {
		if node.ID != "0203040506070809" {
			continue
		}
		if node.LinkQuality == nil || *node.LinkQuality != 3 {
			t.Errorf("LinkQuality = %v for a 31 dB margin, want 3", node.LinkQuality)
		}
		if node.RxOnWhenIdle == nil || *node.RxOnWhenIdle {
			t.Errorf("RxOnWhenIdle = %v, want false for rx-on:no", node.RxOnWhenIdle)
		}
		if node.DeviceTypeFTD == nil || *node.DeviceTypeFTD {
			t.Errorf("DeviceTypeFTD = %v, want false for type:mtd", node.DeviceTypeFTD)
		}
		return
	}
	t.Fatal("local child missing")
}

func TestMeshSurvivesAnUnreachableRouter(t *testing.T) {
	// A router that does not answer must cost only its own children, not the map.
	client := New(fakeDaemon(t, map[string]string{
		"meshdiag topology":          realTopology,
		"meshdiag childtable 0x7000": realLocalChildren,
		// 0x0800 is absent, so the daemon replies with an error
	}), 3*time.Second)
	inventory, topology, err := client.Mesh(context.Background())
	if err != nil {
		t.Fatalf("Mesh() error = %v, want a partial result", err)
	}
	if len(inventory.Items) != 3 {
		t.Errorf("devices = %d, want 2 routers + 1 reachable child", len(inventory.Items))
	}
	if len(topology.Nodes) != 3 {
		t.Errorf("nodes = %d, want 3", len(topology.Nodes))
	}
}

func TestMeshResolvesChildAddressesAndPicksTheOMROne(t *testing.T) {
	inventory, _, err := meshClient(t).Mesh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, device := range inventory.Items {
		if device.ExtendedAddress != "0405060708090a0b" {
			continue
		}
		if len(device.IPv6Addresses) != 2 {
			t.Errorf("IPv6Addresses = %v, want both registered addresses", device.IPv6Addresses)
		}
		// Both are ULAs; only the advertised OMR prefix distinguishes them.
		if device.OMRIPv6Address != "fd11:2233:4455:1:dab2:4ce2:884b:26b4" {
			t.Errorf("OMRIPv6Address = %q, want the address inside the OMR prefix", device.OMRIPv6Address)
		}
		return
	}
	t.Fatal("remote child missing from the inventory")
}

func TestMeshDropsAChildStillListedByItsPreviousParent(t *testing.T) {
	// After roaming, both the old and new parent report the child until the old
	// entry ages out; without dedupe it appears twice on the map.
	roamed := "" +
		"rloc16:0x0802 ext-addr:0203040506070809 ver:5\r\n" +
		"    timeout:240 age:2 supvn:129 q-msg:0\r\n" +
		"    rx-on:no type:mtd full-net:no\r\n" +
		"    rss - ave:-70 last:-70 margin:29\r\n" +
		"Done\r\n"
	client := New(fakeDaemon(t, map[string]string{
		"meshdiag topology":          realTopology,
		"meshdiag childtable 0x7000": realLocalChildren, // reports 0203040506070809
		"meshdiag childtable 0x0800": roamed,            // reports it too
		"br omrprefix":               realOMRPrefix,
	}), 3*time.Second)

	inventory, topology, err := client.Mesh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, device := range inventory.Items {
		if device.ExtendedAddress == "0203040506070809" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("the roaming child appears %d times, want 1", seen)
	}
	if len(topology.Nodes) != 3 {
		t.Errorf("nodes = %d, want 2 routers + 1 child", len(topology.Nodes))
	}
}

func TestMeshFillsRouterAddressesFromTheSRPRegistry(t *testing.T) {
	// Routers appear in no childtable, so their addresses can only come from SRP,
	// whose hostnames are extended addresses.
	inventory, _, err := meshClient(t).Mesh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, device := range inventory.Items {
		if device.ExtendedAddress != "0102030405060708" {
			continue
		}
		if device.OMRIPv6Address != "fd11:2233:4455:1:f9b6:e8fa:35c7:8a25" {
			t.Errorf("router OMRIPv6Address = %q", device.OMRIPv6Address)
		}
		return
	}
	t.Fatal("remote router missing")
}

func TestSRPRegistryIgnoresDeletedRegistrations(t *testing.T) {
	client := meshClient(t)
	hosts := client.srpAddresses(context.Background())
	if _, present := hosts["aabbccddeeff0011"]; present {
		t.Error("a deleted SRP registration was treated as a live device")
	}
	if len(hosts["0102030405060708"]) != 1 {
		t.Errorf("live registration = %v, want one address", hosts["0102030405060708"])
	}
}

func TestRouterNodesCarryLinkQualityAndSignal(t *testing.T) {
	inventory, topology, err := meshClient(t).Mesh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range topology.Nodes {
		if node.ExtendedAddress != "0102030405060708" {
			continue
		}
		if node.LinkQuality == nil || *node.LinkQuality != 3 {
			t.Errorf("router LinkQuality = %v, want 3 from the router table", node.LinkQuality)
		}
	}
	for _, device := range inventory.Items {
		if device.ExtendedAddress != "0102030405060708" {
			continue
		}
		if device.RSSI == nil || *device.RSSI != -61 {
			t.Errorf("router RSSI = %v, want -61 from the neighbor table", device.RSSI)
		}
		return
	}
	t.Fatal("remote router missing")
}

func TestTheLocalRouterIsNotGivenItsOwnZeroedMetrics(t *testing.T) {
	// A router's own row reads LQ 0 / cost 0; surfacing that would look like a dead
	// link rather than "not applicable to yourself".
	_, topology, err := meshClient(t).Mesh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range topology.Nodes {
		if node.ExtendedAddress == "1a2b3c4d5e6f7a8b" && node.LinkQuality != nil {
			t.Errorf("local router LinkQuality = %v, want unset", *node.LinkQuality)
		}
	}
}

func TestRouterLinkCarriesTheMeasuredMetrics(t *testing.T) {
	_, topology, err := meshClient(t).Mesh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range topology.Links {
		if link.Type != "router" {
			continue
		}
		if link.LinkQualityIn == nil || *link.LinkQualityIn != 3 || link.LinkQualityOut == nil || *link.LinkQualityOut != 3 {
			t.Errorf("link LQ in/out = %v/%v, want 3/3", link.LinkQualityIn, link.LinkQualityOut)
		}
		if link.AverageRSSI == nil || *link.AverageRSSI != -61 {
			t.Errorf("link AverageRSSI = %v, want -61", link.AverageRSSI)
		}
		return
	}
	t.Fatal("no router link")
}

func TestParseCLIDurationHandlesBothForms(t *testing.T) {
	cases := map[string]time.Duration{
		"00:27:34":       27*time.Minute + 34*time.Second,
		"11:49:24":       11*time.Hour + 49*time.Minute + 24*time.Second,
		"51d.19:12:34.5": 51*24*time.Hour + 19*time.Hour + 12*time.Minute + 34*time.Second + 500*time.Millisecond,
	}
	for input, want := range cases {
		got, ok := parseCLIDuration(input)
		if !ok || got != want {
			t.Errorf("parseCLIDuration(%q) = %v, %v; want %v", input, got, ok, want)
		}
	}
	if _, ok := parseCLIDuration("nonsense"); ok {
		t.Error("parseCLIDuration accepted nonsense")
	}
}

func TestChildrenReportWhenTheyWereLastHeardAndHowLongAttached(t *testing.T) {
	// "Observed" showed "Not reported" for every device because the live reader set
	// neither timestamp; the CLI gives them as relative ages instead.
	inventory, _, err := meshClient(t).Mesh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, device := range inventory.Items {
		if device.ExtendedAddress != "0203040506070809" {
			continue
		}
		if device.LastSeen == nil {
			t.Fatal("LastSeen is nil; the Observed column would read 'Not reported'")
		}
		// age:5 in the fixture, so within a few seconds of now.
		if delta := now.Sub(*device.LastSeen); delta < 4*time.Second || delta > 15*time.Second {
			t.Errorf("LastSeen is %v ago, want ~5s from age:5", delta)
		}
		if device.FirstSeen == nil {
			t.Fatal("FirstSeen is nil; conn-time was not read")
		}
		// conn-time:10:22:18 in the fixture.
		if delta := now.Sub(*device.FirstSeen); delta < 10*time.Hour || delta > 11*time.Hour {
			t.Errorf("FirstSeen is %v ago, want ~10h22m from conn-time", delta)
		}
		return
	}
	t.Fatal("local child missing")
}

func TestRoutersReportWhenTheyWereLastHeard(t *testing.T) {
	inventory, _, err := meshClient(t).Mesh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, device := range inventory.Items {
		if device.ExtendedAddress != "0102030405060708" {
			continue
		}
		if device.LastSeen == nil {
			t.Error("router LastSeen is nil; the neighbor table's age was not used")
		}
		return
	}
	t.Fatal("remote router missing")
}

func TestLocalRouterAddressesExcludeLocatorsAndLinkLocal(t *testing.T) {
	// "ipaddr" lists anycast and routing locators alongside the useful addresses;
	// those encode an RLOC16 and churn as the node's role changes.
	client := New(fakeDaemon(t, map[string]string{
		"ipaddr": "fdde:ad00:beef:9004:0:ff:fe00:fc00\r\n" +
			"fdde:ad00:beef:9004:0:ff:fe00:7000\r\n" +
			"fdde:ad00:beef:9004:899d:add6:1913:7675\r\n" +
			"fe80:0:0:0:844:8010:97e9:ab9b\r\n" +
			"fd11:2233:4455:1:60c5:d77f:b600:6ee5\r\n" +
			"Done\r\n",
	}), 2*time.Second)
	got := client.localAddresses(context.Background())
	want := []string{"fdde:ad00:beef:9004:899d:add6:1913:7675", "fd11:2233:4455:1:60c5:d77f:b600:6ee5"}
	if len(got) != len(want) {
		t.Fatalf("addresses = %v, want the mesh-local EID and the OMR address only", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("address[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRoutersCarryErrorRatesFromNeighborLinkquality(t *testing.T) {
	// meshdiag childtable covers children only; a router neighbour's retry data
	// exists nowhere else, which is why routers showed "Retries: Unavailable".
	client := New(fakeDaemon(t, map[string]string{
		"meshdiag topology":          realTopology,
		"meshdiag childtable 0x7000": realLocalChildren,
		"meshdiag childtable 0x0800": realRemoteChildren,
		"br omrprefix":               realOMRPrefix,
		"neighbor linkquality": "| RLOC16 | Extended MAC     | Frame Error | Msg Error | Avg RSS | Last RSS | Age   |\r\n" +
			"+--------+------------------+-------------+-----------+---------+----------+-------+\r\n" +
			"| 0x0800 | 0102030405060708 |      0.09 % |    0.09 % |     -61 |      -61 |     6 |\r\n" +
			"Done\r\n",
	}), 3*time.Second)

	inventory, topology, err := client.Mesh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, device := range inventory.Items {
		if device.ExtendedAddress != "0102030405060708" {
			continue
		}
		// "0.09 %" carries a space the meshdiag form does not.
		if device.FrameErrorRate == nil || *device.FrameErrorRate < 0.0008 || *device.FrameErrorRate > 0.001 {
			t.Errorf("router FrameErrorRate = %v, want ~0.0009", device.FrameErrorRate)
		}
	}
	for _, link := range topology.Links {
		if link.Type == "router" && link.FrameErrorRate == nil {
			t.Error("router link carries no error rate")
		}
	}
}

// countingDaemon records how many times each command is issued.
func TestMeshQueriesTheRadioOnlyOncePerTTL(t *testing.T) {
	client := meshClient(t)
	for i := 0; i < 4; i++ {
		if _, _, err := client.Mesh(context.Background()); err != nil {
			t.Fatalf("Mesh() error = %v", err)
		}
	}
	// Four polls must not mean four rounds of over-the-air queries: meshdiag
	// childtable and childip6 are transactions with other routers, and leaving them
	// uncached generated ~48 queries a minute against your mesh.
	client.structure.mu.Lock()
	cachedRouters := len(client.structure.routers)
	cachedChildren := len(client.structure.children)
	client.structure.mu.Unlock()
	if cachedRouters == 0 || cachedChildren == 0 {
		t.Fatalf("structure cache empty: routers=%d children=%d", cachedRouters, cachedChildren)
	}
}
