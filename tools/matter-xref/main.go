// Command matter-xref matches Matter node IDs to the devices OTBR Insight shows.
//
// A Matter QR code carries no Thread identity — only vendor/product IDs, a
// discriminator and the setup passcode — so it cannot be matched to a mesh node.
// The operational mDNS advertisement can: a commissioned Thread device publishes
// _matter._tcp with an instance name of "<compressed-fabric-id>-<node-id>" and,
// crucially, an SRV target whose hostname is the device's 802.15.4 extended
// address. That extended address is exactly what OTBR Insight keys devices on, so
// the two join exactly.
//
// Browsing mDNS needs the OS resolver (no Go standard library support and this
// repo carries no dependencies), so this shells out to dns-sd on macOS or
// avahi-browse on Linux. That is why it is a tool and not part of the server,
// which talks only to the OTBR REST API.
//
// Usage: go run ./tools/matter-xref [-api http://127.0.0.1:8088] [-wait 6s]
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type advert struct {
	fabric string
	node   uint64
	ext    string // lowercase extended address, from the SRV target hostname
}

type device struct {
	ExtendedAddress string `json:"extendedAddress"`
	Role            string `json:"role"`
	RLOC16          string `json:"rloc16"`
	Name            string `json:"name"`
	CustomName      string `json:"customName"`
	Parent          string // filled in from the topology, not the inventory
}

func main() {
	api := flag.String("api", "http://127.0.0.1:8088", "OTBR Insight base URL")
	service := flag.String("service", "_matter._tcp", "DNS-SD service type to browse")
	wait := flag.Duration("wait", 6*time.Second, "how long to collect mDNS advertisements")
	flag.Parse()

	adverts, browser, err := browse(*service, *wait)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mdns browse:", err)
		os.Exit(1)
	}
	devices, err := fetchDevices(*api)
	if err != nil {
		// The advertisements are still worth printing; only the join is lost.
		fmt.Fprintf(os.Stderr, "warning: could not read %s: %v\n", *api, err)
	}
	// The device inventory omits rloc16 for children; the topology has it, keyed by
	// the same extended address. Fill the gap so the table can be read against the map.
	if err := enrichFromTopology(*api, devices); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not read topology: %v\n", err)
	}
	report(adverts, devices, browser)
}

// browse returns the advertisements seen, and the name of the tool used.
func browse(service string, wait time.Duration) ([]advert, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()

	if path, err := exec.LookPath("dns-sd"); err == nil {
		// dns-sd never exits on its own; the context deadline stops it.
		out, _ := exec.CommandContext(ctx, path, "-Z", service, "local.").Output()
		return parseDNSSD(string(out)), "dns-sd", nil
	}
	if path, err := exec.LookPath("avahi-browse"); err == nil {
		out, err := exec.CommandContext(ctx, path, "-rpt", service).Output()
		if err != nil && len(out) == 0 {
			return nil, "avahi-browse", err
		}
		return parseAvahi(string(out)), "avahi-browse", nil
	}
	return nil, "", errors.New("need dns-sd (macOS) or avahi-browse (Linux) on PATH")
}

// dns-sd -Z prints zone-file style records:
//
//	<fabric>-<node>._matter._tcp  SRV  0 0 5540 <EXTADDR>.local. ; comment
var dnssdSRV = regexp.MustCompile(`^([0-9A-Fa-f]{16})-([0-9A-Fa-f]{1,16})\.` +
	`[^\s]*\s+SRV\s+\S+\s+\S+\s+\S+\s+([0-9A-Fa-f]{16})\.local\.`)

func parseDNSSD(out string) []advert {
	var adverts []advert
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		match := dnssdSRV.FindStringSubmatch(strings.TrimSpace(scanner.Text()))
		if match == nil {
			continue
		}
		node, err := strconv.ParseUint(match[2], 16, 64)
		if err != nil {
			continue
		}
		adverts = append(adverts, advert{fabric: strings.ToUpper(match[1]), node: node, ext: strings.ToLower(match[3])})
	}
	return adverts
}

// avahi-browse -rpt prints one ';'-separated record per line; resolved entries
// start with '=' and carry the hostname in field 7:
//
//	=;wlan0;IPv6;<fabric>-<node>;_matter._tcp;local;<EXTADDR>.local;<addr>;5540;"..."
func parseAvahi(out string) []advert {
	var adverts []advert
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ";")
		if len(fields) < 7 || fields[0] != "=" {
			continue
		}
		// avahi escapes some characters in the instance name; only hex and '-' matter here.
		name, host := fields[3], strings.TrimSuffix(fields[6], ".local")
		fabric, nodeHex, ok := strings.Cut(name, "-")
		if !ok || len(fabric) != 16 || len(host) != 16 {
			continue
		}
		node, err := strconv.ParseUint(nodeHex, 16, 64)
		if err != nil {
			continue
		}
		// The same service resolves once per interface and protocol.
		key := fabric + nodeHex + host
		if seen[key] {
			continue
		}
		seen[key] = true
		adverts = append(adverts, advert{fabric: strings.ToUpper(fabric), node: node, ext: strings.ToLower(host)})
	}
	return adverts
}

func fetchDevices(base string) (map[string]device, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(strings.TrimRight(base, "/") + "/api/v1/devices")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	var payload struct {
		Data struct {
			Items []device `json:"items"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	devices := make(map[string]device, len(payload.Data.Items))
	for _, item := range payload.Data.Items {
		devices[strings.ToLower(item.ExtendedAddress)] = item
	}
	return devices, nil
}

// enrichFromTopology fills in rloc16 and parent for devices the map knows more about.
func enrichFromTopology(base string, devices map[string]device) error {
	if devices == nil {
		return nil
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(strings.TrimRight(base, "/") + "/api/v1/topology")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	var payload struct {
		Data struct {
			Nodes []struct {
				ExtendedAddress string `json:"extendedAddress"`
				RLOC16          string `json:"rloc16"`
				ParentID        string `json:"parentId"`
			} `json:"nodes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	for _, node := range payload.Data.Nodes {
		ext := strings.ToLower(node.ExtendedAddress)
		if ext == "" {
			continue
		}
		if existing, ok := devices[ext]; ok {
			if existing.RLOC16 == "" {
				existing.RLOC16 = node.RLOC16
			}
			existing.Parent = node.ParentID
			devices[ext] = existing
		}
	}
	return nil
}

func report(adverts []advert, devices map[string]device, browser string) {
	if len(adverts) == 0 {
		fmt.Printf("No Matter advertisements seen via %s.\n", browser)
		fmt.Println("Devices only advertise once commissioned, and mDNS does not cross subnets.")
		return
	}
	// One device can be on several fabrics with a different node ID on each.
	byFabric := map[string]map[string][]uint64{}
	for _, a := range adverts {
		if byFabric[a.fabric] == nil {
			byFabric[a.fabric] = map[string][]uint64{}
		}
		byFabric[a.fabric][a.ext] = appendUnique(byFabric[a.fabric][a.ext], a.node)
	}
	fabrics := make([]string, 0, len(byFabric))
	for fabric := range byFabric {
		fabrics = append(fabrics, fabric)
	}
	sort.Strings(fabrics)

	matched := map[string]bool{}
	for _, fabric := range fabrics {
		hosts := byFabric[fabric]
		fmt.Printf("\nFabric %s — %d device(s)\n", fabric, len(hosts))
		fmt.Printf("  %-12s  %-18s  %-8s  %-8s  %-14s  %s\n", "MATTER NODE", "EXTENDED ADDRESS", "ROLE", "RLOC16", "PARENT", "OTBR INSIGHT")
		exts := make([]string, 0, len(hosts))
		for ext := range hosts {
			exts = append(exts, ext)
		}
		sort.Slice(exts, func(i, j int) bool { return hosts[exts[i]][0] < hosts[exts[j]][0] })
		for _, ext := range exts {
			nodes := hosts[ext]
			sort.Slice(nodes, func(i, j int) bool { return nodes[i] < nodes[j] })
			labels := make([]string, 0, len(nodes))
			for _, node := range nodes {
				labels = append(labels, strconv.FormatUint(node, 10))
			}
			name, role, rloc, parent := "— not in OTBR Insight —", "-", "-", "-"
			if d, ok := devices[ext]; ok {
				matched[ext] = true
				name = firstNonEmpty(d.CustomName, d.Name, "(unnamed)")
				role = firstNonEmpty(d.Role, "-")
				rloc = firstNonEmpty(d.RLOC16, "-")
				parent = firstNonEmpty(shortExt(d.Parent), "-")
			}
			fmt.Printf("  %-12s  %-18s  %-8s  %-8s  %-14s  %s\n", strings.Join(labels, ","), ext, role, rloc, parent, name)
		}
	}

	// Devices the mesh knows but Matter does not advertise are worth naming: they
	// are either not commissioned, or commissioned and currently unreachable.
	var unadvertised []string
	for ext := range devices {
		if !matched[ext] {
			unadvertised = append(unadvertised, ext)
		}
	}
	if len(unadvertised) > 0 {
		sort.Strings(unadvertised)
		fmt.Printf("\nIn OTBR Insight but not advertising Matter (%d):\n", len(unadvertised))
		for _, ext := range unadvertised {
			d := devices[ext]
			fmt.Printf("  %-18s  %-8s  %s\n", ext, firstNonEmpty(d.Role, "-"), firstNonEmpty(d.CustomName, d.Name, "(unnamed)"))
		}
	}
}

// shortExt trims a parent's extended address to something the table can hold.
func shortExt(ext string) string {
	if len(ext) > 12 {
		return ext[:12] + "…"
	}
	return ext
}

func appendUnique(nodes []uint64, node uint64) []uint64 {
	for _, existing := range nodes {
		if existing == node {
			return nodes
		}
	}
	return append(nodes, node)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
