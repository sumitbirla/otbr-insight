package otctl

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/model"
)

// historyDepth bounds how many recorded entries are read. OpenThread keeps a
// fixed-size ring, so this is a display limit rather than a retention setting.
const historyDepth = 30

// History reads OpenThread's own event recorder. Nothing else in this app can
// answer "when did that happen": the REST API exposes only current state, so a
// device that left is simply absent, with no record of when or at what signal.
func (c *Client) History(ctx context.Context) (*model.History, error) {
	history := &model.History{
		Status: "available", Source: "OpenThread history tracker",
		Network: []model.NetworkHistoryEntry{}, Neighbors: []model.NeighborHistoryEntry{},
	}
	now := time.Now().UTC()

	netLines, err := c.Execute(ctx, "history netinfo "+strconv.Itoa(historyDepth))
	if err != nil {
		return nil, err
	}
	for _, row := range pipeTable(netLines) {
		age, ok := parseCLIDuration(row["age"])
		if !ok {
			continue
		}
		entry := model.NetworkHistoryEntry{
			At: now.Add(-age), Role: row["role"], Mode: row["mode"], RLOC16: row["rloc16"],
		}
		if partition, err := strconv.ParseUint(strings.TrimSpace(row["partition id"]), 10, 32); err == nil {
			value := uint32(partition)
			entry.PartitionID = &value
		}
		history.Network = append(history.Network, entry)
	}

	// Best-effort: a build without neighbour history still yields the network log.
	if neighbourLines, err := c.Execute(ctx, "history neighbor "+strconv.Itoa(historyDepth)); err == nil {
		for _, row := range pipeTable(neighbourLines) {
			age, ok := parseCLIDuration(row["age"])
			if !ok {
				continue
			}
			history.Neighbors = append(history.Neighbors, model.NeighborHistoryEntry{
				At: now.Add(-age), Type: row["type"], Event: row["event"],
				ExtendedAddress: strings.ToLower(row["extended address"]),
				RLOC16:          row["rloc16"], AverageRSSI: intPtr(row["ave rss"]),
			})
		}
	}
	return history, nil
}
