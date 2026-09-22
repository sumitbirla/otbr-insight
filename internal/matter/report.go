package matter

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// Rendering a diagnostics export for reading.
//
// The export is a flat map of "<endpoint>/<cluster>/<attribute>" to a raw JSON
// value — 230 entries for a door sensor. Naming them needs the Matter cluster
// spec, so the knowledge lives here in one table rather than in the frontend.
//
// The table is deliberately not the whole spec: it covers the clusters a Thread
// device on this mesh actually reports. Everything else still appears, under
// its cluster name with its raw value, because an export that silently drops
// what the decoder does not recognise is worse than a slightly raw row — the
// reader cannot tell the difference between "not present" and "not understood".

// attributeSpec names one attribute and renders its value.
type attributeSpec struct {
	label  string
	render func(json.RawMessage) string
}

// section groups attribute paths under a heading, in the order given.
type sectionSpec struct {
	title string
	group string
	icon  string
	note  string
	paths []string
}

// Report renders every attribute in the export, grouped and named.
func Report(attributes map[string]json.RawMessage) []model.ReportSection {
	used := map[string]bool{}
	var sections []model.ReportSection
	for _, spec := range reportSections {
		section := model.ReportSection{Title: spec.title, Group: spec.group, Icon: spec.icon, Note: spec.note}
		for _, path := range spec.paths {
			raw, ok := attributes[path]
			if !ok {
				continue
			}
			used[path] = true
			entry := renderAttribute(path, raw)
			if entry.Value == "" {
				continue
			}
			section.Entries = append(section.Entries, entry)
		}
		if len(section.Entries) > 0 {
			sections = append(sections, section)
		}
	}
	sections = append(sections, tableSections(attributes, used)...)
	sections = append(sections, remainingSections(attributes, used)...)
	// Banded last, not built in band order: the table and catch-all sections are
	// appended after the spec ones, so without this a group's cards arrive in
	// two runs and the frontend draws its heading twice.
	sort.SliceStable(sections, func(i, j int) bool {
		return groupRank(sections[i].Group) < groupRank(sections[j].Group)
	})
	return sections
}

// groupOrder is the order bands are shown in. An unrecognised group sorts last
// rather than first, so adding a section without a band cannot displace the
// ones a reader opens the report for.
var groupOrder = []string{groupOverview, groupThread, groupMatter, groupEndpoints, groupRaw}

func groupRank(group string) int {
	for index, name := range groupOrder {
		if name == group {
			return index
		}
	}
	return len(groupOrder)
}

func renderAttribute(path string, raw json.RawMessage) model.ReportEntry {
	entry := model.ReportEntry{Label: attributeLabel(path), Source: path}
	if spec, ok := lookupSpec(path); ok && spec.render != nil {
		entry.Value = spec.render(raw)
		return entry
	}
	entry.Value = renderRaw(raw)
	return entry
}

// lookupSpec resolves an attribute path, falling back to an endpoint-agnostic
// "*/cluster/attribute" entry. Descriptor, Identify and the sensor clusters
// appear on every endpoint, and keying them all to endpoint 0 would leave a
// contact sensor's own state rendered as a bare number.
func lookupSpec(path string) (attributeSpec, bool) {
	if spec, ok := knownAttributes[path]; ok {
		return spec, true
	}
	parts := strings.Split(path, "/")
	if len(parts) != 3 {
		return attributeSpec{}, false
	}
	spec, ok := knownAttributes["*/"+parts[1]+"/"+parts[2]]
	return spec, ok
}

func attributeLabel(path string) string {
	if spec, ok := lookupSpec(path); ok && spec.label != "" {
		return spec.label
	}
	parts := strings.Split(path, "/")
	if len(parts) == 3 {
		if name, ok := globalAttributes[parts[2]]; ok {
			return name
		}
		return "Attribute " + parts[2]
	}
	return path
}

// remainingSections emits every attribute no section claimed, grouped by
// cluster and endpoint so the tail of the report is still navigable. Protocol
// bookkeeping (the 6552x attributes every cluster carries) is split off into
// its own section, since it is the bulk of the count and none of the meaning.
func remainingSections(attributes map[string]json.RawMessage, used map[string]bool) []model.ReportSection {
	byGroup := map[string][]model.ReportEntry{}
	bands := map[string]string{}
	var metadata []model.ReportEntry
	for path, raw := range attributes {
		if used[path] {
			continue
		}
		parts := strings.Split(path, "/")
		if len(parts) != 3 {
			continue
		}
		entry := renderAttribute(path, raw)
		if entry.Value == "" {
			entry.Value = "empty"
		}
		if _, global := globalAttributes[parts[2]]; global {
			entry.Label = clusterTitle(parts[0], parts[1]) + " · " + entry.Label
			metadata = append(metadata, entry)
			continue
		}
		title := clusterTitle(parts[0], parts[1])
		byGroup[title] = append(byGroup[title], entry)
		// An endpoint past the root is where the device's actual function lives
		// — a contact sensor's own state, a light's on/off — so those get their
		// own band rather than being filed under leftovers.
		if parts[0] != "0" {
			bands[title] = groupEndpoints
		}
	}
	titles := make([]string, 0, len(byGroup))
	for title := range byGroup {
		titles = append(titles, title)
	}
	sort.Strings(titles)
	var sections []model.ReportSection
	for _, title := range titles {
		entries := byGroup[title]
		sort.Slice(entries, func(i, j int) bool { return sourceOrder(entries[i].Source) < sourceOrder(entries[j].Source) })
		band := bands[title]
		if band == "" {
			band = groupRaw
		}
		sections = append(sections, model.ReportSection{Title: title, Group: band, Icon: "grid", Entries: entries})
	}
	if len(metadata) > 0 {
		sort.Slice(metadata, func(i, j int) bool { return metadata[i].Label < metadata[j].Label })
		sections = append(sections, model.ReportSection{
			Title:   "Cluster metadata",
			Group:   groupRaw,
			Icon:    "code",
			Note:    "Protocol bookkeeping every Matter cluster carries: which commands and attributes it implements, and its revision. Included for completeness; nothing here describes the device's state.",
			Entries: metadata,
		})
	}
	return sections
}

func sourceOrder(path string) int {
	parts := strings.Split(path, "/")
	if len(parts) != 3 {
		return 0
	}
	value, _ := strconv.Atoi(parts[2])
	return value
}

func clusterTitle(endpoint, cluster string) string {
	name, ok := clusterNames[cluster]
	if !ok {
		name = "Cluster " + cluster
	}
	if endpoint == "0" {
		return name
	}
	return "Endpoint " + endpoint + " · " + name
}

// renderRaw is the fallback: compact JSON, with long lists summarised rather
// than printed in full so one attribute cannot swamp the report.
func renderRaw(raw json.RawMessage) string {
	// Checked before the list decode: encoding/json happily unmarshals null
	// into a nil slice, so a null scalar would otherwise render as "none" —
	// "the device reported nothing here" read as "the list is empty".
	if strings.TrimSpace(string(raw)) == "null" {
		return "Not set"
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err == nil {
		if len(list) == 0 {
			return "none"
		}
		parts := make([]string, 0, len(list))
		for _, item := range list {
			parts = append(parts, strings.TrimSpace(string(item)))
		}
		if len(parts) > 12 {
			return strings.Join(parts[:12], ", ") + fmt.Sprintf(" … (%d in total)", len(parts))
		}
		return strings.Join(parts, ", ")
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if text == "" {
			return "empty"
		}
		return text
	}
	return strings.TrimSpace(string(raw))
}

// ---------------------------------------------------------------- formatting

func asFloat(raw json.RawMessage) (float64, bool) {
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	return value, true
}

func asUint(raw json.RawMessage) (uint64, bool) {
	var value uint64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	return value, true
}

func plain(raw json.RawMessage) string { return renderRaw(raw) }

func count(unit string) func(json.RawMessage) string {
	return func(raw json.RawMessage) string {
		value, ok := asFloat(raw)
		if !ok {
			return renderRaw(raw)
		}
		formatted := strconv.FormatFloat(value, 'f', -1, 64)
		if unit == "" {
			return formatted
		}
		if value == 1 {
			return formatted + " " + unit
		}
		return formatted + " " + unit + "s"
	}
}

func suffix(unit string) func(json.RawMessage) string {
	return func(raw json.RawMessage) string {
		value, ok := asFloat(raw)
		if !ok {
			return renderRaw(raw)
		}
		return strconv.FormatFloat(value, 'f', -1, 64) + " " + unit
	}
}

func yesNo(raw json.RawMessage) string {
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return renderRaw(raw)
	}
	if value {
		return "Yes"
	}
	return "No"
}

func enum(values map[uint64]string) func(json.RawMessage) string {
	return func(raw json.RawMessage) string {
		value, ok := asUint(raw)
		if !ok {
			return renderRaw(raw)
		}
		if name, ok := values[value]; ok {
			return name
		}
		return fmt.Sprintf("Unknown (%d)", value)
	}
}

// seconds renders a duration the way a reader compares it: days and hours for
// an uptime, plain seconds while it is still small.
func seconds(raw json.RawMessage) string {
	value, ok := asUint(raw)
	if !ok {
		return renderRaw(raw)
	}
	return formatSeconds(value) + fmt.Sprintf(" (%d s)", value)
}

func formatSeconds(total uint64) string {
	switch {
	case total < 60:
		return fmt.Sprintf("%d seconds", total)
	case total < 3600:
		return fmt.Sprintf("%d minutes", total/60)
	case total < 86400:
		return fmt.Sprintf("%dh %dm", total/3600, (total%3600)/60)
	default:
		return fmt.Sprintf("%dd %dh", total/86400, (total%86400)/3600)
	}
}

func millis(raw json.RawMessage) string {
	value, ok := asUint(raw)
	if !ok {
		return renderRaw(raw)
	}
	if value < 1000 {
		return fmt.Sprintf("%d ms", value)
	}
	return fmt.Sprintf("%.3g s (%d ms)", float64(value)/1000, value)
}

func hexValue(width int) func(json.RawMessage) string {
	return func(raw json.RawMessage) string {
		value, ok := asUint(raw)
		if !ok {
			return renderRaw(raw)
		}
		return fmt.Sprintf("0x%0*X", width, value)
	}
}

// halfPercent renders BatPercentRemaining, which the spec defines in half a
// percent per unit — printing it raw reads as 200% on a full battery.
func halfPercent(raw json.RawMessage) string {
	value, ok := asFloat(raw)
	if !ok {
		return renderRaw(raw)
	}
	return strconv.FormatFloat(value/2, 'f', -1, 64) + "%"
}

func millivolts(raw json.RawMessage) string {
	value, ok := asFloat(raw)
	if !ok {
		return renderRaw(raw)
	}
	return fmt.Sprintf("%.3f V (%.0f mV)", value/1000, value)
}

// softwareVersion prints the numeric version beside its string form; only the
// number is comparable, and only the string is recognisable.
func softwareVersion(raw json.RawMessage) string {
	value, ok := asUint(raw)
	if !ok {
		return renderRaw(raw)
	}
	return fmt.Sprintf("%d (0x%X)", value, value)
}

func manufacturingDate(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil || len(text) < 8 {
		return renderRaw(raw)
	}
	return text[0:4] + "-" + text[4:6] + "-" + text[6:8]
}

// redactable blanks Home Assistant's in-place redaction marker. Showing it as a
// value reads as though the device were really called "**REDACTED**".
func redactable(raw json.RawMessage) string {
	value := renderRaw(raw)
	if strings.Contains(value, "REDACTED") {
		return ""
	}
	return value
}

// base64Bytes renders an octet string as hex, which is how every other identity
// in this app is written.
func base64Bytes(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return renderRaw(raw)
	}
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return text
	}
	return strings.ToUpper(hex.EncodeToString(decoded))
}

// base64Address renders a 16-byte octet string as an IPv6 address.
func base64Address(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return renderRaw(raw)
	}
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(decoded) != 16 {
		return base64Bytes(raw)
	}
	address, ok := netip.AddrFromSlice(decoded)
	if !ok {
		return base64Bytes(raw)
	}
	return address.String()
}

func channelMask(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return renderRaw(raw)
	}
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(decoded) == 0 {
		return renderRaw(raw)
	}
	// The mask is a bit string, not an integer: the most significant bit of the
	// first byte is channel 0. Reading it as a big-endian number reports the
	// 802.15.4 band (11-26) as channels 5-20.
	var channels []string
	for index, b := range decoded {
		for bit := 0; bit < 8; bit++ {
			if b&(1<<uint(7-bit)) != 0 {
				channels = append(channels, strconv.Itoa(index*8+bit))
			}
		}
	}
	if len(channels) == 0 {
		return "none"
	}
	if len(channels) > 2 && contiguous(channels) {
		return "channels " + channels[0] + "–" + channels[len(channels)-1]
	}
	return "channels " + strings.Join(channels, ", ")
}

func contiguous(channels []string) bool {
	first, _ := strconv.Atoi(channels[0])
	last, _ := strconv.Atoi(channels[len(channels)-1])
	return last-first+1 == len(channels)
}

// meshLocalPrefix renders the 9-byte octet string the spec defines: a prefix
// length followed by the prefix itself, not a plain address.
func meshLocalPrefix(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return renderRaw(raw)
	}
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(decoded) != 9 {
		return base64Bytes(raw)
	}
	length := int(decoded[0])
	padded := make([]byte, 16)
	copy(padded, decoded[1:])
	address, ok := netip.AddrFromSlice(padded)
	if !ok {
		return base64Bytes(raw)
	}
	return fmt.Sprintf("%s/%d", address.String(), length)
}

func faultList(raw json.RawMessage) string {
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return renderRaw(raw)
	}
	if len(list) == 0 {
		return "None"
	}
	return renderRaw(raw)
}

func securityPolicy(raw json.RawMessage) string {
	var value struct {
		Rotation uint64 `json:"0"`
		Flags    uint64 `json:"1"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return renderRaw(raw)
	}
	return fmt.Sprintf("Key rotation every %d hours, flags 0x%04X", value.Rotation, value.Flags)
}

func capabilityMinima(raw json.RawMessage) string {
	var value struct {
		CaseSessions     uint64 `json:"0"`
		SubscriptionsPer uint64 `json:"1"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return renderRaw(raw)
	}
	return fmt.Sprintf("%d CASE sessions, %d subscriptions per fabric", value.CaseSessions, value.SubscriptionsPer)
}

func deviceTypeList(raw json.RawMessage) string {
	var list []struct {
		Type     uint64 `json:"0"`
		Revision uint64 `json:"1"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return renderRaw(raw)
	}
	if len(list) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(list))
	for _, item := range list {
		name, ok := deviceTypes[item.Type]
		if !ok {
			name = fmt.Sprintf("Type 0x%04X", item.Type)
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}

func clusterList(raw json.RawMessage) string {
	var list []uint64
	if err := json.Unmarshal(raw, &list); err != nil {
		return renderRaw(raw)
	}
	if len(list) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(list))
	for _, id := range list {
		key := strconv.FormatUint(id, 10)
		if name, ok := clusterNames[key]; ok {
			parts = append(parts, name)
			continue
		}
		parts = append(parts, "Cluster "+key)
	}
	return strings.Join(parts, ", ")
}
