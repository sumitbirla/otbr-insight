package otbr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// EnergyScanner is an optional source for channel energy scans that bypasses the
// REST API — the daemon socket answers synchronously with a table, where REST
// needs an action posted and its result polled.
type EnergyScanner interface {
	Available() bool
	EnergyScan(ctx context.Context) ([]model.ChannelEnergy, error)
}

// SetEnergyScanner installs a scanner preferred over the REST action. Call before serving.
func (c *Client) SetEnergyScanner(scanner EnergyScanner) { c.energy = scanner }

// Channels 11–26 are the 2.4 GHz IEEE 802.15.4 channels Thread can use.
var allChannels = func() []int {
	channels := make([]int, 0, 16)
	for ch := 11; ch <= 26; ch++ {
		channels = append(channels, ch)
	}
	return channels
}()

// REST energy scan parameters, verified against a Silicon Labs OTBR build: the
// action needs destination as an extended address and channelMask as a JSON
// array of channels (an integer mask is answered with 422 and an empty detail),
// and count/period/scanDuration are all required. 100 ms per channel is the
// dwell that has been exercised safely; see otctl.EnergyScan for why longer
// dwells are not offered.
const (
	energyScanCount      = 1
	energyScanPeriodMs   = 100
	energyScanDwellMs    = 100
	energyScanTimeoutSec = 30
	energyScanPollEvery  = time.Second
	energyScanWait       = 45 * time.Second
)

// A single sweep is a snapshot: Wi-Fi is bursty and a ten-millisecond listen
// catches a neighbour's access point only sometimes. Rather than lengthen the
// dwell — which hung the RCP — the safe sweep is repeated for a while with
// pauses that let the radio serve its own network in between, and the loudest
// and median readings per channel are kept. Variables so tests can shorten them.
var (
	energyScanRun       = 10 * time.Second
	energyScanPause     = 300 * time.Millisecond
	energyScanMaxSweeps = 40
	// Each REST sweep is an action posted and polled (~6 s), so far fewer fit.
	energyScanRESTSweeps = 3
)

// EnergyScan measures the peak RSSI on every channel, preferring the daemon socket
// and falling back to the REST getEnergyScanTask action. It never returns an
// error for a scan that could not run: the reason travels in the result so the
// UI and the MCP tool can show it.
func (c *Client) EnergyScan(ctx context.Context) (*model.EnergyScan, error) {
	started := time.Now()
	result := func(status, source, errText string, channels []model.ChannelEnergy) *model.EnergyScan {
		if channels == nil {
			channels = []model.ChannelEnergy{}
		}
		return &model.EnergyScan{
			Status: status, Source: source, ScannedAt: time.Now().UTC(), DurationMs: time.Since(started).Milliseconds(),
			Channels: channels, Error: errText,
		}
	}
	// The overview supplies the current channel for the result and, for the REST
	// path, the border router's own extended address as the action destination.
	var currentChannel *int
	var ownAddress string
	if overview, err := c.Overview(ctx); err == nil && overview != nil {
		ownAddress = overview.ExtendedAddress
		if ch, err := strconv.Atoi(strings.TrimSpace(overview.RCPChannel)); err == nil {
			currentChannel = &ch
		}
	}
	finish := func(scan *model.EnergyScan) *model.EnergyScan {
		scan.CurrentChannel = currentChannel
		return scan
	}

	if c.energy != nil && c.energy.Available() {
		combined, sweeps, err := repeatSweeps(ctx, energyScanRun, energyScanPause, energyScanMaxSweeps, c.energy.EnergyScan)
		var unusable interface{ SocketUnavailable() bool }
		switch {
		case err == nil:
			scan := result("available", "OpenThread daemon energy scan", "", combined)
			scan.Sweeps = sweeps
			return finish(scan), nil
		case errors.As(err, &unusable) && unusable.SocketUnavailable():
			// Present but unopenable (non-root); nothing was reached, so try REST.
		default:
			return finish(result("unavailable", "OpenThread daemon energy scan", err.Error(), nil)), nil
		}
	}

	if ownAddress == "" {
		return finish(result("unavailable", "OTBR energy scan action", "the border router's extended address is not known yet, so the scan has no destination", nil)), nil
	}
	channels, sweeps, err := repeatSweeps(ctx, 0, 0, energyScanRESTSweeps, func(ctx context.Context) ([]model.ChannelEnergy, error) {
		return c.restEnergyScan(ctx, ownAddress)
	})
	if err != nil {
		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusNotFound || statusErr.StatusCode == http.StatusNotImplemented || statusErr.StatusCode == http.StatusMethodNotAllowed) {
			return finish(result("unsupported", "OTBR energy scan action", "this OTBR build does not support the energy scan action", nil)), nil
		}
		return finish(result("unavailable", "OTBR energy scan action", err.Error(), nil)), nil
	}
	scan := result("available", "OTBR energy scan action", "", channels)
	scan.Sweeps = sweeps
	return finish(scan), nil
}

// repeatSweeps runs sweep until run has elapsed or maxSweeps is reached (a zero
// run means exactly maxSweeps), pausing between passes, and combines the
// readings per channel: the loudest as MaxRSSI and the median as TypicalRSSI.
// A failure on the first sweep is returned; a later failure ends the run early
// with what was gathered, since a partial run is still a measurement.
func repeatSweeps(ctx context.Context, run, pause time.Duration, maxSweeps int, sweep func(context.Context) ([]model.ChannelEnergy, error)) ([]model.ChannelEnergy, int, error) {
	started := time.Now()
	samples := map[int][]int{}
	sweeps := 0
	for sweeps < maxSweeps {
		channels, err := sweep(ctx)
		if err != nil {
			if sweeps == 0 {
				return nil, 0, err
			}
			break
		}
		sweeps++
		for _, ch := range channels {
			samples[ch.Channel] = append(samples[ch.Channel], ch.MaxRSSI)
		}
		if run > 0 && time.Since(started) >= run || run == 0 && sweeps >= maxSweeps {
			break
		}
		if pause > 0 {
			select {
			case <-ctx.Done():
				return combineSamples(samples, sweeps), sweeps, nil
			case <-time.After(pause):
			}
		}
	}
	return combineSamples(samples, sweeps), sweeps, nil
}

func combineSamples(samples map[int][]int, sweeps int) []model.ChannelEnergy {
	channels := make([]model.ChannelEnergy, 0, len(samples))
	for channel, values := range samples {
		sorted := append([]int(nil), values...)
		sort.Ints(sorted)
		entry := model.ChannelEnergy{Channel: channel, MaxRSSI: sorted[len(sorted)-1]}
		if sweeps > 1 {
			median := sorted[len(sorted)/2]
			if len(sorted)%2 == 0 {
				median = (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
			}
			entry.TypicalRSSI = &median
		}
		channels = append(channels, entry)
	}
	sort.Slice(channels, func(i, j int) bool { return channels[i].Channel < channels[j].Channel })
	return channels
}

// restEnergyScan posts a getEnergyScanTask, waits for it to complete, and reads
// the energyScanReport it links to under /api/diagnostics.
func (c *Client) restEnergyScan(ctx context.Context, destination string) ([]model.ChannelEnergy, error) {
	request := map[string]any{"data": []map[string]any{{
		"type": "getEnergyScanTask",
		"attributes": map[string]any{
			"destination": destination, "channelMask": allChannels,
			"count": energyScanCount, "period": energyScanPeriodMs, "scanDuration": energyScanDwellMs,
			"timeout": energyScanTimeoutSec,
		},
	}}}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	response, err := c.postJSONAPI(ctx, "/api/actions", body)
	if err != nil {
		return nil, err
	}
	actionID := actionIDFrom(response)
	if actionID == "" {
		return nil, errors.New("OTBR accepted the energy scan action but returned no action id")
	}

	deadline := time.Now().Add(energyScanWait)
	var resultID string
	for {
		record, _, err := c.getWithAccept(ctx, "/api/actions/"+actionID, "application/vnd.api+json")
		if err != nil {
			return nil, err
		}
		var parsed struct {
			Data struct {
				Attributes struct {
					Status string `json:"status"`
				} `json:"attributes"`
				Relationships struct {
					Result struct {
						Data struct {
							ID string `json:"id"`
						} `json:"data"`
					} `json:"result"`
				} `json:"relationships"`
			} `json:"data"`
		}
		if err := json.Unmarshal(record, &parsed); err != nil {
			return nil, fmt.Errorf("decode energy scan action: %w", err)
		}
		status := strings.ToLower(parsed.Data.Attributes.Status)
		resultID = parsed.Data.Relationships.Result.Data.ID
		if status == "completed" && resultID != "" {
			break
		}
		if status == "stopped" || status == "failed" {
			return nil, fmt.Errorf("energy scan action ended in state %q", status)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("energy scan action still %q after %s", status, energyScanWait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(energyScanPollEvery):
		}
	}

	report, _, err := c.getWithAccept(ctx, "/api/diagnostics/"+resultID, "application/vnd.api+json")
	if err != nil {
		return nil, err
	}
	return parseEnergyScanReport(report)
}

// parseEnergyScanReport reads the energyScanReport record. Each channel carries
// maxRssi as an array (one entry per scan count); the peak across them is kept.
func parseEnergyScanReport(body []byte) ([]model.ChannelEnergy, error) {
	var parsed struct {
		Data struct {
			Attributes struct {
				Report []struct {
					Channel int   `json:"channel"`
					MaxRSSI []int `json:"maxRssi"`
				} `json:"report"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode energy scan report: %w", err)
	}
	channels := []model.ChannelEnergy{}
	for _, entry := range parsed.Data.Attributes.Report {
		if len(entry.MaxRSSI) == 0 {
			continue
		}
		peak := entry.MaxRSSI[0]
		for _, value := range entry.MaxRSSI[1:] {
			if value > peak {
				peak = value
			}
		}
		channels = append(channels, model.ChannelEnergy{Channel: entry.Channel, MaxRSSI: peak})
	}
	return channels, nil
}

// actionIDFrom finds the created action's id whether OTBR echoes the request as a
// JSON:API array or a single object.
func actionIDFrom(body []byte) string {
	var asArray struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &asArray); err == nil && len(asArray.Data) > 0 {
		return asArray.Data[0].ID
	}
	var asObject struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &asObject); err == nil {
		return asObject.Data.ID
	}
	return ""
}

// postJSONAPI posts a JSON:API body and returns the response body, which write()
// discards; the actions endpoint answers with the records it created.
func (c *Client) postJSONAPI(ctx context.Context, endpoint string, body []byte) ([]byte, error) {
	u := *c.baseURL
	u.Path = strings.TrimRight(c.baseURL.Path, "/") + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/vnd.api+json")
	req.Header.Set("Accept", "application/vnd.api+json, application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request OTBR endpoint %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPStatusError{Endpoint: endpoint, StatusCode: resp.StatusCode}
	}
	return data, nil
}
