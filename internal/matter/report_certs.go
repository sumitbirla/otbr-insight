package matter

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Rendering the certificate and access-control attributes.
//
// These carry the most meaning per byte in the whole export and are the least
// readable raw: three of them are base64 Matter TLV, and one is the list of
// controllers allowed to administer the device. Printing the base64 back would
// satisfy "show everything" only in the letter.

// matterEpoch is the Matter certificate epoch: seconds since 2000-01-01 UTC,
// not the Unix epoch.
var matterEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// Distinguished-name tags used in operational certificates (spec §6.5.6.2).
var dnTagNames = map[uint8]string{
	17: "node",
	18: "firmware signing",
	19: "intermediate CA",
	20: "root CA",
	21: "fabric",
	22: "CASE authenticated tag",
}

// certificateList renders TrustedRootCertificates: one line per fabric.
func certificateList(raw json.RawMessage) string {
	var encoded []string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return renderRaw(raw)
	}
	lines := make([]string, 0, len(encoded))
	for _, value := range encoded {
		lines = append(lines, describeCertificate(value))
	}
	if len(lines) == 0 {
		return "none"
	}
	return strings.Join(lines, "\n")
}

// nocList renders the NOCs attribute, which pairs each fabric's operational
// certificate with the intermediate CA that signed it. It is fabric-filtered,
// so only the reading controller's entry is ever present.
func nocList(raw json.RawMessage) string {
	var entries []struct {
		NOC         string `json:"1"`
		ICAC        string `json:"2"`
		FabricIndex int    `json:"254"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return renderRaw(raw)
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		lines = append(lines, fmt.Sprintf("Fabric index %d", entry.FabricIndex))
		lines = append(lines, "  Operational certificate: "+describeCertificate(entry.NOC))
		if entry.ICAC != "" {
			lines = append(lines, "  Intermediate CA: "+describeCertificate(entry.ICAC))
		}
	}
	if len(lines) == 0 {
		return "none"
	}
	return strings.Join(lines, "\n")
}

// fabricDescriptors renders the Fabrics attribute, also fabric-filtered.
func fabricDescriptors(raw json.RawMessage) string {
	var entries []struct {
		RootPublicKey string `json:"1"`
		VendorID      int    `json:"2"`
		FabricID      uint64 `json:"3"`
		NodeID        uint64 `json:"4"`
		Label         string `json:"5"`
		FabricIndex   int    `json:"254"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return renderRaw(raw)
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		label := entry.Label
		if label == "" {
			label = "(no label)"
		}
		lines = append(lines, fmt.Sprintf("%q at fabric index %d", label, entry.FabricIndex))
		key, err := base64.StdEncoding.DecodeString(entry.RootPublicKey)
		if err == nil && len(key) == 65 {
			if compressed, err := compressedFabricID(hexOf(key), entry.FabricID); err == nil {
				lines = append(lines, "  Compressed fabric ID: "+compressed)
			}
		}
		lines = append(lines, fmt.Sprintf("  Fabric ID 0x%X, this device is node 0x%X", entry.FabricID, entry.NodeID))
		lines = append(lines, "  Controller vendor: "+vendorLabel(entry.VendorID))
	}
	if len(lines) == 0 {
		return "none"
	}
	return strings.Join(lines, "\n")
}

func vendorLabel(id int) string {
	if name, ok := vendorNames[id]; ok {
		return fmt.Sprintf("%d — %s", id, name)
	}
	return fmt.Sprintf("%d", id)
}

// describeCertificate summarises one Matter operational certificate: who it
// identifies and how long it is valid.
func describeCertificate(encoded string) string {
	der, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "could not be decoded"
	}
	fields, err := tlvDocument(der)
	if err != nil {
		return "could not be decoded"
	}
	var parts []string
	if subject, ok := tlvField(fields, certSubjectTag); ok {
		parts = append(parts, describeDN(subject.children))
	}
	notBefore, hasBefore := tlvField(fields, certNotBeforeTag)
	notAfter, hasAfter := tlvField(fields, certNotAfterTag)
	if hasBefore {
		validity := "issued " + matterTime(notBefore.num)
		if hasAfter {
			if notAfter.num == 0 {
				validity += ", no expiry"
			} else {
				validity += ", expires " + matterTime(notAfter.num)
			}
		}
		parts = append(parts, validity)
	}
	if len(parts) == 0 {
		return "no identifying fields"
	}
	return strings.Join(parts, " · ")
}

// describeDN renders a distinguished name: the identifiers a Matter
// certificate carries instead of a common name.
func describeDN(fields []tlvElement) string {
	var parts []string
	for _, field := range fields {
		if !field.tagged {
			continue
		}
		name, ok := dnTagNames[field.tag]
		if !ok {
			name = fmt.Sprintf("tag %d", field.tag)
		}
		parts = append(parts, fmt.Sprintf("%s 0x%X", name, field.num))
	}
	if len(parts) == 0 {
		return "no subject"
	}
	return strings.Join(parts, ", ")
}

func matterTime(offset uint64) string {
	return matterEpoch.Add(time.Duration(offset) * time.Second).Format("2006-01-02")
}

func hexOf(value []byte) string {
	out := make([]byte, 0, len(value)*2)
	const digits = "0123456789abcdef"
	for _, b := range value {
		out = append(out, digits[b>>4], digits[b&0x0f])
	}
	return string(out)
}

// ------------------------------------------------------------ access control

var aclPrivileges = map[int]string{
	1: "View", 2: "Proxy view", 3: "Operate", 4: "Manage", 5: "Administer",
}

var aclAuthModes = map[int]string{1: "PASE", 2: "CASE", 3: "Group"}

// accessControlList renders who may talk to this device and at what privilege.
// It is fabric-filtered, so it shows the reading controller's entries only.
func accessControlList(raw json.RawMessage) string {
	var entries []struct {
		Privilege   int               `json:"1"`
		AuthMode    int               `json:"2"`
		Subjects    []uint64          `json:"3"`
		Targets     []json.RawMessage `json:"4"`
		FabricIndex int               `json:"254"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return renderRaw(raw)
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		subjects := make([]string, 0, len(entry.Subjects))
		for _, subject := range entry.Subjects {
			subjects = append(subjects, fmt.Sprintf("node 0x%X (%d)", subject, subject))
		}
		who := "anyone on the fabric"
		if len(subjects) > 0 {
			who = strings.Join(subjects, ", ")
		}
		scope := "all clusters"
		if len(entry.Targets) > 0 {
			scope = fmt.Sprintf("%d specific target(s)", len(entry.Targets))
		}
		lines = append(lines, fmt.Sprintf("%s over %s, on fabric index %d: %s, %s",
			enumInt(aclPrivileges, entry.Privilege), enumInt(aclAuthModes, entry.AuthMode), entry.FabricIndex, who, scope))
	}
	if len(lines) == 0 {
		return "none"
	}
	return strings.Join(lines, "\n")
}

func enumInt(values map[int]string, value int) string {
	if name, ok := values[value]; ok {
		return name
	}
	return fmt.Sprintf("Level %d", value)
}

// groupKeyMap renders which group keys apply to which groups.
func groupKeyMap(raw json.RawMessage) string {
	var entries []struct {
		GroupID     int `json:"1"`
		GroupKeySet int `json:"2"`
		FabricIndex int `json:"254"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return renderRaw(raw)
	}
	if len(entries) == 0 {
		return "none"
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		lines = append(lines, fmt.Sprintf("Group 0x%04X uses key set %d (fabric index %d)", entry.GroupID, entry.GroupKeySet, entry.FabricIndex))
	}
	return strings.Join(lines, "\n")
}

// commissionedNetworks renders the Networks attribute: which network the device
// is provisioned onto and whether it is connected to it.
func commissionedNetworks(raw json.RawMessage) string {
	var entries []struct {
		NetworkID string `json:"0"`
		Connected bool   `json:"1"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return renderRaw(raw)
	}
	if len(entries) == 0 {
		return "none"
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		state := "not connected"
		if entry.Connected {
			state = "connected"
		}
		// A Thread network's ID is its extended PAN ID.
		lines = append(lines, base64Bytes(json.RawMessage(quote(entry.NetworkID)))+" — "+state)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func quote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
