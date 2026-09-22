package matter

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// Identifying fabrics from a controller's diagnostics export.
//
// An mDNS browse can only see fabrics whose controllers are advertising, and it
// never learns their names — a compressed fabric ID is a hash. A Matter device,
// though, stores one root certificate per fabric it belongs to, and Home
// Assistant's per-device diagnostics download carries that whole attribute dump.
//
// Two attributes matter, and the difference between them is the whole point:
//
//   - 0/62/4 TrustedRootCertificates is NOT fabric-filtered, so it lists every
//     fabric the device is in, including ones this controller cannot see.
//   - 0/62/1 Fabrics IS fabric-filtered, so it describes only the fabric of the
//     controller that produced the file — but with a label, vendor ID and the
//     device's node ID in that fabric.
//
// So the export names one fabric and proves the existence of the rest. Joining
// the two by root public key is what lets the named one be attributed: measured
// against a real export, Home Assistant's own root certificate carries no
// fabric ID in its subject, so its compressed ID cannot be derived from the
// certificate alone and comes from the Fabrics entry instead.

// Matter TLV context tags within an operational certificate (spec §6.5).
const (
	certSerialTag    uint8 = 1
	certNotBeforeTag uint8 = 4
	certNotAfterTag  uint8 = 5
	certSubjectTag   uint8 = 6
	certPublicKeyTag uint8 = 9

	// Distinguished-name tags inside the subject.
	dnRCACIDTag uint8 = 20
	dnFabricTag uint8 = 21
)

// Fabrics attribute (0/62/1) fields, keyed by their TLV tag rendered as a
// decimal string because Home Assistant serialises the struct as a JSON object.
const (
	fabricRootKeyField  = "1"
	fabricVendorIDField = "2"
	fabricIDField       = "3"
	fabricNodeIDField   = "4"
	fabricLabelField    = "5"
)

// compressedFabricInfo is the HKDF info string from Matter spec §4.3.2.2. The
// key material is the root public key with its leading 0x04 uncompressed-point
// marker stripped, and the salt is the fabric ID as eight big-endian bytes.
const compressedFabricInfo = "CompressedFabric"

// MaxDiagnosticsSize bounds an upload. A real export is tens of kilobytes; a
// bridge with many endpoints is larger, but not by three orders of magnitude.
const MaxDiagnosticsSize = 8 << 20

// vendorNames covers the controller vendors seen on a home network. It is a
// display hint, not an authority: an unlisted ID is reported as a bare number
// rather than guessed at.
var vendorNames = map[int]string{
	4362:  "SmartThings",
	4631:  "Amazon",
	4937:  "Apple",
	24582: "Google",
	65521: "Test vendor (Home Assistant, chip-tool)",
}

// diagnosticsFile is the subset of Home Assistant's export this reads. Every
// other key is ignored, and nothing from the file is retained after decoding.
type diagnosticsFile struct {
	HomeAssistant struct {
		Version string `json:"version"`
	} `json:"home_assistant"`
	IntegrationManifest struct {
		Domain string `json:"domain"`
	} `json:"integration_manifest"`
	Data struct {
		ServerInfo struct {
			CompressedFabricID *uint64 `json:"compressed_fabric_id"`
		} `json:"server_info"`
		Node struct {
			NodeID     *uint64                    `json:"node_id"`
			Attributes map[string]json.RawMessage `json:"attributes"`
		} `json:"node"`
	} `json:"data"`
}

// Identify decodes a Matter controller's diagnostics export into the fabrics
// the device belongs to. It reads only; nothing is stored and the export is
// discarded once decoded.
func Identify(raw []byte) (*model.FabricEvidence, error) {
	var file diagnosticsFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("this is not a JSON file: %w", err)
	}
	attributes := file.Data.Node.Attributes
	if len(attributes) == 0 {
		return nil, errors.New("no Matter device data in this file — download the diagnostics from the device page, not the integration page")
	}

	evidence := &model.FabricEvidence{Fabrics: []model.EvidenceFabric{}}
	if version := file.HomeAssistant.Version; version != "" {
		evidence.Source = "Home Assistant " + version
	}
	evidence.Device = decodeDeviceIdentity(attributes)
	evidence.Reported = intAttribute(attributes, "0/62/5")

	owned := decodeOwnFabric(attributes)
	roots := decodeRootCertificates(attributes)
	if len(roots) == 0 && owned == nil {
		return nil, errors.New("no Matter root certificates in this file — it may be from a non-Matter integration")
	}

	var unattributed int
	for _, root := range roots {
		fabric := model.EvidenceFabric{}
		fabricID, known := root.fabricID, root.hasFabricID
		// The certificate's own subject is preferred, but Home Assistant's root
		// omits the fabric ID, so fall back to the Fabrics entry that carries
		// the same root public key. Matching on the key rather than by
		// elimination keeps this right when several roots lack a subject ID.
		if owned != nil && root.publicKey == owned.publicKey {
			fabric.IsSource = true
			fabric.Label = owned.label
			fabric.VendorID = owned.vendorID
			fabric.VendorName = vendorNames[owned.vendorID]
			fabric.NodeID = formatID(owned.nodeID)
			if !known {
				fabricID, known = owned.fabricID, true
			}
		}
		if !known {
			unattributed++
			continue
		}
		fabric.FabricID = formatID(fabricID)
		compressed, err := compressedFabricID(root.publicKey, fabricID)
		if err != nil {
			unattributed++
			continue
		}
		fabric.ID = compressed
		evidence.Fabrics = append(evidence.Fabrics, fabric)
	}

	evidence.Report = Report(attributes)
	evidence.Highlights = Highlights(attributes)
	evidence.Warnings = diagnosticsWarnings(evidence, owned, file, unattributed)
	sort.SliceStable(evidence.Fabrics, func(i, j int) bool {
		if evidence.Fabrics[i].IsSource != evidence.Fabrics[j].IsSource {
			return evidence.Fabrics[i].IsSource
		}
		return evidence.Fabrics[i].ID < evidence.Fabrics[j].ID
	})
	return evidence, nil
}

func diagnosticsWarnings(evidence *model.FabricEvidence, owned *ownFabric, file diagnosticsFile, unattributed int) []string {
	var warnings []string
	if unattributed == 1 {
		warnings = append(warnings, "One root certificate carries no fabric ID and matches no fabric this controller can see, so its fabric could not be identified.")
	} else if unattributed > 1 {
		warnings = append(warnings, fmt.Sprintf("%d root certificates carry no fabric ID and match no fabric this controller can see, so their fabrics could not be identified.", unattributed))
	}
	if evidence.Reported > 0 && evidence.Reported != len(evidence.Fabrics)+unattributed {
		warnings = append(warnings, fmt.Sprintf("The device reports %d commissioned fabrics but the export carries %d root certificates, so this list may be incomplete.", evidence.Reported, len(evidence.Fabrics)+unattributed))
	}
	// A cross-check, not a source: the compressed ID is derived from the root
	// key and fabric ID, and the controller states its own separately. They
	// disagreeing means one of the two was misread.
	if owned != nil && file.Data.ServerInfo.CompressedFabricID != nil {
		// Rendered the way the compressed ID is everywhere else: sixteen padded
		// hex digits, not formatID's trimmed "0x" form, or this never matches.
		stated := fmt.Sprintf("%016X", *file.Data.ServerInfo.CompressedFabricID)
		for _, fabric := range evidence.Fabrics {
			if fabric.IsSource && fabric.ID != "" && !strings.EqualFold(fabric.ID, stated) {
				warnings = append(warnings, "The compressed fabric ID derived from the root certificate does not match the one the controller reports, so this export may be inconsistent.")
			}
		}
	}
	return warnings
}

// rootCertificate is the part of a trusted root certificate this needs.
type rootCertificate struct {
	publicKey   string // hex, so it compares as a map key
	fabricID    uint64
	hasFabricID bool
}

func decodeRootCertificates(attributes map[string]json.RawMessage) []rootCertificate {
	var encoded []string
	if raw, ok := attributes["0/62/4"]; ok {
		_ = json.Unmarshal(raw, &encoded)
	}
	roots := make([]rootCertificate, 0, len(encoded))
	for _, value := range encoded {
		der, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			continue
		}
		fields, err := tlvDocument(der)
		if err != nil {
			continue
		}
		key, ok := tlvField(fields, certPublicKeyTag)
		if !ok || len(key.bytes) == 0 {
			continue
		}
		root := rootCertificate{publicKey: hex.EncodeToString(key.bytes)}
		if subject, ok := tlvField(fields, certSubjectTag); ok {
			if fabric, ok := tlvField(subject.children, dnFabricTag); ok {
				root.fabricID, root.hasFabricID = fabric.num, true
			}
		}
		roots = append(roots, root)
	}
	return roots
}

// ownFabric is the Fabrics entry for the controller that produced the export.
// The attribute is fabric-filtered, so there is at most one.
type ownFabric struct {
	publicKey string
	fabricID  uint64
	nodeID    uint64
	vendorID  int
	label     string
}

func decodeOwnFabric(attributes map[string]json.RawMessage) *ownFabric {
	var entries []map[string]json.RawMessage
	raw, ok := attributes["0/62/1"]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(raw, &entries); err != nil || len(entries) == 0 {
		return nil
	}
	entry := entries[0]
	var encodedKey string
	if err := json.Unmarshal(entry[fabricRootKeyField], &encodedKey); err != nil {
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil || len(key) == 0 {
		return nil
	}
	own := &ownFabric{publicKey: hex.EncodeToString(key)}
	_ = json.Unmarshal(entry[fabricIDField], &own.fabricID)
	_ = json.Unmarshal(entry[fabricNodeIDField], &own.nodeID)
	_ = json.Unmarshal(entry[fabricVendorIDField], &own.vendorID)
	_ = json.Unmarshal(entry[fabricLabelField], &own.label)
	return own
}

// decodeDeviceIdentity reads the Basic Information cluster. VendorName and
// ProductName are mandatory there, so a name is all but guaranteed — but not
// quite: Home Assistant redacts some strings in place, and a **bridge** reports
// its own identity on endpoint 0 while each bridged device carries its name in
// Bridged Device Basic Information on its own endpoint. So Name is best-effort
// and callers must cope with it being empty rather than assume one exists.
func decodeDeviceIdentity(attributes map[string]json.RawMessage) model.EvidenceDevice {
	device := model.EvidenceDevice{
		NodeLabel:   stringAttribute(attributes, "0/40/5"),
		VendorName:  stringAttribute(attributes, "0/40/1"),
		ProductName: stringAttribute(attributes, "0/40/3"),
		VendorID:    intAttribute(attributes, "0/40/2"),
		ProductID:   intAttribute(attributes, "0/40/4"),
		UniqueID:    stringAttribute(attributes, "0/40/18"),
		Serial:      stringAttribute(attributes, "0/40/15"),
	}
	// Home Assistant redacts some strings in place; a redaction marker is not a
	// value and showing it as one would read as the device's real name.
	if strings.Contains(device.Serial, "REDACTED") {
		device.Serial = ""
	}
	if strings.Contains(device.NodeLabel, "REDACTED") {
		device.NodeLabel = ""
	}
	// NodeLabel is what a person called this device through a controller, so it
	// wins over the model name the manufacturer chose. It is usually empty.
	device.Name = firstNonEmpty(device.NodeLabel, device.ProductName)
	return device
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func stringAttribute(attributes map[string]json.RawMessage, key string) string {
	var value string
	if raw, ok := attributes[key]; ok {
		_ = json.Unmarshal(raw, &value)
	}
	return value
}

func intAttribute(attributes map[string]json.RawMessage, key string) int {
	var value int
	if raw, ok := attributes[key]; ok {
		_ = json.Unmarshal(raw, &value)
	}
	return value
}

// compressedFabricID derives the eight-byte identifier that mDNS advertises,
// per Matter spec §4.3.2.2: HKDF-SHA256 over the root public key with the
// uncompressed-point marker removed, salted with the fabric ID.
func compressedFabricID(publicKeyHex string, fabricID uint64) (string, error) {
	key, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		return "", err
	}
	if len(key) != 65 || key[0] != 0x04 {
		return "", errors.New("root public key is not an uncompressed P-256 point")
	}
	salt := make([]byte, 8)
	binary.BigEndian.PutUint64(salt, fabricID)
	compressed, err := hkdf.Key(sha256.New, key[1:], salt, compressedFabricInfo, 8)
	if err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(compressed)), nil
}

// formatID renders a fabric or node ID the way Identity does for the mDNS
// instance name, so a decoded node ID compares directly with a browsed one.
func formatID(value uint64) string {
	return "0x" + strings.ToUpper(strconv.FormatUint(value, 16))
}
