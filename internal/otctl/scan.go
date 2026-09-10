package otctl

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// discoveryChannels are the 2.4 GHz channels a discovery covers, in scan order.
var discoveryChannels = func() []int {
	channels := make([]int, 0, 16)
	for ch := 11; ch <= 26; ch++ {
		channels = append(channels, ch)
	}
	return channels
}()

// discoveryChannelPause is how long the radio is left on its own channel between
// per-channel discoveries. Measured: a whole-band "discover" keeps the radio away
// from the network's channel for 4.9 s without a break, which is long enough for a
// sleepy child to fail several data polls in a row, give up on its parent and
// re-attach — and, since the border router is still deaf, re-attach to any other
// router instead. One channel takes 0.3 s, so scanning channel by channel with a
// pause at home between them keeps every absence shorter than a poll retry.
var discoveryChannelPause = 150 * time.Millisecond

// ScanNetworks performs an active scan for nearby Thread networks.
//
// "discover" is used because it reports the network name, extended PAN ID and
// joinability that "scan" omits — otbr-web used "scan", so this is strictly more
// than /available_network returned. It is issued one channel at a time (see
// discoveryChannelPause); firmware that rejects the channel argument gets one
// whole-band "discover", and firmware without "discover" at all falls back to "scan".
func (c *Client) ScanNetworks(ctx context.Context) ([]model.AvailableNetwork, error) {
	networks, err := c.discoverPerChannel(ctx)
	if err == nil {
		return networks, nil
	}
	if errors.Is(err, ErrUnavailable) {
		return nil, err // no socket to fall back onto
	}
	lines, err := c.Execute(ctx, "discover")
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return nil, err
		}
		fallback, scanErr := c.Execute(ctx, "scan")
		if scanErr != nil {
			return nil, scanErr
		}
		lines = fallback
	}
	return parseScanTable(lines), nil
}

// discoverPerChannel runs "discover <channel>" for each channel with a pause on
// the home channel between them, merging the rows. A failure on the first channel
// is returned so the caller can fall back; a failure on a later channel is a
// transient (the daemon serves one session at a time) and that channel is skipped.
func (c *Client) discoverPerChannel(ctx context.Context) ([]model.AvailableNetwork, error) {
	networks := []model.AvailableNetwork{}
	seen := map[string]bool{}
	for i, channel := range discoveryChannels {
		lines, err := c.Execute(ctx, "discover "+strconv.Itoa(channel))
		if err != nil {
			if i == 0 {
				return nil, err
			}
			continue
		}
		for _, network := range parseScanTable(lines) {
			key := networkKey(network)
			if seen[key] {
				continue
			}
			seen[key] = true
			networks = append(networks, network)
		}
		if i == len(discoveryChannels)-1 || discoveryChannelPause == 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return networks, nil
		case <-time.After(discoveryChannelPause):
		}
	}
	return networks, nil
}

func networkKey(network model.AvailableNetwork) string {
	return network.Name + "\x00" + network.ExtendedPANID + "\x00" + network.PANID + "\x00" + network.HardwareAddress
}

// parseScanTable reads the CLI's pipe-delimited table. Both commands share the
// shape, differing only in which columns are present, so the header row is used to
// locate each field rather than assuming fixed positions:
//
//	| J | Network Name     | Extended PAN     | PAN  | MAC Address      | Ch | dBm | LQI |
//	| PAN  | MAC Address      | Ch | dBm | LQI |
func parseScanTable(lines []string) []model.AvailableNetwork {
	networks := []model.AvailableNetwork{}
	seen := map[string]bool{}
	var columns []string
	for _, line := range lines {
		cells := splitRow(line)
		if cells == nil {
			continue // separators and any stray output
		}
		if columns == nil {
			// The first row is the header; normalise it for lookup.
			if looksLikeHeader(cells) {
				columns = make([]string, len(cells))
				for i, cell := range cells {
					columns[i] = strings.ToLower(cell)
				}
				continue
			}
			// No header at all: fall back to the bare "scan" column order.
			columns = []string{"pan", "mac address", "ch", "dbm", "lqi"}
		}
		network, ok := rowToNetwork(columns, cells)
		if !ok {
			continue
		}
		// A real discover repeats a network once per responding beacon, so the same
		// row can appear several times. Collapse exact duplicates.
		key := networkKey(network)
		if seen[key] {
			continue
		}
		seen[key] = true
		networks = append(networks, network)
	}
	return networks
}

// pipeTable reads any of the CLI's pipe-delimited tables into header-keyed rows,
// so a caller can name a column instead of counting positions. Column sets differ
// between commands and firmware versions, which is exactly what broke assumptions
// about "discover" having a joinable column.
func pipeTable(lines []string) []map[string]string {
	var columns []string
	var rows []map[string]string
	for _, line := range lines {
		cells := splitRow(line)
		if cells == nil {
			continue
		}
		if columns == nil {
			columns = make([]string, len(cells))
			for i, cell := range cells {
				columns[i] = strings.ToLower(cell)
			}
			continue
		}
		row := make(map[string]string, len(cells))
		for i, cell := range cells {
			if i < len(columns) {
				row[columns[i]] = cell
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func splitRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "|") {
		return nil
	}
	if strings.ContainsAny(trimmed, "+-") && strings.Trim(trimmed, "+-| ") == "" {
		return nil // the +----+----+ separator
	}
	parts := strings.Split(strings.Trim(trimmed, "|"), "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, strings.TrimSpace(part))
	}
	return cells
}

func looksLikeHeader(cells []string) bool {
	for _, cell := range cells {
		switch strings.ToLower(cell) {
		case "pan", "mac address", "network name", "extended pan":
			return true
		}
	}
	return false
}

func rowToNetwork(columns, cells []string) (model.AvailableNetwork, bool) {
	network := model.AvailableNetwork{}
	found := false
	for index, cell := range cells {
		if index >= len(columns) || cell == "" || cell == "-" {
			continue
		}
		switch columns[index] {
		case "network name":
			network.Name = cell
			found = true
		case "extended pan":
			network.ExtendedPANID = strings.ToLower(cell)
			found = true
		case "pan":
			// The CLI prints a bare hex PAN ID; the rest of the app expects 0x form.
			network.PANID = "0x" + strings.ToUpper(strings.TrimPrefix(strings.ToLower(cell), "0x"))
			found = true
		case "mac address":
			network.HardwareAddress = strings.ToUpper(cell)
			found = true
		case "ch":
			if channel, err := strconv.Atoi(cell); err == nil {
				network.Channel = &channel
				found = true
			}
		}
	}
	return network, found
}
