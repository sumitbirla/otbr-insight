package otbr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const activeDatasetJSON = `{
	"activeTimestamp": {"seconds": 1, "ticks": 0, "authoritative": false},
	"networkKey": "00112233445566778899aabbccddeeff",
	"networkName": "OpenThread-a1b2",
	"extPanId": "1122334455667788",
	"meshLocalPrefix": "fdde:ad00:beef:9004::/64",
	"panId": 41394,
	"channel": 25,
	"pskc": "ffeeddccbbaa99887766554433221100"
}`

func TestActiveDatasetMasksCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, activeDatasetJSON)
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())

	dataset, err := client.ActiveDataset(context.Background())
	if err != nil {
		t.Fatalf("ActiveDataset() error = %v", err)
	}
	if !dataset.Present || dataset.NetworkName != "OpenThread-a1b2" {
		t.Fatalf("dataset = %+v", dataset)
	}
	if dataset.PANID != "0xa1b2" || dataset.Channel == nil || *dataset.Channel != 25 {
		t.Errorf("panId/channel = %q / %v", dataset.PANID, dataset.Channel)
	}
	if !dataset.HasNetworkKey || !dataset.HasPSKc {
		t.Error("expected HasNetworkKey and HasPSKc to be true")
	}
	// The masked view must never carry the raw secrets.
	blob, _ := json.Marshal(dataset)
	if body := string(blob); indexOf(body, "00112233") >= 0 || indexOf(body, "ffeeddcc") >= 0 {
		t.Errorf("masked dataset leaked a credential: %s", body)
	}
}

func TestActiveDatasetNoContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())
	dataset, err := client.ActiveDataset(context.Background())
	if err != nil {
		t.Fatalf("ActiveDataset() error = %v", err)
	}
	if dataset.Present {
		t.Error("expected Present=false for 204 response")
	}
}

func TestFormNetworkSequenceAndBody(t *testing.T) {
	var mu sync.Mutex
	var states []string
	var datasetBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/node/state":
			body, _ := io.ReadAll(r.Body)
			var value string
			_ = json.Unmarshal(body, &value) // body is a JSON string: "enable"/"disable"
			states = append(states, value)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && r.URL.Path == "/node/dataset/active":
			_ = json.NewDecoder(r.Body).Decode(&datasetBody)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())

	channel := 20
	if err := client.FormNetwork(context.Background(), FormRequest{NetworkName: "Home Lab", Channel: &channel}); err != nil {
		t.Fatalf("FormNetwork() error = %v", err)
	}
	if len(states) != 2 || states[0] != "disable" || states[1] != "enable" {
		t.Fatalf("state sequence = %v, want [disable enable]", states)
	}
	if datasetBody["networkName"] != "Home Lab" {
		t.Errorf("networkName = %v", datasetBody["networkName"])
	}
	if datasetBody["channel"].(float64) != 20 {
		t.Errorf("channel = %v", datasetBody["channel"])
	}
	for _, field := range []string{"networkKey", "extPanId", "pskc", "meshLocalPrefix", "panId"} {
		if _, ok := datasetBody[field]; !ok {
			t.Errorf("form dataset missing generated field %q", field)
		}
	}
}

func TestJoinNetworkValidatesKey(t *testing.T) {
	client, _ := NewClient("http://127.0.0.1:0", nil)
	err := client.JoinNetwork(context.Background(), JoinRequest{NetworkName: "Net", NetworkKey: "tooshort"})
	if err == nil {
		t.Fatal("expected error for invalid network key")
	}
}

func TestFormNetworkRejectsBadInput(t *testing.T) {
	client, _ := NewClient("http://127.0.0.1:0", nil)
	channel := 99
	if err := client.FormNetwork(context.Background(), FormRequest{NetworkName: "Net", Channel: &channel}); err == nil {
		t.Error("expected error for out-of-range channel")
	}
	if err := client.FormNetwork(context.Background(), FormRequest{NetworkName: ""}); err == nil {
		t.Error("expected error for empty network name")
	}
}

// indexOf is a tiny helper so the leak assertion reads clearly.
func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestValidationFailuresAreTyped(t *testing.T) {
	client, _ := NewClient("http://127.0.0.1:1", nil)
	channel := 3
	cases := map[string]error{
		"empty name":  client.FormNetwork(context.Background(), FormRequest{NetworkName: ""}),
		"bad channel": client.FormNetwork(context.Background(), FormRequest{NetworkName: "Net", Channel: &channel}),
		"short key":   client.JoinNetwork(context.Background(), JoinRequest{NetworkName: "Net", NetworkKey: "tooshort"}),
		"bad tlv":     client.JoinNetworkTLV(context.Background(), "zz"),
	}
	for name, err := range cases {
		var validation ValidationError
		if !errors.As(err, &validation) {
			t.Errorf("%s: error %v is not a ValidationError", name, err)
		}
	}
	// A transport failure must not be mistaken for bad input.
	err := client.SetEnabled(context.Background(), true)
	var validation ValidationError
	if err == nil || errors.As(err, &validation) {
		t.Fatalf("SetEnabled against a closed port: error = %v, want a non-validation error", err)
	}
}

func TestJoinNetworkCarriesOptionalPSKcAndPrefix(t *testing.T) {
	var mu sync.Mutex
	var datasetBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPut && r.URL.Path == "/node/dataset/active" {
			_ = json.NewDecoder(r.Body).Decode(&datasetBody)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())

	channel := 15
	err := client.JoinNetwork(context.Background(), JoinRequest{
		NetworkName: "Net", NetworkKey: "00112233445566778899aabbccddeeff", Channel: &channel,
		PSKc: "0xFFEEDDCCBBAA99887766554433221100", MeshLocalPrefix: "FD11:22:0:0::1/64",
	})
	if err != nil {
		t.Fatalf("JoinNetwork() error = %v", err)
	}
	if datasetBody["pskc"] != "ffeeddccbbaa99887766554433221100" {
		t.Errorf("pskc = %v, want the normalized supplied value", datasetBody["pskc"])
	}
	if datasetBody["meshLocalPrefix"] != "fd11:22::/64" {
		t.Errorf("meshLocalPrefix = %v, want fd11:22::/64", datasetBody["meshLocalPrefix"])
	}

	// Omitted: no PSKc is invented, the prefix is generated.
	datasetBody = nil
	if err := client.JoinNetwork(context.Background(), JoinRequest{NetworkName: "Net", NetworkKey: "00112233445566778899aabbccddeeff"}); err != nil {
		t.Fatalf("JoinNetwork() without options error = %v", err)
	}
	if _, ok := datasetBody["pskc"]; ok {
		t.Errorf("pskc = %v, want none when not supplied", datasetBody["pskc"])
	}
	if prefix, _ := datasetBody["meshLocalPrefix"].(string); !strings.HasPrefix(prefix, "fd") || !strings.HasSuffix(prefix, "::/64") {
		t.Errorf("meshLocalPrefix = %v, want a generated fd../64", datasetBody["meshLocalPrefix"])
	}

	for name, req := range map[string]JoinRequest{
		"short pskc":     {NetworkName: "Net", NetworkKey: "00112233445566778899aabbccddeeff", PSKc: "abcd"},
		"prefix not /64": {NetworkName: "Net", NetworkKey: "00112233445566778899aabbccddeeff", MeshLocalPrefix: "fd11:22::/48"},
		"prefix not ULA": {NetworkName: "Net", NetworkKey: "00112233445566778899aabbccddeeff", MeshLocalPrefix: "2001:db8::/64"},
		"prefix is IPv4": {NetworkName: "Net", NetworkKey: "00112233445566778899aabbccddeeff", MeshLocalPrefix: "10.0.0.0/8"},
	} {
		var validation ValidationError
		if err := client.JoinNetwork(context.Background(), req); !errors.As(err, &validation) {
			t.Errorf("%s: error = %v, want ValidationError", name, err)
		}
	}
}

// Two dataset changes at once must run back to back, never interleaved: each
// disable / PUT dataset / enable triple has to complete before the next starts.
func TestControlWritesAreSerialized(t *testing.T) {
	var mu sync.Mutex
	var sequence []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step := r.URL.Path
		if r.URL.Path == "/node/state" {
			body, _ := io.ReadAll(r.Body)
			step = strings.Trim(string(body), `"`)
		}
		mu.Lock()
		sequence = append(sequence, step)
		mu.Unlock()
		time.Sleep(5 * time.Millisecond) // give an unserialized caller room to interleave
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())

	const writers = 6
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			switch i % 3 {
			case 0:
				err = client.FormNetwork(context.Background(), FormRequest{NetworkName: "A"})
			case 1:
				err = client.JoinNetworkTLV(context.Background(), "0e080000000000010000")
			default:
				err = client.LeaveNetwork(context.Background())
			}
			if err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	// Walk the recorded requests: after a "disable" the same operation's remaining
	// steps must follow immediately.
	for i := 0; i < len(sequence); {
		if sequence[i] != "disable" {
			t.Fatalf("request %d = %q, want an operation to start with disable (sequence %v)", i, sequence[i], sequence)
		}
		if i+1 < len(sequence) && sequence[i+1] == "/node/dataset/active" {
			if i+2 < len(sequence) && sequence[i+2] == "enable" {
				i += 3 // form / join: disable, PUT dataset, enable
			} else {
				i += 2 // leave: disable, DELETE dataset
			}
			continue
		}
		t.Fatalf("interleaved control writes: %v", sequence)
	}
}
