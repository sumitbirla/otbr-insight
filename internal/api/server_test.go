package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/backup"
	"github.com/otbr-insight/otbr-insight/internal/model"
	"github.com/otbr-insight/otbr-insight/internal/otbr"
)

type fakeSnapshotter struct{ overview model.Overview }

func (f *fakeSnapshotter) Snapshot() model.Overview { return f.overview }
func (f *fakeSnapshotter) DeviceSnapshot() model.DeviceInventory {
	return model.DeviceInventory{Items: []model.Device{}}
}
func (f *fakeSnapshotter) TopologySnapshot() model.Topology {
	return model.Topology{Nodes: []model.TopologyNode{}, Links: []model.TopologyLink{}}
}
func (f *fakeSnapshotter) ScanNetworks(context.Context) (*model.NetworkScan, error) {
	return &model.NetworkScan{Status: "available", Items: []model.AvailableNetwork{}}, nil
}
func (f *fakeSnapshotter) CapabilitySnapshot() model.Capabilities { return model.Capabilities{} }

// fakeEnergySnapshotter adds the optional energy scan, which registers the channels route.
type fakeEnergySnapshotter struct {
	fakeSnapshotter
	scan *model.EnergyScan
	err  error
}

func (f *fakeEnergySnapshotter) EnergyScan(context.Context) (*model.EnergyScan, error) {
	return f.scan, f.err
}

func TestChannelsRouteNeedsAnEnergyScanner(t *testing.T) {
	plain := Handler(&fakeSnapshotter{}, &fakeNames{names: map[string]string{}}, &fakeController{}, &fakeBackups{}, http.NotFoundHandler(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/channels", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("without a scanner: status %d, want 404", rec.Code)
	}

	channel := 25
	withScan := Handler(&fakeEnergySnapshotter{scan: &model.EnergyScan{Status: "available", CurrentChannel: &channel,
		Channels: []model.ChannelEnergy{{Channel: 11, MaxRSSI: -80}, {Channel: 25, MaxRSSI: -91}}}},
		&fakeNames{names: map[string]string{}}, &fakeController{}, &fakeBackups{}, http.NotFoundHandler(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec = httptest.NewRecorder()
	withScan.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/channels", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("with a scanner: status %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data model.EnergyScan `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Status != "available" || len(body.Data.Channels) != 2 || body.Data.CurrentChannel == nil || *body.Data.CurrentChannel != 25 {
		t.Fatalf("payload = %+v", body.Data)
	}

	failing := Handler(&fakeEnergySnapshotter{err: errors.New("radio busy")}, &fakeNames{names: map[string]string{}}, &fakeController{}, &fakeBackups{}, http.NotFoundHandler(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec = httptest.NewRecorder()
	failing.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/channels", nil))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "radio busy") {
		t.Fatalf("failing scanner: status %d body %s", rec.Code, rec.Body.String())
	}
}

type fakeNames struct{ names map[string]string }

func (f *fakeNames) Enabled() bool               { return true }
func (f *fakeNames) Snapshot() map[string]string { return f.names }
func (f *fakeNames) Set(ext, name string) error  { f.names[ext] = name; return nil }
func (f *fakeNames) Delete(ext string) error     { delete(f.names, ext); return nil }

// fakeController records which write it received; err is returned from every write.
type fakeController struct {
	calls []string
	err   error
}

func (f *fakeController) NetworkConfig(context.Context) (*model.NetworkConfig, error) {
	return &model.NetworkConfig{State: "leader", Dataset: model.Dataset{Present: true, NetworkName: "lab"}}, nil
}
func (f *fakeController) DatasetCredentials(context.Context) (*otbr.Credentials, error) {
	return &otbr.Credentials{Present: true, NetworkName: "lab", TLV: "0e08"}, nil
}
func (f *fakeController) FormNetwork(_ context.Context, req otbr.FormRequest) error {
	f.calls = append(f.calls, "form:"+req.NetworkName)
	return f.err
}
func (f *fakeController) JoinNetwork(_ context.Context, req otbr.JoinRequest) error {
	f.calls = append(f.calls, "join:"+req.NetworkName)
	return f.err
}
func (f *fakeController) JoinNetworkTLV(_ context.Context, tlv string) error {
	f.calls = append(f.calls, "tlv:"+tlv)
	return f.err
}
func (f *fakeController) SetEnabled(_ context.Context, enabled bool) error {
	if enabled {
		f.calls = append(f.calls, "enable")
	} else {
		f.calls = append(f.calls, "disable")
	}
	return f.err
}
func (f *fakeController) LeaveNetwork(context.Context) error {
	f.calls = append(f.calls, "leave")
	return f.err
}

type fakeBackups struct{ tlv, name string }

func (f *fakeBackups) Save(tlv, name string) error { f.tlv, f.name = tlv, name; return nil }
func (f *fakeBackups) Meta() backup.Meta {
	return backup.Meta{Present: f.tlv != "", NetworkName: f.name, SavedAt: time.Now()}
}
func (f *fakeBackups) TLV() string { return f.tlv }

func newTestServer(t *testing.T, control *fakeController) (*httptest.Server, *fakeNames) {
	t.Helper()
	names := &fakeNames{names: map[string]string{}}
	handler := Handler(&fakeSnapshotter{}, names, control, &fakeBackups{}, http.NotFoundHandler(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, names
}

func do(t *testing.T, server *httptest.Server, method, path, body string, headers map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	payload := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	return resp, payload
}

func TestCrossSiteWritesAreRejected(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		path    string
		body    string
		headers map[string]string
	}{
		// A text/plain POST is a CORS "simple request": no preflight, Origin set.
		{"simple POST from another origin", http.MethodPost, "/api/v1/network/form", `{"networkName":"pwned"}`,
			map[string]string{"Origin": "http://evil.example", "Content-Type": "text/plain"}},
		{"JSON POST from another origin", http.MethodPost, "/api/v1/network/join/tlv", `{"tlv":"0e08"}`,
			map[string]string{"Origin": "http://evil.example", "Content-Type": "application/json"}},
		{"DELETE from another origin", http.MethodDelete, "/api/v1/network", "",
			map[string]string{"Origin": "http://evil.example"}},
		{"restore from another origin", http.MethodPost, "/api/v1/network/restore", "",
			map[string]string{"Origin": "http://evil.example"}},
		{"opaque null origin", http.MethodPost, "/api/v1/network/restore", "",
			map[string]string{"Origin": "null"}},
		{"Sec-Fetch-Site cross-site", http.MethodPost, "/api/v1/network/restore", "",
			map[string]string{"Sec-Fetch-Site": "cross-site"}},
		{"Sec-Fetch-Site same-site (other port)", http.MethodPut, "/api/v1/network/state", `{"enabled":false}`,
			map[string]string{"Sec-Fetch-Site": "same-site", "Content-Type": "application/json"}},
		{"rename from another origin", http.MethodPut, "/api/v1/devices/1a2b3c4d5e6f7a8b/name", `{"name":"x"}`,
			map[string]string{"Origin": "http://evil.example", "Content-Type": "application/json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			control := &fakeController{}
			server, names := newTestServer(t, control)
			resp, payload := do(t, server, tc.method, tc.path, tc.body, tc.headers)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body %v)", resp.StatusCode, payload)
			}
			if len(control.calls) != 0 {
				t.Fatalf("controller was called: %v", control.calls)
			}
			if len(names.names) != 0 {
				t.Fatalf("name store was written: %v", names.names)
			}
		})
	}
}

func TestSameOriginAndNonBrowserWritesPass(t *testing.T) {
	control := &fakeController{}
	server, _ := newTestServer(t, control)
	host := strings.TrimPrefix(server.URL, "http://")

	// The app's own fetch: Origin matches Host, Sec-Fetch-Site same-origin.
	resp, payload := do(t, server, http.MethodPost, "/api/v1/network/form", `{"networkName":"lab"}`, map[string]string{
		"Origin": "http://" + host, "Sec-Fetch-Site": "same-origin", "Content-Type": "application/json",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("same-origin form: status = %d, body %v", resp.StatusCode, payload)
	}
	// curl and scripts send no provenance headers at all.
	resp, payload = do(t, server, http.MethodDelete, "/api/v1/network", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("headerless delete: status = %d, body %v", resp.StatusCode, payload)
	}
	// Behind a reverse proxy the Origin names the proxy, which forwards its host.
	resp, payload = do(t, server, http.MethodPut, "/api/v1/network/state", `{"enabled":true}`, map[string]string{
		"Origin": "https://thread.home.example", "X-Forwarded-Host": "thread.home.example", "Content-Type": "application/json",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxied state: status = %d, body %v", resp.StatusCode, payload)
	}
	want := []string{"form:lab", "leave", "enable"}
	if strings.Join(control.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("controller calls = %v, want %v", control.calls, want)
	}
}

func TestBodiesMustBeJSON(t *testing.T) {
	control := &fakeController{}
	server, _ := newTestServer(t, control)
	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		resp, _ := do(t, server, http.MethodPost, "/api/v1/network/form", `{"networkName":"lab"}`, map[string]string{"Content-Type": contentType})
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("content-type %q: status = %d, want 415", contentType, resp.StatusCode)
		}
	}
	resp, _ := do(t, server, http.MethodPost, "/api/v1/network/form", `{"networkName":"lab"}`, map[string]string{"Content-Type": "application/json; charset=utf-8"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("json with charset: status = %d, want 200", resp.StatusCode)
	}
	if len(control.calls) != 1 {
		t.Fatalf("controller calls = %v, want exactly one", control.calls)
	}
}

func TestReadsIgnoreOrigin(t *testing.T) {
	server, _ := newTestServer(t, &fakeController{})
	resp, _ := do(t, server, http.MethodGet, "/api/v1/overview", "", map[string]string{"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET overview with foreign Origin: status = %d, want 200", resp.StatusCode)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}

func TestControlErrorStatusCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"validation", otbr.ValidationError("network name is required"), http.StatusBadRequest},
		{"OTBR conflict", &otbr.HTTPStatusError{Endpoint: "/node/dataset/active", StatusCode: http.StatusConflict}, http.StatusConflict},
		{"OTBR server error", &otbr.HTTPStatusError{Endpoint: "/node/state", StatusCode: http.StatusInternalServerError}, http.StatusBadGateway},
		{"OTBR unreachable", errors.New("request OTBR endpoint /node/state: dial tcp 127.0.0.1:1: connect: connection refused"), http.StatusBadGateway},
		{"OTBR timeout", timeoutErr{}, http.StatusGatewayTimeout},
		{"wrapped timeout", errors.Join(errors.New("disable interface"), timeoutErr{}), http.StatusGatewayTimeout},
		{"context deadline", context.DeadlineExceeded, http.StatusGatewayTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newTestServer(t, &fakeController{err: tc.err})
			resp, payload := do(t, server, http.MethodPut, "/api/v1/network/state", `{"enabled":true}`, map[string]string{"Content-Type": "application/json"})
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d (body %v)", resp.StatusCode, tc.want, payload)
			}
			if payload["error"] == "" {
				t.Fatal("response carries no error message")
			}
		})
	}
}
