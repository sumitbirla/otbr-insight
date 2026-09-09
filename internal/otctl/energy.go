package otctl

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// EnergyScan runs an IEEE 802.15.4 energy-detect scan across every channel and
// reports the peak RSSI heard on each — the raw material for picking a quiet
// channel.
//
// It deliberately uses the firmware's default dwell time and offers no way to
// lengthen it. A 500 ms dwell hung the EFR32 RCP on the reference border
// router: spinel stopped answering, otbr-agent aborted with RadioSpinelNoResponse,
// and only a USB power cycle of the dongle brought the radio back. The default
// dwell has been exercised repeatedly without incident.
//
// Verified output (OPENTHREAD/8fbe09e):
//
//	| Ch | RSSI |
//	+----+------+
//	| 11 |  -80 |
//	| 12 |  -68 |
//	Done
func (c *Client) EnergyScan(ctx context.Context) ([]model.ChannelEnergy, error) {
	lines, err := c.Execute(ctx, "scan energy")
	if err != nil {
		return nil, err
	}
	channels := []model.ChannelEnergy{}
	for _, row := range pipeTable(lines) {
		channel, err := strconv.Atoi(strings.TrimSpace(row["ch"]))
		if err != nil {
			continue
		}
		rssi, err := strconv.Atoi(strings.TrimSpace(row["rssi"]))
		if err != nil {
			continue
		}
		channels = append(channels, model.ChannelEnergy{Channel: channel, MaxRSSI: rssi})
	}
	sort.Slice(channels, func(i, j int) bool { return channels[i].Channel < channels[j].Channel })
	return channels, nil
}
