package otctl

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDaemon serves the OpenThread CLI over a UNIX socket, answering from replies
// keyed by command. It mimics the daemon's habit of echoing a prompt and the
// command before its output.
func fakeDaemon(t *testing.T, replies map[string]string) string {
	t.Helper()
	// A UNIX socket path is capped near 104 bytes on macOS, and t.TempDir() embeds
	// the test name, which overruns it. Use a short directory instead.
	dir, err := os.MkdirTemp("/tmp", "otctl")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "d.sock")
	listener, err2 := net.Listen("unix", path)
	err = err2
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				scanner := bufio.NewScanner(conn)
				for scanner.Scan() {
					command := strings.TrimSpace(scanner.Text())
					reply, ok := replies[command]
					if !ok {
						_, _ = conn.Write([]byte("> " + command + "\r\nError 35: InvalidCommand\r\n"))
						continue
					}
					_, _ = conn.Write([]byte("> " + command + "\r\n" + reply))
				}
			}()
		}
	}()
	return path
}

func TestExecuteReturnsOutputWithoutPromptEchoOrDone(t *testing.T) {
	path := fakeDaemon(t, map[string]string{
		"state":   "leader\r\nDone\r\n",
		"channel": "25\r\nDone\r\n",
	})
	client := New(path, 2*time.Second)
	if !client.Available() {
		t.Fatal("Available() = false for a live socket")
	}
	lines, err := client.Execute(context.Background(), "state")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(lines) != 1 || lines[0] != "leader" {
		t.Errorf("lines = %q, want [leader]", lines)
	}
}

func TestExecuteSurfacesDaemonErrors(t *testing.T) {
	path := fakeDaemon(t, map[string]string{})
	client := New(path, 2*time.Second)
	_, err := client.Execute(context.Background(), "bogus")
	if err == nil || !strings.Contains(err.Error(), "Error 35") {
		t.Errorf("Execute() error = %v, want the daemon's error text", err)
	}
}

func TestExecuteRejectsMultiLineCommands(t *testing.T) {
	// Without this a caller could smuggle a second command past the API surface.
	client := New(fakeDaemon(t, map[string]string{}), time.Second)
	if _, err := client.Execute(context.Background(), "state\nfactoryreset"); err == nil {
		t.Error("Execute() accepted an embedded newline")
	}
}

func TestUnavailableWhenTheSocketIsMissing(t *testing.T) {
	client := New("/tmp/otctl-does-not-exist.sock", time.Second)
	if client.Available() {
		t.Error("Available() = true for a missing socket")
	}
	_, err := client.Execute(context.Background(), "state")
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Execute() error = %v, want ErrUnavailable", err)
	}
}

func TestExecuteFailsOnATruncatedReply(t *testing.T) {
	// A reply with no "Done" must not be handed back as if it were complete.
	path := fakeDaemon(t, map[string]string{"discover": "| PAN  | MAC Address      |\r\n"})
	client := New(path, time.Second)
	if _, err := client.Execute(context.Background(), "discover"); err == nil {
		t.Error("Execute() accepted a reply with no terminator")
	}
}

const discoverOutput = "" +
	"| J | Network Name     | Extended PAN     | PAN  | MAC Address      | Ch | dBm | LQI |\r\n" +
	"+---+------------------+------------------+------+------------------+----+-----+-----+\r\n" +
	"| 0 | OpenThread-a1b2  | 1122334455667788 | a1b2 | 0708090a0b0c0d0e | 25 | -45 |  60 |\r\n" +
	"| 1 | NeighborNet2    | 8899aabbccddeeff | 3303 | 08090a0b0c0d0e0f | 25 | -72 |  20 |\r\n" +
	"Done\r\n"

func TestScanNetworksParsesTheDiscoverTable(t *testing.T) {
	client := New(fakeDaemon(t, map[string]string{"discover": discoverOutput}), 2*time.Second)
	networks, err := client.ScanNetworks(context.Background())
	if err != nil {
		t.Fatalf("ScanNetworks() error = %v", err)
	}
	if len(networks) != 2 {
		t.Fatalf("networks = %d, want 2", len(networks))
	}
	first := networks[0]
	if first.Name != "OpenThread-a1b2" {
		t.Errorf("Name = %q", first.Name)
	}
	if first.ExtendedPANID != "1122334455667788" {
		t.Errorf("ExtendedPANID = %q", first.ExtendedPANID)
	}
	// The rest of the app renders PAN IDs in 0x form; the CLI prints bare hex.
	if first.PANID != "0xA1B2" {
		t.Errorf("PANID = %q, want 0xA1B2", first.PANID)
	}
	if first.HardwareAddress != "0708090A0B0C0D0E" {
		t.Errorf("HardwareAddress = %q", first.HardwareAddress)
	}
	if first.Channel == nil || *first.Channel != 25 {
		t.Errorf("Channel = %v, want 25", first.Channel)
	}
}

func TestScanNetworksFallsBackToScanWhenDiscoverIsUnsupported(t *testing.T) {
	// Older firmware answers "discover" with an error; the bare scan table has
	// fewer columns and no header-independent ordering to rely on.
	path := fakeDaemon(t, map[string]string{
		"scan": "| PAN  | MAC Address      | Ch | dBm | LQI |\r\n" +
			"+------+------------------+----+-----+-----+\r\n" +
			"| a1b2 | 0708090a0b0c0d0e | 25 | -45 |  60 |\r\n" +
			"Done\r\n",
	})
	client := New(path, 2*time.Second)
	networks, err := client.ScanNetworks(context.Background())
	if err != nil {
		t.Fatalf("ScanNetworks() error = %v", err)
	}
	if len(networks) != 1 {
		t.Fatalf("networks = %d, want 1", len(networks))
	}
	if networks[0].PANID != "0xA1B2" || networks[0].Name != "" {
		t.Errorf("network = %+v; scan carries no name", networks[0])
	}
	if networks[0].Channel == nil || *networks[0].Channel != 25 {
		t.Errorf("Channel = %v", networks[0].Channel)
	}
}

func TestScanNetworksReturnsEmptyRatherThanErrorWhenNothingIsOnAir(t *testing.T) {
	client := New(fakeDaemon(t, map[string]string{
		"discover": "| J | Network Name     | Extended PAN     | PAN  | MAC Address      | Ch | dBm | LQI |\r\n" +
			"+---+------------------+------------------+------+------------------+----+-----+-----+\r\nDone\r\n",
	}), 2*time.Second)
	networks, err := client.ScanNetworks(context.Background())
	if err != nil {
		t.Fatalf("ScanNetworks() error = %v", err)
	}
	if networks == nil || len(networks) != 0 {
		t.Errorf("networks = %v, want an empty non-nil slice", networks)
	}
}

// realDiscoverOutput is captured verbatim from `ot-ctl discover` on an OTBR running
// OPENTHREAD/8fbe09e. Note there is no joinable ("J") column, and a network is
// repeated once per responding beacon.
const realDiscoverOutput = "" +
	"| Network Name     | Extended PAN     | PAN  | MAC Address      | Ch | dBm | LQI |\r\n" +
	"+------------------+------------------+------+------------------+----+-----+-----+\r\n" +
	"| OpenThread-a1b2  | 1122334455667788 | a1b2 | 0102030405060708 | 25 | -62 | 255 |\r\n" +
	"| OpenThread-a1b2  | 1122334455667788 | a1b2 | 0102030405060708 | 25 | -62 | 255 |\r\n" +
	"| NeighborNet1         | 090a0b0c0d0e0f01 | 3303 | 08090a0b0c0d0e0f | 25 | -76 | 255 |\r\n" +
	"Done\r\n"

func TestScanNetworksParsesRealFirmwareOutput(t *testing.T) {
	client := New(fakeDaemon(t, map[string]string{"discover": realDiscoverOutput}), 2*time.Second)
	networks, err := client.ScanNetworks(context.Background())
	if err != nil {
		t.Fatalf("ScanNetworks() error = %v", err)
	}
	// Three rows, two of them identical: the duplicate must not reach the UI.
	if len(networks) != 2 {
		t.Fatalf("networks = %d, want 2 after collapsing the repeated beacon: %+v", len(networks), networks)
	}
	if networks[0].Name != "OpenThread-a1b2" || networks[0].ExtendedPANID != "1122334455667788" {
		t.Errorf("first = %+v", networks[0])
	}
	if networks[0].PANID != "0xA1B2" || networks[0].HardwareAddress != "0102030405060708" {
		t.Errorf("first = %+v", networks[0])
	}
	if networks[1].Name != "NeighborNet1" || networks[1].PANID != "0x3303" {
		t.Errorf("second = %+v", networks[1])
	}
	if networks[0].Channel == nil || *networks[0].Channel != 25 {
		t.Errorf("Channel = %v", networks[0].Channel)
	}
}

func TestStatusFillsTheFieldsOtbrWebUsedToSupply(t *testing.T) {
	client := New(fakeDaemon(t, map[string]string{
		"version":     "OPENTHREAD/8fbe09e; POSIX; Apr 22 2026 22:59:52\r\nDone\r\n",
		"version api": "591\r\nDone\r\n",
		"rcp version": "SL-OPENTHREAD/2.4.4.0_GitHub-7074a43e4; EFR32\r\nDone\r\n",
		"channel":     "25\r\nDone\r\n",
		"txpower":     "0 dBm\r\nDone\r\n",
		"state":       "leader\r\nDone\r\n",
		"eui64":       "0304050607080901\r\nDone\r\n",
		"panid":       "0xa1b2\r\nDone\r\n",
		"ipaddr": "fdde:ad00:beef:9004:0:ff:fe00:7000\r\n" +
			"fdde:ad00:beef:9004:899d:add6:1913:7675\r\n" +
			"fe80:0:0:0:844:8010:97e9:ab9b\r\n" +
			"Done\r\n",
	}), 2*time.Second)

	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.RCPVersion != "SL-OPENTHREAD/2.4.4.0_GitHub-7074a43e4; EFR32" {
		t.Errorf("RCPVersion = %q", status.RCPVersion)
	}
	if status.RCPChannel != "25" || status.RCPTxPower != "0 dBm" || status.RCPEUI64 != "0304050607080901" {
		t.Errorf("radio fields = %+v", status)
	}
	// Derived from the role, since the CLI has no direct equivalent.
	if status.WPANService != "associated" {
		t.Errorf("WPANService = %q, want associated for a leader", status.WPANService)
	}
	if status.LinkLocalAddress != "fe80:0:0:0:844:8010:97e9:ab9b" {
		t.Errorf("LinkLocalAddress = %q", status.LinkLocalAddress)
	}
	// The RLOC form must not be mistaken for the mesh-local EID.
	if status.MeshLocalAddress != "fdde:ad00:beef:9004:899d:add6:1913:7675" {
		t.Errorf("MeshLocalAddress = %q, want the EID rather than a locator", status.MeshLocalAddress)
	}

	// Near-static values must not be re-read on every 5s overview poll.
	before := client.status.fetched
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !client.status.fetched.Equal(before) {
		t.Error("Status() re-read the daemon inside its cache window")
	}
}

// The OMR address is a ULA too, and on a live border router "ipaddr" listed it
// before the mesh-local EID, so a first-ULA guess reported the OMR address as
// mesh-local. "ipaddr mleid" answers the question directly.
func TestStatusTakesTheMeshLocalEIDFromTheDaemonNotTheAddressOrder(t *testing.T) {
	client := New(fakeDaemon(t, map[string]string{
		"version":      "OPENTHREAD/8fbe09e; POSIX; Apr 22 2026 22:59:52\r\nDone\r\n",
		"ipaddr mleid": "fdde:ad00:beef:9004:899d:add6:1913:7675\r\nDone\r\n",
		"ipaddr": "fdde:ad00:beef:9004:0:ff:fe00:fc11\r\n" +
			"fd11:2233:4455:1:60c5:d77f:b600:6ee5\r\n" +
			"fdde:ad00:beef:9004:0:ff:fe00:7000\r\n" +
			"fdde:ad00:beef:9004:899d:add6:1913:7675\r\n" +
			"fe80:0:0:0:844:8010:97e9:ab9b\r\n" +
			"Done\r\n",
	}), 2*time.Second)

	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.MeshLocalAddress != "fdde:ad00:beef:9004:899d:add6:1913:7675" {
		t.Errorf("MeshLocalAddress = %q, want the EID, not the OMR address listed first", status.MeshLocalAddress)
	}
}

func TestStatusFailsWhenTheDaemonCannotAnswer(t *testing.T) {
	client := New(fakeDaemon(t, map[string]string{}), time.Second)
	if _, err := client.Status(context.Background()); err == nil {
		t.Error("Status() succeeded with no version available")
	}
}
