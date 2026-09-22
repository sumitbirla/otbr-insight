package matter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// attributesFrom builds an export body from a literal attribute map.
func attributesFrom(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	var attributes map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &attributes); err != nil {
		t.Fatalf("parse attributes: %v", err)
	}
	return attributes
}

// TestReportCoversEveryAttribute is the guarantee the report rests on: an
// attribute the spec table does not know still reaches the reader, under its
// cluster, rather than being silently dropped. Without this, adding a section
// and mistyping one path would quietly lose a reading.
func TestReportCoversEveryAttribute(t *testing.T) {
	attributes := attributesFrom(t, `{
		"0/40/1": "Example Vendor",
		"0/40/3": "Door sensor",
		"0/53/18": 20,
		"0/53/49": 79,
		"0/47/12": 200,
		"0/51/1": 5,
		"0/99/7": 1234,
		"0/99/65533": 2,
		"2/64999/0": true
	}`)
	sections := Report(attributes)
	seen := map[string]bool{}
	for _, section := range sections {
		if section.Title == "" {
			t.Fatal("a section has no title")
		}
		for _, entry := range section.Entries {
			if entry.Value == "" {
				t.Fatalf("%s has no value", entry.Source)
			}
			seen[entry.Source] = true
		}
	}
	for path := range attributes {
		if !seen[path] {
			t.Fatalf("%s never reached the report", path)
		}
	}
	// An unknown cluster is grouped by its number, not dropped or mislabelled.
	if !hasSection(sections, "Cluster 99") {
		t.Fatalf("unknown cluster lost its own section: %v", titles(sections))
	}
	if !hasSection(sections, "Cluster metadata") {
		t.Fatalf("global attributes were not split out: %v", titles(sections))
	}
	if !hasSection(sections, "Endpoint 2 · Cluster 64999") {
		t.Fatalf("a non-zero endpoint was not labelled: %v", titles(sections))
	}
}

func TestReportGivesValuesTheirUnits(t *testing.T) {
	// Each of these renders as a bare number without a spec entry, and each of
	// those bare numbers reads as something false: 200% charge, 1495 volts,
	// a channel mask of 5-20, a prefix that is not an address.
	attributes := attributesFrom(t, `{
		"0/47/12": 200,
		"0/47/11": 1495,
		"0/51/2": 235551,
		"0/51/4": 6,
		"0/53/1": 2,
		"0/53/5": "QP0RIjNEVWZ3",
		"0/53/60": "AB//4A==",
		"0/60/0": 0,
		"0/70/0": 1800
	}`)
	want := map[string]string{
		"0/47/12": "100%",
		"0/47/11": "1.495 V (1495 mV)",
		"0/51/2":  "2d 17h (235551 s)",
		"0/51/4":  "Software reset",
		"0/53/1":  "Sleepy end device",
		"0/53/5":  "fd11:2233:4455:6677::/64",
		"0/53/60": "channels 11–26",
		"0/60/0":  "Closed — not accepting new controllers",
		"0/70/0":  "30 minutes (1800 s)",
	}
	got := map[string]string{}
	for _, section := range Report(attributes) {
		for _, entry := range section.Entries {
			got[entry.Source] = entry.Value
		}
	}
	for path, expected := range want {
		if got[path] != expected {
			t.Errorf("%s = %q, want %q", path, got[path], expected)
		}
	}
}

// TestReportDistinguishesNullFromEmpty pins a trap in encoding/json: null
// unmarshals into a nil slice without error, so a null scalar would render as
// "none" and read as an empty list rather than as nothing reported.
func TestReportDistinguishesNullFromEmpty(t *testing.T) {
	attributes := attributesFrom(t, `{"0/60/1": null, "0/51/5": []}`)
	got := map[string]string{}
	for _, section := range Report(attributes) {
		for _, entry := range section.Entries {
			got[entry.Source] = entry.Value
		}
	}
	if got["0/60/1"] != "Not set" {
		t.Errorf("null rendered as %q", got["0/60/1"])
	}
	if got["0/51/5"] != "None" {
		t.Errorf("empty fault list rendered as %q", got["0/51/5"])
	}
}

func TestReportDecodesCertificatesRatherThanPrintingThem(t *testing.T) {
	attributes := attributesFrom(t, `{"0/62/4": ["`+testReportCertificate+`"]}`)
	var value string
	for _, section := range Report(attributes) {
		for _, entry := range section.Entries {
			if entry.Source == "0/62/4" {
				value = entry.Value
			}
		}
	}
	if strings.Contains(value, testReportCertificate) {
		t.Fatal("the certificate was printed back as base64")
	}
	if !strings.Contains(value, "fabric 0x1122334455667788") || !strings.Contains(value, "root CA") {
		t.Fatalf("certificate summary = %q", value)
	}
}

// Synthetic, built the same way the diagnostics tests build theirs.
const testReportCertificate = "FTABAQE3BicUCAcGBQQDAgEnFYh3ZlVEMyIRGDAJQQQrKCkuLywtIiMgISYnJCU6Ozg5Pj88PTIzMDE2NzQ1CgsICQ4PDA0CAwABBgcEBRobGBkeHxwdEhMQERYXFBVqGA=="

func hasSection(sections []model.ReportSection, title string) bool {
	for _, section := range sections {
		if section.Title == title {
			return true
		}
	}
	return false
}

func titles(sections []model.ReportSection) []string {
	out := make([]string, 0, len(sections))
	for _, section := range sections {
		out = append(out, section.Title)
	}
	return out
}

// TestReportBandsEachGroupContiguously pins the ordering the frontend relies on:
// it emits a band heading whenever the group changes, so a group split across
// two runs draws its heading twice. The table and catch-all sections are built
// after the spec ones, which is exactly how that happened.
func TestReportBandsEachGroupContiguously(t *testing.T) {
	attributes := attributesFrom(t, `{
		"0/40/1": "Example Vendor",
		"0/47/12": 200,
		"0/53/0": 25,
		"0/53/7": [{"0": 1, "1": 10, "2": 2048, "5": 3, "7": -70}],
		"0/51/0": [{"0": "thread", "1": true, "7": 4}],
		"0/62/3": 2,
		"1/69/0": true,
		"0/99/7": 1234
	}`)
	var order []string
	seen := map[string]bool{}
	for _, section := range Report(attributes) {
		if len(order) == 0 || order[len(order)-1] != section.Group {
			if seen[section.Group] {
				t.Fatalf("group %q appears in two separate runs: %v", section.Group, order)
			}
			seen[section.Group] = true
			order = append(order, section.Group)
		}
	}
	want := []string{groupOverview, groupThread, groupMatter, groupEndpoints, groupRaw}
	if len(order) != len(want) {
		t.Fatalf("bands = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("bands = %v, want %v", order, want)
		}
	}
}

func TestReportGivesEverySectionAnIcon(t *testing.T) {
	attributes := attributesFrom(t, `{"0/40/1": "Example", "0/99/7": 1, "1/69/0": true, "0/40/65533": 3}`)
	for _, section := range Report(attributes) {
		if section.Icon == "" {
			t.Errorf("%q has no icon", section.Title)
		}
		if section.Group == "" {
			t.Errorf("%q has no group", section.Title)
		}
	}
}
