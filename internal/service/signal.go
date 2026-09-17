package service

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// A device's signal is polled every few seconds but read only occasionally, so
// samples are folded into fixed time buckets rather than kept one per poll: a
// two-hour window costs 120 buckets per device instead of ~1400 readings.
const (
	signalBucket    = time.Minute
	signalBuckets   = 120
	signalMaxTrails = 256
)

// signalTrail records how each device's radio link has behaved over the last
// couple of hours, so "is this device flaky?" is answered by a shape rather than
// by one instantaneous reading.
//
// It is **memory only, and deliberately**. A sample every poll means a disk
// write every few seconds forever, which is not a trade this app makes for a
// diagnostic aid — the persistent state here is still just names and one dataset
// backup. The cost is that the trail starts empty after a restart, and the API
// says so through coveredSeconds rather than pretending the window is full.
type signalTrail struct {
	mu      sync.Mutex
	devices map[string]*deviceTrail
}

type deviceTrail struct {
	// buckets is oldest-first and at most signalBuckets long; the last entry is
	// always the bucket currently being filled.
	buckets []signalBucketData
}

type signalBucketData struct {
	at      time.Time
	count   int
	sum     int
	min     int
	max     int
	quality int
	present bool
}

// roundToInt rounds half away from zero; RSSI means are negative, and the
// default truncation would bias every one of them upward.
func roundToInt(f float64) int {
	if f < 0 {
		return int(f - 0.5)
	}
	return int(f + 0.5)
}

func newSignalTrail() *signalTrail {
	return &signalTrail{devices: map[string]*deviceTrail{}}
}

// Record folds one inventory reading into every trail. Devices absent from the
// inventory still get their buckets advanced, so a departure leaves a visible
// gap instead of the trail simply resuming where it left off.
func (t *signalTrail) Record(items []model.Device, now time.Time) {
	if t == nil {
		return
	}
	bucketAt := now.UTC().Truncate(signalBucket)
	t.mu.Lock()
	defer t.mu.Unlock()

	seen := make(map[string]bool, len(items))
	for _, device := range items {
		key := strings.ToLower(device.ExtendedAddress)
		if key == "" {
			continue
		}
		seen[key] = true
		trail := t.devices[key]
		if trail == nil {
			if len(t.devices) >= signalMaxTrails {
				continue
			}
			trail = &deviceTrail{}
			t.devices[key] = trail
		}
		trail.advance(bucketAt)
		trail.add(device)
	}
	for key, trail := range t.devices {
		if seen[key] {
			continue
		}
		trail.advance(bucketAt)
		// A trail whose whole window is absence tells us nothing; drop it so a
		// long-departed device does not hold a slot forever.
		if trail.empty() {
			delete(t.devices, key)
		}
	}
}

// advance appends buckets up to and including at, marking the skipped ones absent.
func (d *deviceTrail) advance(at time.Time) {
	if len(d.buckets) == 0 {
		d.buckets = append(d.buckets, signalBucketData{at: at})
		return
	}
	last := d.buckets[len(d.buckets)-1].at
	if !at.After(last) {
		return
	}
	// A long gap should not allocate one bucket per elapsed minute.
	if at.Sub(last) > signalBucket*signalBuckets {
		d.buckets = d.buckets[:0]
		d.buckets = append(d.buckets, signalBucketData{at: at})
		return
	}
	for next := last.Add(signalBucket); !next.After(at); next = next.Add(signalBucket) {
		d.buckets = append(d.buckets, signalBucketData{at: next})
	}
	if len(d.buckets) > signalBuckets {
		d.buckets = append(d.buckets[:0], d.buckets[len(d.buckets)-signalBuckets:]...)
	}
}

func (d *deviceTrail) add(device model.Device) {
	bucket := &d.buckets[len(d.buckets)-1]
	bucket.present = true
	if device.LinkQuality != nil {
		bucket.quality = *device.LinkQuality
	}
	// The local border router has no link to itself, so presence is all it has.
	if device.RSSI == nil {
		return
	}
	rssi := *device.RSSI
	if bucket.count == 0 || rssi < bucket.min {
		bucket.min = rssi
	}
	if bucket.count == 0 || rssi > bucket.max {
		bucket.max = rssi
	}
	bucket.sum += rssi
	bucket.count++
}

func (d *deviceTrail) empty() bool {
	for _, bucket := range d.buckets {
		if bucket.present {
			return false
		}
	}
	return true
}

// History renders one device's trail. An unknown device yields an empty history
// rather than an error: it may simply not have been seen since the last restart.
func (t *signalTrail) History(ext string) model.SignalHistory {
	key := strings.ToLower(strings.TrimSpace(ext))
	out := model.SignalHistory{
		ExtendedAddress: key,
		BucketSeconds:   int(signalBucket.Seconds()),
		WindowSeconds:   int(signalBucket.Seconds()) * signalBuckets,
		Samples:         []model.SignalSample{},
	}
	if t == nil {
		return out
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	trail := t.devices[key]
	if trail == nil {
		return out
	}

	total, count, present := 0, 0, 0
	var low, high *int
	for _, bucket := range trail.buckets {
		sample := model.SignalSample{At: bucket.at, Present: bucket.present, Samples: bucket.count}
		if bucket.present {
			present++
		}
		if bucket.count > 0 {
			mean := roundToInt(float64(bucket.sum) / float64(bucket.count))
			min, max, quality := bucket.min, bucket.max, bucket.quality
			sample.RSSI, sample.MinRSSI, sample.MaxRSSI = &mean, &min, &max
			if quality != 0 {
				sample.LinkQual = &quality
			}
			total += bucket.sum
			count += bucket.count
			if low == nil || min < *low {
				low = &min
			}
			if high == nil || max > *high {
				high = &max
			}
		}
		out.Samples = append(out.Samples, sample)
	}
	out.CoveredSecs = len(trail.buckets) * int(signalBucket.Seconds())
	if count > 0 {
		mean := roundToInt(float64(total) / float64(count))
		out.MeanRSSI, out.MinRSSI, out.MaxRSSI = &mean, low, high
	}
	if len(trail.buckets) > 0 {
		pct := present * 100 / len(trail.buckets)
		out.PresentPct = &pct
	}
	return out
}

// knownSignalDevices lists the devices with a trail, newest activity first. Used
// only by tests and diagnostics.
func (t *signalTrail) known() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	keys := make([]string, 0, len(t.devices))
	for key := range t.devices {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
