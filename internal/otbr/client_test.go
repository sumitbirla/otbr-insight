package otbr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

const suppliedNodeResponse = `{
  "data": {
    "id": "1a2b3c4d5e6f7a8b",
    "type": "threadBorderRouter",
    "attributes": {
      "extAddress": "1a2b3c4d5e6f7a8b",
      "mlEidIid": "060708090a0b0c0d",
      "omrIpv6Address": "fd11:2233:4455:1:60c5:d77f:b600:6ee5",
      "hostName": "",
      "role": "router",
      "mode": {"deviceTypeFTD": true, "rxOnWhenIdle": true, "fullNetworkData": true},
      "baId": "0123456789abcdef0123456789abcdef",
      "baState": "active",
      "state": "router",
      "routerCount": 2,
      "rlocAddress": "fdde:ad00:beef:9004:0:ff:fe00:7000",
      "networkName": "OpenThread-a1b2",
      "rloc16": "0x7000",
      "routerId": 28,
      "leaderData": {"partitionId": 627211073, "weighting": 64, "dataVersion": 154, "stableDataVersion": 124, "leaderRouterId": 37},
      "extPanId": "1122334455667788",
      "created": "2026-07-16T17:33:05-05:00",
      "updated": "2026-07-16T18:07:53-05:00"
    }
  }
}`

func TestOverviewParsesAndNormalizesSuppliedResponse(t *testing.T) {
	server := nodeServer(t, http.StatusOK, suppliedNodeResponse)
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	overview, err := client.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview() error = %v", err)
	}
	if overview.NetworkName != "OpenThread-a1b2" {
		t.Errorf("NetworkName = %q", overview.NetworkName)
	}
	if overview.Role != "router" || overview.State != "router" {
		t.Errorf("role/state = %q/%q", overview.Role, overview.State)
	}
	if overview.RLOC16 != "0x7000" {
		t.Errorf("RLOC16 = %q", overview.RLOC16)
	}
	if overview.RouterID == nil || *overview.RouterID != 28 {
		t.Errorf("RouterID = %v", overview.RouterID)
	}
	if overview.PartitionID == nil || *overview.PartitionID != 627211073 {
		t.Errorf("PartitionID = %v", overview.PartitionID)
	}
	if overview.LeaderRouterID == nil || *overview.LeaderRouterID != 37 {
		t.Errorf("LeaderRouterID = %v", overview.LeaderRouterID)
	}
	if overview.RouterCount == nil || *overview.RouterCount != 2 {
		t.Errorf("RouterCount = %v", overview.RouterCount)
	}
	if overview.ExtendedAddress != "1a2b3c4d5e6f7a8b" {
		t.Errorf("ExtendedAddress = %q", overview.ExtendedAddress)
	}
	if overview.ExtendedPANID != "1122334455667788" {
		t.Errorf("ExtendedPANID = %q", overview.ExtendedPANID)
	}
	if overview.OMRIPv6Address != "fd11:2233:4455:1:60c5:d77f:b600:6ee5" {
		t.Errorf("OMRIPv6Address = %q", overview.OMRIPv6Address)
	}
	if overview.BorderAgentState != "active" {
		t.Errorf("BorderAgentState = %q", overview.BorderAgentState)
	}
}

func TestStatusPropertiesDecodeAndEnrichOverview(t *testing.T) {
	properties, err := decodeStatusProperties([]byte(`{
		"error":0,
		"result":{
			"IPv6:LinkLocalAddress":"fe80::1",
			"IPv6:LocalAddress":"fd12::1",
			"IPv6:MeshLocalAddress":"fd38::1",
			"IPv6:MeshLocalPrefix":"fd38::",
			"Network:Name":"TestMesh",
			"Network:PANID":"0x1234",
			"Network:PartitionID":"627211073",
			"Network:XPANID":"0011223344556677",
			"OpenThread:Version":"OPENTHREAD/test",
			"OpenThread:Version API":"591",
			"RCP:Channel":"25",
			"RCP:EUI64":"0304050607080901",
			"RCP:State":"router",
			"RCP:TxPower":"0 dBm",
			"RCP:Version":"SL-OPENTHREAD/test",
			"WPAN service":"associated"
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	overview := &model.Overview{}
	applyStatusProperties(overview, properties)
	if overview.LinkLocalAddress != "fe80::1" || overview.OMRIPv6Address != "fd12::1" || overview.MeshLocalAddress != "fd38::1" {
		t.Errorf("IPv6 properties = %+v", overview)
	}
	if overview.PANID != "0x1234" || overview.PartitionID == nil || *overview.PartitionID != 627211073 {
		t.Errorf("network properties = %+v", overview)
	}
	if overview.RCPChannel != "25" || overview.RCPEUI64 != "0304050607080901" || overview.WPANService != "associated" {
		t.Errorf("RCP properties = %+v", overview)
	}
}

func TestDecodeAvailableNetworksAllowsOmittedNames(t *testing.T) {
	items, err := decodeAvailableNetworks([]byte(`{
		"error":0,
		"result":[
			{"ch":14,"ha":"0C0D0E0F01020304","pi":"0x5294"},
			{"nn":"Office Thread","xp":"0011223344556677","ch":25,"ha":"0708090A0B0C0D0E","pi":"0xA1B2"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "" || items[0].Channel == nil || *items[0].Channel != 14 {
		t.Fatalf("items = %+v", items)
	}
	if items[1].Name != "Office Thread" || items[1].ExtendedPANID != "0011223344556677" || items[1].PANID != "0xA1B2" {
		t.Errorf("named network = %+v", items[1])
	}
}

func TestDecodeAvailableNetworksReportsStringResult(t *testing.T) {
	_, err := decodeAvailableNetworks([]byte(`{"error":1,"result":"Scan is already in progress"}`))
	if err == nil {
		t.Fatal("expected error for string result")
	}
	if got := err.Error(); got != "OTBR available network scan failed: Scan is already in progress" {
		t.Errorf("error = %q", got)
	}
}

func TestDecodeAvailableNetworksMissingResult(t *testing.T) {
	if _, err := decodeAvailableNetworks([]byte(`{"error":0}`)); err == nil {
		t.Fatal("expected error for missing result")
	}
	if _, err := decodeAvailableNetworks([]byte(`{"error":0,"result":null}`)); err == nil {
		t.Fatal("expected error for null result")
	}
}

func TestOverviewAllowsMissingOptionalFields(t *testing.T) {
	server := nodeServer(t, http.StatusOK, `{"data":{"id":"abc","attributes":{"networkName":"minimal"}}}`)
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())
	overview, err := client.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview() error = %v", err)
	}
	if overview.NetworkName != "minimal" {
		t.Errorf("NetworkName = %q", overview.NetworkName)
	}
	if overview.RouterID != nil || overview.PartitionID != nil || overview.RouterCount != nil {
		t.Error("optional numeric fields should remain nil")
	}
}

func TestOverviewFallsBackToLegacyBareNodeEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/node" {
			_, _ = w.Write([]byte(`{"error":"not found"}`))
			return
		}
		if r.URL.Path == "/node" {
			_, _ = w.Write([]byte(`{
				"extAddress":"0a0b0c0d0e0f0102",
				"state":"router",
				"routerCount":2,
				"networkName":"LegacyMesh",
				"rloc16":"0x7400",
				"leaderData":{"partitionId":123,"leaderRouterId":9},
				"extPanId":"dead00beef00cafe"
			}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())
	overview, err := client.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview() error = %v", err)
	}
	if overview.NetworkName != "LegacyMesh" || overview.ExtendedAddress != "0a0b0c0d0e0f0102" {
		t.Fatalf("overview = %+v", overview)
	}
	if overview.Role != "router" {
		t.Errorf("Role fallback = %q", overview.Role)
	}
}

func TestDecodeNodeAcceptsLegacyBareObject(t *testing.T) {
	attributes, err := decodeNode([]byte(`{
		"extAddress":"0a0b0c0d0e0f0102",
		"state":"router",
		"networkName":"LegacyMesh",
		"rloc16":"0x7400"
	}`))
	if err != nil {
		t.Fatalf("decodeNode() error = %v", err)
	}
	if attributes.NetworkName != "LegacyMesh" || attributes.ExtAddress != "0a0b0c0d0e0f0102" {
		t.Fatalf("attributes = %+v", attributes)
	}
}

func TestOverviewRejectsMalformedResponse(t *testing.T) {
	server := nodeServer(t, http.StatusOK, `{"data":`)
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())
	if _, err := client.Overview(context.Background()); err == nil {
		t.Fatal("Overview() expected malformed JSON error")
	}
}

func TestOverviewUnavailable(t *testing.T) {
	server := nodeServer(t, http.StatusServiceUnavailable, `{}`)
	client, _ := NewClient(server.URL, server.Client())
	server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.Overview(ctx); err == nil {
		t.Fatal("Overview() expected connection error")
	}
}

func TestCapabilitiesTreatUnsupportedEndpointsGracefully(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/node" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(suppliedNodeResponse))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())
	capabilities, err := client.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities() error = %v", err)
	}
	if len(capabilities.Items) != 4 {
		t.Fatalf("items = %d", len(capabilities.Items))
	}
	if !capabilities.Items[0].Supported {
		t.Error("node capability should be supported")
	}
	for _, item := range capabilities.Items[1:] {
		if item.Supported {
			t.Errorf("%s unexpectedly supported", item.Endpoint)
		}
		if item.StatusCode != http.StatusNotFound {
			t.Errorf("%s status = %d", item.Endpoint, item.StatusCode)
		}
		if item.Error != "unsupported by this OTBR version" {
			t.Errorf("%s error = %q", item.Endpoint, item.Error)
		}
	}
}

func TestDevicesParsesAndNormalizesCollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/devices" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.api+json" {
			t.Errorf("Accept = %q", got)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{
			"data":[{
				"id":"0b0c0d0e0f010203",
				"type":"threadDevice",
				"attributes":{
					"extAddress":"0b0c0d0e0f010203",
					"mlEidIid":"1d934f57e21e35",
					"omrIpv6Address":"fd00::1234",
					"hostname":"kitchen-sensor",
					"role":"child",
					"rloc16":"0xc801",
					"ipv6Addresses":["fd00::abcd"],
					"threadVersion":"1.3",
					"rssi":-67,
					"linkQuality":3,
					"updated":"2026-07-22T12:00:00Z"
				}
			}]
		}`))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())
	inventory, err := client.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices() error = %v", err)
	}
	if !inventory.CollectionSupported || inventory.Status != "available" || len(inventory.Items) != 1 {
		t.Fatalf("inventory = %+v", inventory)
	}
	device := inventory.Items[0]
	if device.Name != "kitchen-sensor" || device.Role != "child" || device.RSSI == nil || *device.RSSI != -67 {
		t.Fatalf("device = %+v", device)
	}
	if len(device.IPv6Addresses) != 2 || device.IPv6Addresses[1] != "fd00::1234" {
		t.Errorf("IPv6Addresses = %v", device.IPv6Addresses)
	}
	if device.LastSeen == nil || !device.LastSeen.Equal(time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("LastSeen = %v", device.LastSeen)
	}
}

func TestDecodeDevicesAcceptsJSONAPICollection(t *testing.T) {
	items, err := decodeDevices([]byte(`{
		"data":[{
			"id":"0011223344556677",
			"type":"threadDevice",
			"attributes":{"extAddress":"0011223344556677","hostname":"desk-sensor","role":"child"}
		}]
	}`))
	if err != nil {
		t.Fatalf("decodeDevices() error = %v", err)
	}
	if len(items) != 1 || items[0].Name != "desk-sensor" || items[0].Role != "child" {
		t.Fatalf("items = %+v", items)
	}
}

func TestDecodeDevicesKeepsCreatedSeparateFromLastSeen(t *testing.T) {
	items, err := decodeDevices([]byte(`{
		"data":[{
			"id":"0011223344556677",
			"attributes":{"extAddress":"0011223344556677","created":"2026-07-16T18:07:37-05:00"}
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].FirstSeen == nil {
		t.Fatalf("items = %+v", items)
	}
	if items[0].LastSeen != nil {
		t.Errorf("created timestamp must not be represented as last seen: %v", items[0].LastSeen)
	}
}

func TestDecodeDevicesAcceptsBareArray(t *testing.T) {
	items, err := decodeDevices([]byte(`[
		{
			"id":"8899aabbccddeeff",
			"extAddress":"8899aabbccddeeff",
			"hostname":"hall-sensor",
			"role":"child",
			"rloc16":"0x8401"
		},
		{
			"extAddress":"0011223344556677",
			"hostName":"garage-sensor",
			"role":"router"
		}
	]`))
	if err != nil {
		t.Fatalf("decodeDevices() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Name != "hall-sensor" || items[0].RLOC16 != "0x8401" {
		t.Errorf("first item = %+v", items[0])
	}
	if items[1].ID != "0011223344556677" || items[1].Name != "garage-sensor" {
		t.Errorf("second item = %+v", items[1])
	}
}

func TestDecodeTopologyBuildsRouterAndChildLinks(t *testing.T) {
	nodes, links, err := decodeTopology([]byte(`{
		"data":[
			{"id":"diag-a","attributes":{
				"extAddress":"aaaaaaaaaaaaaaaa","rloc16":"0x7000","routerId":28,
				"route":{"routeData":[{"routeId":37,"linkQualityIn":3,"linkQualityOut":3,"routeCost":1}]},
				"childTable":[{"childId":2,"timeout":12,"linkQuality":3,"mode":{"rxOnWhenIdle":false,"deviceTypeFTD":false,"fullNetworkData":false}}],
				"routerNeighbors":[{"extAddress":"bbbbbbbbbbbbbbbb","rloc16":"0x9400","linkMargin":42,"averageRssi":-58}],
				"created":"2026-07-22T12:00:00Z"
			}},
			{"id":"diag-b","attributes":{
				"extAddress":"bbbbbbbbbbbbbbbb","rloc16":"0x9400","routerId":37,
				"route":{"routeData":[{"routeId":28,"linkQualityIn":3,"linkQualityOut":3,"routeCost":1}]},
				"childTable":[{"childId":1,"timeout":12,"linkQuality":2}],
				"created":"2026-07-22T12:00:00Z"
			}}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 4 {
		t.Fatalf("nodes = %+v", nodes)
	}
	if len(links) != 3 {
		t.Fatalf("links = %+v", links)
	}
	if nodes[2].RLOC16 != "0x7002" || nodes[2].ParentID != "aaaaaaaaaaaaaaaa" || nodes[2].LinkQuality == nil || *nodes[2].LinkQuality != 3 {
		t.Errorf("first child = %+v", nodes[2])
	}
	if nodes[3].RLOC16 != "0x9401" || nodes[3].ParentID != "bbbbbbbbbbbbbbbb" {
		t.Errorf("second child = %+v", nodes[3])
	}
	if links[0].Type != "router" || links[0].LinkMargin == nil || *links[0].LinkMargin != 42 {
		t.Errorf("router link = %+v", links[0])
	}
}

func TestDevicesUnsupportedReturnsPartialFoundation(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())
	inventory, err := client.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices() error = %v", err)
	}
	if inventory.Status != "unsupported" || inventory.CollectionSupported || inventory.Error == "" {
		t.Fatalf("inventory = %+v", inventory)
	}
}

func TestDevicesRejectsMissingCollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":"unexpected response"}`))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())
	if _, err := client.Devices(context.Background()); err == nil {
		t.Fatal("Devices() expected missing collection error")
	}
}

func nodeServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Overview reconciles against the authoritative /node/state.
		if r.URL.Path == "/node/state" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`"router"`))
			return
		}
		// Overview reads the dataset only to fill blanks; these fixtures have none.
		if r.URL.Path == "/node/dataset/active" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path != "/api/node" && r.URL.Path != "/node" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestRefreshMeshPostsEveryRequiredAttribute(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotType   string
		gotBody   []byte
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotType = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":[{"id":"8c181f5e","type":"updateDeviceCollectionTask","attributes":{"status":"pending"}}]}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	if err := client.RefreshMesh(context.Background(), []string{"1a2b3c4d5e6f7a8b"}); err != nil {
		t.Fatalf("RefreshMesh() error = %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/actions" {
		t.Errorf("request = %s %s, want POST /api/actions", gotMethod, gotPath)
	}
	if gotType != "application/vnd.api+json" {
		t.Errorf("Content-Type = %q", gotType)
	}

	// OTBR rejects the action with 422 unless data is an array and every
	// attribute is present, defaults documented or not.
	var payload struct {
		Data []struct {
			Type       string `json:"type"`
			Attributes struct {
				MaxAge      *int            `json:"maxAge"`
				MaxRetries  *int            `json:"maxRetries"`
				DeviceCount *int            `json:"deviceCount"`
				Timeout     *int            `json:"timeout"`
				Destination string          `json:"destination"`
				Types       []string        `json:"types"`
				Extra       json.RawMessage `json:"destinationType"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	// One collection task plus one diagnostic per router, in a single array.
	if len(payload.Data) != 2 {
		t.Fatalf("data = %d actions, want 2 (collection + one diagnostic)", len(payload.Data))
	}
	collection, diagnostic := payload.Data[0], payload.Data[1]
	if collection.Type != "updateDeviceCollectionTask" {
		t.Errorf("first action type = %q", collection.Type)
	}
	if collection.Attributes.MaxAge == nil || collection.Attributes.MaxRetries == nil ||
		collection.Attributes.DeviceCount == nil || collection.Attributes.Timeout == nil {
		t.Error("collection task must send maxAge, maxRetries, deviceCount and timeout; OTBR 422s without all four")
	}
	if diagnostic.Type != "getNetworkDiagnosticTask" {
		t.Errorf("second action type = %q", diagnostic.Type)
	}
	if diagnostic.Attributes.Destination != "1a2b3c4d5e6f7a8b" {
		t.Errorf("diagnostic destination = %q", diagnostic.Attributes.Destination)
	}
	// Without childTable the topology has no parent links and children float free.
	if !slices.Contains(diagnostic.Attributes.Types, "childTable") {
		t.Errorf("diagnostic types = %v, must include childTable", diagnostic.Attributes.Types)
	}
	// A short timeout makes OTBR abandon the task in state "stopped", writing nothing.
	if diagnostic.Attributes.Timeout == nil || *diagnostic.Attributes.Timeout < 30 {
		t.Errorf("diagnostic timeout = %v, want at least 30s", diagnostic.Attributes.Timeout)
	}
	for _, action := range payload.Data {
		if action.Attributes.Extra != nil {
			t.Error("destinationType must not be sent; it is not part of the request schema")
		}
	}
}

func TestRefreshMeshWithoutRoutersStillSweepsDevices(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	// Blank entries must not become malformed actions.
	if err := client.RefreshMesh(context.Background(), []string{"", "   "}); err != nil {
		t.Fatalf("RefreshMesh() error = %v", err)
	}
	var payload struct {
		Data []struct {
			Type string `json:"type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Data[0].Type != "updateDeviceCollectionTask" {
		t.Errorf("data = %+v, want only the collection task", payload.Data)
	}
}

func TestRefreshMeshTreatsMissingEndpointAsUnsupported(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusNotImplemented, http.StatusMethodNotAllowed} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		client, err := NewClient(server.URL, server.Client())
		if err != nil {
			t.Fatal(err)
		}
		err = client.RefreshMesh(context.Background(), []string{"1a2b3c4d5e6f7a8b"})
		var unsupported interface{ DiscoveryUnsupported() bool }
		if !errors.As(err, &unsupported) || !unsupported.DiscoveryUnsupported() {
			t.Errorf("status %d: error = %v, want a DiscoveryUnsupported error", status, err)
		}
		server.Close()
	}
}

func TestRefreshMeshSurfacesOtherFailures(t *testing.T) {
	// A 422 means the payload shape is wrong — a bug to surface, not a capability gap.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = client.RefreshMesh(context.Background(), []string{"1a2b3c4d5e6f7a8b"})
	if err == nil {
		t.Fatal("RefreshMesh() error = nil, want an error")
	}
	var unsupported interface{ DiscoveryUnsupported() bool }
	if errors.As(err, &unsupported) {
		t.Errorf("422 must not be reported as unsupported: %v", err)
	}
}

const childrenTLVResponse = `{"data":[{"id":"1a2b3c4d5e6f7a8b","type":"diagnostics","attributes":{
  "extAddress":"1a2b3c4d5e6f7a8b","rloc16":"0x7000","routerId":28,
  "childTable":[
    {"childId":1,"timeout":12,"linkQuality":3,"mode":{"rxOnWhenIdle":false,"deviceTypeFTD":false,"fullNetworkData":false}},
    {"childId":7,"timeout":12,"linkQuality":2,"mode":{"rxOnWhenIdle":false,"deviceTypeFTD":false,"fullNetworkData":false}}
  ],
  "children":[
    {"childId":1,"extAddress":"0203040506070809","rloc16":"0x7001","timeout":240,"rxOnWhenIdle":false,"deviceTypeFTD":false,"fullNetworkData":false,"linkMargin":36,"averageRssi":-64,"lastRssi":-64,"frameErrorRate":0.195},
    {"childId":7,"extAddress":"05060708090a0b0c","rloc16":"0x7007","timeout":240,"rxOnWhenIdle":false,"deviceTypeFTD":false,"fullNetworkData":false,"linkMargin":39,"averageRssi":-61,"lastRssi":-60,"frameErrorRate":0.299}
  ]}}]}`

func TestTopologyIdentifiesChildrenByExtendedAddress(t *testing.T) {
	nodes, links, err := decodeTopology([]byte(childrenTLVResponse))
	if err != nil {
		t.Fatal(err)
	}
	children := map[string]model.TopologyNode{}
	for _, node := range nodes {
		if node.Role == "child" {
			children[node.ID] = node
		}
	}
	if len(children) != 2 {
		t.Fatalf("child nodes = %d, want 2", len(children))
	}
	// The extended address is the whole point: it is what matches the device
	// inventory, and so what lets a user's name reach the map.
	first, ok := children["0203040506070809"]
	if !ok {
		t.Fatalf("child not keyed by extended address; got ids %v", keysOf(children))
	}
	if first.ExtendedAddress != "0203040506070809" {
		t.Errorf("ExtendedAddress = %q", first.ExtendedAddress)
	}
	if first.RLOC16 != "0x7001" {
		t.Errorf("RLOC16 = %q, want the reported 0x7001", first.RLOC16)
	}
	// linkQuality comes only from childTable, so the merge must keep it.
	if first.LinkQuality == nil || *first.LinkQuality != 3 {
		t.Errorf("LinkQuality = %v, want 3 merged in from childTable", first.LinkQuality)
	}
	// timeout is reported by both; the children TLV gives plain seconds.
	if first.Timeout == nil || *first.Timeout != 240 {
		t.Errorf("Timeout = %v, want 240 from the children TLV", first.Timeout)
	}

	var childLinks int
	for _, link := range links {
		if link.Type != "child" {
			continue
		}
		childLinks++
		if link.Target == "0203040506070809" {
			if link.AverageRSSI == nil || *link.AverageRSSI != -64 {
				t.Errorf("link AverageRSSI = %v, want -64", link.AverageRSSI)
			}
			if link.LinkMargin == nil || *link.LinkMargin != 36 {
				t.Errorf("link LinkMargin = %v, want 36", link.LinkMargin)
			}
		}
	}
	if childLinks != 2 {
		t.Errorf("child links = %d, want 2", childLinks)
	}
}

func TestTopologyFallsBackToDerivedIDsWithoutChildrenTLV(t *testing.T) {
	// Firmware that predates the children TLV still reports childTable only.
	body := `{"data":[{"id":"1a2b3c4d5e6f7a8b","type":"diagnostics","attributes":{
	  "extAddress":"1a2b3c4d5e6f7a8b","rloc16":"0x7000","routerId":28,
	  "childTable":[{"childId":1,"timeout":12,"linkQuality":3,"mode":{"rxOnWhenIdle":false}}]}}]}`
	nodes, _, err := decodeTopology([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if node.Role != "child" {
			continue
		}
		if node.ID != "1a2b3c4d5e6f7a8b/child/1" {
			t.Errorf("child ID = %q, want the derived form", node.ID)
		}
		if node.RLOC16 != "0x7001" {
			t.Errorf("RLOC16 = %q, want 0x7001 derived from the parent", node.RLOC16)
		}
		return
	}
	t.Fatal("no child node decoded")
}

func keysOf(m map[string]model.TopologyNode) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestOverviewFallsBackToActiveDatasetForIdentity(t *testing.T) {
	datasetReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/node", "/node":
			_, _ = w.Write([]byte(`{"data":{"id":"abc","attributes":{"extAddress":"abc","networkName":"Lab","state":"leader","extPanId":"1111222233334444"}}}`))
		case "/node/dataset/active":
			datasetReads++
			_, _ = w.Write([]byte(`{"networkName":"Lab","channel":15,"panId":41394,"extPanId":"deadbeefdeadbeef","meshLocalPrefix":"fd11:22::/64","networkKey":"00112233445566778899aabbccddeeff","pskc":"ffeeddccbbaa99887766554433221100"}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	// httptest never listens on 8081, so there is no legacy web service to ask.
	client, _ := NewClient(server.URL, server.Client())
	overview, err := client.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview() error = %v", err)
	}
	if datasetReads != 1 {
		t.Errorf("dataset reads = %d, want 1", datasetReads)
	}
	if overview.RCPChannel != "15" {
		t.Errorf("RCPChannel = %q, want 15", overview.RCPChannel)
	}
	if overview.PANID != "0xa1b2" {
		t.Errorf("PANID = %q, want 0xa1b2", overview.PANID)
	}
	if overview.MeshLocalPrefix != "fd11:22::/64" {
		t.Errorf("MeshLocalPrefix = %q", overview.MeshLocalPrefix)
	}
	// The node's own value wins over the dataset when it is present.
	if overview.ExtendedPANID != "1111222233334444" {
		t.Errorf("ExtendedPANID = %q, want the node's value", overview.ExtendedPANID)
	}
	// Nothing sensitive may leak onto the polled overview.
	encoded, _ := json.Marshal(overview)
	for _, secret := range []string{"00112233445566778899aabbccddeeff", "ffeeddccbbaa99887766554433221100"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("overview carries a credential: %s", encoded)
		}
	}
}

func TestScanNetworksReportsAMissingWebServiceAsUnavailable(t *testing.T) {
	// otbr-web is a separate service; when it is not running the user should get an
	// explanation, not a Go dial error with a raw IPv6 address in it.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := dead.URL
	dead.Close() // nothing is listening now

	client, err := NewClient(base, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	// NewClient only derives the legacy web URL for port 8081, so point it by hand.
	legacy, _ := url.Parse(base)
	client.legacyWebURL = legacy

	scan, err := client.ScanNetworks(context.Background())
	if err != nil {
		t.Fatalf("ScanNetworks() error = %v, want a populated result instead", err)
	}
	if scan.Status != "unavailable" {
		t.Errorf("Status = %q, want unavailable", scan.Status)
	}
	if !strings.Contains(scan.Error, "otbr-web") {
		t.Errorf("Error = %q, should name the service the user has to start", scan.Error)
	}
	if strings.Contains(scan.Error, "dial tcp") {
		t.Errorf("Error = %q, should not leak the raw transport error", scan.Error)
	}
}

func TestScanNetworksTreatsAMissingEndpointAsUnsupported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := url.Parse(server.URL)
	client.legacyWebURL = legacy

	scan, err := client.ScanNetworks(context.Background())
	if err != nil {
		t.Fatalf("ScanNetworks() error = %v", err)
	}
	if scan.Status != "unsupported" {
		t.Errorf("Status = %q, want unsupported", scan.Status)
	}
}

func TestStatusPropertiesStopBeingReportedWhenTheSourceGoesAway(t *testing.T) {
	// The cache used to keep serving the last good values forever, so a stale RCP
	// version stayed on screen after otbr-web was stopped.
	var serve atomic.Bool
	serve.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !serve.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"error":0,"result":{"RCP:Version":"SL-OPENTHREAD/test","WPAN service":"associated"}}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := url.Parse(server.URL)
	client.legacyWebURL = legacy

	properties, ok := client.optionalStatusProperties(context.Background())
	if !ok || properties.RCPVersion != "SL-OPENTHREAD/test" {
		t.Fatalf("first fetch = %+v, %v; want the properties", properties, ok)
	}

	serve.Store(false)
	client.propertiesLastAttempt = time.Time{} // skip the one-minute retry window
	if _, ok := client.optionalStatusProperties(context.Background()); ok {
		t.Error("properties still reported after the source stopped answering")
	}
	// And they must stay gone on the cached path, not reappear a poll later.
	if _, ok := client.optionalStatusProperties(context.Background()); ok {
		t.Error("cached path resurrected properties from a source that is gone")
	}
}

type stubScanner struct {
	available bool
	items     []model.AvailableNetwork
	err       error
	// passes, when set, is answered one pass per call in rotation.
	passes [][]model.AvailableNetwork
	calls  *int
}

func (s stubScanner) Available() bool { return s.available }
func (s stubScanner) ScanNetworks(context.Context) ([]model.AvailableNetwork, error) {
	if s.calls != nil {
		*s.calls++
	}
	if len(s.passes) > 0 {
		n := 0
		if s.calls != nil {
			n = *s.calls - 1
		}
		return s.passes[n%len(s.passes)], s.err
	}
	return s.items, s.err
}

func singlePass(t *testing.T) {
	t.Helper()
	passes, pause := networkScanPasses, networkScanPause
	networkScanPasses, networkScanPause = 1, 0
	t.Cleanup(func() { networkScanPasses, networkScanPause = passes, pause })
}

func TestScanNetworksMergesSeveralDiscoveryPasses(t *testing.T) {
	passes, pause := networkScanPasses, networkScanPause
	networkScanPasses, networkScanPause = 3, 0
	t.Cleanup(func() { networkScanPasses, networkScanPause = passes, pause })
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())
	ch14, ch25 := 14, 25
	own := model.AvailableNetwork{Name: "OpenThread-a1b2", ExtendedPANID: "1122334455667788", PANID: "0xA1B2", HardwareAddress: "0708090A0B0C0D0E", Channel: &ch25}
	nest := model.AvailableNetwork{Name: "NEST-PAN-5294", ExtendedPANID: "8899aabbccddeeff", PANID: "0x5294", HardwareAddress: "08090A0B0C0D0E0F", Channel: &ch14}
	other := model.AvailableNetwork{Name: "MyHome", ExtendedPANID: "0011223344556677", PANID: "0x1234", HardwareAddress: "090A0B0C0D0E0F10", Channel: &ch25}
	calls := 0
	// Neighbours answer intermittently: each pass hears a different subset.
	client.SetNetworkScanner(stubScanner{available: true, calls: &calls, passes: [][]model.AvailableNetwork{
		{own, nest}, {own}, {other, own},
	}})
	scan, err := client.ScanNetworks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || scan.Passes != 3 {
		t.Fatalf("passes = %d (calls %d), want 3", scan.Passes, calls)
	}
	if len(scan.Items) != 3 {
		t.Fatalf("items = %+v, want the union of the three passes", scan.Items)
	}
	if scan.Items[0].Name != "MyHome" || scan.Items[1].Name != "NEST-PAN-5294" || scan.Items[2].Name != "OpenThread-a1b2" {
		t.Fatalf("items not deduplicated and sorted by name: %+v", scan.Items)
	}
}

func TestScanNetworksPrefersTheDaemonSocketOverOtbrWeb(t *testing.T) {
	singlePass(t)
	var legacyCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		legacyCalled = true
		_, _ = w.Write([]byte(`{"error":0,"result":[]}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := url.Parse(server.URL)
	client.legacyWebURL = legacy

	channel := 25
	client.SetNetworkScanner(stubScanner{available: true, items: []model.AvailableNetwork{
		{Name: "OpenThread-a1b2", PANID: "0xA1B2", Channel: &channel},
	}})
	scan, err := client.ScanNetworks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if scan.Status != "available" || len(scan.Items) != 1 || scan.Items[0].Name != "OpenThread-a1b2" {
		t.Errorf("scan = %+v, want the socket's result", scan)
	}
	if scan.Source != "OpenThread daemon scan" {
		t.Errorf("Source = %q, should say where the data came from", scan.Source)
	}
	if legacyCalled {
		t.Error("otbr-web was called even though the socket was available")
	}
}

func TestScanNetworksFallsBackToOtbrWebWhenTheSocketIsAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":0,"result":[{"nn":"Legacy","ch":15,"pi":"0x5294","ha":"AABBCCDDEEFF0011"}]}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := url.Parse(server.URL)
	client.legacyWebURL = legacy
	client.SetNetworkScanner(stubScanner{available: false})

	scan, err := client.ScanNetworks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if scan.Status != "available" || len(scan.Items) != 1 || scan.Items[0].Name != "Legacy" {
		t.Errorf("scan = %+v, want the otbr-web result", scan)
	}
	if scan.Source != "OTBR active scan" {
		t.Errorf("Source = %q", scan.Source)
	}
}

func TestScanNetworksReportsASocketFailureWithoutFallingBack(t *testing.T) {
	// A socket that is present but failing is a real fault; silently using otbr-web
	// would hide it and make the two sources indistinguishable in the UI.
	client, err := NewClient("http://127.0.0.1:8081", &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	client.SetNetworkScanner(stubScanner{available: true, err: errors.New("Error 6: Parse")})
	scan, err := client.ScanNetworks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if scan.Status != "unavailable" || !strings.Contains(scan.Error, "Error 6") {
		t.Errorf("scan = %+v, want the daemon error surfaced", scan)
	}
}

type unreachableSocket struct{}

func (unreachableSocket) Error() string           { return "connect: permission denied" }
func (unreachableSocket) SocketUnavailable() bool { return true }

func TestScanNetworksFallsBackWhenTheSocketCannotBeOpened(t *testing.T) {
	// The socket is mode 0755 root:root, so a non-root process can stat it but not
	// connect. That must not shadow a working otbr-web.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":0,"result":[{"nn":"ViaWeb","ch":25,"pi":"0xA1B2","ha":"AABBCCDDEEFF0011"}]}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := url.Parse(server.URL)
	client.legacyWebURL = legacy
	// Available() is a stat, so it says yes even though connecting fails.
	client.SetNetworkScanner(stubScanner{available: true, err: unreachableSocket{}})

	scan, err := client.ScanNetworks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if scan.Status != "available" || len(scan.Items) != 1 || scan.Items[0].Name != "ViaWeb" {
		t.Errorf("scan = %+v, want the otbr-web fallback", scan)
	}
	if scan.Source != "OTBR active scan" {
		t.Errorf("Source = %q, should show the fallback was used", scan.Source)
	}
}

type stubMesh struct {
	available bool
	calls     int
	err       error
}

func (s *stubMesh) Available() bool { return s.available }
func (s *stubMesh) Mesh(context.Context) (*model.DeviceInventory, *model.Topology, error) {
	s.calls++
	if s.err != nil {
		return nil, nil, s.err
	}
	return &model.DeviceInventory{Status: "available", Source: "live", Items: []model.Device{{ID: "live-device"}}},
		&model.Topology{Status: "available", Source: "live", Nodes: []model.TopologyNode{{ID: "live-node"}}}, nil
}

func TestLiveMeshIsPreferredAndReadOncePerPoll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	mesh := &stubMesh{available: true}
	client.SetMeshReader(mesh)

	devices, err := client.Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	topology, err := client.Topology(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if devices.Source != "live" || topology.Source != "live" {
		t.Errorf("sources = %q/%q, want the live reader", devices.Source, topology.Source)
	}
	// Devices and Topology always arrive together; querying every router twice per
	// poll would double the over-the-air cost for nothing.
	if mesh.calls != 1 {
		t.Errorf("Mesh() called %d times for one poll, want 1", mesh.calls)
	}
}

func TestRestIsUsedWhenTheLiveReaderFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"rest","type":"device","attributes":{"extAddress":"aabb","role":"router"}}]}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.SetMeshReader(&stubMesh{available: true, err: errors.New("meshdiag timed out")})

	devices, err := client.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices() error = %v, want the REST fallback", err)
	}
	if devices.Source == "live" || len(devices.Items) != 1 {
		t.Errorf("devices = %+v, want the REST collection", devices)
	}
}

func TestDiscoverySweepsAreSkippedWhenLiveMeshIsInUse(t *testing.T) {
	var posted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posted = true
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.SetMeshReader(&stubMesh{available: true})
	if err := client.RefreshMesh(context.Background(), []string{"1a2b3c4d5e6f7a8b"}); err != nil {
		t.Fatal(err)
	}
	if posted {
		t.Error("posted a discovery action while live mesh data was available")
	}

	// Without a live reader the sweep must still run, or the REST fallback goes stale.
	client.SetMeshReader(nil)
	if err := client.RefreshMesh(context.Background(), []string{"1a2b3c4d5e6f7a8b"}); err != nil {
		t.Fatal(err)
	}
	if !posted {
		t.Error("no discovery action posted when the live reader was absent")
	}
}
