package otctl

import (
	"context"
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// Mesh reads the live device inventory and topology straight from the stack.
//
// This is the point of running on the border router. OTBR's REST collections are
// caches that only change when a client posts a discovery action, so they list
// departed devices indefinitely and omit ones that joined since. "meshdiag" asks
// the mesh itself: the answer is current at the moment it is read, and sleepy
// children are included because their parent reports them.
//
// Cost: "meshdiag topology" and one "meshdiag childtable" per router are network
// queries, not local reads, so call this at the device-poll cadence rather than
// the fast overview cadence.
func (c *Client) Mesh(ctx context.Context) (*model.DeviceInventory, *model.Topology, error) {
	started := time.Now()
	routers, childrenByRouter, addressesByRouter, err := c.meshStructure(ctx)
	if err != nil {
		return nil, nil, err
	}
	// Ages and signal come from local tables on every call, so "last seen" tracks
	// reality even though the mesh queries above are only repeated occasionally.
	fresh := c.localNeighbours(ctx)
	// Needed to tell a child's OMR address from its mesh-local one; both are ULAs.
	omrPrefix := c.omrPrefix(ctx)
	// Routers do not appear in any childtable, so their addresses come from the SRP
	// registry the border router already keeps — hostnames there are extended
	// addresses. Best-effort: a device that never registered simply has none.
	registered := c.srpAddresses(ctx)
	// Link quality for routers, which no childtable covers.
	metrics := c.routerMetrics(ctx)
	// The local router registers nothing with its own SRP server, so its addresses
	// have to come from the stack directly.
	localAddrs := c.localAddresses(ctx)

	devices := make([]model.Device, 0, len(routers))
	nodes := make([]model.TopologyNode, 0, len(routers))
	links := []model.TopologyLink{}
	byRouterID := map[int]string{}
	byExtOfID := map[int]string{}
	selfExt := ""

	for _, router := range routers {
		id := router.ExtAddress
		if id == "" {
			id = router.RLOC16
		}
		byRouterID[router.RouterID] = id
		byExtOfID[router.RouterID] = router.ExtAddress
		if router.IsSelf {
			selfExt = router.ExtAddress
		}
		routerID := router.RouterID
		role := "router"
		if router.IsLeader {
			role = "leader"
		}
		metric := metrics[router.ExtAddress]
		node := model.TopologyNode{
			ID: id, Role: role, RLOC16: router.RLOC16, RouterID: &routerID,
			ExtendedAddress: router.ExtAddress, IsBorderRouter: router.IsBorderRouter,
		}
		// The local router reports zeros about itself; showing "LQI 0" would read as
		// a bad link rather than "not applicable".
		if !router.IsSelf {
			node.LinkQuality = metric.LinkQualityIn
		}
		nodes = append(nodes, node)
		addresses := registered[router.ExtAddress]
		if router.IsSelf && len(localAddrs) > 0 {
			addresses = localAddrs
		}
		device := model.Device{
			ID: id, Role: role, ExtendedAddress: router.ExtAddress, RLOC16: router.RLOC16,
			RouterID: &routerID, IsBorderRouter: router.IsBorderRouter,
			IPv6Addresses: addresses, OMRIPv6Address: pickOMR(addresses, omrPrefix),
		}
		if !router.IsSelf {
			device.LinkQuality = metric.LinkQualityIn
			device.RSSI = metric.AverageRSSI
			device.FrameErrorRate, device.MessageErrorRate = metric.FrameErrorRate, metric.MessageErrorRate
			if metric.Age != nil {
				seen := time.Now().UTC().Add(-time.Duration(*metric.Age) * time.Second)
				device.LastSeen = &seen
			}
		}
		devices = append(devices, device)
	}

	// Router adjacency, deduplicated: each router reports the link from its own side.
	seenLink := map[string]bool{}
	for _, router := range routers {
		source := byRouterID[router.RouterID]
		for peerID, quality := range router.Links {
			target, ok := byRouterID[peerID]
			if !ok || source == "" || source == target {
				continue
			}
			key := source + "\x00" + target
			reverse := target + "\x00" + source
			if seenLink[key] || seenLink[reverse] {
				continue
			}
			seenLink[key] = true
			linkQuality := quality
			link := model.TopologyLink{
				Source: source, Target: target, Type: "router", LinkQuality: &linkQuality,
			}
			// Enrich with what the local node measured, for whichever end is not us.
			for _, ext := range []string{router.ExtAddress, byExtOfID[peerID]} {
				metric, ok := metrics[ext]
				if !ok || ext == selfExt {
					continue
				}
				link.LinkQualityIn, link.LinkQualityOut = metric.LinkQualityIn, metric.LinkQualityOut
				link.RouteCost, link.AverageRSSI, link.LastRSSI = metric.RouteCost, metric.AverageRSSI, metric.LastRSSI
				link.FrameErrorRate, link.MessageErrorRate = metric.FrameErrorRate, metric.MessageErrorRate
			}
			links = append(links, link)
		}
	}

	// A child that has just roamed is still listed by its previous parent, so the
	// same device can arrive twice. Keep the first sighting.
	seenChild := map[string]bool{}
	for _, router := range routers {
		parent := byRouterID[router.RouterID]
		children := childrenByRouter[router.RLOC16]
		addresses := addressesByRouter[router.RLOC16]
		for _, child := range children {
			if latest, ok := fresh[child.ExtAddress]; ok {
				// The parent's own table is authoritative and seconds old.
				child.Age = latest.Age
				if latest.AverageRSSI != nil {
					child.AverageRSSI, child.LastRSSI = latest.AverageRSSI, latest.LastRSSI
				}
			}
			if child.ExtAddress != "" {
				if seenChild[child.ExtAddress] {
					continue
				}
				seenChild[child.ExtAddress] = true
			}
			child.ParentID = parent
			child.IPv6Addresses = addresses[child.RLOC16]
			if len(child.IPv6Addresses) == 0 {
				child.IPv6Addresses = registered[child.ExtAddress]
			}
			child.OMRIPv6Address = pickOMR(child.IPv6Addresses, omrPrefix)
			nodes = append(nodes, child.node())
			devices = append(devices, child.device())
			links = append(links, model.TopologyLink{
				Source: parent, Target: child.ID(), Type: "child",
				LinkQuality: child.LinkQuality, LinkMargin: child.LinkMargin,
				AverageRSSI: child.AverageRSSI, LastRSSI: child.LastRSSI,
				FrameErrorRate: child.FrameErrorRate, MessageErrorRate: child.MessageErrorRate,
			})
		}
	}

	latency := time.Since(started).Milliseconds()
	return &model.DeviceInventory{
			Status: "available", CollectionSupported: true, Items: devices,
			Source: "OpenThread mesh diagnostics", RequestLatencyMs: latency,
		}, &model.Topology{
			Status: "available", CollectionSupported: true, Nodes: nodes, Links: links,
			Source: "OpenThread mesh diagnostics", RequestLatencyMs: latency,
		}, nil
}

// srpAddresses reads the border router's SRP registry, keyed by extended address.
// A Thread device registering with SRP uses its extended address as the hostname
// ("0102030405060708.default.service.arpa."), which is exactly how this app keys
// devices, so the two join directly.
func (c *Client) srpAddresses(ctx context.Context) map[string][]string {
	lines, err := c.Execute(ctx, "srp server host")
	if err != nil {
		return nil
	}
	hosts := map[string][]string{}
	current := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if name, _, ok := strings.Cut(trimmed, ".default.service.arpa."); ok && !strings.Contains(name, " ") {
			current = strings.ToLower(name)
			continue
		}
		if current == "" {
			continue
		}
		// A deleted registration is a lease that has not expired yet, not a device.
		if trimmed == "deleted: true" {
			delete(hosts, current)
			current = ""
			continue
		}
		if list, ok := strings.CutPrefix(trimmed, "addresses: ["); ok {
			for _, candidate := range strings.Split(strings.TrimSuffix(list, "]"), ",") {
				candidate = strings.TrimSpace(candidate)
				if _, err := netip.ParseAddr(candidate); err == nil {
					hosts[current] = append(hosts[current], candidate)
				}
			}
		}
	}
	return hosts
}

// meshStructureTTL bounds how often the over-the-air queries repeat. The shape of
// the mesh changes slowly; ages and signal do not, and come from local reads.
// Every meshdiag query is a transaction with another router, so this bounds real
// mesh traffic. The shape of the network changes slowly; ages and signal come from
// local reads, so a longer TTL costs only how quickly a *new* device appears.
const meshStructureTTL = 30 * time.Second

// meshStructure returns the router set, re-querying the mesh only when the cached
// copy has expired.
func (c *Client) meshStructure(ctx context.Context) ([]meshRouter, map[string][]meshChild, map[string]map[string][]string, error) {
	c.structure.mu.Lock()
	defer c.structure.mu.Unlock()
	if len(c.structure.routers) > 0 && time.Since(c.structure.fetched) < meshStructureTTL {
		return c.structure.routers, c.structure.children, c.structure.addresses, nil
	}
	lines, err := c.Execute(ctx, "meshdiag topology")
	if err != nil {
		return nil, nil, nil, err
	}
	routers := parseMeshTopology(lines)
	if len(routers) == 0 {
		return nil, nil, nil, fmt.Errorf("meshdiag topology returned no routers")
	}
	// Each router costs two more queries, so they belong behind the same TTL rather
	// than being repeated on every poll.
	children := map[string][]meshChild{}
	addresses := map[string]map[string][]string{}
	for _, router := range routers {
		kids, err := c.childrenOf(ctx, router.RLOC16)
		if err != nil {
			continue // an unreachable router costs only its own children
		}
		children[router.RLOC16] = kids
		addresses[router.RLOC16] = c.childAddresses(ctx, router.RLOC16)
	}
	c.structure.routers, c.structure.children, c.structure.addresses = routers, children, addresses
	c.structure.fetched = time.Now()
	return routers, children, addresses, nil
}

// localNeighbours reads the neighbour table, which the local node maintains itself.
// It costs no radio time and is current to the second, unlike a meshdiag query.
func (c *Client) localNeighbours(ctx context.Context) map[string]routerMetric {
	lines, err := c.Execute(ctx, "neighbor table")
	if err != nil {
		return nil
	}
	fresh := map[string]routerMetric{}
	for _, row := range pipeTable(lines) {
		ext := strings.ToLower(row["extended mac"])
		if ext == "" {
			continue
		}
		fresh[ext] = routerMetric{
			Age: intPtr(row["age"]), AverageRSSI: intPtr(row["avg rssi"]),
			LastRSSI: intPtr(row["last rssi"]), LinkQualityIn: intPtr(row["lq in"]),
		}
	}
	return fresh
}

// routerMetrics collects what the local node knows about every router: link
// quality and route cost from the router table, and signal strength from the
// neighbor table for routers we can hear directly. Both are local reads with no
// over-the-air cost, unlike meshdiag.
type routerMetric struct {
	LinkQualityIn    *int
	LinkQualityOut   *int
	RouteCost        *int
	AverageRSSI      *int
	LastRSSI         *int
	Age              *int // seconds since we last heard this router
	FrameErrorRate   *float64
	MessageErrorRate *float64
}

func (c *Client) routerMetrics(ctx context.Context) map[string]routerMetric {
	metrics := map[string]routerMetric{}
	if lines, err := c.Execute(ctx, "router table"); err == nil {
		for _, row := range pipeTable(lines) {
			ext := strings.ToLower(row["extended mac"])
			if ext == "" {
				continue
			}
			metric := metrics[ext]
			metric.LinkQualityIn = intPtr(row["lq in"])
			metric.LinkQualityOut = intPtr(row["lq out"])
			metric.RouteCost = intPtr(row["path cost"])
			metric.Age = intPtr(row["age"])
			metrics[ext] = metric
		}
	}
	// "neighbor linkquality" is the only place a *router* neighbour's error rates
	// appear; meshdiag childtable covers children only, which is why routers showed
	// no retry data at all.
	if lines, err := c.Execute(ctx, "neighbor linkquality"); err == nil {
		for _, row := range pipeTable(lines) {
			ext := strings.ToLower(row["extended mac"])
			if ext == "" {
				continue
			}
			metric := metrics[ext]
			metric.FrameErrorRate = ratePtr(row["frame error"])
			metric.MessageErrorRate = ratePtr(row["msg error"])
			if metric.AverageRSSI == nil {
				metric.AverageRSSI = intPtr(row["avg rss"])
			}
			if metric.LastRSSI == nil {
				metric.LastRSSI = intPtr(row["last rss"])
			}
			metrics[ext] = metric
		}
	}
	if lines, err := c.Execute(ctx, "neighbor table"); err == nil {
		for _, row := range pipeTable(lines) {
			ext := strings.ToLower(row["extended mac"])
			if ext == "" {
				continue
			}
			metric := metrics[ext]
			metric.AverageRSSI = intPtr(row["avg rssi"])
			metric.LastRSSI = intPtr(row["last rssi"])
			if metric.LinkQualityIn == nil {
				metric.LinkQualityIn = intPtr(row["lq in"])
			}
			if age := intPtr(row["age"]); age != nil {
				metric.Age = age
			}
			metrics[ext] = metric
		}
	}
	return metrics
}

// localAddresses lists the border router's own routable Thread addresses. It drops
// the link-local one and every anycast/routing locator — the "…:0:ff:fe00:xxxx"
// forms, which encode an RLOC16 or ALOC and change as the node's role changes —
// leaving the mesh-local EID and the OMR address, which are the stable ones.
func (c *Client) localAddresses(ctx context.Context) []string {
	lines, err := c.Execute(ctx, "ipaddr")
	if err != nil {
		return nil
	}
	var addresses []string
	for _, line := range lines {
		candidate := strings.TrimSpace(line)
		parsed, err := netip.ParseAddr(candidate)
		if err != nil || !parsed.Is6() || parsed.IsLinkLocalUnicast() {
			continue
		}
		if strings.Contains(strings.ToLower(candidate), ":0:ff:fe00:") {
			continue
		}
		addresses = append(addresses, candidate)
	}
	return addresses
}

// omrPrefix reads the off-mesh-routable prefix the border router advertises. It is
// best-effort: without it a child's addresses are still listed, just unclassified.
func (c *Client) omrPrefix(ctx context.Context) netip.Prefix {
	lines, err := c.Execute(ctx, "br omrprefix")
	if err != nil {
		return netip.Prefix{}
	}
	// "Favored: fd11:2233:4455:1::/64 prf:low" — prefer Favored, fall back to Local.
	var local netip.Prefix
	for _, line := range lines {
		label, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		prefix, err := netip.ParsePrefix(strings.Fields(strings.TrimSpace(value))[0])
		if err != nil {
			continue
		}
		switch label {
		case "Favored":
			return prefix
		case "Local":
			local = prefix
		}
	}
	return local
}

// childAddresses maps each child's RLOC16 to the addresses its parent has
// registered for it. Best-effort: an error simply leaves the addresses unknown.
func (c *Client) childAddresses(ctx context.Context, routerRLOC string) map[string][]string {
	lines, err := c.Execute(ctx, "meshdiag childip6 "+routerRLOC)
	if err != nil {
		return nil
	}
	addresses := map[string][]string{}
	current := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(trimmed, "child-rloc16:"); ok {
			current = strings.ToLower(strings.TrimSpace(rest))
			continue
		}
		if current == "" {
			continue
		}
		if _, err := netip.ParseAddr(trimmed); err == nil {
			addresses[current] = append(addresses[current], trimmed)
		}
	}
	return addresses
}

// pickOMR selects the off-mesh-routable address — the one reachable from outside
// the mesh. A child also holds a mesh-local address, and both are ULAs, so they
// cannot be told apart by shape alone.
func pickOMR(addresses []string, omr netip.Prefix) string {
	if !omr.IsValid() {
		return ""
	}
	for _, candidate := range addresses {
		if parsed, err := netip.ParseAddr(candidate); err == nil && omr.Contains(parsed) {
			return candidate
		}
	}
	return ""
}

func (c *Client) childrenOf(ctx context.Context, rloc16 string) ([]meshChild, error) {
	lines, err := c.Execute(ctx, "meshdiag childtable "+rloc16)
	if err != nil {
		return nil, err
	}
	return parseMeshChildTable(lines), nil
}

type meshRouter struct {
	RouterID       int
	RLOC16         string
	ExtAddress     string
	IsSelf         bool
	IsLeader       bool
	IsBorderRouter bool
	Links          map[int]int // peer router ID -> link quality
}

type meshChild struct {
	RLOC16           string
	ExtAddress       string
	ParentID         string
	Timeout          *int
	LinkQuality      *int
	LinkMargin       *int
	AverageRSSI      *int
	LastRSSI         *int
	FrameErrorRate   *float64
	MessageErrorRate *float64
	RxOnWhenIdle     *bool
	FullThreadDevice *bool
	IPv6Addresses    []string
	OMRIPv6Address   string
	Age              *int           // seconds since the parent last heard it
	ConnectedFor     *time.Duration // length of the current attachment
}

func (m meshChild) ID() string {
	if m.ExtAddress != "" {
		return m.ExtAddress
	}
	return m.ParentID + "/child/" + m.RLOC16
}

func (m meshChild) node() model.TopologyNode {
	return model.TopologyNode{
		ID: m.ID(), Role: "child", RLOC16: m.RLOC16, ExtendedAddress: m.ExtAddress,
		ParentID: m.ParentID, LinkQuality: m.LinkQuality, Timeout: m.Timeout,
		RxOnWhenIdle: m.RxOnWhenIdle, DeviceTypeFTD: m.FullThreadDevice,
	}
}

// seenTimes converts the CLI's relative ages into the absolute timestamps the
// model carries. "age" is seconds since the parent last heard the child, and
// "conn-time" is how long the current attachment has lasted — which is what the
// UI means by first discovered, since roaming starts a new attachment.
func (m meshChild) seenTimes(now time.Time) (first, last *time.Time) {
	if m.Age != nil {
		seen := now.Add(-time.Duration(*m.Age) * time.Second)
		last = &seen
	}
	if m.ConnectedFor != nil {
		since := now.Add(-*m.ConnectedFor)
		first = &since
	}
	return first, last
}

func (m meshChild) device() model.Device {
	first, last := m.seenTimes(time.Now().UTC())
	return model.Device{
		ID: m.ID(), Role: "child", ExtendedAddress: m.ExtAddress, RLOC16: m.RLOC16,
		Parent: m.ParentID, LinkQuality: m.LinkQuality, LinkMargin: m.LinkMargin,
		RSSI: m.AverageRSSI, IPv6Addresses: m.IPv6Addresses, OMRIPv6Address: m.OMRIPv6Address,
		FirstSeen: first, LastSeen: last,
		FrameErrorRate: m.FrameErrorRate, MessageErrorRate: m.MessageErrorRate,
	}
}

// meshdiag topology prints one block per router:
//
//	id:28 rloc16:0x7000 ext-addr:1a2b3c4d5e6f7a8b ver:5 - me - leader - br
//	    3-links:{ 02 }
var (
	routerHeader = regexp.MustCompile(`^id:(\d+)\s+rloc16:(0x[0-9a-fA-F]+)\s+ext-addr:([0-9a-fA-F]+)`)
	linkSet      = regexp.MustCompile(`^(\d)-links:\{([^}]*)\}`)
)

func parseMeshTopology(lines []string) []meshRouter {
	var routers []meshRouter
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if match := routerHeader.FindStringSubmatch(trimmed); match != nil {
			id, _ := strconv.Atoi(match[1])
			routers = append(routers, meshRouter{
				RouterID: id, RLOC16: strings.ToLower(match[2]), ExtAddress: strings.ToLower(match[3]),
				// The trailing " - me - leader - br" markers describe this router.
				IsSelf:         strings.Contains(trimmed, "- me"),
				IsLeader:       strings.Contains(trimmed, "- leader"),
				IsBorderRouter: strings.Contains(trimmed, "- br"),
				Links:          map[int]int{},
			})
			continue
		}
		if len(routers) == 0 {
			continue
		}
		if match := linkSet.FindStringSubmatch(trimmed); match != nil {
			quality, _ := strconv.Atoi(match[1])
			for _, peer := range strings.Fields(match[2]) {
				if id, err := strconv.Atoi(peer); err == nil {
					routers[len(routers)-1].Links[id] = quality
				}
			}
		}
	}
	return routers
}

// meshdiag childtable prints one block per child, the header line naming it and
// indented lines carrying its metrics.
var childHeader = regexp.MustCompile(`^rloc16:(0x[0-9a-fA-F]+)\s+ext-addr:([0-9a-fA-F]+)`)

func parseMeshChildTable(lines []string) []meshChild {
	var children []meshChild
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if match := childHeader.FindStringSubmatch(trimmed); match != nil {
			children = append(children, meshChild{
				RLOC16: strings.ToLower(match[1]), ExtAddress: strings.ToLower(match[2]),
			})
			continue
		}
		if len(children) == 0 {
			continue
		}
		child := &children[len(children)-1]
		for _, field := range strings.Fields(trimmed) {
			key, value, ok := strings.Cut(field, ":")
			if !ok {
				continue
			}
			switch key {
			case "timeout":
				child.Timeout = intPtr(value)
			case "age":
				child.Age = intPtr(value)
			case "conn-time":
				// Rejoined by the whole field: "conn-time:00:27:34" splits on ":" too.
				if duration, ok := parseCLIDuration(strings.TrimPrefix(trimmed, "conn-time:")); ok {
					child.ConnectedFor = &duration
				}
			case "ave":
				child.AverageRSSI = intPtr(value)
			case "last":
				child.LastRSSI = intPtr(value)
			case "margin":
				child.LinkMargin = intPtr(value)
				// The CLI reports a margin in dB; the UI's LQI scale is 0-3, and
				// OpenThread's own mapping is 3 above 20dB, 2 above 10, 1 above 2.
				if child.LinkMargin != nil {
					child.LinkQuality = qualityFromMargin(*child.LinkMargin)
				}
			case "frame":
				child.FrameErrorRate = ratePtr(value)
			case "msg":
				child.MessageErrorRate = ratePtr(value)
			case "rx-on":
				child.RxOnWhenIdle = boolPtr(value == "yes")
			case "type":
				child.FullThreadDevice = boolPtr(value == "ftd")
			}
		}
	}
	return children
}

func qualityFromMargin(margin int) *int {
	quality := 0
	switch {
	case margin > 20:
		quality = 3
	case margin > 10:
		quality = 2
	case margin > 2:
		quality = 1
	}
	return &quality
}

// parseCLIDuration reads the CLI's elapsed-time format, "HH:MM:SS[.mmm]" with an
// optional "Nd." day prefix as used by uptime and conn-time.
func parseCLIDuration(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	total := time.Duration(0)
	if days, rest, ok := strings.Cut(value, " days "); ok {
		count, err := strconv.Atoi(strings.TrimSpace(days))
		if err != nil {
			return 0, false
		}
		total += time.Duration(count) * 24 * time.Hour
		value = strings.TrimSpace(rest)
	} else if days, rest, ok := strings.Cut(value, " day "); ok {
		count, err := strconv.Atoi(strings.TrimSpace(days))
		if err != nil {
			return 0, false
		}
		total += time.Duration(count) * 24 * time.Hour
		value = strings.TrimSpace(rest)
	}
	if days, rest, ok := strings.Cut(value, "d."); ok {
		count, err := strconv.Atoi(strings.TrimSpace(days))
		if err != nil {
			return 0, false
		}
		total += time.Duration(count) * 24 * time.Hour
		value = rest
	}
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0, false
	}
	hours, err1 := strconv.Atoi(parts[0])
	minutes, err2 := strconv.Atoi(parts[1])
	seconds, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	total += time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute
	total += time.Duration(seconds * float64(time.Second))
	return total, true
}

func intPtr(value string) *int {
	parsed, err := strconv.Atoi(strings.TrimSuffix(value, ","))
	if err != nil {
		return nil
	}
	return &parsed
}

func ratePtr(value string) *float64 {
	// meshdiag writes "30.97%", neighbor linkquality writes "48.68 %".
	cleaned := strings.TrimSpace(strings.ReplaceAll(strings.TrimSuffix(strings.TrimSpace(value), ","), "%", ""))
	parsed, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return nil
	}
	// The CLI prints two decimals of a percentage, so four decimals of a fraction
	// is the full precision; dividing without rounding leaves 0.16010000000000002.
	fraction := math.Round(parsed*100) / 10000
	return &fraction
}

func boolPtr(value bool) *bool { return &value }
