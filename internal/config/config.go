package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/otctl"
)

type Config struct {
	Listen            string
	OTBRURL           string
	PollInterval      time.Duration
	DiscoveryInterval time.Duration
	LogLevel          string
	DataDir           string
	OTBRSocket        string
}

// defaults returns the configuration before any flag is applied: the built-in
// values, overridden by OTBR_INSIGHT_* environment variables.
func defaults() (Config, error) {
	cfg := Config{
		Listen:       envOr("OTBR_INSIGHT_LISTEN", ":8088"),
		OTBRURL:      envOr("OTBR_INSIGHT_OTBR_URL", "http://127.0.0.1:8081"),
		PollInterval: 5 * time.Second,
		// OTBR only refreshes its device collection when asked. Sweeping costs mesh
		// airtime, so this is deliberately unhurried.
		DiscoveryInterval: 5 * time.Minute,
		LogLevel:          envOr("OTBR_INSIGHT_LOG_LEVEL", "info"),
		DataDir:           resolveDataDir(os.Getenv("OTBR_INSIGHT_DATA_DIR")),
		// Only usable when otbr-insight runs on the border router itself; harmless
		// elsewhere, where the socket simply will not exist.
		OTBRSocket: envOr("OTBR_INSIGHT_OTBR_SOCKET", otctl.DefaultSocket),
	}
	if raw := os.Getenv("OTBR_INSIGHT_POLL_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse OTBR_INSIGHT_POLL_INTERVAL: %w", err)
		}
		cfg.PollInterval = d
	}
	if raw := os.Getenv("OTBR_INSIGHT_DISCOVERY_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse OTBR_INSIGHT_DISCOVERY_INTERVAL: %w", err)
		}
		cfg.DiscoveryInterval = d
	}
	return cfg, nil
}

// bindFlags registers every flag against cfg. Parse and Usage share it so the
// help text can never drift from what the binary actually accepts.
func bindFlags(fs *flag.FlagSet, cfg *Config) {
	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "HTTP listen address")
	fs.StringVar(&cfg.OTBRURL, "otbr-url", cfg.OTBRURL, "OTBR REST API base URL")
	fs.DurationVar(&cfg.PollInterval, "poll-interval", cfg.PollInterval, "how often to poll OTBR for node status (minimum 1s)")
	fs.DurationVar(&cfg.DiscoveryInterval, "discovery-interval", cfg.DiscoveryInterval, "how often to ask OTBR to rediscover the mesh; 0 disables (minimum 30s)")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level: debug, info, warn, error")
	fs.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "directory for persisted device names and the dataset backup (empty disables both)")
	fs.StringVar(&cfg.OTBRSocket, "otbr-socket", cfg.OTBRSocket, "OpenThread daemon socket for capabilities the REST API lacks; empty disables (only usable on the border router host)")
}

// envVars documents each flag's environment-variable equivalent, in the order the
// flags are listed by Usage.
var envVars = [][2]string{
	{"OTBR_INSIGHT_LISTEN", "listen"},
	{"OTBR_INSIGHT_OTBR_URL", "otbr-url"},
	{"OTBR_INSIGHT_POLL_INTERVAL", "poll-interval"},
	{"OTBR_INSIGHT_DISCOVERY_INTERVAL", "discovery-interval"},
	{"OTBR_INSIGHT_LOG_LEVEL", "log-level"},
	{"OTBR_INSIGHT_DATA_DIR", "data-dir"},
	{"OTBR_INSIGHT_OTBR_SOCKET", "otbr-socket"},
}

// printDefaults is flag.PrintDefaults with double-hyphen names, matching how this
// project documents itself (README, CLAUDE.md, the systemd unit). Both forms are
// accepted at the command line — Go's flag package makes no distinction.
func printDefaults(w io.Writer, fs *flag.FlagSet) {
	fs.VisitAll(func(f *flag.Flag) {
		kind, usage := flag.UnquoteUsage(f)
		name := "  --" + f.Name
		if kind != "" {
			name += " " + kind
		}
		fmt.Fprintln(w, name)
		def := f.DefValue
		if kind == "string" {
			def = strconv.Quote(def)
		}
		fmt.Fprintf(w, "    \t%s (default %s)\n", usage, def)
	})
}

// Usage writes the command-line help. Defaults shown are the effective ones for
// the current environment, so what is printed is what running with no flags gives.
func Usage(w io.Writer) {
	cfg, err := defaults()
	if err != nil {
		// A malformed env var must not stop -h from explaining the flags.
		fmt.Fprintf(w, "warning: %v\n\n", err)
		cfg = Config{}
	}
	fmt.Fprint(w, "OTBR Insight — monitoring and management dashboard for an OpenThread Border Router.\n\n")
	fmt.Fprint(w, "Usage:\n  otbr-insight [flags]\n\nFlags:\n")
	fs := flag.NewFlagSet("otbr-insight", flag.ContinueOnError)
	fs.SetOutput(w)
	bindFlags(fs, &cfg)
	printDefaults(w, fs)
	fmt.Fprint(w, "\nEach flag can also be set by environment variable; a flag wins over its variable:\n")
	for _, pair := range envVars {
		fmt.Fprintf(w, "  %-33s same as --%s\n", pair[0], pair[1])
	}
	fmt.Fprint(w, "\nDefaults above reflect the current environment. The data directory falls back to\n")
	fmt.Fprint(w, "systemd's STATE_DIRECTORY, then the user config directory.\n")
}

func Parse(args []string) (Config, error) {
	// A malformed environment variable must not stop -h from working, so flags are
	// parsed before the env error is reported.
	cfg, defaultsErr := defaults()
	fs := flag.NewFlagSet("otbr-insight", flag.ContinueOnError)
	// Errors are reported by the caller; -h is handled there too, via flag.ErrHelp.
	fs.SetOutput(io.Discard)
	bindFlags(fs, &cfg)
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if defaultsErr != nil {
		return Config{}, defaultsErr
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Listen) == "" {
		return errors.New("listen address cannot be empty")
	}
	if c.PollInterval < time.Second {
		return errors.New("poll interval must be at least 1s")
	}
	// A sweep can take over a minute and costs mesh airtime; anything faster than
	// this would overlap runs. Zero switches discovery off entirely.
	if c.DiscoveryInterval != 0 && c.DiscoveryInterval < 30*time.Second {
		return errors.New("discovery interval must be 0 or at least 30s")
	}
	u, err := url.Parse(c.OTBRURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("OTBR URL must be an absolute http or https URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("OTBR URL must not contain credentials, a query, or a fragment")
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("log level must be debug, info, warn, or error")
	}
	return nil
}

// resolveDataDir picks where device names are stored. An explicit value wins;
// otherwise it prefers systemd's StateDirectory, then the user config dir. It
// returns "" only when no writable location can be determined, which disables
// naming rather than guessing a bad path.
func resolveDataDir(explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	if state := os.Getenv("STATE_DIRECTORY"); state != "" {
		// systemd may provide a colon-separated list; use the first entry.
		return strings.SplitN(state, ":", 2)[0]
	}
	if base, err := os.UserConfigDir(); err == nil {
		return filepath.Join(base, "otbr-insight")
	}
	return ""
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
