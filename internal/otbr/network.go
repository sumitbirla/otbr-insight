package otbr

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// writeTimeout bounds dataset writes, which include bringing the Thread
// interface down and back up and can take several seconds.
const writeTimeout = 25 * time.Second

// ValidationError marks a request rejected before anything was sent to OTBR:
// the caller's input was wrong, not the border router. The API layer uses the
// distinction to answer 400 rather than 502.
type ValidationError string

func (e ValidationError) Error() string { return string(e) }

// FormRequest describes a brand-new network. Missing credential fields are
// generated locally so a complete, valid operational dataset is committed.
type FormRequest struct {
	NetworkName string
	Channel     *int
	PANID       *int
}

// JoinRequest describes an existing network to attach to using its credentials.
// PSKc and MeshLocalPrefix are optional: the leader's dataset supersedes ours on
// attach anyway, but supplying them makes this border router a faithful copy
// from the start — a matching PSKc is what lets it commission for the network.
type JoinRequest struct {
	NetworkName     string
	NetworkKey      string
	Channel         *int
	PANID           *int
	ExtPANID        string
	PSKc            string
	MeshLocalPrefix string
}

// NetworkConfig returns the interface state plus the credential-masked dataset.
func (c *Client) NetworkConfig(ctx context.Context) (*model.NetworkConfig, error) {
	state, err := c.NetworkState(ctx)
	if err != nil {
		return nil, err
	}
	dataset, err := c.ActiveDataset(ctx)
	if err != nil {
		return nil, err
	}
	return &model.NetworkConfig{State: state, Dataset: *dataset}, nil
}

// Credentials carries the sensitive dataset fields. It is returned only from
// the explicit on-demand reveal endpoint, never the polled read path.
type Credentials struct {
	Present     bool   `json:"present"`
	NetworkName string `json:"networkName,omitempty"`
	NetworkKey  string `json:"networkKey,omitempty"`
	PSKc        string `json:"pskc,omitempty"`
	ExtPANID    string `json:"extPanId,omitempty"`
	TLV         string `json:"tlv,omitempty"`
}

// ActiveDatasetTLV reads the active dataset as TLV hex — the compact interchange
// format other border routers (Home Assistant, Apple, Google) import.
func (c *Client) ActiveDatasetTLV(ctx context.Context) (string, error) {
	body, status, err := c.getWithAccept(ctx, "/node/dataset/active", "text/plain")
	if err != nil {
		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusNoContent || statusErr.StatusCode == http.StatusNotFound) {
			return "", nil
		}
		return "", err
	}
	if status == http.StatusNoContent {
		return "", nil
	}
	return strings.Trim(strings.TrimSpace(string(body)), `"`), nil
}

func validDatasetTLV(tlv string) (string, error) {
	clean := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			return -1
		}
		return r
	}, strings.TrimSpace(tlv))
	clean = strings.TrimPrefix(strings.ToLower(clean), "0x")
	if clean == "" {
		return "", ValidationError("dataset TLV is required")
	}
	if len(clean) > 1024 {
		return "", ValidationError("dataset TLV is too long")
	}
	if _, err := hex.DecodeString(clean); err != nil {
		return "", ValidationError("dataset TLV must be hexadecimal")
	}
	return clean, nil
}

// DatasetCredentials reads the active dataset WITHOUT masking. Callers must not
// log the result or place it on any continuously polled response.
func (c *Client) DatasetCredentials(ctx context.Context) (*Credentials, error) {
	body, status, err := c.get(ctx, "/node/dataset/active")
	if err != nil {
		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusNoContent || statusErr.StatusCode == http.StatusNotFound) {
			return &Credentials{Present: false}, nil
		}
		return nil, err
	}
	if status == http.StatusNoContent || len(bytes.TrimSpace(body)) == 0 {
		return &Credentials{Present: false}, nil
	}
	var raw struct {
		NetworkName string `json:"networkName"`
		NetworkKey  string `json:"networkKey"`
		PSKc        string `json:"pskc"`
		ExtPANID    string `json:"extPanId"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode active dataset: %w", err)
	}
	creds := &Credentials{
		Present:     true,
		NetworkName: raw.NetworkName,
		NetworkKey:  raw.NetworkKey,
		PSKc:        raw.PSKc,
		ExtPANID:    raw.ExtPANID,
	}
	// Best-effort: include the TLV form so the reveal panel can offer it for
	// export to other border routers.
	creds.TLV, _ = c.ActiveDatasetTLV(ctx)
	return creds, nil
}

// NetworkState returns the Thread interface role (disabled/detached/child/...).
func (c *Client) NetworkState(ctx context.Context) (string, error) {
	body, _, err := c.get(ctx, "/node/state")
	if err != nil {
		return "", err
	}
	var state string
	if err := json.Unmarshal(bytes.TrimSpace(body), &state); err != nil {
		// Some builds return a bare token rather than a JSON string.
		return strings.Trim(strings.TrimSpace(string(body)), `"`), nil
	}
	return state, nil
}

// ActiveDataset reads the active operational dataset with the network key and
// PSKc stripped; only their presence is reported.
func (c *Client) ActiveDataset(ctx context.Context) (*model.Dataset, error) {
	body, status, err := c.get(ctx, "/node/dataset/active")
	if err != nil {
		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusNoContent || statusErr.StatusCode == http.StatusNotFound) {
			return &model.Dataset{Present: false}, nil
		}
		return nil, err
	}
	if status == http.StatusNoContent || len(bytes.TrimSpace(body)) == 0 {
		return &model.Dataset{Present: false}, nil
	}
	var raw struct {
		NetworkName     string `json:"networkName"`
		NetworkKey      string `json:"networkKey"`
		PSKc            string `json:"pskc"`
		ExtPANID        string `json:"extPanId"`
		MeshLocalPrefix string `json:"meshLocalPrefix"`
		PANID           *int   `json:"panId"`
		Channel         *int   `json:"channel"`
		ActiveTimestamp *struct {
			Seconds int64 `json:"seconds"`
		} `json:"activeTimestamp"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode active dataset: %w", err)
	}
	dataset := &model.Dataset{
		Present:         true,
		NetworkName:     raw.NetworkName,
		Channel:         raw.Channel,
		ExtPANID:        raw.ExtPANID,
		MeshLocalPrefix: raw.MeshLocalPrefix,
		HasNetworkKey:   raw.NetworkKey != "",
		HasPSKc:         raw.PSKc != "",
	}
	if raw.PANID != nil {
		dataset.PANID = fmt.Sprintf("0x%04x", *raw.PANID)
	}
	if raw.ActiveTimestamp != nil {
		seconds := raw.ActiveTimestamp.Seconds
		dataset.ActiveTimestamp = &seconds
	}
	return dataset, nil
}

// SetEnabled brings the Thread interface up or down. The OTBR REST API expects
// PUT /node/state with an application/json body that is the JSON string
// "enable" or "disable" (quotes included); other method/content-type/body
// combinations are rejected with 405/404.
func (c *Client) SetEnabled(ctx context.Context, enable bool) error {
	c.controlMu.Lock()
	defer c.controlMu.Unlock()
	return c.setState(ctx, enable)
}

// setState is SetEnabled without the lock, for callers that already hold it.
func (c *Client) setState(ctx context.Context, enable bool) error {
	value := "disable"
	if enable {
		value = "enable"
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = c.write(ctx, http.MethodPut, "/node/state", "application/json", body)
	return err
}

// FormNetwork commits a freshly generated dataset and enables the interface.
func (c *Client) FormNetwork(ctx context.Context, req FormRequest) error {
	name, err := validNetworkName(req.NetworkName)
	if err != nil {
		return err
	}
	channel, err := resolveChannel(req.Channel)
	if err != nil {
		return err
	}
	panID, err := resolvePANID(req.PANID)
	if err != nil {
		return err
	}
	dataset := map[string]any{
		"activeTimestamp": map[string]any{"seconds": 1, "ticks": 0, "authoritative": false},
		"networkName":     name,
		"channel":         channel,
		"panId":           panID,
		"extPanId":        randomHex(8),
		"networkKey":      randomHex(16),
		"pskc":            randomHex(16),
		"meshLocalPrefix": randomMeshLocalPrefix(),
	}
	return c.applyDataset(ctx, dataset)
}

// JoinNetwork commits a dataset built from supplied credentials and enables.
func (c *Client) JoinNetwork(ctx context.Context, req JoinRequest) error {
	name, err := validNetworkName(req.NetworkName)
	if err != nil {
		return err
	}
	networkKey, err := validHex(req.NetworkKey, 16, "network key")
	if err != nil {
		return err
	}
	channel, err := resolveChannel(req.Channel)
	if err != nil {
		return err
	}
	panID, err := resolvePANID(req.PANID)
	if err != nil {
		return err
	}
	extPANID := strings.TrimSpace(req.ExtPANID)
	if extPANID != "" {
		if extPANID, err = validHex(extPANID, 8, "extended PAN ID"); err != nil {
			return err
		}
	} else {
		extPANID = randomHex(8)
	}
	meshLocalPrefix := strings.TrimSpace(req.MeshLocalPrefix)
	if meshLocalPrefix != "" {
		if meshLocalPrefix, err = validMeshLocalPrefix(meshLocalPrefix); err != nil {
			return err
		}
	} else {
		meshLocalPrefix = randomMeshLocalPrefix()
	}
	dataset := map[string]any{
		"activeTimestamp": map[string]any{"seconds": 1, "ticks": 0, "authoritative": false},
		"networkName":     name,
		"channel":         channel,
		"panId":           panID,
		"extPanId":        extPANID,
		"networkKey":      networkKey,
		"meshLocalPrefix": meshLocalPrefix,
	}
	if pskc := strings.TrimSpace(req.PSKc); pskc != "" {
		if dataset["pskc"], err = validHex(pskc, 16, "PSKc"); err != nil {
			return err
		}
	}
	return c.applyDataset(ctx, dataset)
}

// LeaveNetwork disables the interface and clears the active dataset. It is the
// REST-only stand-in for a factory reset (which requires ot-ctl).
func (c *Client) LeaveNetwork(ctx context.Context) error {
	c.controlMu.Lock()
	defer c.controlMu.Unlock()
	if err := c.setState(ctx, false); err != nil {
		return err
	}
	if _, err := c.write(ctx, http.MethodDelete, "/node/dataset/active", "", nil); err != nil {
		return err
	}
	return nil
}

// JoinNetworkTLV commits a full operational dataset supplied as TLV hex (the
// interchange format used by Home Assistant, Apple, and Google border routers)
// and enables the interface.
func (c *Client) JoinNetworkTLV(ctx context.Context, tlv string) error {
	clean, err := validDatasetTLV(tlv)
	if err != nil {
		return err
	}
	return c.applyDatasetRaw(ctx, "text/plain", []byte(clean))
}

// applyDataset commits a dataset expressed as structured fields.
func (c *Client) applyDataset(ctx context.Context, dataset map[string]any) error {
	payload, err := json.Marshal(dataset)
	if err != nil {
		return err
	}
	return c.applyDatasetRaw(ctx, "application/json", payload)
}

// applyDatasetRaw disables Thread, commits the active dataset, and re-enables
// it. The active dataset cannot be replaced while the interface is up.
//
// NOTE: OTBR's REST API advertises a misleading `Allow: GET, POST, DELETE`
// header for /node/state and /node/dataset/active that omits PUT, yet PUT is
// the correct method for both (confirmed live against /node/state). Do not
// "fix" these to POST based on the Allow header.
func (c *Client) applyDatasetRaw(ctx context.Context, contentType string, body []byte) error {
	c.controlMu.Lock()
	defer c.controlMu.Unlock()
	if err := c.setState(ctx, false); err != nil {
		return fmt.Errorf("disable interface: %w", err)
	}
	if _, err := c.write(ctx, http.MethodPut, "/node/dataset/active", contentType, body); err != nil {
		return fmt.Errorf("set active dataset: %w", err)
	}
	if err := c.setState(ctx, true); err != nil {
		return fmt.Errorf("enable interface: %w", err)
	}
	return nil
}

// write performs a mutating request against the OTBR REST API.
func (c *Client) write(ctx context.Context, method, endpoint, contentType string, body []byte) (int, error) {
	u := *c.baseURL
	u.Path = strings.TrimRight(c.baseURL.Path, "/") + endpoint
	u.RawQuery = ""
	u.Fragment = ""
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	writeClient := *c.httpClient
	writeClient.Timeout = writeTimeout
	resp, err := writeClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request OTBR endpoint %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, &HTTPStatusError{Endpoint: endpoint, StatusCode: resp.StatusCode}
	}
	return resp.StatusCode, nil
}

func validNetworkName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", ValidationError("network name is required")
	}
	if len(trimmed) > 16 {
		return "", ValidationError("network name must be at most 16 bytes")
	}
	return trimmed, nil
}

func validHex(value string, wantBytes int, label string) (string, error) {
	clean := strings.ToLower(strings.TrimSpace(value))
	clean = strings.TrimPrefix(clean, "0x")
	decoded, err := hex.DecodeString(clean)
	if err != nil || len(decoded) != wantBytes {
		return "", ValidationError(fmt.Sprintf("%s must be %d hex characters", label, wantBytes*2))
	}
	return clean, nil
}

// validMeshLocalPrefix accepts a Thread mesh-local prefix: a /64 inside the
// unique-local fd00::/8 range, returned in canonical form.
func validMeshLocalPrefix(value string) (string, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err != nil || !prefix.Addr().Is6() || prefix.Addr().Is4In6() {
		return "", ValidationError("mesh-local prefix must be an IPv6 prefix such as fd11:22::/64")
	}
	if prefix.Bits() != 64 {
		return "", ValidationError("mesh-local prefix must be a /64")
	}
	if prefix.Addr().As16()[0] != 0xfd {
		return "", ValidationError("mesh-local prefix must be inside fd00::/8")
	}
	return prefix.Masked().String(), nil
}

func resolveChannel(channel *int) (int, error) {
	if channel == nil {
		return 15, nil
	}
	if *channel < 11 || *channel > 26 {
		return 0, ValidationError("channel must be between 11 and 26")
	}
	return *channel, nil
}

func resolvePANID(panID *int) (int, error) {
	if panID == nil {
		return randomPANID(), nil
	}
	if *panID < 0 || *panID > 0xfffe {
		return 0, ValidationError("PAN ID must be between 0x0000 and 0xfffe")
	}
	return *panID, nil
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func randomPANID() int {
	buf := make([]byte, 2)
	_, _ = rand.Read(buf)
	value := int(buf[0])<<8 | int(buf[1])
	if value >= 0xfffe {
		value = 0xfffd
	}
	return value
}

// randomMeshLocalPrefix returns a random fd00::/8 unique-local /64 prefix.
func randomMeshLocalPrefix() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	buf[0] = 0xfd
	return fmt.Sprintf("%02x%02x:%02x%02x:%02x%02x:%02x%02x::/64",
		buf[0], buf[1], buf[2], buf[3], buf[4], buf[5], buf[6], buf[7])
}
