package mdns

import (
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func name(t *testing.T, s string) dnsmessage.Name {
	t.Helper()
	n, err := dnsmessage.NewName(s)
	if err != nil {
		t.Fatalf("NewName(%q): %v", s, err)
	}
	return n
}

func answer(t *testing.T, owner string, body dnsmessage.ResourceBody) dnsmessage.Resource {
	t.Helper()
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: name(t, owner), Class: dnsmessage.ClassINET, TTL: 120},
		Body:   body,
	}
}

func pack(t *testing.T, answers ...dnsmessage.Resource) []byte {
	t.Helper()
	msg := dnsmessage.Message{Header: dnsmessage.Header{Response: true}, Answers: answers}
	raw, err := msg.Pack()
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	return raw
}

// A responder that stays quiet on the broad "_matter._tcp" question still answers
// when asked for its own fabric subtype. That is the whole reason the browser
// enumerates service types first: on the reference LAN one node answered only the
// narrow question, so the fabric it was the sole member of went missing entirely.
func TestCollectorLearnsSubtypesAndAcceptsTheirInstances(t *testing.T) {
	c := newCollector("_matter._tcp", "_matter._tcp.local.")

	// Round one: the service-type enumeration names the subtype; nothing else yet.
	c.absorb(pack(t,
		answer(t, serviceTypeEnumeration, &dnsmessage.PTRResource{PTR: name(t, "_matter._tcp.local.")}),
		answer(t, serviceTypeEnumeration, &dnsmessage.PTRResource{PTR: name(t, "_I0F0E0D0C0B0A0908._sub._matter._tcp.local.")}),
		answer(t, serviceTypeEnumeration, &dnsmessage.PTRResource{PTR: name(t, "_http._tcp.local.")}),
	))
	if len(c.instances()) != 0 {
		t.Fatalf("a service type is not an instance: %+v", c.instances())
	}
	want := []string{"_matter._tcp.local.", "_i0f0e0d0c0b0a0908._sub._matter._tcp.local."}
	got := c.queryNames()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("queryNames() = %v, want %v", got, want)
	}

	// Round two: the subtype query draws out the instance the broad one missed.
	addr := netip.MustParseAddr("fd11:2233:4455:1::9").As16()
	c.absorb(pack(t,
		answer(t, "_I0F0E0D0C0B0A0908._sub._matter._tcp.local.", &dnsmessage.PTRResource{PTR: name(t, "0F0E0D0C0B0A0908-0A0B0C0D0E0F0102._matter._tcp.local.")}),
		answer(t, "0F0E0D0C0B0A0908-0A0B0C0D0E0F0102._matter._tcp.local.", &dnsmessage.SRVResource{Port: 5541, Target: name(t, "0102030405060708.local.")}),
		answer(t, "0F0E0D0C0B0A0908-0A0B0C0D0E0F0102._matter._tcp.local.", &dnsmessage.TXTResource{TXT: []string{"T=6", "SII=9500"}}),
		answer(t, "0102030405060708.local.", &dnsmessage.AAAAResource{AAAA: addr}),
	))

	got2 := c.instances()
	if len(got2) != 1 {
		t.Fatalf("instances = %+v, want 1", got2)
	}
	one := got2[0]
	if one.Name != "0F0E0D0C0B0A0908-0A0B0C0D0E0F0102" || one.Service != "_matter._tcp" {
		t.Errorf("instance identity = %+v", one)
	}
	if one.Host != "0102030405060708.local." || one.Port != 5541 {
		t.Errorf("SRV not applied: %+v", one)
	}
	if len(one.Addresses) != 1 || one.Addresses[0] != "fd11:2233:4455:1::9" {
		t.Errorf("addresses = %v", one.Addresses)
	}
	if one.TXT["T"] != "6" || one.TXT["SII"] != "9500" {
		t.Errorf("TXT = %v", one.TXT)
	}
}

// The same instance is pointed at by the base service and by every subtype it
// belongs to, so it must be collected once however many times it is named.
func TestCollectorDeduplicatesAcrossBaseAndSubtype(t *testing.T) {
	c := newCollector("_matter._tcp", "_matter._tcp.local.")
	instance := "1122334455667788-000000000000001A._matter._tcp.local."
	c.absorb(pack(t,
		answer(t, "_matter._tcp.local.", &dnsmessage.PTRResource{PTR: name(t, instance)}),
		answer(t, "_I1122334455667788._sub._matter._tcp.local.", &dnsmessage.PTRResource{PTR: name(t, instance)}),
	))
	if got := c.instances(); len(got) != 1 {
		t.Fatalf("instances = %+v, want 1", got)
	}
}

// Another service's records share the wire; none of them belong to this browse.
func TestCollectorIgnoresOtherServices(t *testing.T) {
	c := newCollector("_matter._tcp", "_matter._tcp.local.")
	c.absorb(pack(t,
		answer(t, "_hap._tcp.local.", &dnsmessage.PTRResource{PTR: name(t, "Living Room TV._hap._tcp.local.")}),
	))
	if got := c.instances(); len(got) != 0 {
		t.Fatalf("instances = %+v, want none", got)
	}
	if len(c.queryNames()) != 1 {
		t.Fatalf("queryNames picked up a foreign subtype: %v", c.queryNames())
	}
}
