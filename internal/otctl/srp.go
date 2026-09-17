package otctl

import (
	"context"
	"encoding/hex"
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// srpHost is one device's registration with the border router's SRP server:
// its addresses, its lease, and the services it advertises.
//
// SRP is how a Thread device tells the rest of the home what it is. The mesh has
// no multicast DNS — multicast is too expensive on a low-power radio — so each
// device registers once with the border router, which re-advertises the entry on
// the LAN over mDNS. A device that never registered may be perfectly healthy on
// Thread and still be invisible to every controller.
type srpHost struct {
	Addresses    []string
	Registration model.ServiceRegistration
	Services     []model.AdvertisedService
}

// srpDomain is the suffix OpenThread's SRP server appends to every name.
const srpDomain = ".default.service.arpa."

// srpRegistry reads both halves of the SRP registry — "srp server host" for
// addresses and leases, "srp server service" for what each host advertises —
// keyed by extended address, which is what a Thread device uses as its hostname
// ("0102030405060708.default.service.arpa.") and how this app keys devices.
//
// A host marked "deleted: true" is an unexpired key lease, not a live device: it
// is kept, flagged Lapsed and stripped of addresses, so a device that is still on
// the mesh can be shown as having lost its registration rather than never having
// had one. Best-effort: any failure yields an empty map.
func (c *Client) srpRegistry(ctx context.Context) map[string]*srpHost {
	hosts := map[string]*srpHost{}
	if lines, err := c.Execute(ctx, "srp server host"); err == nil {
		parseSRPHosts(lines, hosts)
	}
	if len(hosts) == 0 {
		return hosts
	}
	if lines, err := c.Execute(ctx, "srp server service"); err == nil {
		parseSRPServices(lines, hosts)
	}
	return hosts
}

// srpAddresses is the address-only view of the registry.
func (c *Client) srpAddresses(ctx context.Context) map[string][]string {
	addresses := map[string][]string{}
	for ext, host := range c.srpRegistry(ctx) {
		if len(host.Addresses) > 0 {
			addresses[ext] = host.Addresses
		}
	}
	return addresses
}

// parseSRPHosts reads "srp server host":
//
//	0102030405060708.default.service.arpa.
//	    deleted: false
//	    addresses: [fd11:2233:4455:1:f9b6:e8fa:35c7:8a25]
//	    lease: 7200
//	    key-lease: 680400
//	    remaining lease: 5562.705
//	    remaining key-lease: 678762.705
func parseSRPHosts(lines []string, hosts map[string]*srpHost) {
	var current *srpHost
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if name, ok := srpHostName(trimmed); ok {
			current = &srpHost{}
			hosts[name] = current
			continue
		}
		if current == nil {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "deleted":
			current.Registration.Lapsed = value == "true"
		case "addresses":
			current.Addresses = parseAddressList(value)
		case "lease":
			current.Registration.LeaseSeconds = parseSeconds(value)
		case "remaining lease":
			current.Registration.RemainingSeconds = parseSeconds(value)
		}
	}
	// A lapsed registration's addresses are what the device *had*; showing them
	// as current would misdirect a ping.
	for _, host := range hosts {
		if host.Registration.Lapsed {
			host.Addresses = nil
		}
	}
}

// parseSRPServices reads "srp server service":
//
//	8899AABBCCDDEEFF-00000000AABBCCDD._matter._tcp.default.service.arpa.
//	    deleted: false
//	    subtypes: _I8899AABBCCDDEEFF
//	    port: 5540
//	    ...
//	    TXT: [SII=3135383030, SAI=32353030, SAT=31303030]
//	    host: 0102030405060708.default.service.arpa.
//	    addresses: [fd11:2233:4455:1:f9b6:e8fa:35c7:8a25]
//
// A deleted service prints no host line at all, so it can only be attributed to
// a device by its instance name — which for a commissioning advert is opaque —
// and is therefore dropped. Only the live ones matter to a reader anyway.
func parseSRPServices(lines []string, hosts map[string]*srpHost) {
	var service *model.AdvertisedService
	deleted := false
	flush := func() {
		service = nil
		deleted = false
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasSuffix(trimmed, srpDomain) && !strings.HasPrefix(line, " ") {
			flush()
			instance, kind, ok := splitServiceName(strings.TrimSuffix(trimmed, srpDomain))
			if !ok {
				continue
			}
			service = &model.AdvertisedService{Instance: instance, Type: kind}
			if kind == "_matter._tcp" {
				service.FabricID, service.NodeID = matterIdentity(instance)
			}
			continue
		}
		if service == nil {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "deleted":
			deleted = value == "true"
		case "port":
			if port, err := strconv.Atoi(value); err == nil {
				service.Port = &port
			}
		case "TXT":
			service.TXT = parseTXT(value)
		case "host":
			// The host line is the last attribute worth reading and the one that
			// attributes the service, so it closes the record.
			if name, ok := srpHostName(value); ok && !deleted {
				if host := hosts[name]; host != nil {
					host.Services = append(host.Services, *service)
					if kind := service.Type; kind == "_matterc._udp" {
						host.Registration.Commissionable = true
					}
				}
			}
			flush()
		}
	}
}

// srpHostName returns the lower-cased extended address from a host name such as
// "0102030405060708.default.service.arpa.".
func srpHostName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	ext, ok := strings.CutSuffix(name, srpDomain)
	if !ok || ext == "" || strings.ContainsAny(ext, " .") {
		return "", false
	}
	if _, err := hex.DecodeString(ext); err != nil || len(ext) != 16 {
		return "", false
	}
	return strings.ToLower(ext), true
}

// splitServiceName separates "<instance>._matter._tcp" into its instance and
// service type. The type is the trailing "_name._proto" pair; the instance may
// itself contain dots or underscores.
func splitServiceName(name string) (instance, kind string, ok bool) {
	parts := strings.Split(name, ".")
	if len(parts) < 3 {
		return "", "", false
	}
	kind = parts[len(parts)-2] + "." + parts[len(parts)-1]
	if !strings.HasPrefix(parts[len(parts)-2], "_") || !strings.HasPrefix(parts[len(parts)-1], "_") {
		return "", "", false
	}
	return strings.Join(parts[:len(parts)-2], "."), kind, true
}

// matterIdentity decodes a Matter operational instance name,
// "<compressed-fabric-id>-<node-id>" as two 16-digit hex fields, into the
// fabric (as printed) and the node ID as a short "0x…" literal, since controllers
// show node IDs that way and "0x14" is easier to match than sixteen digits.
func matterIdentity(instance string) (fabric, node string) {
	fabric, node, ok := strings.Cut(instance, "-")
	if !ok || len(fabric) != 16 || len(node) != 16 {
		return "", ""
	}
	if _, err := hex.DecodeString(fabric); err != nil {
		return "", ""
	}
	if _, err := hex.DecodeString(node); err != nil {
		return "", ""
	}
	short := strings.TrimLeft(strings.ToUpper(node), "0")
	if short == "" {
		short = "0"
	}
	return strings.ToUpper(fabric), "0x" + short
}

// parseTXT decodes "[SII=3135383030, SAI=32353030]" into a map. The CLI prints
// each value hex-encoded; the decoded bytes are used when they are printable
// text (Matter's are decimal strings), otherwise the hex is kept as printed.
func parseTXT(value string) map[string]string {
	value = strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
	txt := map[string]string{}
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		key, encoded, _ := strings.Cut(entry, "=")
		decoded, err := hex.DecodeString(encoded)
		if err == nil && utf8.Valid(decoded) && printable(decoded) {
			txt[key] = string(decoded)
		} else {
			txt[key] = encoded
		}
	}
	if len(txt) == 0 {
		return nil
	}
	return txt
}

func printable(b []byte) bool {
	for _, r := range string(b) {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func parseAddressList(value string) []string {
	value = strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
	var addresses []string
	for _, candidate := range strings.Split(value, ",") {
		candidate = strings.TrimSpace(candidate)
		if _, err := netip.ParseAddr(candidate); err == nil {
			addresses = append(addresses, candidate)
		}
	}
	return addresses
}

// parseSeconds reads "5562.705" or "7200" as whole seconds.
func parseSeconds(value string) *int {
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return nil
	}
	seconds := int(f)
	return &seconds
}
