package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/backup"
	"github.com/otbr-insight/otbr-insight/internal/mcpserver"
	"github.com/otbr-insight/otbr-insight/internal/model"
	"github.com/otbr-insight/otbr-insight/internal/names"
	"github.com/otbr-insight/otbr-insight/internal/otbr"
)

type Snapshotter interface {
	Snapshot() model.Overview
	DeviceSnapshot() model.DeviceInventory
	TopologySnapshot() model.Topology
	ScanNetworks(context.Context) (*model.NetworkScan, error)
	CapabilitySnapshot() model.Capabilities
}

// NameStore persists user-assigned device labels keyed by extended address.
type NameStore interface {
	Enabled() bool
	Snapshot() map[string]string
	Set(ext, name string) error
	Delete(ext string) error
}

// NetworkController performs read/write Thread network management against OTBR.
type NetworkController interface {
	NetworkConfig(context.Context) (*model.NetworkConfig, error)
	DatasetCredentials(context.Context) (*otbr.Credentials, error)
	FormNetwork(context.Context, otbr.FormRequest) error
	JoinNetwork(context.Context, otbr.JoinRequest) error
	JoinNetworkTLV(context.Context, string) error
	SetEnabled(context.Context, bool) error
	LeaveNetwork(context.Context) error
}

// BackupStore persists the previous dataset so a destructive change can be undone.
type BackupStore interface {
	Save(tlv, networkName string) error
	Meta() backup.Meta
	TLV() string
}

func Handler(data Snapshotter, names NameStore, control NetworkController, backups BackupStore, frontend http.Handler, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()
	registerControlRoutes(mux, data, control, backups, logger)
	registerReachabilityRoute(mux, control)
	registerHistoryRoute(mux, control, names)
	mux.HandleFunc("GET /api/v1/overview", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": data.Snapshot()})
	})
	mux.HandleFunc("GET /api/v1/capabilities", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": data.CapabilitySnapshot()})
	})
	mux.HandleFunc("GET /api/v1/devices", func(w http.ResponseWriter, _ *http.Request) {
		inventory := data.DeviceSnapshot()
		applyDeviceNames(&inventory, names.Snapshot())
		writeJSON(w, http.StatusOK, map[string]any{"data": inventory})
	})
	mux.HandleFunc("GET /api/v1/topology", func(w http.ResponseWriter, _ *http.Request) {
		topology := data.TopologySnapshot()
		applyTopologyNames(&topology, names.Snapshot())
		writeJSON(w, http.StatusOK, map[string]any{"data": topology})
	})
	mux.HandleFunc("PUT /api/v1/devices/{ext}/name", func(w http.ResponseWriter, r *http.Request) {
		handleSetName(w, r, names, logger)
	})
	mux.HandleFunc("DELETE /api/v1/devices/{ext}/name", func(w http.ResponseWriter, r *http.Request) {
		ext := r.PathValue("ext")
		if err := names.Delete(ext); err != nil {
			writeNameError(w, err)
			return
		}
		logger.Info("device name cleared", "extendedAddress", ext)
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"extendedAddress": ext, "name": ""}})
	})
	mux.HandleFunc("GET /api/v1/networks", func(w http.ResponseWriter, r *http.Request) {
		scan, err := data.ScanNetworks(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"data": model.NetworkScan{
				Status: "unavailable", Items: []model.AvailableNetwork{}, Source: "OTBR active scan",
				ScannedAt: time.Now().UTC(), Error: err.Error(),
			}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": scan})
	})
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		overview := data.Snapshot()
		status := http.StatusOK
		if overview.Status == "offline" && !overview.HasData {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]any{"status": overview.Status, "apiHealth": overview.APIHealth})
	})
	// MCP endpoint for assistants, on the same listener and under the same
	// trust model as the REST API. Read-only plus ping; see internal/mcpserver.
	mux.Handle("/mcp", mcpserver.Handler(data, names, control, logger))
	mux.Handle("/", frontend)
	return securityHeaders(requestLog(rejectCrossSite(mux), logger))
}

// rejectCrossSite refuses state-changing requests that a browser sent from
// another origin. The API has no authentication, so without this any web page a
// LAN user visits could POST to the network endpoints — a text/plain POST needs
// no CORS preflight. Browsers always attach Origin (and Sec-Fetch-Site) to
// cross-origin POST/PUT/DELETE; non-browser clients such as curl send neither
// and pass through, which keeps scripting against the API possible.
func rejectCrossSite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !sameOrigin(r) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "cross-site requests are not allowed"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin reports whether the browser-supplied provenance headers, when
// present, name this server. Sec-Fetch-Site is the browser's own verdict;
// Origin is compared against the Host the request arrived on (or the host a
// reverse proxy forwarded, which a cross-site simple request cannot set).
func sameOrigin(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
	case "", "same-origin", "none":
	default:
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false // includes the opaque "null" origin
	}
	for _, host := range []string{r.Host, r.Header.Get("X-Forwarded-Host")} {
		if host != "" && strings.EqualFold(parsed.Host, strings.TrimSpace(host)) {
			return true
		}
	}
	return false
}

// requireJSON rejects a request body whose declared media type is not JSON.
// Cross-site simple requests cannot carry application/json, so this is a
// second, independent line against forged writes as well as a sanity check.
func requireJSON(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error": "request body must be application/json"})
		return false
	}
	return true
}

// Pinger is implemented by a provider that can test reachability from the border
// router itself, which reaches mesh-local addresses a LAN client cannot.
type Pinger interface {
	Ping(ctx context.Context, address string, count int) (*model.PingResult, error)
}

// registerReachabilityRoute exposes a read-only probe. It is a POST because it
// makes the border router transmit, but it changes nothing.
func registerReachabilityRoute(mux *http.ServeMux, control NetworkController) {
	pinger, ok := control.(Pinger)
	if !ok {
		return
	}
	mux.HandleFunc("POST /api/v1/devices/{address}/ping", func(w http.ResponseWriter, r *http.Request) {
		address := r.PathValue("address")
		// A sleepy device answers only when it next wakes, so this can take tens of
		// seconds; the client has to be prepared to wait.
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		result, err := pinger.Ping(ctx, address, 3)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	})
}

// HistoryProvider exposes OpenThread's recorded events.
type HistoryProvider interface {
	History(ctx context.Context) (*model.History, error)
}

// registerHistoryRoute serves the event log on demand rather than polling it: it
// is a diagnostic view, not something every dashboard poll needs.
func registerHistoryRoute(mux *http.ServeMux, control NetworkController, names NameStore) {
	provider, ok := control.(HistoryProvider)
	if !ok {
		return
	}
	mux.HandleFunc("GET /api/v1/history", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		history, err := provider.History(ctx)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		// The recorder knows only extended addresses, so a device reads as a hex
		// string unless the user's label is overlaid the way the device list does it.
		if labels := names.Snapshot(); len(labels) > 0 {
			for i := range history.Neighbors {
				if name, ok := labels[nameKey(history.Neighbors[i].ExtendedAddress, "")]; ok {
					history.Neighbors[i].CustomName = name
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": history})
	})
}

// registerControlRoutes wires the Thread network management endpoints. These
// are the app's only writes to OTBR and, per the deployment model, are
// unauthenticated: bind to a trusted LAN interface.
func registerControlRoutes(mux *http.ServeMux, data Snapshotter, control NetworkController, backups BackupStore, logger *slog.Logger) {
	refresh := func(ctx context.Context) {
		if r, ok := data.(interface{ RefreshNow(context.Context) }); ok {
			r.RefreshNow(ctx)
		}
	}
	// snapshot backs up the current dataset before a destructive change so it
	// can be restored. Best-effort: a failure is logged but does not block.
	snapshot := func(ctx context.Context) {
		creds, err := control.DatasetCredentials(ctx)
		if err != nil || creds == nil || !creds.Present || creds.TLV == "" {
			return
		}
		if err := backups.Save(creds.TLV, creds.NetworkName); err != nil {
			logger.Warn("dataset backup before change failed", "error", err)
			return
		}
		logger.Info("dataset backed up before change", "networkName", creds.NetworkName)
	}
	mux.HandleFunc("GET /api/v1/network", func(w http.ResponseWriter, r *http.Request) {
		config, err := control.NetworkConfig(r.Context())
		if err != nil {
			writeControlError(w, err)
			return
		}
		data := map[string]any{"state": config.State, "dataset": config.Dataset}
		if m := backups.Meta(); m.Present {
			data["backup"] = map[string]any{"networkName": m.NetworkName, "savedAt": m.SavedAt}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	})
	mux.HandleFunc("POST /api/v1/network/restore", func(w http.ResponseWriter, r *http.Request) {
		tlv := backups.TLV()
		if tlv == "" {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "no saved network to restore"})
			return
		}
		snapshot(r.Context()) // back up the current network before overwriting it
		if err := control.JoinNetworkTLV(r.Context(), tlv); err != nil {
			writeControlError(w, err)
			return
		}
		logger.Info("previous network restored")
		refresh(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"status": "restored"}})
	})
	// On-demand credential reveal. Deliberately a separate endpoint from the
	// polled /api/v1/network so secrets are never on the continuously rendered
	// path. The values are never logged.
	mux.HandleFunc("GET /api/v1/network/credentials", func(w http.ResponseWriter, r *http.Request) {
		creds, err := control.DatasetCredentials(r.Context())
		if err != nil {
			writeControlError(w, err)
			return
		}
		logger.Info("network credentials revealed")
		writeJSON(w, http.StatusOK, map[string]any{"data": creds})
	})
	mux.HandleFunc("POST /api/v1/network/form", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			NetworkName string `json:"networkName"`
			Channel     *int   `json:"channel"`
			PANID       *int   `json:"panId"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		snapshot(r.Context())
		if err := control.FormNetwork(r.Context(), otbr.FormRequest{NetworkName: body.NetworkName, Channel: body.Channel, PANID: body.PANID}); err != nil {
			writeControlError(w, err)
			return
		}
		logger.Info("thread network formed", "networkName", body.NetworkName)
		refresh(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"status": "formed"}})
	})
	mux.HandleFunc("POST /api/v1/network/join", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			NetworkName     string `json:"networkName"`
			NetworkKey      string `json:"networkKey"`
			Channel         *int   `json:"channel"`
			PANID           *int   `json:"panId"`
			ExtPANID        string `json:"extPanId"`
			PSKc            string `json:"pskc"`
			MeshLocalPrefix string `json:"meshLocalPrefix"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		snapshot(r.Context())
		if err := control.JoinNetwork(r.Context(), otbr.JoinRequest{
			NetworkName: body.NetworkName, NetworkKey: body.NetworkKey, Channel: body.Channel, PANID: body.PANID,
			ExtPANID: body.ExtPANID, PSKc: body.PSKc, MeshLocalPrefix: body.MeshLocalPrefix,
		}); err != nil {
			writeControlError(w, err)
			return
		}
		logger.Info("thread network joined", "networkName", body.NetworkName)
		refresh(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"status": "joined"}})
	})
	mux.HandleFunc("POST /api/v1/network/join/tlv", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			TLV string `json:"tlv"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		snapshot(r.Context())
		if err := control.JoinNetworkTLV(r.Context(), body.TLV); err != nil {
			writeControlError(w, err)
			return
		}
		logger.Info("thread network joined from dataset TLV")
		refresh(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"status": "joined"}})
	})
	mux.HandleFunc("PUT /api/v1/network/state", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if err := control.SetEnabled(r.Context(), body.Enabled); err != nil {
			writeControlError(w, err)
			return
		}
		logger.Info("thread interface state changed", "enabled", body.Enabled)
		refresh(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"enabled": body.Enabled}})
	})
	mux.HandleFunc("DELETE /api/v1/network", func(w http.ResponseWriter, r *http.Request) {
		snapshot(r.Context())
		if err := control.LeaveNetwork(r.Context()); err != nil {
			writeControlError(w, err)
			return
		}
		logger.Info("thread network left")
		refresh(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"status": "left"}})
	})
}

func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	if !requireJSON(w, r) {
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return false
	}
	return true
}

// writeControlError maps a failed control call to a status code: the caller's
// bad input is 400, an OTBR rejection is 409 or 502, an OTBR that did not
// answer in time is 504, and anything else on the way to or from OTBR is 502.
func writeControlError(w http.ResponseWriter, err error) {
	var validation otbr.ValidationError
	if errors.As(err, &validation) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	var statusErr *otbr.HTTPStatusError
	if errors.As(err, &statusErr) {
		if statusErr.StatusCode == http.StatusConflict {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "OTBR rejected the change; the interface may need to be disabled first"})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{"error": "OTBR did not respond in time: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]any{"error": "OTBR is unreachable: " + err.Error()})
}

func handleSetName(w http.ResponseWriter, r *http.Request, names NameStore, logger *slog.Logger) {
	ext := r.PathValue("ext")
	var body struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if err := names.Set(ext, body.Name); err != nil {
		writeNameError(w, err)
		return
	}
	logger.Info("device name set", "extendedAddress", ext)
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"extendedAddress": ext, "name": strings.TrimSpace(body.Name)}})
}

func writeNameError(w http.ResponseWriter, err error) {
	if errors.Is(err, names.ErrDisabled) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
}

// applyDeviceNames overlays stored labels onto an inventory snapshot, matching
// on the device's extended address (falling back to its ID for inventory rows
// whose ID is the extended address).
func applyDeviceNames(inventory *model.DeviceInventory, labels map[string]string) {
	if len(labels) == 0 {
		return
	}
	for i := range inventory.Items {
		if name, ok := labels[nameKey(inventory.Items[i].ExtendedAddress, inventory.Items[i].ID)]; ok {
			inventory.Items[i].CustomName = name
		}
	}
}

func applyTopologyNames(topology *model.Topology, labels map[string]string) {
	if len(labels) == 0 {
		return
	}
	for i := range topology.Nodes {
		if topology.Nodes[i].ExtendedAddress == "" {
			continue
		}
		if name, ok := labels[strings.ToLower(topology.Nodes[i].ExtendedAddress)]; ok {
			topology.Nodes[i].CustomName = name
		}
	}
}

func nameKey(ext, id string) string {
	if ext != "" {
		return strings.ToLower(ext)
	}
	return strings.ToLower(id)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func requestLog(next http.Handler, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/api/v1/overview" && r.URL.Path != "/api/v1/capabilities" && r.URL.Path != "/api/v1/devices" && r.URL.Path != "/api/v1/topology" && r.URL.Path != "/api/v1/networks" {
			logger.Debug("HTTP request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
		}
	})
}
