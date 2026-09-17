package service

import (
	"testing"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

func rssi(v int) *int { return &v }

func TestSignalTrailFoldsPollsIntoBuckets(t *testing.T) {
	trail := newSignalTrail()
	base := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	// Three polls inside one minute become one bucket carrying mean, min and max.
	for i, v := range []int{-60, -70, -65} {
		trail.Record([]model.Device{{ExtendedAddress: "0102030405060708", RSSI: rssi(v), LinkQuality: rssi(3)}},
			base.Add(time.Duration(i*5)*time.Second))
	}
	history := trail.History("0102030405060708")
	if len(history.Samples) != 1 {
		t.Fatalf("samples = %+v, want one bucket", history.Samples)
	}
	one := history.Samples[0]
	if one.Samples != 3 || *one.RSSI != -65 || *one.MinRSSI != -70 || *one.MaxRSSI != -60 {
		t.Fatalf("bucket = %+v, want 3 samples meaning -65 across -70..-60", one)
	}
	if !one.Present || *one.LinkQual != 3 {
		t.Errorf("presence and quality: %+v", one)
	}
	if *history.MeanRSSI != -65 || *history.MinRSSI != -70 || *history.MaxRSSI != -60 {
		t.Errorf("summary = %+v", history)
	}
	if history.BucketSeconds != 60 || history.WindowSeconds != 7200 {
		t.Errorf("window description = %+v", history)
	}
}

// A device that stops being reported must leave a gap, not a trail that resumes
// as though nothing happened — the gap is the whole point of keeping presence.
func TestSignalTrailMarksAbsenceAsGaps(t *testing.T) {
	trail := newSignalTrail()
	base := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	device := model.Device{ExtendedAddress: "0102030405060708", RSSI: rssi(-60)}
	other := model.Device{ExtendedAddress: "0203040506070809", RSSI: rssi(-70)}

	trail.Record([]model.Device{device, other}, base)
	// Two minutes with only the other device present.
	trail.Record([]model.Device{other}, base.Add(time.Minute))
	trail.Record([]model.Device{other}, base.Add(2*time.Minute))
	trail.Record([]model.Device{device, other}, base.Add(3*time.Minute))

	history := trail.History("0102030405060708")
	if len(history.Samples) != 4 {
		t.Fatalf("samples = %d, want four buckets", len(history.Samples))
	}
	present := []bool{true, false, false, true}
	for i, want := range present {
		if history.Samples[i].Present != want {
			t.Errorf("bucket %d present = %v, want %v", i, history.Samples[i].Present, want)
		}
	}
	if history.PresentPct == nil || *history.PresentPct != 50 {
		t.Errorf("presentPercent = %v, want 50", history.PresentPct)
	}
	if history.CoveredSecs != 240 {
		t.Errorf("coveredSeconds = %d, want 240", history.CoveredSecs)
	}
}

func TestSignalTrailBoundsItsWindowAndForgetsDepartedDevices(t *testing.T) {
	trail := newSignalTrail()
	base := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	device := model.Device{ExtendedAddress: "0102030405060708", RSSI: rssi(-60)}
	for i := 0; i < signalBuckets+30; i++ {
		trail.Record([]model.Device{device}, base.Add(time.Duration(i)*time.Minute))
	}
	if got := len(trail.History("0102030405060708").Samples); got != signalBuckets {
		t.Fatalf("samples = %d, want the window capped at %d", got, signalBuckets)
	}
	// Once the whole window is absence the trail is dropped rather than kept empty.
	gone := base.Add(time.Duration(signalBuckets+30) * time.Minute)
	for i := 0; i <= signalBuckets; i++ {
		trail.Record([]model.Device{{ExtendedAddress: "0203040506070809", RSSI: rssi(-70)}}, gone.Add(time.Duration(i)*time.Minute))
	}
	if known := trail.known(); len(known) != 1 || known[0] != "0203040506070809" {
		t.Fatalf("known trails = %v, want only the live device", known)
	}
}

// An unknown device is not an error: nothing has been seen of it since start-up.
func TestSignalTrailReportsUnknownDeviceAsEmpty(t *testing.T) {
	history := newSignalTrail().History("0A0B0C0D0E0F0102")
	if len(history.Samples) != 0 || history.MeanRSSI != nil || history.CoveredSecs != 0 {
		t.Fatalf("history = %+v, want empty", history)
	}
	if history.ExtendedAddress != "0a0b0c0d0e0f0102" {
		t.Errorf("key not normalised: %q", history.ExtendedAddress)
	}
}
