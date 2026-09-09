package otbr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

type stubEnergyScanner struct {
	available bool
	channels  []model.ChannelEnergy
	err       error
	// sweeps, when set, is answered one sweep per call and overrides channels.
	sweeps [][]model.ChannelEnergy
	calls  int
}

func (s *stubEnergyScanner) Available() bool { return s.available }
func (s *stubEnergyScanner) EnergyScan(context.Context) ([]model.ChannelEnergy, error) {
	s.calls++
	if len(s.sweeps) > 0 {
		return s.sweeps[(s.calls-1)%len(s.sweeps)], s.err
	}
	return s.channels, s.err
}

// quickSweeps shortens the run for tests and restores the defaults afterwards.
func quickSweeps(t *testing.T, maxSweeps int) {
	t.Helper()
	run, pause, max, rest := energyScanRun, energyScanPause, energyScanMaxSweeps, energyScanRESTSweeps
	energyScanRun, energyScanPause, energyScanMaxSweeps, energyScanRESTSweeps = 0, 0, maxSweeps, maxSweeps
	t.Cleanup(func() {
		energyScanRun, energyScanPause, energyScanMaxSweeps, energyScanRESTSweeps = run, pause, max, rest
	})
}

// nodeOnly serves the legacy /node document so Overview succeeds and nothing else.
func nodeOnly(t *testing.T, extra http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/node" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"extAddress":"0102030405060708","state":"leader","networkName":"OpenThreadDemo","rloc16":"0x7000"}`))
			return
		}
		if extra != nil {
			extra(w, r)
			return
		}
		http.NotFound(w, r)
	}))
}

func TestEnergyScanPrefersTheDaemonSocket(t *testing.T) {
	quickSweeps(t, 1)
	var restCalls int32
	server := nodeOnly(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/actions" {
			atomic.AddInt32(&restCalls, 1)
		}
		http.NotFound(w, r)
	})
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())
	client.SetEnergyScanner(&stubEnergyScanner{available: true, channels: []model.ChannelEnergy{{Channel: 11, MaxRSSI: -80}, {Channel: 25, MaxRSSI: -91}}})

	scan, err := client.EnergyScan(context.Background())
	if err != nil {
		t.Fatalf("EnergyScan() error = %v", err)
	}
	if scan.Status != "available" || len(scan.Channels) != 2 || scan.Source != "OpenThread daemon energy scan" {
		t.Fatalf("scan = %+v", scan)
	}
	if atomic.LoadInt32(&restCalls) != 0 {
		t.Fatal("REST action posted although the socket answered")
	}
}

func TestEnergyScanCombinesRepeatedSweeps(t *testing.T) {
	quickSweeps(t, 3)
	server := nodeOnly(t, nil)
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())
	scanner := &stubEnergyScanner{available: true, sweeps: [][]model.ChannelEnergy{
		{{Channel: 11, MaxRSSI: -90}, {Channel: 25, MaxRSSI: -95}},
		{{Channel: 11, MaxRSSI: -62}, {Channel: 25, MaxRSSI: -94}}, // one Wi-Fi burst on 11
		{{Channel: 11, MaxRSSI: -88}, {Channel: 25, MaxRSSI: -96}},
	}}
	client.SetEnergyScanner(scanner)

	scan, err := client.EnergyScan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if scan.Sweeps != 3 || scanner.calls != 3 {
		t.Fatalf("sweeps = %d (calls %d), want 3", scan.Sweeps, scanner.calls)
	}
	ch11, ch25 := scan.Channels[0], scan.Channels[1]
	// Peak keeps the burst; the median shows the channel is normally quiet.
	if ch11.MaxRSSI != -62 || ch11.TypicalRSSI == nil || *ch11.TypicalRSSI != -88 {
		t.Fatalf("channel 11 = %+v (typical %v)", ch11, ch11.TypicalRSSI)
	}
	if ch25.MaxRSSI != -94 || ch25.TypicalRSSI == nil || *ch25.TypicalRSSI != -95 {
		t.Fatalf("channel 25 = %+v (typical %v)", ch25, ch25.TypicalRSSI)
	}
}

func TestEnergyScanKeepsAPartialRunWhenALaterSweepFails(t *testing.T) {
	quickSweeps(t, 5)
	server := nodeOnly(t, nil)
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())
	scanner := &failAfter{good: 2}
	client.SetEnergyScanner(scanner)
	scan, err := client.EnergyScan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if scan.Status != "available" || scan.Sweeps != 2 || len(scan.Channels) != 1 {
		t.Fatalf("scan = %+v", scan)
	}
}

type failAfter struct{ good, calls int }

func (f *failAfter) Available() bool { return true }
func (f *failAfter) EnergyScan(context.Context) ([]model.ChannelEnergy, error) {
	f.calls++
	if f.calls > f.good {
		return nil, io.ErrUnexpectedEOF
	}
	return []model.ChannelEnergy{{Channel: 20, MaxRSSI: -80 - f.calls}}, nil
}

func TestEnergyScanFallsBackToTheRESTAction(t *testing.T) {
	quickSweeps(t, 2)
	var polls, posts int32
	server := nodeOnly(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/actions":
			// The firmware answers 415 to anything but JSON:API, and 422 unless the
			// destination is an extended address and channelMask a JSON array.
			if r.Header.Get("Content-Type") != "application/vnd.api+json" {
				w.WriteHeader(http.StatusUnsupportedMediaType)
				return
			}
			body, _ := io.ReadAll(r.Body)
			var request struct {
				Data []struct {
					Type       string `json:"type"`
					Attributes struct {
						Destination  string `json:"destination"`
						ChannelMask  []int  `json:"channelMask"`
						Count        int    `json:"count"`
						Period       int    `json:"period"`
						ScanDuration int    `json:"scanDuration"`
						Timeout      int    `json:"timeout"`
					} `json:"attributes"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &request); err != nil || len(request.Data) != 1 || request.Data[0].Type != "getEnergyScanTask" ||
				request.Data[0].Attributes.Destination != "0102030405060708" || len(request.Data[0].Attributes.ChannelMask) != 16 ||
				request.Data[0].Attributes.ScanDuration == 0 || request.Data[0].Attributes.Count == 0 || request.Data[0].Attributes.Period == 0 || request.Data[0].Attributes.Timeout == 0 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			atomic.AddInt32(&posts, 1)
			w.Header().Set("Content-Type", "application/vnd.api+json")
			_, _ = w.Write([]byte(`{"data":[{"id":"act-1","type":"getEnergyScanTask","attributes":{"status":"pending"}}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/actions/act-1":
			w.Header().Set("Content-Type", "application/vnd.api+json")
			if atomic.AddInt32(&polls, 1) == 1 {
				_, _ = w.Write([]byte(`{"data":{"id":"act-1","type":"getEnergyScanTask","attributes":{"status":"active"}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"id":"act-1","type":"getEnergyScanTask","attributes":{"status":"completed"},"relationships":{"result":{"data":{"type":"diagnostics","id":"rep-1"}}}}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/diagnostics/rep-1":
			w.Header().Set("Content-Type", "application/vnd.api+json")
			_, _ = w.Write([]byte(`{"data":{"id":"rep-1","type":"energyScanReport","attributes":{"origin":"899dadd619137675","report":[{"channel":11,"maxRssi":[-97,-90]},{"channel":25,"maxRssi":[-94]}]}}}`))
		default:
			http.NotFound(w, r)
		}
	})
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())

	scan, err := client.EnergyScan(context.Background())
	if err != nil {
		t.Fatalf("EnergyScan() error = %v", err)
	}
	if scan.Status != "available" || scan.Source != "OTBR energy scan action" || scan.Sweeps != 2 {
		t.Fatalf("scan = %+v", scan)
	}
	if len(scan.Channels) != 2 || scan.Channels[0].Channel != 11 || scan.Channels[0].MaxRSSI != -90 || scan.Channels[1].MaxRSSI != -94 {
		t.Fatalf("channels = %+v, want the peak of each maxRssi array", scan.Channels)
	}
	// The verified request is repeated as-is, once per sweep; the first poll of the
	// first action saw it still running.
	if atomic.LoadInt32(&posts) != 2 || atomic.LoadInt32(&polls) != 3 {
		t.Fatalf("posts = %d polls = %d, want 2 and 3", posts, polls)
	}
}

func TestEnergyScanReportsAMissingActionAsUnsupported(t *testing.T) {
	quickSweeps(t, 1)
	server := nodeOnly(t, nil)
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())
	scan, err := client.EnergyScan(context.Background())
	if err != nil {
		t.Fatalf("EnergyScan() error = %v", err)
	}
	if scan.Status != "unsupported" || len(scan.Channels) != 0 {
		t.Fatalf("scan = %+v", scan)
	}
}

func TestEnergyScanSurfacesADaemonFaultInsteadOfFallingBack(t *testing.T) {
	quickSweeps(t, 1)
	server := nodeOnly(t, nil)
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())
	client.SetEnergyScanner(&stubEnergyScanner{available: true, err: io.ErrUnexpectedEOF})
	scan, err := client.EnergyScan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if scan.Status != "unavailable" || scan.Source != "OpenThread daemon energy scan" || scan.Error == "" {
		t.Fatalf("scan = %+v, want the daemon's fault reported rather than a silent REST fallback", scan)
	}
}
