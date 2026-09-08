// Package otctl speaks the OpenThread CLI over the POSIX daemon's UNIX socket —
// the same channel ot-ctl and otbr-web use.
//
// It exists because a few capabilities have no REST equivalent. Active network
// discovery is the first: otbr-web's /available_network is a JSON wrapper around
// the CLI's scan table (src/web/web-service/ot_client.cpp), and the REST API's
// action list offers only an energy-detect scan, which reports RSSI per channel
// rather than the networks on air.
//
// A UNIX socket is not reachable over a network, so this only works when
// otbr-insight runs on the border router itself. Everything else in this codebase
// talks to OTBR over the REST API and can run anywhere; this deliberately does not,
// and is therefore optional — absent socket, absent capability.
package otctl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultSocket is where the OpenThread POSIX daemon listens on a stock OTBR
// install. The interface name is part of the path, so a non-default interface
// needs an explicit path.
const DefaultSocket = "/run/openthread-wpan0.sock"

// maxLines caps a reply so a chatty or wedged daemon cannot exhaust memory.
const maxLines = 2048

// unavailableError marks "the socket cannot be used at all" — missing, or present
// but not connectable, which is what a non-root process sees since the socket is
// mode 0755 root:root and connect() needs write. It reports itself through a
// method so callers can recognise it without importing this package.
type unavailableError struct{ message string }

func (e unavailableError) Error() string { return e.message }

// SocketUnavailable distinguishes "cannot reach the daemon" from "the daemon
// answered badly": the first should fall back to another source, the second is a
// fault worth surfacing.
func (unavailableError) SocketUnavailable() bool { return true }

// ErrUnavailable reports that no daemon socket is usable, which is the normal
// case when otbr-insight runs anywhere other than the border router.
var ErrUnavailable error = unavailableError{"OpenThread daemon socket is unavailable"}

// meshStructureCache holds everything that costs over-the-air queries. Ages and
// signal are deliberately not cached — they come from local tables on every call.
type meshStructureCache struct {
	mu        sync.Mutex
	routers   []meshRouter
	children  map[string][]meshChild
	addresses map[string]map[string][]string
	fetched   time.Time
}

type Client struct {
	path    string
	timeout time.Duration
	// The daemon serves one command at a time; serialise so concurrent callers
	// cannot interleave their output.
	mu        sync.Mutex
	status    statusCache
	structure meshStructureCache
}

func New(path string, timeout time.Duration) *Client {
	if strings.TrimSpace(path) == "" {
		path = DefaultSocket
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{path: path, timeout: timeout}
}

func (c *Client) Path() string { return c.path }

// Available reports whether the socket exists and is a socket. It is checked per
// call rather than cached so that starting or restarting otbr-agent is picked up
// without restarting otbr-insight.
func (c *Client) Available() bool {
	info, err := os.Stat(c.path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSocket != 0
}

// Execute runs one CLI command and returns its output lines, excluding the
// terminating "Done". An "Error N: reason" reply becomes a Go error.
func (c *Client) Execute(ctx context.Context, command string) ([]string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, errors.New("empty command")
	}
	// A newline would let a caller smuggle a second command into one call.
	if strings.ContainsAny(command, "\r\n") {
		return nil, errors.New("command must be a single line")
	}
	if !c.Available() {
		return nil, fmt.Errorf("%w at %s", ErrUnavailable, c.path)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	deadline := time.Now().Add(c.timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	// Closing the write side would end the session before the reply arrives, so the
	// command is simply written and the reply read on the same connection.
	if _, err := conn.Write([]byte(command + "\n")); err != nil {
		return nil, fmt.Errorf("write %q to daemon: %w", command, err)
	}

	var lines []string
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 8192), 1<<20)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		// The daemon echoes a "> " prompt, sometimes several on one line.
		for strings.HasPrefix(line, "> ") {
			line = line[2:]
		}
		line = strings.TrimPrefix(line, ">")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// The daemon echoes the command it was given before answering.
		if len(lines) == 0 && trimmed == command {
			continue
		}
		if trimmed == "Done" {
			return lines, nil
		}
		if strings.HasPrefix(trimmed, "Error ") {
			return nil, fmt.Errorf("%q: %s", command, trimmed)
		}
		lines = append(lines, line)
		if len(lines) >= maxLines {
			return nil, fmt.Errorf("%q: reply exceeded %d lines", command, maxLines)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read reply to %q: %w", command, err)
	}
	// The connection closed without "Done": treat a truncated reply as a failure
	// rather than returning a half-parsed table.
	return nil, fmt.Errorf("%q: daemon closed the connection before finishing", command)
}
