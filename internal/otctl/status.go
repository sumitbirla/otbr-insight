package otctl

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// statusTTL caches the runtime fields, which are near-static: version strings,
// EUI-64 and TX power never change while running, and channel rarely does. Without
// it every 5s overview poll would issue nine socket round trips for the same answers.
const statusTTL = 60 * time.Second

type statusCache struct {
	mu      sync.Mutex
	value   *model.Overview
	fetched time.Time
}

// Status supplies the runtime fields the REST API does not expose — the ones
// otbr-web served from /get_properties, which vanish when that service is stopped.
// Only the fields it knows are set; the caller merges them over the REST overview.
func (c *Client) Status(ctx context.Context) (*model.Overview, error) {
	c.status.mu.Lock()
	defer c.status.mu.Unlock()
	if c.status.value != nil && time.Since(c.status.fetched) < statusTTL {
		return c.status.value, nil
	}

	first := func(command string) string {
		lines, err := c.Execute(ctx, command)
		if err != nil || len(lines) == 0 {
			return ""
		}
		return strings.TrimSpace(lines[0])
	}
	// "version" must succeed; the rest are best-effort so one unsupported command
	// cannot blank the whole panel.
	version := first("version")
	if version == "" {
		return nil, ErrUnavailable
	}
	overview := &model.Overview{
		OpenThreadVersion:    version,
		OpenThreadAPIVersion: first("version api"),
		RCPVersion:           first("rcp version"),
		RCPChannel:           first("channel"),
		RCPTxPower:           first("txpower"),
		RCPState:             first("state"),
		RCPEUI64:             first("eui64"),
		PANID:                first("panid"),
	}
	// Derived, not reported: otbr-web's "WPAN service" reflected whether the
	// interface had joined a network, which is what a routing role means here.
	switch overview.RCPState {
	case "child", "router", "leader":
		overview.WPANService = "associated"
	case "":
	default:
		overview.WPANService = overview.RCPState
	}
	for _, address := range c.allAddresses(ctx) {
		switch {
		case strings.HasPrefix(strings.ToLower(address), "fe80:"):
			if overview.LinkLocalAddress == "" {
				overview.LinkLocalAddress = address
			}
		case strings.Contains(strings.ToLower(address), ":0:ff:fe00:"):
			// A routing or anycast locator, not a stable address.
		case overview.MeshLocalAddress == "" && meshLocalLooking(address, overview.OMRIPv6Address):
			overview.MeshLocalAddress = address
		}
	}
	c.status.value, c.status.fetched = overview, time.Now()
	return overview, nil
}

// allAddresses returns every address "ipaddr" reports, unfiltered.
func (c *Client) allAddresses(ctx context.Context) []string {
	lines, err := c.Execute(ctx, "ipaddr")
	if err != nil {
		return nil
	}
	addresses := make([]string, 0, len(lines))
	for _, line := range lines {
		if candidate := strings.TrimSpace(line); candidate != "" {
			addresses = append(addresses, candidate)
		}
	}
	return addresses
}

// meshLocalLooking reports whether an address is the mesh-local EID rather than the
// OMR one. Both are ULAs; the OMR address is known separately, so anything else
// under fd00::/8 that is not a locator is the mesh-local EID.
func meshLocalLooking(address, omr string) bool {
	lower := strings.ToLower(address)
	return strings.HasPrefix(lower, "fd") && lower != strings.ToLower(omr)
}
