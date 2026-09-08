package otctl

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// pingTimeoutSecs must accommodate a sleepy end device, which answers only when it
// next wakes to poll its parent. Measured replies here take 2.5-4.5s; the CLI's
// one-second default reports a live device as 100% lost.
const pingTimeoutSecs = 10

var pingSummary = regexp.MustCompile(`(\d+) packets transmitted, (\d+) packets received`)
var pingRoundTrip = regexp.MustCompile(`min/avg/max = ([\d.]+)/([\d.]+)/([\d.]+)`)

// Ping tests reachability from the border router, which can reach a device's
// mesh-local address as well as its OMR one — something a browser on the LAN
// cannot do.
func (c *Client) Ping(ctx context.Context, address string, count int) (*model.PingResult, error) {
	parsed, err := netip.ParseAddr(strings.TrimSpace(address))
	if err != nil {
		return nil, fmt.Errorf("not an IP address: %q", address)
	}
	if count < 1 || count > 5 {
		count = 3
	}
	// size, count, interval, hoplimit, timeout — all positional in the CLI.
	command := fmt.Sprintf("ping %s 8 %d 1 64 %d", parsed.String(), count, pingTimeoutSecs)
	// Each unanswered packet costs its full timeout, so allow for the worst case.
	deadline := time.Duration(count*(pingTimeoutSecs+1)) * time.Second
	pingCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	lines, err := c.Execute(pingCtx, command)
	if err != nil {
		return nil, err
	}
	result := &model.PingResult{Address: parsed.String(), Sent: count}
	for _, line := range lines {
		if match := pingSummary.FindStringSubmatch(line); match != nil {
			result.Sent, _ = strconv.Atoi(match[1])
			result.Received, _ = strconv.Atoi(match[2])
		}
		if match := pingRoundTrip.FindStringSubmatch(line); match != nil {
			min, _ := strconv.ParseFloat(match[1], 64)
			avg, _ := strconv.ParseFloat(match[2], 64)
			max, _ := strconv.ParseFloat(match[3], 64)
			result.MinMs, result.AverageMs, result.MaxMs = &min, &avg, &max
		}
	}
	result.Reachable = result.Received > 0
	return result, nil
}
