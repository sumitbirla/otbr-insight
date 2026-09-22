package matter

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// Cluster, device-type and enum names from the Matter core and cluster specs,
// covering what a Thread device on this mesh reports. Unlisted IDs fall back to
// their number rather than being guessed at.

var clusterNames = map[string]string{
	"3":    "Identify",
	"4":    "Groups",
	"6":    "On/off",
	"29":   "Descriptor",
	"30":   "Binding",
	"31":   "Access control",
	"40":   "Basic information",
	"42":   "Software update requestor",
	"43":   "Localization",
	"44":   "Time format localization",
	"45":   "Unit localization",
	"46":   "Power source configuration",
	"47":   "Power source",
	"48":   "General commissioning",
	"49":   "Network commissioning",
	"50":   "Diagnostic logs",
	"51":   "General diagnostics",
	"52":   "Software diagnostics",
	"53":   "Thread network diagnostics",
	"54":   "Wi-Fi network diagnostics",
	"55":   "Ethernet network diagnostics",
	"56":   "Time synchronization",
	"57":   "Bridged device basic information",
	"59":   "Switch",
	"60":   "Administrator commissioning",
	"62":   "Operational credentials",
	"63":   "Group key management",
	"64":   "Fixed label",
	"65":   "User label",
	"69":   "Boolean state",
	"91":   "Air quality",
	"70":   "Intermittently connected device management",
	"1026": "Temperature",
	"1029": "Relative humidity",
	"1036": "PM1 concentration",
	"1037": "PM2.5 concentration",
	"1043": "Nitrogen dioxide concentration",
	"1045": "Ozone concentration",
	"1066": "PM10 concentration",
	"1067": "Volatile organic compounds",
	"1068": "Radon concentration",
	"1069": "Carbon monoxide concentration",
	"1070": "Carbon dioxide concentration",
}

var globalAttributes = map[string]string{
	"65528": "Generated commands",
	"65529": "Accepted commands",
	"65530": "Events",
	"65531": "Attributes implemented",
	"65532": "Feature map",
	"65533": "Cluster revision",
}

var deviceTypes = map[uint64]string{
	0x0011: "Power source",
	0x0012: "OTA requestor",
	0x0013: "Bridged node",
	0x0014: "OTA provider",
	0x0015: "Contact sensor",
	0x002C: "Air quality sensor",
	0x0016: "Root node",
	0x0100: "On/off light",
	0x0106: "Light sensor",
	0x0107: "Occupancy sensor",
	0x0302: "Temperature sensor",
	0x0307: "Humidity sensor",
	0x000A: "Door lock",
	0x0203: "Thermostat",
}

var routingRoles = map[uint64]string{
	0: "Unspecified", 1: "Unassigned", 2: "Sleepy end device",
	3: "End device", 4: "Router-eligible end device", 5: "Router", 6: "Leader",
}

var bootReasons = map[uint64]string{
	0: "Unspecified", 1: "Power-on reboot", 2: "Brown-out reset",
	3: "Software watchdog reset", 4: "Hardware watchdog reset",
	5: "Software update completed", 6: "Software reset",
}

var interfaceTypes = map[uint64]string{
	0: "Unspecified", 1: "Wi-Fi", 2: "Ethernet", 3: "Cellular", 4: "Thread",
}

var updateStates = map[uint64]string{
	0: "Unknown", 1: "Idle", 2: "Querying", 3: "Delayed on query",
	4: "Downloading", 5: "Applying", 6: "Delayed on apply", 7: "Rolling back",
	8: "Delayed on user consent",
}

var windowStatuses = map[uint64]string{
	0: "Closed — not accepting new controllers",
	1: "Open (enhanced) — a controller can commission it now",
	2: "Open (basic) — a controller can commission it now",
}

var powerStatuses = map[uint64]string{
	0: "Unspecified", 1: "Active", 2: "Standby", 3: "Unavailable",
}

var chargeLevels = map[uint64]string{0: "OK", 1: "Warning", 2: "Critical"}

var replaceability = map[uint64]string{
	0: "Unspecified", 1: "Not replaceable", 2: "User replaceable", 3: "Factory replaceable",
}

var commissioningErrors = map[uint64]string{
	0: "OK", 1: "Value outside range", 2: "Invalid authentication",
	3: "No fail-safe active", 4: "Busy with another admin",
}

var networkingStatuses = map[uint64]string{
	0: "Success", 1: "Out of range", 2: "Boundary exceeded", 3: "Network ID not found",
	4: "Duplicate network ID", 5: "Network not found", 6: "Regulatory error",
	7: "Auth failure", 8: "Unsupported security", 9: "Other connection failure",
	10: "IPv6 failed", 11: "IP bind failed", 12: "Unknown error",
}

// reportSections lists what goes where, in reading order. A path absent from
// the export is skipped, so a device that implements fewer clusters simply
// produces a shorter report.
// The bands sections are shown under. Ordered as a reader works down: what the
// device is and how it is doing, then how it behaves on the radio, then its
// Matter identity, and last the attributes nothing has a name for.
const (
	groupOverview  = "Overview"
	groupThread    = "Thread and radio"
	groupMatter    = "Matter"
	groupEndpoints = "What this device does"
	groupRaw       = "Everything else"
)

var reportSections = []sectionSpec{
	{
		title: "Device",
		group: groupOverview,
		icon:  "tag",
		paths: []string{"0/40/1", "0/40/3", "0/40/2", "0/40/4", "0/40/5", "0/40/6", "0/40/18", "0/40/15", "0/40/12", "0/40/11", "0/40/17", "0/40/16"},
	},
	{
		title: "Firmware and hardware",
		group: groupOverview,
		icon:  "chip",
		paths: []string{"0/40/10", "0/40/9", "0/40/8", "0/40/7", "0/40/0", "0/40/21", "0/40/19", "0/40/22"},
	},
	{
		title: "Battery",
		group: groupOverview,
		icon:  "battery",
		note:  "The device's own reading. Thread carries no battery data at all, so this is the only place it appears.",
		paths: []string{"0/47/0", "0/47/2", "0/47/12", "0/47/11", "0/47/14", "0/47/15", "0/47/13", "0/47/16", "0/47/19", "0/47/20", "0/47/25", "0/47/1", "0/47/31"},
	},
	{
		title: "Health and uptime",
		group: groupOverview,
		icon:  "heart",
		note:  "Counted by the device since it was manufactured, except uptime, which resets on every boot.",
		paths: []string{"0/51/1", "0/51/2", "0/51/3", "0/51/4", "0/51/5", "0/51/6", "0/51/7", "0/51/8"},
	},
	{
		title: "Thread network",
		group: groupThread,
		icon:  "mesh",
		paths: []string{"0/53/2", "0/53/0", "0/53/3", "0/53/4", "0/53/1", "0/53/5", "0/53/9", "0/53/13", "0/53/10", "0/53/11", "0/53/12", "0/53/59", "0/53/60", "0/53/61", "0/53/62"},
	},
	{
		title: "Thread stability",
		group: groupThread,
		icon:  "activity",
		note:  "Cumulative since the device was manufactured. Attach attempts and parent changes climbing over time is the signature of a marginal link; a single detach at power-on is normal.",
		paths: []string{"0/53/18", "0/53/21", "0/53/19", "0/53/20", "0/53/14", "0/53/15", "0/53/16", "0/53/17", "0/53/6"},
	},
	{
		title: "Radio counters",
		group: groupThread,
		icon:  "signal",
		note:  "Every frame the device has sent and received. Ack-requested minus acked is how many transmissions went unanswered; duplicates received suggest its own acks are not getting back.",
		paths: []string{
			"0/53/22", "0/53/23", "0/53/24", "0/53/25", "0/53/26", "0/53/27", "0/53/28", "0/53/29",
			"0/53/30", "0/53/31", "0/53/32", "0/53/33", "0/53/34", "0/53/35", "0/53/36", "0/53/37", "0/53/38",
			"0/53/39", "0/53/40", "0/53/41", "0/53/42", "0/53/43", "0/53/44", "0/53/45", "0/53/46",
			"0/53/47", "0/53/48", "0/53/49", "0/53/50", "0/53/51", "0/53/52", "0/53/53", "0/53/54", "0/53/55",
		},
	},
	{
		title: "Sleepy behaviour",
		group: groupThread,
		icon:  "moon",
		note:  "How long the device stays asleep between check-ins, which is what makes a \"last seen\" of a few minutes normal rather than a fault.",
		paths: []string{"0/70/0", "0/70/1", "0/70/2", "0/70/3", "0/70/4", "0/70/5"},
	},
	{
		title: "Commissioning",
		group: groupMatter,
		icon:  "key",
		paths: []string{"0/60/0", "0/60/1", "0/60/2", "0/48/0", "0/48/1", "0/48/2", "0/48/3", "0/48/4", "0/49/4", "0/49/5", "0/49/1", "0/49/6", "0/49/2", "0/49/3", "0/49/7", "0/49/9", "0/49/10", "0/49/0"},
	},
	{
		title: "Software updates",
		group: groupMatter,
		icon:  "download",
		paths: []string{"0/42/2", "0/42/1", "0/42/3", "0/42/0"},
	},
	{
		title: "Time",
		group: groupMatter,
		icon:  "moon",
		paths: []string{"0/56/1", "0/56/0", "0/56/7", "0/56/5", "0/56/6", "0/56/2", "0/56/8", "0/56/10", "0/56/11"},
	},
	{
		title: "Matter fabrics",
		group: groupMatter,
		icon:  "globe",
		note:  "Only the controller that produced this export appears in full; the others are known from their root certificates alone, which is why they have no label here.",
		paths: []string{"0/62/2", "0/62/3", "0/62/5", "0/62/1", "0/62/4", "0/62/0"},
	},
	{
		title: "What this device is",
		group: groupMatter,
		icon:  "grid",
		paths: []string{"0/29/0", "0/29/1", "0/29/2", "0/29/3"},
	},
	{
		title: "Access and groups",
		group: groupMatter,
		icon:  "shield",
		note:  "Fabric-filtered, like the fabric details above: these are the entries belonging to the controller that produced the export, not every controller's.",
		paths: []string{"0/31/0", "0/31/1", "0/31/2", "0/31/3", "0/31/4", "0/63/0", "0/63/1", "0/63/2", "0/63/3"},
	},
}

// knownAttributes names and renders each attribute. Where a value has units or
// is an enum, rendering it raw would be a number without meaning, which is the
// whole reason this table exists.
var knownAttributes = map[string]attributeSpec{
	// Basic information (0x0028).
	"0/40/0":  {"Data model revision", plain},
	"0/40/1":  {"Vendor", plain},
	"0/40/2":  {"Vendor ID", vendorID},
	"0/40/3":  {"Product", plain},
	"0/40/4":  {"Product ID", plain},
	"0/40/5":  {"Node label", redactable},
	"0/40/6":  {"Location", redactable},
	"0/40/7":  {"Hardware version", plain},
	"0/40/8":  {"Hardware version name", plain},
	"0/40/9":  {"Software version", softwareVersion},
	"0/40/10": {"Software version name", plain},
	"0/40/11": {"Manufactured", manufacturingDate},
	"0/40/12": {"Part number", plain},
	"0/40/15": {"Serial number", redactable},
	"0/40/16": {"Local config disabled", yesNo},
	"0/40/17": {"Reachable", yesNo},
	"0/40/18": {"Unique ID", plain},
	"0/40/19": {"Capability minima", capabilityMinima},
	"0/40/21": {"Specification version", softwareVersion},
	"0/40/22": {"Max paths per invoke", plain},

	// Power source (0x002F).
	"0/47/0":  {"Status", enum(powerStatuses)},
	"0/47/1":  {"Order", plain},
	"0/47/2":  {"Description", plain},
	"0/47/11": {"Battery voltage", millivolts},
	"0/47/12": {"Charge remaining", halfPercent},
	"0/47/13": {"Time remaining", seconds},
	"0/47/14": {"Charge level", enum(chargeLevels)},
	"0/47/15": {"Replacement needed", yesNo},
	"0/47/16": {"Replaceability", enum(replaceability)},
	"0/47/19": {"Replacement description", plain},
	"0/47/20": {"Common designation", plain},
	"0/47/25": {"Cells", count("cell")},
	"0/47/31": {"Endpoints powered", plain},

	// General diagnostics (0x0033).
	"0/51/1": {"Reboots", count("reboot")},
	"0/51/2": {"Uptime since last boot", seconds},
	"0/51/3": {"Total operational time", count("hour")},
	"0/51/4": {"Last boot reason", enum(bootReasons)},
	"0/51/5": {"Hardware faults", faultList},
	"0/51/6": {"Radio faults", faultList},
	"0/51/7": {"Network faults", faultList},
	"0/51/8": {"Test event triggers enabled", yesNo},

	// Thread network diagnostics (0x0035).
	"0/53/0":  {"Channel", plain},
	"0/53/1":  {"Role on the mesh", enum(routingRoles)},
	"0/53/2":  {"Network name", plain},
	"0/53/3":  {"PAN ID", hexValue(4)},
	"0/53/4":  {"Extended PAN ID", hexValue(16)},
	"0/53/5":  {"Mesh-local prefix", meshLocalPrefix},
	"0/53/6":  {"Overruns", count("overrun")},
	"0/53/9":  {"Partition ID", plain},
	"0/53/10": {"Leader weighting", plain},
	"0/53/11": {"Network data version", plain},
	"0/53/12": {"Stable network data version", plain},
	"0/53/13": {"Leader router ID", plain},
	"0/53/14": {"Times detached", count("time")},
	"0/53/15": {"Times a child", count("time")},
	"0/53/16": {"Times a router", count("time")},
	"0/53/17": {"Times leader", count("time")},
	"0/53/18": {"Attach attempts", count("attempt")},
	"0/53/19": {"Partition changes", count("change")},
	"0/53/20": {"Better-partition attach attempts", count("attempt")},
	"0/53/21": {"Parent changes", count("change")},
	"0/53/22": {"TX total", plain},
	"0/53/23": {"TX unicast", plain},
	"0/53/24": {"TX broadcast", plain},
	"0/53/25": {"TX ack requested", plain},
	"0/53/26": {"TX acked", plain},
	"0/53/27": {"TX no ack requested", plain},
	"0/53/28": {"TX data frames", plain},
	"0/53/29": {"TX data polls", plain},
	"0/53/30": {"TX beacons", plain},
	"0/53/31": {"TX beacon requests", plain},
	"0/53/32": {"TX other frames", plain},
	"0/53/33": {"TX retries", plain},
	"0/53/34": {"TX direct max retry failures", plain},
	"0/53/35": {"TX indirect max retry failures", plain},
	"0/53/36": {"TX errors — CCA", plain},
	"0/53/37": {"TX errors — abort", plain},
	"0/53/38": {"TX errors — busy channel", plain},
	"0/53/39": {"RX total", plain},
	"0/53/40": {"RX unicast", plain},
	"0/53/41": {"RX broadcast", plain},
	"0/53/42": {"RX data frames", plain},
	"0/53/43": {"RX data polls", plain},
	"0/53/44": {"RX beacons", plain},
	"0/53/45": {"RX beacon requests", plain},
	"0/53/46": {"RX other frames", plain},
	"0/53/47": {"RX filtered by address", plain},
	"0/53/48": {"RX filtered by destination", plain},
	"0/53/49": {"RX duplicates", plain},
	"0/53/50": {"RX errors — no frame", plain},
	"0/53/51": {"RX errors — unknown neighbour", plain},
	"0/53/52": {"RX errors — invalid source", plain},
	"0/53/53": {"RX errors — security", plain},
	"0/53/54": {"RX errors — checksum", plain},
	"0/53/55": {"RX errors — other", plain},
	"0/53/59": {"Security policy", securityPolicy},
	"0/53/60": {"Channels permitted", channelMask},
	"0/53/61": {"Dataset parts held", datasetComponents},
	"0/53/62": {"Active network faults", faultList},

	// Intermittently connected device management (0x0046).
	"0/70/0": {"Idle check-in interval", seconds},
	"0/70/1": {"Stays awake for", millis},
	"0/70/2": {"Wakes on activity within", millis},
	"0/70/3": {"Registered clients", plain},
	"0/70/4": {"Client registrations allowed", plain},
	"0/70/5": {"Operating mode", plain},

	// Administrator commissioning (0x003C) and general commissioning (0x0030).
	"0/60/0": {"Commissioning window", enum(windowStatuses)},
	"0/60/1": {"Opened by fabric index", plain},
	"0/60/2": {"Opened by vendor", vendorID},
	"0/48/0": {"Fail-safe breadcrumb", plain},
	"0/48/1": {"Fail-safe limits", failSafeLimits},
	"0/48/2": {"Regulatory location", plain},
	"0/48/3": {"Location capability", plain},
	"0/48/4": {"Supports concurrent connection", yesNo},

	// Network commissioning (0x0031).
	"0/49/0":  {"Max networks", plain},
	"0/49/2":  {"Scan timeout", count("second")},
	"0/49/3":  {"Connect timeout", count("second")},
	"0/49/4":  {"Interface enabled", yesNo},
	"0/49/5":  {"Last networking status", enum(networkingStatuses)},
	"0/49/6":  {"Last network ID", base64Bytes},
	"0/49/7":  {"Last connect error", plain},
	"0/49/9":  {"Supported Thread features", hexValue(2)},
	"0/49/10": {"Thread version", plain},

	// Operational credentials (0x003E).
	"0/62/2": {"Supported fabrics", count("fabric")},
	"0/62/3": {"Commissioned fabrics", count("fabric")},
	"0/62/5": {"This controller's fabric index", plain},

	// Descriptor (0x001D).
	"0/29/0": {"Device types", deviceTypeList},
	"0/29/1": {"Clusters on this endpoint", clusterList},
	"0/29/2": {"Client clusters", clusterList},
	"0/29/3": {"Child endpoints", plain},

	// Operational credentials (0x003E), certificates decoded rather than printed.
	"0/62/0": {"Operational certificates", nocList},
	"0/62/1": {"This controller's fabric", fabricDescriptors},
	"0/62/4": {"Trusted root certificates", certificateList},

	// Access control (0x001F) and group key management (0x003F).
	"0/31/0": {"Who may administer this device", accessControlList},
	"0/31/1": {"Access control extensions", plain},
	"0/31/2": {"Max access control entries per fabric", plain},
	"0/31/3": {"Max subjects per entry", plain},
	"0/31/4": {"Max targets per entry", plain},
	"0/63/0": {"Group key map", groupKeyMap},
	"0/63/1": {"Group table", plain},
	"0/63/2": {"Max groups per fabric", plain},
	"0/63/3": {"Max key sets per fabric", plain},

	"0/49/1": {"Provisioned networks", commissionedNetworks},

	// Time synchronization (0x0038).
	"0/56/0":  {"UTC time", plain},
	"0/56/1":  {"Clock granularity", enum(timeGranularity)},
	"0/56/2":  {"Time source", plain},
	"0/56/5":  {"Time zone", timeZoneList},
	"0/56/6":  {"Daylight saving offsets", dstOffsets},
	"0/56/7":  {"Local time", plain},
	"0/56/8":  {"Time zone database", plain},
	"0/56/10": {"Max time zone entries", plain},
	"0/56/11": {"Max daylight saving entries", plain},

	// Sensor clusters. Every one of these reports a bare integer in hundredths
	// or a float with its unit in a separate attribute, so an unnamed row here
	// reads as a meaningless number — 2519 rather than 25.19 °C.
	"*/91/0":   {"Air quality", enum(airQuality)},
	"*/6/0":    {"On", yesNo},
	"*/1026/0": {"Temperature", centi("°C")},
	"*/1026/1": {"Lowest measurable", centi("°C")},
	"*/1026/2": {"Highest measurable", centi("°C")},
	"*/1026/3": {"Tolerance", centi("°C")},
	"*/1029/0": {"Relative humidity", centi("%")},
	"*/1029/1": {"Lowest measurable", centi("%")},
	"*/1029/2": {"Highest measurable", centi("%")},
	"*/1029/3": {"Tolerance", centi("%")},

	// Clusters that appear on any endpoint, so they are keyed without one.
	"*/29/0": {"Device types", deviceTypeList},
	"*/29/1": {"Clusters on this endpoint", clusterList},
	"*/29/2": {"Client clusters", clusterList},
	"*/29/3": {"Child endpoints", plain},
	"*/3/0":  {"Identify countdown", count("second")},
	"*/3/1":  {"Identify type", plain},
	"*/69/0": {"Sensor state", booleanState},

	// Software update requestor (0x002A).
	"0/42/0": {"Configured update providers", plain},
	"0/42/1": {"Update possible", yesNo},
	"0/42/2": {"Update state", enum(updateStates)},
	"0/42/3": {"Update progress", plain},
}

var timeGranularity = map[uint64]string{
	0: "No time known", 1: "Within a minute", 2: "Within a second",
	3: "Within a millisecond", 4: "Within a microsecond",
}

var airQuality = map[uint64]string{
	0: "Unknown", 1: "Good", 2: "Fair", 3: "Moderate",
	4: "Poor", 5: "Very poor", 6: "Extremely poor",
}

// Concentration measurement clusters share one layout, so they are generated
// rather than written out per pollutant: the spec gives them the same attribute
// numbers, and the unit lives in attribute 8 rather than in the value.
var concentrationClusters = []string{"1036", "1037", "1043", "1045", "1066", "1067", "1068", "1069", "1070"}

var concentrationAttributes = map[string]attributeSpec{
	"0":  {"Measured", plain},
	"1":  {"Lowest measurable", plain},
	"2":  {"Highest measurable", plain},
	"3":  {"Peak measured", plain},
	"4":  {"Peak window", seconds},
	"5":  {"Average measured", plain},
	"6":  {"Average window", seconds},
	"7":  {"Uncertainty", plain},
	"8":  {"Unit", enum(measurementUnits)},
	"9":  {"Medium", enum(measurementMedia)},
	"10": {"Level", enum(concentrationLevels)},
}

var measurementUnits = map[uint64]string{
	0: "ppm", 1: "ppb", 2: "ppt", 3: "mg/m³", 4: "µg/m³", 5: "ng/m³",
	6: "particles per m³", 7: "Bq/m³",
}

var measurementMedia = map[uint64]string{0: "Air", 1: "Water", 2: "Soil"}

var concentrationLevels = map[uint64]string{
	0: "Unknown", 1: "Low", 2: "Medium", 3: "High", 4: "Critical",
}

func init() {
	for _, cluster := range concentrationClusters {
		for attribute, spec := range concentrationAttributes {
			knownAttributes["*/"+cluster+"/"+attribute] = spec
		}
	}
}

// centi renders the hundredths-of-a-unit integers the measurement clusters use.
func centi(unit string) func(json.RawMessage) string {
	return func(raw json.RawMessage) string {
		value, ok := asFloat(raw)
		if !ok {
			return renderRaw(raw)
		}
		return fmt.Sprintf("%.2f %s", value/100, unit)
	}
}

// dstOffsets renders the daylight-saving schedule. Matter counts these in
// microseconds since 2000-01-01, not the Unix epoch and not seconds.
func dstOffsets(raw json.RawMessage) string {
	var entries []struct {
		Offset     int    `json:"0"`
		ValidStart uint64 `json:"1"`
		ValidUntil uint64 `json:"2"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil || len(entries) == 0 {
		return renderRaw(raw)
	}
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		label := "no offset"
		if entry.Offset != 0 {
			label = fmt.Sprintf("%+.2g hours", float64(entry.Offset)/3600)
		}
		if entry.ValidUntil > 0 {
			label += " until " + matterTime(entry.ValidUntil/1_000_000)
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, ", ")
}

// timeZoneList renders the device's configured time zone, which is otherwise an
// offset in seconds inside a list of structs.
func timeZoneList(raw json.RawMessage) string {
	var entries []struct {
		Offset int    `json:"0"`
		Valid  int64  `json:"1"`
		Name   string `json:"2"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil || len(entries) == 0 {
		return renderRaw(raw)
	}
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		label := fmt.Sprintf("UTC%+.2g hours", float64(entry.Offset)/3600)
		if entry.Name != "" {
			label = entry.Name + " (" + label + ")"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, ", ")
}

// booleanState renders the Boolean State cluster. The spec leaves which way
// round it means to the device type, so this says what the bit is without
// claiming to know whether the door is open.
// datasetComponents names which parts of the Thread operational dataset the
// device holds. A missing network key or PSKc here is the difference between a
// device that can rejoin on its own and one that must be re-commissioned.
func datasetComponents(raw json.RawMessage) string {
	names := []string{
		"active timestamp", "pending timestamp", "network key", "network name",
		"extended PAN ID", "mesh-local prefix", "delay", "PAN ID", "channel",
		"PSKc", "security policy", "channel mask",
	}
	var value map[string]bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return renderRaw(raw)
	}
	var held, missing []string
	for index, name := range names {
		present, known := value[strconv.Itoa(index)]
		if !known {
			continue
		}
		if present {
			held = append(held, name)
		} else {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return "all of them"
	}
	return strings.Join(held, ", ") + " — missing: " + strings.Join(missing, ", ")
}

func booleanState(raw json.RawMessage) string {
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return renderRaw(raw)
	}
	if value {
		return "true — for a contact sensor, the contact is closed"
	}
	return "false — for a contact sensor, the contact is open"
}

func vendorID(raw json.RawMessage) string {
	value, ok := asUint(raw)
	if !ok {
		return renderRaw(raw)
	}
	if name, known := vendorNames[int(value)]; known {
		return fmt.Sprintf("%d — %s", value, name)
	}
	return fmt.Sprintf("%d", value)
}

func failSafeLimits(raw json.RawMessage) string {
	var value struct {
		Fail    uint64 `json:"0"`
		Maximum uint64 `json:"1"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return renderRaw(raw)
	}
	return fmt.Sprintf("%d s default, %d s maximum", value.Fail, value.Maximum)
}

// tableSections render the list-of-struct attributes, which carry the most
// diagnostically useful detail in the whole export and are unreadable raw.
func tableSections(attributes map[string]json.RawMessage, used map[string]bool) []model.ReportSection {
	var sections []model.ReportSection
	if section, ok := neighbourSection(attributes, used); ok {
		sections = append(sections, section)
	}
	if section, ok := routeSection(attributes, used); ok {
		sections = append(sections, section)
	}
	if section, ok := interfaceSection(attributes, used); ok {
		sections = append(sections, section)
	}
	return sections
}

func neighbourSection(attributes map[string]json.RawMessage, used map[string]bool) (model.ReportSection, bool) {
	raw, ok := attributes["0/53/7"]
	if !ok {
		return model.ReportSection{}, false
	}
	used["0/53/7"] = true
	var list []struct {
		ExtAddress       uint64  `json:"0"`
		Age              uint64  `json:"1"`
		RLOC16           uint64  `json:"2"`
		LinkFrameCounter uint64  `json:"3"`
		MLEFrameCounter  uint64  `json:"4"`
		LQI              uint64  `json:"5"`
		AverageRSSI      *int    `json:"6"`
		LastRSSI         *int    `json:"7"`
		FrameErrorRate   float64 `json:"8"`
		MessageErrorRate float64 `json:"9"`
		RxOnWhenIdle     bool    `json:"10"`
		FullThreadDevice bool    `json:"11"`
		FullNetworkData  bool    `json:"12"`
		IsChild          bool    `json:"13"`
	}
	if err := json.Unmarshal(raw, &list); err != nil || len(list) == 0 {
		return model.ReportSection{}, false
	}
	section := model.ReportSection{
		Title: "Neighbours as this device hears them",
		Group: groupThread,
		Icon:  "signal",
		Note:  "The other half of every link measurement on the map, reported from the device's end rather than the border router's. A sleepy child has exactly one neighbour: its parent.",
	}
	for _, item := range list {
		prefix := fmt.Sprintf("%016X", item.ExtAddress)
		role := "Neighbour"
		if item.IsChild {
			role = "Child"
		} else if !item.RxOnWhenIdle {
			role = "Sleepy neighbour"
		} else {
			role = "Parent or router"
		}
		section.Entries = append(section.Entries,
			model.ReportEntry{Label: role, Value: prefix, Detail: fmt.Sprintf("RLOC16 0x%04X", item.RLOC16), Source: "0/53/7"},
			model.ReportEntry{Label: "  Last heard", Value: formatSeconds(item.Age) + " ago", Source: "0/53/7"},
			model.ReportEntry{Label: "  Link quality", Value: fmt.Sprintf("%d of 3", item.LQI), Source: "0/53/7"},
			model.ReportEntry{Label: "  Last RSSI", Value: signalText(item.LastRSSI), Source: "0/53/7"},
			model.ReportEntry{Label: "  Average RSSI", Value: signalText(item.AverageRSSI), Detail: "Reported by the device; treat with suspicion when it is far stronger than the last reading", Source: "0/53/7"},
			model.ReportEntry{Label: "  Frame error rate", Value: percentText(item.FrameErrorRate), Source: "0/53/7"},
			model.ReportEntry{Label: "  Message error rate", Value: percentText(item.MessageErrorRate), Source: "0/53/7"},
			model.ReportEntry{Label: "  Listens while idle", Value: boolText(item.RxOnWhenIdle), Source: "0/53/7"},
			model.ReportEntry{Label: "  Full Thread device", Value: boolText(item.FullThreadDevice), Source: "0/53/7"},
		)
	}
	return section, true
}

func routeSection(attributes map[string]json.RawMessage, used map[string]bool) (model.ReportSection, bool) {
	raw, ok := attributes["0/53/8"]
	if !ok {
		return model.ReportSection{}, false
	}
	used["0/53/8"] = true
	var list []struct {
		ExtAddress      uint64 `json:"0"`
		RLOC16          uint64 `json:"1"`
		RouterID        uint64 `json:"2"`
		NextHop         uint64 `json:"3"`
		PathCost        uint64 `json:"4"`
		LQIIn           uint64 `json:"5"`
		LQIOut          uint64 `json:"6"`
		Age             uint64 `json:"7"`
		Allocated       bool   `json:"8"`
		LinkEstablished bool   `json:"9"`
	}
	if err := json.Unmarshal(raw, &list); err != nil || len(list) == 0 {
		return model.ReportSection{}, false
	}
	section := model.ReportSection{
		Title: "Routers this device knows about",
		Group: groupThread,
		Icon:  "route",
		Note:  "The routing table as the device sees it. A sleepy child keeps only its own parent here.",
	}
	for _, item := range list {
		section.Entries = append(section.Entries,
			model.ReportEntry{Label: "Router", Value: fmt.Sprintf("%016X", item.ExtAddress), Detail: fmt.Sprintf("RLOC16 0x%04X, router ID %d", item.RLOC16, item.RouterID), Source: "0/53/8"},
			model.ReportEntry{Label: "  Path cost", Value: fmt.Sprintf("%d", item.PathCost), Source: "0/53/8"},
			model.ReportEntry{Label: "  Link quality in / out", Value: fmt.Sprintf("%d / %d", item.LQIIn, item.LQIOut), Source: "0/53/8"},
			model.ReportEntry{Label: "  Link established", Value: boolText(item.LinkEstablished), Source: "0/53/8"},
		)
	}
	return section, true
}

func interfaceSection(attributes map[string]json.RawMessage, used map[string]bool) (model.ReportSection, bool) {
	raw, ok := attributes["0/51/0"]
	if !ok {
		return model.ReportSection{}, false
	}
	used["0/51/0"] = true
	var list []struct {
		Name            string   `json:"0"`
		IsOperational   bool     `json:"1"`
		OffPremise      *bool    `json:"2"`
		HardwareAddress string   `json:"4"`
		IPv4Addresses   []string `json:"5"`
		IPv6Addresses   []string `json:"6"`
		Type            uint64   `json:"7"`
	}
	if err := json.Unmarshal(raw, &list); err != nil || len(list) == 0 {
		return model.ReportSection{}, false
	}
	section := model.ReportSection{
		Title: "Network interfaces",
		Group: groupThread,
		Icon:  "globe",
		Note:  "The hardware address of a Thread interface is the device's extended address — the same identifier the mesh map and the device list key on.",
	}
	for _, item := range list {
		section.Entries = append(section.Entries,
			model.ReportEntry{Label: "Interface", Value: item.Name, Detail: enumName(interfaceTypes, item.Type), Source: "0/51/0"},
			model.ReportEntry{Label: "  Operational", Value: boolText(item.IsOperational), Source: "0/51/0"},
		)
		if item.HardwareAddress != "" {
			section.Entries = append(section.Entries, model.ReportEntry{
				Label: "  Extended address", Value: base64Bytes(json.RawMessage(strconv.Quote(item.HardwareAddress))), Source: "0/51/0",
			})
		}
		for _, address := range item.IPv6Addresses {
			section.Entries = append(section.Entries, model.ReportEntry{
				Label: "  IPv6 address", Value: base64Address(json.RawMessage(strconv.Quote(address))), Source: "0/51/0",
			})
		}
	}
	return section, true
}

func enumName(values map[uint64]string, value uint64) string {
	if name, ok := values[value]; ok {
		return name
	}
	return fmt.Sprintf("Unknown (%d)", value)
}

func signalText(value *int) string {
	if value == nil {
		return "not reported"
	}
	return fmt.Sprintf("%d dBm", *value)
}

// percentText renders the error rates, which the export gives as a fraction.
func percentText(value float64) string {
	if value == 0 {
		return "0%"
	}
	if value <= 1 {
		return fmt.Sprintf("%.2f%%", value*100)
	}
	return fmt.Sprintf("%.2f%%", value)
}

func boolText(value bool) string {
	if value {
		return "Yes"
	}
	return "No"
}
