package matter

import (
	"encoding/json"
	"fmt"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// The headline readings.
//
// A full export is around 250 rows, and almost nobody opens one to read all of
// them: they want to know whether the battery is dying, whether the thing has
// been rebooting, and how well it hears its parent. Those are scattered across
// four clusters, so they are lifted out here.
//
// Tones are deliberately conservative. A highlight coloured "bad" says the
// device reported a problem, never that this code inferred one — an inferred
// alarm on a healthy device teaches the reader to ignore the colour.

const (
	toneGood = "good"
	toneWarn = "warn"
	toneBad  = "bad"
)

// Highlights picks the readings worth showing before the sections.
func Highlights(attributes map[string]json.RawMessage) []model.ReportHighlight {
	var out []model.ReportHighlight
	if battery, ok := batteryHighlight(attributes); ok {
		out = append(out, battery)
	}
	if link, ok := linkHighlight(attributes); ok {
		out = append(out, link)
	}
	if role, ok := stringHighlight(attributes, "0/53/1", "Role on the mesh"); ok {
		out = append(out, role)
	}
	if uptime, ok := stringHighlight(attributes, "0/51/2", "Up for"); ok {
		// The seconds in brackets are for the detail rows, not a headline.
		uptime.Value = trimParenthetical(uptime.Value)
		out = append(out, uptime)
	}
	if reboots, ok := rebootHighlight(attributes); ok {
		out = append(out, reboots)
	}
	if firmware, ok := stringHighlight(attributes, "0/40/10", "Firmware"); ok {
		out = append(out, firmware)
	}
	if faults, ok := faultHighlight(attributes); ok {
		out = append(out, faults)
	}
	return out
}

func batteryHighlight(attributes map[string]json.RawMessage) (model.ReportHighlight, bool) {
	raw, ok := attributes["0/47/12"]
	if !ok {
		return model.ReportHighlight{}, false
	}
	percent, ok := asFloat(raw)
	if !ok {
		return model.ReportHighlight{}, false
	}
	percent /= 2
	highlight := model.ReportHighlight{Label: "Battery", Value: fmt.Sprintf("%.0f%%", percent), Tone: toneGood}
	if voltage, ok := asFloat(attributes["0/47/11"]); ok {
		highlight.Detail = fmt.Sprintf("%.3f V", voltage/1000)
	}
	// The device's own verdict outranks the percentage: a chemistry whose
	// voltage curve is flat can read high and still be flagged for replacement.
	if level, ok := asUint(attributes["0/47/14"]); ok && level > 0 {
		highlight.Tone = toneWarn
		if level > 1 {
			highlight.Tone = toneBad
		}
		highlight.Detail = "Device reports charge level " + enumName(map[uint64]string{1: "warning", 2: "critical"}, level)
	}
	var needed bool
	if err := json.Unmarshal(attributes["0/47/15"], &needed); err == nil && needed {
		highlight.Tone = toneBad
		highlight.Detail = "Device asks for replacement"
	}
	if highlight.Tone == toneGood && percent <= 30 {
		highlight.Tone = toneWarn
	}
	return highlight, true
}

// linkHighlight reports the parent link as the device hears it, which is the
// half the mesh map cannot show. LastRSSI is used rather than the average,
// which real hardware reports implausibly high.
func linkHighlight(attributes map[string]json.RawMessage) (model.ReportHighlight, bool) {
	raw, ok := attributes["0/53/7"]
	if !ok {
		return model.ReportHighlight{}, false
	}
	var list []struct {
		RLOC16   uint64 `json:"2"`
		LQI      uint64 `json:"5"`
		LastRSSI *int   `json:"7"`
	}
	if err := json.Unmarshal(raw, &list); err != nil || len(list) == 0 || list[0].LastRSSI == nil {
		return model.ReportHighlight{}, false
	}
	rssi := *list[0].LastRSSI
	highlight := model.ReportHighlight{
		Label:  "Hears its parent at",
		Value:  fmt.Sprintf("%d dBm", rssi),
		Detail: fmt.Sprintf("RLOC16 0x%04X, link quality %d of 3", list[0].RLOC16, list[0].LQI),
		Tone:   toneGood,
	}
	switch {
	case rssi <= -85:
		highlight.Tone = toneBad
	case rssi <= -75:
		highlight.Tone = toneWarn
	}
	return highlight, true
}

// rebootHighlight leaves the tone neutral: a device that has been up for weeks
// still counts every reboot since it was made, so a number here is history,
// not a fault.
func rebootHighlight(attributes map[string]json.RawMessage) (model.ReportHighlight, bool) {
	raw, ok := attributes["0/51/1"]
	if !ok {
		return model.ReportHighlight{}, false
	}
	value, ok := asUint(raw)
	if !ok {
		return model.ReportHighlight{}, false
	}
	highlight := model.ReportHighlight{Label: "Reboots", Value: fmt.Sprintf("%d", value)}
	if reason, ok := asUint(attributes["0/51/4"]); ok {
		highlight.Detail = "Last: " + enumName(bootReasons, reason)
	}
	return highlight, true
}

// faultHighlight surfaces the three fault lists as one reading, and only says
// "none" when the device actually reported all three as empty.
func faultHighlight(attributes map[string]json.RawMessage) (model.ReportHighlight, bool) {
	paths := map[string]string{"0/51/5": "hardware", "0/51/6": "radio", "0/51/7": "network", "0/53/62": "Thread"}
	var reported []string
	var present int
	for path, name := range paths {
		raw, ok := attributes[path]
		if !ok {
			continue
		}
		present++
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err == nil && len(list) > 0 {
			reported = append(reported, fmt.Sprintf("%d %s", len(list), name))
		}
	}
	if present == 0 {
		return model.ReportHighlight{}, false
	}
	if len(reported) == 0 {
		return model.ReportHighlight{Label: "Faults", Value: "None", Detail: "Hardware, radio and network all clear", Tone: toneGood}, true
	}
	return model.ReportHighlight{Label: "Faults", Value: fmt.Sprintf("%d reported", len(reported)), Detail: joinWords(reported), Tone: toneBad}, true
}

func stringHighlight(attributes map[string]json.RawMessage, path, label string) (model.ReportHighlight, bool) {
	raw, ok := attributes[path]
	if !ok {
		return model.ReportHighlight{}, false
	}
	entry := renderAttribute(path, raw)
	if entry.Value == "" {
		return model.ReportHighlight{}, false
	}
	return model.ReportHighlight{Label: label, Value: entry.Value}, true
}

func trimParenthetical(value string) string {
	if index := indexOfByte(value, '('); index > 0 {
		return trimTrailingSpace(value[:index])
	}
	return value
}

func indexOfByte(value string, target byte) int {
	for i := 0; i < len(value); i++ {
		if value[i] == target {
			return i
		}
	}
	return -1
}

func trimTrailingSpace(value string) string {
	for len(value) > 0 && value[len(value)-1] == ' ' {
		value = value[:len(value)-1]
	}
	return value
}

func joinWords(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		out := parts[0]
		for _, part := range parts[1 : len(parts)-1] {
			out += ", " + part
		}
		return out + " and " + parts[len(parts)-1]
	}
}
