package otbr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// meshSnapshotTTL keeps one live reading serving the device and topology calls of
// a single poll without letting it go stale between polls.
const meshSnapshotTTL = 2 * time.Second

const maxResponseBytes = 2 << 20

type HTTPStatusError struct {
	Endpoint   string
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("OTBR endpoint %s returned HTTP %d", e.Endpoint, e.StatusCode)
}

type Client struct {
	baseURL               *url.URL
	legacyWebURL          *url.URL
	scanner               NetworkScanner
	energy                EnergyScanner
	mesh                  MeshReader
	status                StatusReader
	pinger                Pinger
	history               HistoryReader
	meshMu                sync.Mutex
	meshDevices           *model.DeviceInventory
	meshTopology          *model.Topology
	meshFetched           time.Time
	meshFailing           bool
	logger                *slog.Logger
	httpClient            *http.Client
	propertiesMu          sync.Mutex
	properties            statusProperties
	propertiesAvailable   bool
	propertiesLastAttempt time.Time
	// controlMu serializes the network writes. A dataset change is three
	// requests (disable, PUT dataset, enable); two callers interleaving them
	// would leave OTBR in whichever state the last request happened to set.
	controlMu sync.Mutex
}

func NewClient(rawURL string, httpClient *http.Client) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimRight(rawURL, "/"))
	if err != nil {
		return nil, err
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, errors.New("unsupported OTBR URL scheme")
	}
	if baseURL.Host == "" || baseURL.User != nil {
		return nil, errors.New("invalid OTBR URL")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 4 * time.Second}
	}
	client := &Client{baseURL: baseURL, httpClient: httpClient}
	if baseURL.Port() == "8081" {
		legacyURL := *baseURL
		port := "80"
		if baseURL.Scheme == "https" {
			port = "443"
		}
		legacyURL.Host = net.JoinHostPort(baseURL.Hostname(), port)
		legacyURL.Path = ""
		legacyURL.RawQuery = ""
		legacyURL.Fragment = ""
		client.legacyWebURL = &legacyURL
	}
	return client, nil
}

type nodeEnvelope struct {
	Data nodeData `json:"data"`
}

type nodeData struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Attributes nodeAttributes `json:"attributes"`
}

type nodeAttributes struct {
	ExtAddress       string     `json:"extAddress"`
	OMRIPv6Address   string     `json:"omrIpv6Address"`
	Role             string     `json:"role"`
	State            string     `json:"state"`
	RouterCount      *int       `json:"routerCount"`
	RLOC16           string     `json:"rloc16"`
	RLOCAddress      string     `json:"rlocAddress"`
	RouterID         *int       `json:"routerId"`
	NetworkName      string     `json:"networkName"`
	BorderAgentState string     `json:"baState"`
	ExtPANID         string     `json:"extPanId"`
	LeaderData       leaderData `json:"leaderData"`
}

type statusProperties struct {
	LinkLocalAddress     string
	LocalAddress         string
	MeshLocalAddress     string
	MeshLocalPrefix      string
	NetworkName          string
	PANID                string
	PartitionID          string
	XPANID               string
	OpenThreadVersion    string
	OpenThreadAPIVersion string
	RCPChannel           string
	RCPEUI64             string
	RCPState             string
	RCPTxPower           string
	RCPVersion           string
	WPANService          string
}

type leaderData struct {
	PartitionID    *uint32 `json:"partitionId"`
	LeaderRouterID *int    `json:"leaderRouterId"`
}

type deviceHeader struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Attributes json.RawMessage `json:"attributes"`
}

type deviceAttributes struct {
	ExtAddress     string   `json:"extAddress"`
	MLEIDIID       string   `json:"mlEidIid"`
	OMRIPv6Address string   `json:"omrIpv6Address"`
	EUI64          string   `json:"eui64"`
	Hostname       string   `json:"hostname"`
	HostName       string   `json:"hostName"`
	Role           string   `json:"role"`
	RLOC16         string   `json:"rloc16"`
	RouterID       *int     `json:"routerId"`
	Parent         string   `json:"parent"`
	IPv6Addresses  []string `json:"ipv6Addresses"`
	ThreadVersion  string   `json:"threadVersion"`
	Updated        string   `json:"updated"`
	Created        string   `json:"created"`
	RSSI           *int     `json:"rssi"`
	LinkQuality    *int     `json:"linkQuality"`
	LinkMargin     *int     `json:"linkMargin"`
}

type diagnosticAttributes struct {
	ExtAddress      string                  `json:"extAddress"`
	RLOC16          string                  `json:"rloc16"`
	RouterID        *int                    `json:"routerId"`
	Route           diagnosticRoute         `json:"route"`
	ChildTable      []diagnosticChild       `json:"childTable"`
	Children        []diagnosticChildDetail `json:"children"`
	RouterNeighbors []diagnosticNeighbor    `json:"routerNeighbors"`
	Created         string                  `json:"created"`
}

type diagnosticRoute struct {
	RouteData []diagnosticRouteData `json:"routeData"`
}

type diagnosticRouteData struct {
	RouteID        int  `json:"routeId"`
	LinkQualityOut *int `json:"linkQualityOut"`
	LinkQualityIn  *int `json:"linkQualityIn"`
	RouteCost      *int `json:"routeCost"`
}

type diagnosticChild struct {
	ChildID     int            `json:"childId"`
	Timeout     *int           `json:"timeout"`
	LinkQuality *int           `json:"linkQuality"`
	Mode        diagnosticMode `json:"mode"`
}

// diagnosticChildDetail is an entry of the "children" TLV. It repeats childTable's
// mode flags inline (not nested under "mode") and adds the extended address.
type diagnosticChildDetail struct {
	ChildID         int      `json:"childId"`
	ExtAddress      string   `json:"extAddress"`
	RLOC16          string   `json:"rloc16"`
	Timeout         *int     `json:"timeout"`
	RxOnWhenIdle    *bool    `json:"rxOnWhenIdle"`
	DeviceTypeFTD   *bool    `json:"deviceTypeFTD"`
	FullNetworkData *bool    `json:"fullNetworkData"`
	LinkMargin      *int     `json:"linkMargin"`
	AverageRSSI     *int     `json:"averageRssi"`
	LastRSSI        *int     `json:"lastRssi"`
	FrameErrorRate  *float64 `json:"frameErrorRate"`
}

type diagnosticMode struct {
	RxOnWhenIdle    *bool `json:"rxOnWhenIdle"`
	DeviceTypeFTD   *bool `json:"deviceTypeFTD"`
	FullNetworkData *bool `json:"fullNetworkData"`
}

type diagnosticNeighbor struct {
	RLOC16           string   `json:"rloc16"`
	ExtAddress       string   `json:"extAddress"`
	LinkMargin       *int     `json:"linkMargin"`
	AverageRSSI      *int     `json:"averageRssi"`
	LastRSSI         *int     `json:"lastRssi"`
	FrameErrorRate   *float64 `json:"frameErrorRate"`
	MessageErrorRate *float64 `json:"messageErrorRate"`
}

func (c *Client) Overview(ctx context.Context) (*model.Overview, error) {
	started := time.Now()
	apiAttrs, apiErr := c.fetchNodeAttributes(ctx, "/api/node")
	nodeAttrs, nodeErr := c.fetchNodeAttributes(ctx, "/node")
	if apiErr != nil && nodeErr != nil {
		return nil, fmt.Errorf("read OTBR node status: %w", errors.Join(apiErr, nodeErr))
	}
	a := apiAttrs
	switch {
	case apiErr != nil:
		a = nodeAttrs
	case nodeErr == nil:
		// Some firmware serves a stale /api/node (lingering networkName, state,
		// partition, rloc, routerCount) after a dataset change while /node stays
		// fresh. Overlay /node's live values; /api/node keeps fields /node omits
		// (e.g. the OMR address).
		a = mergeFreshNode(apiAttrs, nodeAttrs)
	}
	role := a.Role
	if role == "" {
		role = a.State
	}
	overview := &model.Overview{
		Status: "online", HasData: true, NetworkName: a.NetworkName,
		Role: role, State: a.State, RLOC16: a.RLOC16, RouterID: a.RouterID,
		ExtendedAddress: a.ExtAddress, ExtendedPANID: a.ExtPANID,
		PartitionID: a.LeaderData.PartitionID, LeaderRouterID: a.LeaderData.LeaderRouterID,
		RouterCount: a.RouterCount, OMRIPv6Address: a.OMRIPv6Address, RLOCAddress: a.RLOCAddress,
		BorderAgentState: a.BorderAgentState, APIHealth: "healthy",
	}
	if properties, ok := c.optionalStatusProperties(ctx); ok {
		applyStatusProperties(overview, properties)
	}
	// The socket knows these regardless of whether otbr-web is running, so it fills
	// whatever the scrape above could not.
	if c.status != nil && c.status.Available() {
		if extra, err := c.status.Status(ctx); err == nil {
			mergeStatus(overview, extra)
		}
	}
	c.applyDatasetFallback(ctx, overview)
	overview.RequestLatencyMs = time.Since(started).Milliseconds()
	return overview, nil
}

// applyDatasetFallback fills the network identity fields that the legacy web
// service normally supplies from the active dataset when they are still blank.
// Without it an OTBR that has no web service (Docker images, a REST port other
// than 8081) shows no channel or PAN ID at all. ActiveDataset masks the network
// key and PSKc, so this keeps credentials off the polled path.
func (c *Client) applyDatasetFallback(ctx context.Context, overview *model.Overview) {
	if overview.RCPChannel != "" && overview.PANID != "" && overview.ExtendedPANID != "" && overview.MeshLocalPrefix != "" {
		return
	}
	dataset, err := c.ActiveDataset(ctx)
	if err != nil || !dataset.Present {
		return
	}
	if overview.RCPChannel == "" && dataset.Channel != nil {
		overview.RCPChannel = strconv.Itoa(*dataset.Channel)
	}
	if overview.PANID == "" {
		overview.PANID = dataset.PANID
	}
	if overview.ExtendedPANID == "" {
		overview.ExtendedPANID = dataset.ExtPANID
	}
	if overview.MeshLocalPrefix == "" {
		overview.MeshLocalPrefix = dataset.MeshLocalPrefix
	}
}

func (c *Client) optionalStatusProperties(ctx context.Context) (statusProperties, bool) {
	if c.legacyWebURL == nil {
		return statusProperties{}, false
	}
	c.propertiesMu.Lock()
	if !c.propertiesLastAttempt.IsZero() && time.Since(c.propertiesLastAttempt) < time.Minute {
		properties, available := c.properties, c.propertiesAvailable
		c.propertiesMu.Unlock()
		return properties, available
	}
	c.propertiesLastAttempt = time.Now()
	c.propertiesMu.Unlock()

	// A failed attempt must clear the cache. Leaving the last good values in place
	// left a stale RCP version and WPAN state on screen indefinitely after otbr-web
	// stopped, and made those fields flicker: absent on the poll that retried, then
	// present again from cache for the next minute.
	forget := func() (statusProperties, bool) {
		c.propertiesMu.Lock()
		c.properties = statusProperties{}
		c.propertiesAvailable = false
		c.propertiesMu.Unlock()
		return statusProperties{}, false
	}
	requestCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
	defer cancel()
	propertiesURL := *c.legacyWebURL
	propertiesURL.Path = "/get_properties"
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, propertiesURL.String(), nil)
	if err != nil {
		return forget()
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return forget()
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
		return forget()
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return forget()
	}
	properties, err := decodeStatusProperties(body)
	if err != nil {
		return forget()
	}
	c.propertiesMu.Lock()
	c.properties = properties
	c.propertiesAvailable = true
	c.propertiesMu.Unlock()
	return properties, true
}

// HistoryReader exposes OpenThread's event recorder. Only the daemon socket has
// it; the REST API reports current state with no record of how it got there.
type HistoryReader interface {
	Available() bool
	History(ctx context.Context) (*model.History, error)
}

// SetHistoryReader installs the event-history source.
func (c *Client) SetHistoryReader(reader HistoryReader) { c.history = reader }

// History returns recorded role, partition and neighbour events.
func (c *Client) History(ctx context.Context) (*model.History, error) {
	if c.history == nil || !c.history.Available() {
		return &model.History{
			Status: "unsupported", Network: []model.NetworkHistoryEntry{}, Neighbors: []model.NeighborHistoryEntry{},
			Error: "event history needs the OpenThread daemon socket, which is only available when otbr-insight runs on the border router",
		}, nil
	}
	history, err := c.history.History(ctx)
	if err != nil {
		return &model.History{
			Status: "unavailable", Network: []model.NetworkHistoryEntry{}, Neighbors: []model.NeighborHistoryEntry{},
			Error: err.Error(),
		}, nil
	}
	return history, nil
}

// Pinger is an optional source for reachability tests. Only the daemon socket can
// do this: the REST API has no ping action, and a probe from the dashboard host
// cannot reach a device's mesh-local address at all.
type Pinger interface {
	Available() bool
	Ping(ctx context.Context, address string, count int) (*model.PingResult, error)
}

// SetPinger installs the reachability source.
func (c *Client) SetPinger(pinger Pinger) { c.pinger = pinger }

// Ping tests reachability from the border router.
func (c *Client) Ping(ctx context.Context, address string, count int) (*model.PingResult, error) {
	if c.pinger == nil || !c.pinger.Available() {
		return nil, errors.New("reachability testing needs the OpenThread daemon socket, which is only available when otbr-insight runs on the border router")
	}
	return c.pinger.Ping(ctx, address, count)
}

// StatusReader supplies the runtime fields the REST API does not expose — version
// strings, channel, TX power, EUI-64 and the local addresses. otbr-web served these
// from /get_properties, so they vanish with that service; the daemon socket has
// them regardless.
type StatusReader interface {
	Available() bool
	Status(ctx context.Context) (*model.Overview, error)
}

// SetStatusReader installs a source preferred over the otbr-web scrape.
func (c *Client) SetStatusReader(reader StatusReader) { c.status = reader }

// mergeStatus copies the fields a status source knows onto the REST overview,
// leaving anything it did not report untouched.
func mergeStatus(overview *model.Overview, extra *model.Overview) {
	for _, pair := range [][2]*string{
		{&overview.OpenThreadVersion, &extra.OpenThreadVersion},
		{&overview.OpenThreadAPIVersion, &extra.OpenThreadAPIVersion},
		{&overview.RCPVersion, &extra.RCPVersion},
		{&overview.RCPChannel, &extra.RCPChannel},
		{&overview.RCPTxPower, &extra.RCPTxPower},
		{&overview.RCPState, &extra.RCPState},
		{&overview.RCPEUI64, &extra.RCPEUI64},
		{&overview.PANID, &extra.PANID},
		{&overview.WPANService, &extra.WPANService},
		{&overview.LinkLocalAddress, &extra.LinkLocalAddress},
		{&overview.MeshLocalAddress, &extra.MeshLocalAddress},
	} {
		if *pair[0] == "" && *pair[1] != "" {
			*pair[0] = *pair[1]
		}
	}
}

// MeshReader is an optional live source for the device inventory and topology.
// OTBR's REST collections are caches that only refresh when a client asks; a
// reader backed by the OpenThread stack answers from the mesh itself, so departed
// devices disappear immediately and sleepy children need no discovery sweep.
type MeshReader interface {
	Available() bool
	Mesh(ctx context.Context) (*model.DeviceInventory, *model.Topology, error)
}

// SetMeshReader installs a live source preferred over /api/devices and
// /api/diagnostics. Call before serving.
func (c *Client) SetMeshReader(reader MeshReader) { c.mesh = reader }

// FastDevices reports whether the device inventory is cheap enough to poll at the
// overview cadence. A live reader caches its over-the-air queries and answers from
// local tables in between, so it is; OTBR's REST collections are not, since each
// refresh needs a discovery sweep.
func (c *Client) FastDevices() bool { return c.mesh != nil && c.mesh.Available() }

// SetLogger lets the client report a silent degradation — chiefly falling back
// from live mesh data to OTBR's caches, which is invisible in the API response.
func (c *Client) SetLogger(logger *slog.Logger) { c.logger = logger }

func (c *Client) log() *slog.Logger {
	if c.logger == nil {
		return slog.Default()
	}
	return c.logger
}

// liveMesh returns the current snapshot, reusing one reading for the device and
// topology calls that always arrive together. Without this each poll would query
// every router twice over the air.
func (c *Client) liveMesh(ctx context.Context) (*model.DeviceInventory, *model.Topology, bool) {
	if c.mesh == nil || !c.mesh.Available() {
		return nil, nil, false
	}
	c.meshMu.Lock()
	defer c.meshMu.Unlock()
	if time.Since(c.meshFetched) < meshSnapshotTTL && c.meshDevices != nil {
		return c.meshDevices, c.meshTopology, true
	}
	devices, topology, err := c.mesh.Mesh(ctx)
	if err != nil {
		// Fall back to REST rather than blanking the map; the socket may be
		// unreadable (permissions) or a query may have timed out. Log the
		// transition only, so a persistent failure does not fill the journal.
		if !c.meshFailing {
			c.meshFailing = true
			c.log().Warn("live mesh read failed; using OTBR's cached collections", "error", err)
		}
		c.meshDevices, c.meshTopology = nil, nil
		c.meshFetched = time.Now()
		return nil, nil, false
	}
	if c.meshFailing {
		c.meshFailing = false
		c.log().Info("live mesh data restored")
	}
	c.meshDevices, c.meshTopology, c.meshFetched = devices, topology, time.Now()
	return devices, topology, true
}

// NetworkScanner is an optional source for active scans that does not go through
// the OTBR REST API. It exists so the daemon-socket provider can supply the one
// capability REST has no action for, without this package depending on it.
type NetworkScanner interface {
	// Available reports whether the source can be used right now.
	Available() bool
	ScanNetworks(ctx context.Context) ([]model.AvailableNetwork, error)
}

// SetNetworkScanner installs a scanner that takes precedence over the otbr-web
// endpoint. Call before serving.
func (c *Client) SetNetworkScanner(scanner NetworkScanner) { c.scanner = scanner }

// A discovery pass sends one request per channel and listens briefly; a
// neighbour's router answers after a random delay, so a single pass catches
// roughly half of them and the list changes from press to press. Several passes
// merged catch almost all of them. Variables so tests can shorten the run.
var (
	networkScanPasses = 3
	networkScanPause  = 500 * time.Millisecond
)

func (c *Client) ScanNetworks(ctx context.Context) (*model.NetworkScan, error) {
	started := time.Now()
	// The daemon socket reports network names and extended PAN IDs that otbr-web's
	// endpoint omits, so prefer it whenever it is usable.
	if c.scanner != nil && c.scanner.Available() {
		items, passes, err := mergedDiscovery(ctx, networkScanPasses, networkScanPause, c.scanner.ScanNetworks)
		var unusable interface{ SocketUnavailable() bool }
		switch {
		case err == nil:
		case errors.As(err, &unusable) && unusable.SocketUnavailable():
			// The socket exists but cannot be opened — a non-root process sees this,
			// since it is mode 0755 root:root and connect() needs write. Nothing was
			// reached, so try otbr-web rather than reporting a fault.
			items = nil
		default:
			// The daemon answered badly. Falling back would hide a real fault.
			return &model.NetworkScan{
				Status: "unavailable", Items: []model.AvailableNetwork{}, Source: "OpenThread daemon scan",
				ScannedAt: time.Now().UTC(), DurationMs: time.Since(started).Milliseconds(),
				Error: err.Error(),
			}, nil
		}
		if items != nil {
			return &model.NetworkScan{
				Status: "available", Items: items, Source: "OpenThread daemon scan", Passes: passes,
				ScannedAt: time.Now().UTC(), DurationMs: time.Since(started).Milliseconds(),
			}, nil
		}
	}
	if c.legacyWebURL == nil {
		return &model.NetworkScan{
			Status: "unsupported", Items: []model.AvailableNetwork{}, Source: "OTBR active scan",
			ScannedAt: time.Now().UTC(), Error: "available network scanning requires the standard OTBR web service",
		}, nil
	}
	scanURL := *c.legacyWebURL
	scanURL.Path = "/available_network"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scanURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	scanClient := *c.httpClient
	scanClient.Timeout = 18 * time.Second
	unavailable := func(reason string) *model.NetworkScan {
		return &model.NetworkScan{
			Status: "unavailable", Items: []model.AvailableNetwork{}, Source: "OTBR active scan",
			ScannedAt: time.Now().UTC(), DurationMs: time.Since(started).Milliseconds(),
			Error: reason,
		}
	}
	resp, err := scanClient.Do(req)
	if err != nil {
		// Scanning lives in otbr-web, a separate service from the REST API, and the
		// REST API exposes no active-scan action of its own. If otbr-web is not
		// running there is nothing to scan with, which is a capability gap worth
		// stating plainly rather than surfacing a dial error to the UI.
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return unavailable(fmt.Sprintf("the OTBR web service at %s did not respond in time", scanURL.Host)), nil
		}
		return unavailable(fmt.Sprintf("cannot reach the OTBR web service at %s. Nearby-network scanning is provided by otbr-web, not by the REST API at %s — start otbr-web to restore it", scanURL.Host, c.baseURL.Host)), nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNotImplemented || resp.StatusCode == http.StatusMethodNotAllowed {
			return &model.NetworkScan{
				Status: "unsupported", Items: []model.AvailableNetwork{}, Source: "OTBR active scan",
				ScannedAt: time.Now().UTC(), DurationMs: time.Since(started).Milliseconds(),
				Error: fmt.Sprintf("%s is reachable but exposes no /available_network endpoint", scanURL.Host),
			}, nil
		}
		return nil, &HTTPStatusError{Endpoint: "/available_network", StatusCode: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read available network scan: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("available network scan exceeded size limit")
	}
	items, err := decodeAvailableNetworks(body)
	if err != nil {
		return nil, err
	}
	return &model.NetworkScan{
		Status: "available", Items: items, Source: "OTBR active scan", ScannedAt: time.Now().UTC(),
		DurationMs: time.Since(started).Milliseconds(),
	}, nil
}

func decodeAvailableNetworks(body []byte) ([]model.AvailableNetwork, error) {
	var response struct {
		Error  int             `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode available network scan: %w", err)
	}
	if len(response.Result) == 0 {
		return nil, errors.New("OTBR available network scan failed")
	}
	// The OTBR web service reports scan failures (for example a scan already
	// in progress) as a message string in "result" instead of a network list.
	var message string
	if json.Unmarshal(response.Result, &message) == nil {
		if message == "" {
			return nil, errors.New("OTBR available network scan failed")
		}
		return nil, fmt.Errorf("OTBR available network scan failed: %s", message)
	}
	var entries []struct {
		Name            string `json:"nn"`
		ExtendedPANID   string `json:"xp"`
		PANID           string `json:"pi"`
		Channel         *int   `json:"ch"`
		HardwareAddress string `json:"ha"`
	}
	if err := json.Unmarshal(response.Result, &entries); err != nil {
		return nil, fmt.Errorf("decode available network scan: %w", err)
	}
	if response.Error != 0 || entries == nil {
		return nil, errors.New("OTBR available network scan failed")
	}
	items := make([]model.AvailableNetwork, 0, len(entries))
	for _, network := range entries {
		items = append(items, model.AvailableNetwork{
			Name: network.Name, ExtendedPANID: network.ExtendedPANID, PANID: network.PANID,
			Channel: network.Channel, HardwareAddress: network.HardwareAddress,
		})
	}
	return items, nil
}

func decodeStatusProperties(body []byte) (statusProperties, error) {
	var response struct {
		Error  int               `json:"error"`
		Result map[string]string `json:"result"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return statusProperties{}, fmt.Errorf("decode OTBR status properties: %w", err)
	}
	if response.Error != 0 || response.Result == nil {
		return statusProperties{}, errors.New("OTBR status properties were unavailable")
	}
	value := response.Result
	return statusProperties{
		LinkLocalAddress: value["IPv6:LinkLocalAddress"], LocalAddress: value["IPv6:LocalAddress"],
		MeshLocalAddress: value["IPv6:MeshLocalAddress"], MeshLocalPrefix: value["IPv6:MeshLocalPrefix"],
		NetworkName: value["Network:Name"], PANID: value["Network:PANID"], PartitionID: value["Network:PartitionID"],
		XPANID: value["Network:XPANID"], OpenThreadVersion: value["OpenThread:Version"],
		OpenThreadAPIVersion: value["OpenThread:Version API"], RCPChannel: value["RCP:Channel"],
		RCPEUI64: value["RCP:EUI64"], RCPState: value["RCP:State"], RCPTxPower: value["RCP:TxPower"],
		RCPVersion: value["RCP:Version"], WPANService: value["WPAN service"],
	}, nil
}

func applyStatusProperties(overview *model.Overview, properties statusProperties) {
	overview.LinkLocalAddress = properties.LinkLocalAddress
	if properties.LocalAddress != "" {
		overview.OMRIPv6Address = properties.LocalAddress
	}
	overview.MeshLocalAddress = properties.MeshLocalAddress
	overview.MeshLocalPrefix = properties.MeshLocalPrefix
	overview.PANID = properties.PANID
	overview.OpenThreadVersion = properties.OpenThreadVersion
	overview.OpenThreadAPIVersion = properties.OpenThreadAPIVersion
	overview.RCPChannel = properties.RCPChannel
	overview.RCPEUI64 = properties.RCPEUI64
	overview.RCPState = properties.RCPState
	overview.RCPTxPower = properties.RCPTxPower
	overview.RCPVersion = properties.RCPVersion
	overview.WPANService = properties.WPANService
	if overview.NetworkName == "" {
		overview.NetworkName = properties.NetworkName
	}
	if overview.ExtendedPANID == "" {
		overview.ExtendedPANID = properties.XPANID
	}
	if overview.PartitionID == nil && properties.PartitionID != "" {
		var partitionID uint32
		if _, err := fmt.Sscanf(properties.PartitionID, "%d", &partitionID); err == nil {
			overview.PartitionID = &partitionID
		}
	}
}

func (c *Client) fetchNodeAttributes(ctx context.Context, endpoint string) (nodeAttributes, error) {
	body, _, err := c.get(ctx, endpoint)
	if err != nil {
		return nodeAttributes{}, err
	}
	return decodeNode(body)
}

// mergeFreshNode overlays live values from a fresh node representation (/node)
// onto a base (/api/node) that may be stale after a dataset change, keeping the
// base's fields that the fresh source omits (such as the OMR address). /node
// reports the current role in its "state" field and has no separate role.
func mergeFreshNode(base, fresh nodeAttributes) nodeAttributes {
	merged := base
	if fresh.NetworkName != "" {
		merged.NetworkName = fresh.NetworkName
	}
	if fresh.State != "" {
		merged.State = fresh.State
		merged.Role = fresh.State
	}
	if fresh.RLOCAddress != "" {
		merged.RLOCAddress = fresh.RLOCAddress
	}
	if fresh.RLOC16 != "" {
		merged.RLOC16 = fresh.RLOC16
	}
	if fresh.RouterCount != nil {
		merged.RouterCount = fresh.RouterCount
	}
	if fresh.RouterID != nil {
		merged.RouterID = fresh.RouterID
	}
	if fresh.ExtPANID != "" {
		merged.ExtPANID = fresh.ExtPANID
	}
	if fresh.LeaderData.PartitionID != nil {
		merged.LeaderData.PartitionID = fresh.LeaderData.PartitionID
	}
	if fresh.LeaderData.LeaderRouterID != nil {
		merged.LeaderData.LeaderRouterID = fresh.LeaderData.LeaderRouterID
	}
	return merged
}

func decodeNode(body []byte) (nodeAttributes, error) {
	var envelope nodeEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nodeAttributes{}, fmt.Errorf("decode OTBR node response: %w", err)
	}
	if envelope.Data.ID != "" || hasNodeData(envelope.Data.Attributes) {
		return envelope.Data.Attributes, nil
	}

	var direct nodeAttributes
	if err := json.Unmarshal(body, &direct); err != nil {
		return nodeAttributes{}, fmt.Errorf("decode OTBR node response: %w", err)
	}
	if !hasNodeData(direct) {
		return nodeAttributes{}, errors.New("OTBR node response did not contain recognizable node data")
	}
	return direct, nil
}

func hasNodeData(a nodeAttributes) bool {
	return a.ExtAddress != "" || a.NetworkName != "" || a.State != "" || a.RLOC16 != ""
}

func (c *Client) Devices(ctx context.Context) (*model.DeviceInventory, error) {
	if devices, _, ok := c.liveMesh(ctx); ok {
		return devices, nil
	}
	started := time.Now()
	body, _, err := c.getWithAccept(ctx, "/api/devices", "application/vnd.api+json")
	latency := time.Since(started).Milliseconds()
	if err != nil {
		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusNotFound || statusErr.StatusCode == http.StatusNotImplemented || statusErr.StatusCode == http.StatusMethodNotAllowed) {
			return &model.DeviceInventory{
				Status: "unsupported", CollectionSupported: false, Items: []model.Device{},
				Source: "node overview", RequestLatencyMs: latency,
				Error: "device collection is unsupported by this OTBR version",
			}, nil
		}
		return nil, err
	}
	items, err := decodeDevices(body)
	if err != nil {
		return nil, err
	}
	return &model.DeviceInventory{
		Status: "available", CollectionSupported: true, Items: items,
		Source: "OTBR device collection", RequestLatencyMs: latency,
	}, nil
}

// discoveryUnsupportedError reports itself through a method rather than a shared
// sentinel so internal/service can recognize it without importing this package,
// keeping the provider swappable.
type discoveryUnsupportedError struct{}

func (discoveryUnsupportedError) Error() string {
	return "device discovery is unsupported by this OTBR version"
}

// DiscoveryUnsupported marks this as the "no /api/actions collection" case, which
// is how older OTBR builds present themselves. Retrying cannot change it.
func (discoveryUnsupportedError) DiscoveryUnsupported() bool { return true }

// ErrDiscoveryUnsupported is returned when the border router exposes no discovery action.
var ErrDiscoveryUnsupported error = discoveryUnsupportedError{}

// OTBR requires every attribute of the discovery action even though its schema
// documents a default for each one; omitting any of them is a 422.
const (
	discoveryMaxAge      = 30
	discoveryMaxRetries  = 5
	discoveryDeviceCount = 10
	discoveryTimeoutSecs = 93

	// A diagnostic query walks the mesh and must wait for sleepy children to wake.
	// At 10s the task gives up and lands in state "stopped" with no record written;
	// 60s completes against a small mesh.
	diagnosticTimeoutSecs = 60
)

// diagnosticTypes are the TLVs the topology view is built from. childTable is what
// attaches end devices to their parent; without it children float unconnected.
// "children" (TLV 30) is what makes a child identifiable: unlike childTable it
// carries each child's extAddress, so a map node can be matched to the device
// inventory and to any name the user gave it.
var diagnosticTypes = []string{"extAddress", "rloc16", "route", "childTable", "children", "routerNeighbors"}

// RefreshMesh asks OTBR to rediscover the mesh: the device collection, plus a
// childTable/route diagnostic for each router in routers (pass the routers already
// known; the local border router is always worth including).
//
// /api/devices and /api/diagnostics are caches OTBR fills only when a client
// posts this action — it runs no sweep of its own. Without this the collections
// freeze at whatever the last sweep saw: departed devices linger indefinitely and
// devices that joined since never appear. Sleepy children are only ever visible
// through a sweep, so a mesh of battery-powered end devices can otherwise read as
// empty while every one of them is online.
//
// The actions are asynchronous; results land in the collections a few seconds
// later and are picked up by the normal device and topology polls.
func (c *Client) RefreshMesh(ctx context.Context, routers []string) error {
	// Pointless work when the live reader is in use: these actions only refresh
	// OTBR's caches, and nothing reads them while meshdiag answers from the mesh.
	// Each sweep costs real airtime, so skip it rather than keeping a cache warm
	// that would be stale anyway if we ever needed it.
	if c.mesh != nil && c.mesh.Available() {
		return nil
	}
	// One request carries both kinds of action; /api/actions takes an array.
	items := []any{map[string]any{
		"type": "updateDeviceCollectionTask",
		"attributes": map[string]any{
			"maxAge":      discoveryMaxAge,
			"maxRetries":  discoveryMaxRetries,
			"deviceCount": discoveryDeviceCount,
			"timeout":     discoveryTimeoutSecs,
		},
	}}
	// The device collection carries no parent pointers — links come only from each
	// router's childTable, which is a per-router diagnostic query.
	for _, router := range routers {
		if strings.TrimSpace(router) == "" {
			continue
		}
		items = append(items, map[string]any{
			"type": "getNetworkDiagnosticTask",
			"attributes": map[string]any{
				"destination": router,
				"types":       diagnosticTypes,
				"timeout":     diagnosticTimeoutSecs,
			},
		})
	}
	body, err := json.Marshal(map[string]any{"data": items})
	if err != nil {
		return err
	}
	if _, err := c.write(ctx, http.MethodPost, "/api/actions", "application/vnd.api+json", body); err != nil {
		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusNotFound || statusErr.StatusCode == http.StatusNotImplemented || statusErr.StatusCode == http.StatusMethodNotAllowed) {
			return ErrDiscoveryUnsupported
		}
		return err
	}
	return nil
}

func (c *Client) Topology(ctx context.Context) (*model.Topology, error) {
	if _, topology, ok := c.liveMesh(ctx); ok {
		return topology, nil
	}
	started := time.Now()
	body, _, err := c.getWithAccept(ctx, "/api/diagnostics", "application/vnd.api+json")
	latency := time.Since(started).Milliseconds()
	if err != nil {
		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusNotFound || statusErr.StatusCode == http.StatusNotImplemented || statusErr.StatusCode == http.StatusMethodNotAllowed) {
			return &model.Topology{
				Status: "unsupported", CollectionSupported: false, Nodes: []model.TopologyNode{}, Links: []model.TopologyLink{},
				Source: "OTBR network diagnostics", RequestLatencyMs: latency, Error: "network diagnostics are unsupported by this OTBR version",
			}, nil
		}
		return nil, err
	}
	nodes, links, err := decodeTopology(body)
	if err != nil {
		return nil, err
	}
	return &model.Topology{
		Status: "available", CollectionSupported: true, Nodes: nodes, Links: links,
		Source: "OTBR network diagnostics", RequestLatencyMs: latency,
	}, nil
}

func decodeTopology(body []byte) ([]model.TopologyNode, []model.TopologyLink, error) {
	var rawItems []json.RawMessage
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal(body, &rawItems); err != nil {
			return nil, nil, fmt.Errorf("decode OTBR diagnostics: %w", err)
		}
	} else {
		var envelope struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, nil, fmt.Errorf("decode OTBR diagnostics: %w", err)
		}
		if envelope.Data == nil {
			return nil, nil, errors.New("OTBR diagnostics response did not contain a collection")
		}
		rawItems = envelope.Data
	}

	type diagnosticRecord struct {
		attributes diagnosticAttributes
		created    time.Time
	}
	latest := map[string]diagnosticRecord{}
	for _, raw := range rawItems {
		var header deviceHeader
		if err := json.Unmarshal(raw, &header); err != nil {
			continue
		}
		var attributes diagnosticAttributes
		if len(header.Attributes) > 0 && string(header.Attributes) != "null" {
			if err := json.Unmarshal(header.Attributes, &attributes); err != nil {
				continue
			}
		} else if err := json.Unmarshal(raw, &attributes); err != nil {
			continue
		}
		key := attributes.ExtAddress
		if key == "" {
			key = attributes.RLOC16
		}
		if key == "" {
			continue
		}
		created := time.Time{}
		if parsed := parseTimestamp(attributes.Created); parsed != nil {
			created = *parsed
		}
		if existing, ok := latest[key]; !ok || created.After(existing.created) {
			latest[key] = diagnosticRecord{attributes: attributes, created: created}
		}
	}

	records := make([]diagnosticAttributes, 0, len(latest))
	for _, record := range latest {
		records = append(records, record.attributes)
	}
	sort.Slice(records, func(i, j int) bool {
		left, right := records[i].RouterID, records[j].RouterID
		if left == nil || right == nil {
			return records[i].RLOC16 < records[j].RLOC16
		}
		return *left < *right
	})

	nodes := make([]model.TopologyNode, 0, len(records))
	byExtAddress := map[string]string{}
	byRouterID := map[int]string{}
	for _, attributes := range records {
		id := attributes.ExtAddress
		if id == "" {
			id = attributes.RLOC16
		}
		nodes = append(nodes, model.TopologyNode{
			ID: id, Role: "router", RLOC16: attributes.RLOC16,
			RouterID: attributes.RouterID, ExtendedAddress: attributes.ExtAddress,
		})
		if attributes.ExtAddress != "" {
			byExtAddress[strings.ToLower(attributes.ExtAddress)] = id
		}
		if attributes.RouterID != nil {
			byRouterID[*attributes.RouterID] = id
		}
	}

	links := []model.TopologyLink{}
	routerLinks := map[string]int{}
	addRouterLink := func(link model.TopologyLink) {
		if link.Source == "" || link.Target == "" || link.Source == link.Target {
			return
		}
		left, right := link.Source, link.Target
		if left > right {
			left, right = right, left
		}
		key := left + "\x00" + right
		if index, ok := routerLinks[key]; ok {
			existing := &links[index]
			if existing.LinkQualityIn == nil {
				existing.LinkQualityIn = link.LinkQualityIn
			}
			if existing.LinkQualityOut == nil {
				existing.LinkQualityOut = link.LinkQualityOut
			}
			if existing.RouteCost == nil {
				existing.RouteCost = link.RouteCost
			}
			if existing.LinkMargin == nil {
				existing.LinkMargin = link.LinkMargin
			}
			if existing.AverageRSSI == nil {
				existing.AverageRSSI = link.AverageRSSI
			}
			if existing.LastRSSI == nil {
				existing.LastRSSI = link.LastRSSI
			}
			if existing.FrameErrorRate == nil {
				existing.FrameErrorRate = link.FrameErrorRate
			}
			if existing.MessageErrorRate == nil {
				existing.MessageErrorRate = link.MessageErrorRate
			}
			return
		}
		link.Source, link.Target, link.Type = left, right, "router"
		routerLinks[key] = len(links)
		links = append(links, link)
	}

	for _, attributes := range records {
		parentID := attributes.ExtAddress
		if parentID == "" {
			parentID = attributes.RLOC16
		}
		for _, neighbor := range attributes.RouterNeighbors {
			target := byExtAddress[strings.ToLower(neighbor.ExtAddress)]
			addRouterLink(model.TopologyLink{
				Source: parentID, Target: target, LinkMargin: neighbor.LinkMargin,
				AverageRSSI: neighbor.AverageRSSI, LastRSSI: neighbor.LastRSSI,
				FrameErrorRate: neighbor.FrameErrorRate, MessageErrorRate: neighbor.MessageErrorRate,
			})
		}
		for _, route := range attributes.Route.RouteData {
			addRouterLink(model.TopologyLink{
				Source: parentID, Target: byRouterID[route.RouteID], LinkQualityIn: route.LinkQualityIn,
				LinkQualityOut: route.LinkQualityOut, RouteCost: route.RouteCost,
			})
		}
		parentRLOC, ok := parseRLOC16(attributes.RLOC16)
		// childTable and children describe the same children from different TLVs:
		// childTable brings link quality, children brings the extended address.
		// Merge them by child ID so a node gets both.
		type mergedChild struct {
			node  model.TopologyNode
			order int
		}
		merged := map[int]*mergedChild{}
		order := 0
		entry := func(childID int) *mergedChild {
			if existing, seen := merged[childID]; seen {
				return existing
			}
			derivedRLOC := ""
			if ok {
				derivedRLOC = fmt.Sprintf("0x%04x", (parentRLOC+childID)&0xffff)
			}
			created := &mergedChild{order: order, node: model.TopologyNode{
				// Derived id and RLOC16 are the fallback; the children TLV replaces
				// the id with the extended address when it is available.
				ID: fmt.Sprintf("%s/child/%d", parentID, childID), Role: "child",
				RLOC16: derivedRLOC, RouterID: attributes.RouterID, ParentID: parentID,
			}}
			order++
			merged[childID] = created
			return created
		}
		for _, child := range attributes.ChildTable {
			node := &entry(child.ChildID).node
			node.LinkQuality = child.LinkQuality
			node.Timeout = child.Timeout
			node.RxOnWhenIdle = child.Mode.RxOnWhenIdle
			node.DeviceTypeFTD = child.Mode.DeviceTypeFTD
			node.FullNetworkData = child.Mode.FullNetworkData
		}
		for _, child := range attributes.Children {
			node := &entry(child.ChildID).node
			if child.ExtAddress != "" {
				// A stable identity: this is what lets the map match the device
				// inventory, and therefore show the user's own name for the device.
				node.ID = child.ExtAddress
				node.ExtendedAddress = child.ExtAddress
			}
			if child.RLOC16 != "" {
				node.RLOC16 = child.RLOC16
			}
			if child.Timeout != nil {
				node.Timeout = child.Timeout
			}
			if child.RxOnWhenIdle != nil {
				node.RxOnWhenIdle = child.RxOnWhenIdle
			}
			if child.DeviceTypeFTD != nil {
				node.DeviceTypeFTD = child.DeviceTypeFTD
			}
			if child.FullNetworkData != nil {
				node.FullNetworkData = child.FullNetworkData
			}
		}
		childIDs := make([]int, 0, len(merged))
		for childID := range merged {
			childIDs = append(childIDs, childID)
		}
		sort.Slice(childIDs, func(i, j int) bool { return merged[childIDs[i]].order < merged[childIDs[j]].order })
		for _, childID := range childIDs {
			child := merged[childID]
			nodes = append(nodes, child.node)
			link := model.TopologyLink{
				Source: parentID, Target: child.node.ID, Type: "child", LinkQuality: child.node.LinkQuality,
			}
			for _, detail := range attributes.Children {
				if detail.ChildID == childID {
					link.LinkMargin, link.AverageRSSI, link.LastRSSI = detail.LinkMargin, detail.AverageRSSI, detail.LastRSSI
					link.FrameErrorRate = detail.FrameErrorRate
					break
				}
			}
			links = append(links, link)
		}
	}
	return nodes, links, nil
}

func parseRLOC16(value string) (int, bool) {
	clean := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "0x")
	if clean == "" {
		return 0, false
	}
	var parsed int
	if _, err := fmt.Sscanf(clean, "%x", &parsed); err != nil {
		return 0, false
	}
	return parsed, true
}

func decodeDevices(body []byte) ([]model.Device, error) {
	var rawItems []json.RawMessage
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal(body, &rawItems); err != nil {
			return nil, fmt.Errorf("decode OTBR device collection: %w", err)
		}
	} else {
		var envelope struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, fmt.Errorf("decode OTBR device collection: %w", err)
		}
		if envelope.Data == nil {
			return nil, errors.New("OTBR device response did not contain a device collection")
		}
		rawItems = envelope.Data
	}

	items := make([]model.Device, 0, len(rawItems))
	for _, raw := range rawItems {
		var header deviceHeader
		if err := json.Unmarshal(raw, &header); err != nil {
			continue
		}
		var a deviceAttributes
		if len(header.Attributes) > 0 && string(header.Attributes) != "null" {
			if err := json.Unmarshal(header.Attributes, &a); err != nil {
				continue
			}
		} else if err := json.Unmarshal(raw, &a); err != nil {
			continue
		}
		id := header.ID
		if id == "" {
			id = a.ExtAddress
		}
		if id == "" {
			continue
		}
		name := a.Hostname
		if name == "" {
			name = a.HostName
		}
		addresses := append([]string(nil), a.IPv6Addresses...)
		if a.OMRIPv6Address != "" && !contains(addresses, a.OMRIPv6Address) {
			addresses = append(addresses, a.OMRIPv6Address)
		}
		items = append(items, model.Device{
			ID: id, Name: name, Role: a.Role, RLOC16: a.RLOC16, RouterID: a.RouterID,
			Parent: a.Parent, ExtendedAddress: a.ExtAddress, MLEIDIID: a.MLEIDIID,
			EUI64: a.EUI64, IPv6Addresses: addresses, OMRIPv6Address: a.OMRIPv6Address,
			ThreadVersion: a.ThreadVersion, FirstSeen: parseTimestamp(a.Created), LastSeen: parseTimestamp(a.Updated),
			RSSI: a.RSSI, LinkQuality: a.LinkQuality, LinkMargin: a.LinkMargin,
		})
	}

	return items, nil
}

func parseTimestamp(values ...string) *time.Time {
	for _, value := range values {
		if value == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err == nil {
			parsed = parsed.UTC()
			return &parsed
		}
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type probe struct {
	name      string
	endpoints []string
	required  bool
	nodeData  bool
}

var knownEndpoints = []probe{
	{name: "Node overview", endpoints: []string{"/api/node", "/node"}, required: true, nodeData: true},
	{name: "Device inventory", endpoints: []string{"/api/devices"}},
	{name: "Direct topology endpoint", endpoints: []string{"/api/topology"}},
	{name: "Diagnostics", endpoints: []string{"/api/diagnostics"}},
}

func (c *Client) Capabilities(ctx context.Context) (*model.Capabilities, error) {
	result := &model.Capabilities{LastChecked: time.Now().UTC()}
	for _, p := range knownEndpoints {
		started := time.Now()
		endpoint := strings.Join(p.endpoints, " or ")
		status := 0
		var err error
		for _, candidate := range p.endpoints {
			var body []byte
			body, status, err = c.get(ctx, candidate)
			if err == nil && p.nodeData {
				_, err = decodeNode(body)
			}
			if err == nil {
				endpoint = candidate
				break
			}
		}
		item := model.Capability{
			Name: p.name, Endpoint: endpoint, Required: p.required,
			StatusCode: status, LatencyMs: time.Since(started).Milliseconds(),
			LastChecked: time.Now().UTC(),
		}
		if err == nil {
			item.Supported = true
		} else {
			var statusErr *HTTPStatusError
			if errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusNotFound || statusErr.StatusCode == http.StatusNotImplemented || statusErr.StatusCode == http.StatusMethodNotAllowed) {
				item.Error = "unsupported by this OTBR version"
			} else {
				item.Error = err.Error()
			}
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func (c *Client) get(ctx context.Context, endpoint string) ([]byte, int, error) {
	return c.getWithAccept(ctx, endpoint, "application/json")
}

func (c *Client) getWithAccept(ctx context.Context, endpoint, accept string) ([]byte, int, error) {
	u := *c.baseURL
	u.Path = strings.TrimRight(c.baseURL.Path, "/") + endpoint
	u.RawQuery = ""
	u.Fragment = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", accept)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request OTBR endpoint %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
		return nil, resp.StatusCode, &HTTPStatusError{Endpoint: endpoint, StatusCode: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read OTBR response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, resp.StatusCode, errors.New("OTBR response exceeded size limit")
	}
	return body, resp.StatusCode, nil
}

// mergedDiscovery runs scan up to passes times, pausing between them, and merges
// the results by network identity, keeping the strongest sighting of each. A
// failure on the first pass is returned; a later failure ends the run with what
// was heard so far.
func mergedDiscovery(ctx context.Context, passes int, pause time.Duration, scan func(context.Context) ([]model.AvailableNetwork, error)) ([]model.AvailableNetwork, int, error) {
	merged := []model.AvailableNetwork{}
	index := map[string]int{}
	done := 0
	for done < passes {
		items, err := scan(ctx)
		if err != nil {
			if done == 0 {
				return nil, 0, err
			}
			break
		}
		done++
		for _, network := range items {
			key := strings.ToLower(network.ExtendedPANID + "|" + network.PANID + "|" + network.HardwareAddress + "|" + network.Name)
			if at, seen := index[key]; seen {
				// Same beacon heard again: prefer the sighting that carries more detail.
				if merged[at].Name == "" && network.Name != "" {
					merged[at] = network
				}
				continue
			}
			index[key] = len(merged)
			merged = append(merged, network)
		}
		if done >= passes {
			break
		}
		select {
		case <-ctx.Done():
			return merged, done, nil
		case <-time.After(pause):
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Name != merged[j].Name {
			return merged[i].Name < merged[j].Name
		}
		return merged[i].HardwareAddress < merged[j].HardwareAddress
	})
	return merged, done, nil
}
