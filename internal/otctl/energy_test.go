package otctl

import (
	"context"
	"testing"
	"time"
)

func TestEnergyScanParsesTheChannelTable(t *testing.T) {
	client := New(fakeDaemon(t, map[string]string{
		"scan energy": "| Ch | RSSI |\r\n" +
			"+----+------+\r\n" +
			"| 12 |  -68 |\r\n" +
			"| 11 |  -80 |\r\n" +
			"| 26 |  -81 |\r\n" +
			"Done\r\n",
	}), 2*time.Second)
	channels, err := client.EnergyScan(context.Background())
	if err != nil {
		t.Fatalf("EnergyScan() error = %v", err)
	}
	if len(channels) != 3 {
		t.Fatalf("channels = %+v, want 3", channels)
	}
	// Sorted by channel regardless of the order the CLI printed them.
	if channels[0].Channel != 11 || channels[0].MaxRSSI != -80 || channels[1].Channel != 12 || channels[1].MaxRSSI != -68 || channels[2].Channel != 26 {
		t.Errorf("channels = %+v", channels)
	}
}

func TestEnergyScanReportsADaemonError(t *testing.T) {
	client := New(fakeDaemon(t, map[string]string{}), 2*time.Second)
	if _, err := client.EnergyScan(context.Background()); err == nil {
		t.Fatal("expected the daemon's InvalidCommand error to surface")
	}
}
