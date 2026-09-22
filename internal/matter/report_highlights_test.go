package matter

import (
	"testing"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

func highlightsOf(t *testing.T, body string) map[string]model.ReportHighlight {
	t.Helper()
	out := map[string]model.ReportHighlight{}
	for _, highlight := range Highlights(attributesFrom(t, body)) {
		out[highlight.Label] = highlight
	}
	return out
}

func TestHighlightsGradeTheBatteryOnTheDevicesOwnVerdict(t *testing.T) {
	// A healthy reading stays good, and the percentage alone never raises an
	// alarm while it is high.
	got := highlightsOf(t, `{"0/47/12": 200, "0/47/11": 1495, "0/47/14": 0, "0/47/15": false}`)
	if got["Battery"].Value != "100%" || got["Battery"].Tone != toneGood {
		t.Fatalf("healthy battery = %+v", got["Battery"])
	}
	// The device's own flag outranks a high percentage: some chemistries read
	// full until they collapse, so trusting the number alone misses it.
	got = highlightsOf(t, `{"0/47/12": 200, "0/47/15": true}`)
	if got["Battery"].Tone != toneBad {
		t.Fatalf("replacement-needed battery = %+v", got["Battery"])
	}
	got = highlightsOf(t, `{"0/47/12": 40}`)
	if got["Battery"].Value != "20%" || got["Battery"].Tone != toneWarn {
		t.Fatalf("low battery = %+v", got["Battery"])
	}
}

func TestHighlightsGradeTheParentLinkOnTheLastReading(t *testing.T) {
	for body, want := range map[string]string{
		`{"0/53/7": [{"2": 2048, "5": 3, "6": -10, "7": -60}]}`: toneGood,
		`{"0/53/7": [{"2": 2048, "5": 2, "6": -10, "7": -80}]}`: toneWarn,
		`{"0/53/7": [{"2": 2048, "5": 1, "6": -10, "7": -90}]}`: toneBad,
	} {
		got := highlightsOf(t, body)
		link := got["Hears its parent at"]
		if link.Tone != want {
			t.Errorf("%s -> tone %q, want %q", body, link.Tone, want)
		}
		// Graded on the last reading, never the average: real hardware reports
		// an average tens of dB stronger than any actual measurement.
		if link.Value == "-10 dBm" {
			t.Errorf("%s used the average RSSI", body)
		}
	}
}

func TestHighlightsOnlyClearFaultsTheDeviceActuallyReported(t *testing.T) {
	got := highlightsOf(t, `{"0/51/5": [], "0/51/6": [], "0/51/7": []}`)
	if got["Faults"].Value != "None" || got["Faults"].Tone != toneGood {
		t.Fatalf("clear faults = %+v", got["Faults"])
	}
	got = highlightsOf(t, `{"0/51/5": [], "0/51/6": [3], "0/51/7": []}`)
	if got["Faults"].Tone != toneBad || got["Faults"].Detail != "1 radio" {
		t.Fatalf("radio fault = %+v", got["Faults"])
	}
	// No fault attributes at all must not read as "no faults".
	if _, present := highlightsOf(t, `{"0/40/1": "Example"}`)["Faults"]; present {
		t.Fatal("claimed the device was fault-free without it saying so")
	}
}

func TestHighlightsLeaveRebootsUncoloured(t *testing.T) {
	// Every reboot since manufacture is counted, so a number here is history
	// rather than a fault, and colouring it would cry wolf on every device.
	got := highlightsOf(t, `{"0/51/1": 5, "0/51/4": 6}`)
	if got["Reboots"].Value != "5" || got["Reboots"].Tone != "" {
		t.Fatalf("reboots = %+v", got["Reboots"])
	}
	if got["Reboots"].Detail != "Last: Software reset" {
		t.Fatalf("boot reason = %q", got["Reboots"].Detail)
	}
}
