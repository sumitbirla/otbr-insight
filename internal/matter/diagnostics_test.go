package matter

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/otbr-insight/otbr-insight/internal/model"
	"strings"
	"testing"
)

// Certificates here are built rather than pasted. A real export carries a live
// fabric's root public key and a device's identity, and the point of this test
// is the decoding, which synthetic values exercise identically.

// tlvBuilder writes the subset of Matter TLV the decoder reads.
type tlvBuilder struct{ buf []byte }

func (b *tlvBuilder) open(kind byte, tag uint8, tagged bool) {
	control := kind
	if tagged {
		control |= 1 << 5
	}
	b.buf = append(b.buf, control)
	if tagged {
		b.buf = append(b.buf, tag)
	}
}

func (b *tlvBuilder) end() { b.buf = append(b.buf, tlvEnd) }

func (b *tlvBuilder) bytes(tag uint8, value []byte) {
	b.open(tlvBytes1, tag, true)
	b.buf = append(b.buf, byte(len(value)))
	b.buf = append(b.buf, value...)
}

func (b *tlvBuilder) uint64(tag uint8, value uint64) {
	b.open(tlvUint64, tag, true)
	for i := 0; i < 8; i++ {
		b.buf = append(b.buf, byte(value>>(8*i)))
	}
}

// publicKey returns a 65-byte uncompressed P-256 point shape seeded by n. The
// decoder only strips the 0x04 marker and hashes the rest, so the coordinates
// need not lie on the curve for this to exercise the derivation.
func publicKey(n byte) []byte {
	key := make([]byte, 65)
	key[0] = 0x04
	for i := 1; i < len(key); i++ {
		key[i] = byte(i) ^ n
	}
	return key
}

// rootCert builds a trusted root certificate. withFabric mirrors the two shapes
// seen in the wild: most controllers put the fabric ID in the subject, and Home
// Assistant's own root does not.
func rootCert(key []byte, fabricID uint64, withFabric bool) string {
	var b tlvBuilder
	b.open(tlvStruct, 0, false)
	b.bytes(certSerialTag, []byte{0x01})
	b.open(tlvList, certSubjectTag, true)
	b.uint64(dnRCACIDTag, 0x0102030405060708)
	if withFabric {
		b.uint64(dnFabricTag, fabricID)
	}
	b.end()
	b.bytes(certPublicKeyTag, key)
	b.end()
	return base64.StdEncoding.EncodeToString(b.buf)
}

func export(t *testing.T, attributes map[string]any, serverFabric *uint64) []byte {
	t.Helper()
	raw := map[string]any{}
	for key, value := range attributes {
		raw[key] = value
	}
	server := map[string]any{}
	if serverFabric != nil {
		server["compressed_fabric_id"] = *serverFabric
	}
	file := map[string]any{
		"home_assistant":       map[string]any{"version": "2026.9.1"},
		"integration_manifest": map[string]any{"domain": "matter"},
		"data": map[string]any{
			"server_info": server,
			"node":        map[string]any{"node_id": 26, "attributes": raw},
		},
	}
	encoded, err := json.Marshal(file)
	if err != nil {
		t.Fatalf("marshal export: %v", err)
	}
	return encoded
}

func TestIdentifyDerivesEveryFabricFromTheRootCertificates(t *testing.T) {
	ownKey := publicKey(1)
	otherKey := publicKey(2)
	data := export(t, map[string]any{
		"0/40/1":  "Example Vendor",
		"0/40/2":  4476,
		"0/40/3":  "Door sensor",
		"0/40/4":  32775,
		"0/40/18": "0102030405060708",
		"0/62/1": []any{map[string]any{
			"1": base64.StdEncoding.EncodeToString(ownKey),
			"2": 65521,
			"3": 1,
			"4": 26,
			"5": "Home",
		}},
		// The controller's own root omits the fabric ID; the other carries it.
		"0/62/4": []any{rootCert(otherKey, 0x1122334455667788, true), rootCert(ownKey, 1, false)},
		"0/62/5": 2,
	}, nil)

	evidence, err := Identify(data)
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if len(evidence.Fabrics) != 2 {
		t.Fatalf("got %d fabrics, want 2: %+v", len(evidence.Fabrics), evidence.Fabrics)
	}
	if len(evidence.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", evidence.Warnings)
	}
	// The source fabric sorts first and is the only one that can be named.
	source := evidence.Fabrics[0]
	if !source.IsSource {
		t.Fatalf("expected the controller's own fabric first, got %+v", source)
	}
	if source.Label != "Home" || source.VendorID != 65521 || source.NodeID != "0x1A" {
		t.Fatalf("source fabric lost its Fabrics-attribute detail: %+v", source)
	}
	if !strings.HasPrefix(source.VendorName, "Test vendor") {
		t.Fatalf("vendor name = %q", source.VendorName)
	}
	// Its compressed ID can only come from the Fabrics entry, since the
	// certificate's subject carries no fabric ID.
	if len(source.ID) != 16 {
		t.Fatalf("source compressed fabric ID = %q, want 16 hex digits", source.ID)
	}
	other := evidence.Fabrics[1]
	if other.IsSource || other.Label != "" || other.NodeID != "" {
		t.Fatalf("a fabric-filtered attribute leaked onto a foreign fabric: %+v", other)
	}
	if len(other.ID) != 16 || other.ID == source.ID {
		t.Fatalf("foreign compressed fabric ID = %q (source %q)", other.ID, source.ID)
	}
	if other.FabricID != "0x1122334455667788" {
		t.Fatalf("foreign fabric ID = %q", other.FabricID)
	}
	if evidence.Device.ProductName != "Door sensor" || evidence.Device.VendorName != "Example Vendor" {
		t.Fatalf("device identity = %+v", evidence.Device)
	}
	if evidence.Source != "Home Assistant 2026.9.1" || evidence.Reported != 2 {
		t.Fatalf("source = %q reported = %d", evidence.Source, evidence.Reported)
	}
}

func TestIdentifyIsDeterministicForAKnownVector(t *testing.T) {
	// Pins the HKDF derivation so a refactor cannot silently change the IDs the
	// UI matches on. The expectation was computed by the same spec steps:
	// HKDF-SHA256(ikm = key without its 0x04 marker, salt = fabric ID as eight
	// big-endian bytes, info = "CompressedFabric") run independently in Python.
	got, err := compressedFabricID("04"+strings.Repeat("aa", 64), 1)
	if err != nil {
		t.Fatalf("compressedFabricID: %v", err)
	}
	const want = "C7E53921619626C0"
	if got != want {
		t.Fatalf("compressed fabric ID = %s, want %s", got, want)
	}
}

func TestIdentifyCrossChecksTheControllersStatedFabricID(t *testing.T) {
	ownKey := publicKey(1)
	fabrics := []any{map[string]any{
		"1": base64.StdEncoding.EncodeToString(ownKey),
		"2": 65521, "3": 1, "4": 26, "5": "Home",
	}}
	attributes := map[string]any{
		"0/62/1": fabrics,
		"0/62/4": []any{rootCert(ownKey, 1, false)},
		"0/62/5": 1,
	}

	// The derivation and the controller's own statement must agree. Getting the
	// comparison wrong is silent and fires on every real export, so it is
	// pinned in both directions.
	derived, err := compressedFabricID(hexKey(ownKey), 1)
	if err != nil {
		t.Fatalf("compressedFabricID: %v", err)
	}
	agreeing := parseHex(t, derived)
	evidence, err := Identify(export(t, attributes, &agreeing))
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if len(evidence.Warnings) != 0 {
		t.Fatalf("matching IDs warned anyway: %v", evidence.Warnings)
	}

	disagreeing := agreeing ^ 1
	evidence, err = Identify(export(t, attributes, &disagreeing))
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if len(evidence.Warnings) != 1 || !strings.Contains(evidence.Warnings[0], "does not match") {
		t.Fatalf("mismatched IDs were not reported: %v", evidence.Warnings)
	}
}

func hexKey(key []byte) string {
	return hex.EncodeToString(key)
}

func parseHex(t *testing.T, value string) uint64 {
	t.Helper()
	parsed, err := strconv.ParseUint(value, 16, 64)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

func TestIdentifyReportsARootItCannotAttribute(t *testing.T) {
	data := export(t, map[string]any{
		// No Fabrics attribute, and the root carries no fabric ID, so there is
		// nothing to derive from and saying so beats omitting it silently.
		"0/62/4": []any{rootCert(publicKey(3), 0, false)},
		"0/62/5": 1,
	}, nil)
	evidence, err := Identify(data)
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if len(evidence.Fabrics) != 0 {
		t.Fatalf("got %d fabrics, want none", len(evidence.Fabrics))
	}
	if len(evidence.Warnings) != 1 || !strings.Contains(evidence.Warnings[0], "could not be identified") {
		t.Fatalf("warnings = %v", evidence.Warnings)
	}
}

func TestIdentifyRejectsAFileWithNoDeviceData(t *testing.T) {
	for name, body := range map[string]string{
		"not json": "<html>nope</html>",
		"no node":  `{"home_assistant":{"version":"2026.9.1"},"data":{}}`,
		"no cert":  `{"data":{"node":{"attributes":{"0/40/1":"Example"}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Identify([]byte(body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestIdentifyNamesTheDeviceWithTheLabelAPersonChose(t *testing.T) {
	name := func(t *testing.T, attributes map[string]any) model.EvidenceDevice {
		t.Helper()
		attributes["0/62/4"] = []any{rootCert(publicKey(4), 1, true)}
		evidence, err := Identify(export(t, attributes, nil))
		if err != nil {
			t.Fatalf("Identify: %v", err)
		}
		return evidence.Device
	}
	// The product name is the fallback; a node label a person set wins.
	got := name(t, map[string]any{"0/40/1": "Example Vendor", "0/40/3": "Door sensor"})
	if got.Name != "Door sensor" {
		t.Errorf("product name = %q", got.Name)
	}
	got = name(t, map[string]any{"0/40/3": "Door sensor", "0/40/5": "Back door"})
	if got.Name != "Back door" || got.NodeLabel != "Back door" {
		t.Errorf("node label = %+v", got)
	}
	// An empty label is the common case and must not win over the model name.
	got = name(t, map[string]any{"0/40/3": "Door sensor", "0/40/5": "   "})
	if got.Name != "Door sensor" {
		t.Errorf("blank label = %q", got.Name)
	}
	// Home Assistant redacts some strings in place; the marker is not a name.
	got = name(t, map[string]any{"0/40/3": "Door sensor", "0/40/5": "**REDACTED**"})
	if got.Name != "Door sensor" || got.NodeLabel != "" {
		t.Errorf("redacted label = %+v", got)
	}
	// A file with neither must leave Name empty rather than inventing one, so
	// the caller falls back to its own heading instead of showing a blank title.
	if got := name(t, map[string]any{"0/40/1": "Example Vendor"}); got.Name != "" {
		t.Errorf("nameless device = %q", got.Name)
	}
}
