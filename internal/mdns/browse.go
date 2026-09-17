// Package mdns is a one-shot multicast DNS browser: it asks the LAN which
// instances of a service exist and collects the answers for a short while.
//
// It exists so the server can see Matter nodes on the LAN without shelling out
// to dns-sd or avahi-browse (the tool in tools/matter-xref does that) and without
// a DNS library beyond golang.org/x/net's wire-format parser. It is a browser,
// not a responder: it sends a query and reads replies, nothing more.
package mdns

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Instance is one advertised service instance, with whatever the responder
// volunteered alongside the pointer record. Host, Port, TXT and Addresses are
// filled only when the reply carried them; most responders include all of them.
type Instance struct {
	// Name is the instance label, e.g. "1122334455667788-0000000000000014".
	Name string
	// Service is the type browsed, e.g. "_matter._tcp".
	Service   string
	Host      string // SRV target, e.g. "0102030405060708.local."
	Port      int
	TXT       map[string]string
	Addresses []string
}

// Browser browses over IPv4 multicast on the default interface.
type Browser struct {
	// Wait bounds how long replies are collected; zero means three seconds.
	Wait time.Duration
}

var (
	mdnsGroupV4 = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	localDomain = "local."
)

const (
	// serviceTypeEnumeration is the RFC 6763 §9 meta-query listing every service
	// type advertised on the link, subtypes included.
	serviceTypeEnumeration = "_services._dns-sd._udp.local."
	// subtypeMarker separates a subtype label from the service it narrows,
	// e.g. "_I2C49...._sub._matter._tcp.local.".
	subtypeMarker = "._sub."
	// maxSubtypeQueries bounds how many subtypes one round asks about, so a LAN
	// advertising many of them cannot turn a browse into a flood.
	maxSubtypeQueries = 32
)

// Browse asks the LAN which instances of a service exist and returns every one
// that answered before Wait elapsed or ctx was cancelled.
//
// The query goes out from two sockets. One is bound to port 5353 and joined to
// the group, sharing the port with the host's own responder (avahi,
// mDNSResponder) through address reuse: a query from 5353 is a normal multicast
// query, and responders answer over multicast in as many packets as they need.
// The other is an ephemeral port, which RFC 6762 §6.7 calls a legacy unicast
// query: responders reply straight to it, which works on a host that cannot bind
// 5353 — but a legacy reply must fit one packet, and a LAN of two dozen Matter
// nodes overflows it, so the same nodes went missing on every browse until the
// multicast query was added. Queries are repeated, since mDNS is UDP and one
// loss would drop a whole responder.
//
// **Subtypes are browsed too, and that is not a refinement.** A plain PTR query
// for the service is answered at each responder's discretion, and a node that
// stays quiet is simply absent — measured here, one Matter node never answered
// the general query in any run, so the whole Matter fabric it was the only
// member of was missing from every browse. So the first round also asks the
// service-type enumeration ("_services._dns-sd._udp.local."), which names every
// subtype registered on the LAN — for Matter, one "_I<compressed-fabric-id>" per
// fabric — and later rounds query each of those alongside the base service. A
// node that ignores the broad question still answers the narrow one.
func (b Browser) Browse(ctx context.Context, service string) ([]Instance, error) {
	wait := b.Wait
	if wait <= 0 {
		wait = 3 * time.Second
	}
	service = strings.TrimSuffix(strings.TrimSpace(service), ".")
	if service == "" {
		return nil, errors.New("empty service")
	}
	queryName := service + "." + localDomain

	unicast, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("open mDNS socket: %w", err)
	}
	defer unicast.Close()
	// Best effort: a host that refuses the group still gets unicast replies.
	multicast, err := net.ListenMulticastUDP("udp4", nil, mdnsGroupV4)
	if err == nil {
		defer multicast.Close()
	}

	// One datagram per question, not one packet asking everything. A responder
	// answers a multi-question query in a single response, and where that
	// response is a legacy unicast one it must fit one packet: asking four
	// questions at once made the truncation worse, and a browse from the border
	// router fell from 22 nodes to 17. Separate questions get separate replies.
	send := func(names []string) error {
		var firstErr error
		for _, name := range names {
			query, err := packQuery(name)
			if err != nil {
				return err
			}
			_, err = unicast.WriteToUDP(query, mdnsGroupV4)
			if multicast != nil {
				if _, merr := multicast.WriteToUDP(query, mdnsGroupV4); merr == nil {
					err = nil
				}
			}
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}
	collector := newCollector(service, queryName)
	// Round one learns which subtypes exist; every later round asks them directly.
	if err := send([]string{queryName, serviceTypeEnumeration}); err != nil {
		return nil, fmt.Errorf("send mDNS query: %w", err)
	}

	deadline := time.Now().Add(wait)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	packets := make(chan []byte, 64)
	readers := 1
	go readUntil(unicast, deadline, packets)
	if multicast != nil {
		readers++
		go readUntil(multicast, deadline, packets)
	}
	resend := time.NewTicker(wait / 4)
	defer resend.Stop()
	for readers > 0 {
		select {
		case <-ctx.Done():
			return collector.instances(), ctx.Err()
		case <-resend.C:
			_ = send(collector.queryNames())
		case packet, ok := <-packets:
			if !ok || packet == nil {
				readers--
				continue
			}
			collector.absorb(packet)
		}
	}
	return collector.instances(), nil
}

// readUntil delivers every datagram read before the deadline, then a nil.
func readUntil(conn *net.UDPConn, deadline time.Time, out chan<- []byte) {
	defer func() { out <- nil }()
	_ = conn.SetReadDeadline(deadline)
	buf := make([]byte, 65535)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		packet := make([]byte, n)
		copy(packet, buf[:n])
		out <- packet
	}
}

func packQuery(name string) ([]byte, error) {
	qname, err := dnsmessage.NewName(name)
	if err != nil {
		return nil, fmt.Errorf("mDNS name %q: %w", name, err)
	}
	msg := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: uint16(rand.Uint32())},
		Questions: []dnsmessage.Question{{Name: qname, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}},
	}
	return msg.Pack()
}

// collector merges records across replies. Records arrive in any order and a
// responder may split them across packets, so everything is keyed by name and
// joined only when read out.
type collector struct {
	service   string
	queryName string
	suffix    string          // "." + queryName, what an instance FQDN ends with
	seen      map[string]bool // instance FQDN (lower) -> seen via PTR
	subtypes  map[string]bool // subtype FQDN (lower) -> worth querying directly
	srv       map[string]*dnsmessage.SRVResource
	txt       map[string][]string
	addresses map[string][]string // host FQDN (lower) -> addresses
	order     []string
	subOrder  []string
}

func newCollector(service, queryName string) *collector {
	lower := strings.ToLower(queryName)
	return &collector{
		service: service, queryName: lower, suffix: "." + lower,
		seen: map[string]bool{}, subtypes: map[string]bool{}, srv: map[string]*dnsmessage.SRVResource{},
		txt: map[string][]string{}, addresses: map[string][]string{},
	}
}

// queryNames is what the next round asks for: the service itself plus every
// subtype heard so far, since a node that ignores the broad question may still
// answer the narrow one.
func (c *collector) queryNames() []string {
	names := make([]string, 0, 1+len(c.subOrder))
	names = append(names, c.queryName)
	for _, subtype := range c.subOrder {
		if len(names) > maxSubtypeQueries {
			break
		}
		names = append(names, subtype)
	}
	return names
}

func (c *collector) absorb(packet []byte) {
	var parser dnsmessage.Parser
	header, err := parser.Start(packet)
	if err != nil || !header.Response {
		return
	}
	if err := parser.SkipAllQuestions(); err != nil {
		return
	}
	answers, err := parser.AllAnswers()
	if err != nil {
		return
	}
	_ = parser.SkipAllAuthorities()
	additionals, _ := parser.AllAdditionals()
	for _, rr := range append(answers, additionals...) {
		c.record(rr)
	}
}

func (c *collector) record(rr dnsmessage.Resource) {
	name := strings.ToLower(rr.Header.Name.String())
	switch body := rr.Body.(type) {
	case *dnsmessage.PTRResource:
		// A PTR is keyed on what it points AT, not on which question drew it out:
		// the same instance is named by the base service and by each of its
		// subtypes, and the service-type enumeration names the subtypes.
		target := strings.ToLower(body.PTR.String())
		if target == c.queryName || !strings.HasSuffix(target, c.suffix) {
			return
		}
		if strings.Contains(target, subtypeMarker) {
			if !c.subtypes[target] {
				c.subtypes[target] = true
				c.subOrder = append(c.subOrder, target)
			}
			return
		}
		if !c.seen[target] {
			c.seen[target] = true
			c.order = append(c.order, target)
		}
	case *dnsmessage.SRVResource:
		c.srv[name] = body
	case *dnsmessage.TXTResource:
		c.txt[name] = body.TXT
	case *dnsmessage.AResource:
		c.addAddress(name, netip.AddrFrom4(body.A))
	case *dnsmessage.AAAAResource:
		c.addAddress(name, netip.AddrFrom16(body.AAAA))
	}
}

func (c *collector) addAddress(host string, addr netip.Addr) {
	text := addr.String()
	for _, existing := range c.addresses[host] {
		if existing == text {
			return
		}
	}
	c.addresses[host] = append(c.addresses[host], text)
}

func (c *collector) instances() []Instance {
	suffix := c.suffix
	out := make([]Instance, 0, len(c.order))
	for _, fqdn := range c.order {
		instance := Instance{Name: strings.TrimSuffix(fqdn, suffix), Service: c.service}
		// PTR targets are lower-cased for joining; the label's own case is not
		// significant in DNS, and Matter's hex names read the same either way.
		instance.Name = strings.ToUpper(instance.Name)
		if srv := c.srv[fqdn]; srv != nil {
			instance.Host = strings.ToLower(srv.Target.String())
			instance.Port = int(srv.Port)
			instance.Addresses = append([]string(nil), c.addresses[instance.Host]...)
			sort.Strings(instance.Addresses)
		}
		if txt := c.txt[fqdn]; len(txt) > 0 {
			instance.TXT = map[string]string{}
			for _, entry := range txt {
				key, value, _ := strings.Cut(entry, "=")
				if key != "" {
					instance.TXT[key] = value
				}
			}
		}
		out = append(out, instance)
	}
	return out
}
