package config

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	t.Setenv("OTBR_INSIGHT_LISTEN", "")
	t.Setenv("OTBR_INSIGHT_OTBR_URL", "")
	t.Setenv("OTBR_INSIGHT_POLL_INTERVAL", "")
	t.Setenv("OTBR_INSIGHT_LOG_LEVEL", "")
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":8088" || cfg.OTBRURL != "http://127.0.0.1:8081" || cfg.PollInterval != 5*time.Second {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestParseFlagsOverrideEnvironment(t *testing.T) {
	t.Setenv("OTBR_INSIGHT_LISTEN", ":9000")
	cfg, err := Parse([]string{"--listen", ":8088", "--poll-interval", "12s"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":8088" || cfg.PollInterval != 12*time.Second {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestRejectsUnsafeOTBRURL(t *testing.T) {
	if _, err := Parse([]string{"--otbr-url", "file:///etc/passwd"}); err == nil {
		t.Fatal("expected URL error")
	}
	if _, err := Parse([]string{"--otbr-url", "http://user:pass@localhost:8081"}); err == nil {
		t.Fatal("expected credentials error")
	}
}

func TestDiscoveryIntervalDefaultsAndBounds(t *testing.T) {
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DiscoveryInterval != 5*time.Minute {
		t.Errorf("default DiscoveryInterval = %s, want 5m", cfg.DiscoveryInterval)
	}

	if _, err := Parse([]string{"--discovery-interval", "0"}); err != nil {
		t.Errorf("zero must be accepted to disable discovery: %v", err)
	}
	if _, err := Parse([]string{"--discovery-interval", "10s"}); err == nil {
		t.Error("Parse() error = nil for a 10s discovery interval, want an error")
	}
	cfg, err = Parse([]string{"--discovery-interval", "90s"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DiscoveryInterval != 90*time.Second {
		t.Errorf("DiscoveryInterval = %s, want 90s", cfg.DiscoveryInterval)
	}
}

func TestParseReturnsErrHelpForHelpFlags(t *testing.T) {
	for _, arg := range []string{"-h", "--help"} {
		if _, err := Parse([]string{arg}); !errors.Is(err, flag.ErrHelp) {
			t.Errorf("Parse(%q) error = %v, want flag.ErrHelp", arg, err)
		}
	}
}

func TestHelpSurvivesAMalformedEnvVar(t *testing.T) {
	// -h must explain the flags even when the environment is broken; that is
	// usually the moment someone reaches for it.
	t.Setenv("OTBR_INSIGHT_POLL_INTERVAL", "nonsense")
	if _, err := Parse([]string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("Parse(-h) error = %v, want flag.ErrHelp", err)
	}
	// A real run must still refuse to start on the same input.
	if _, err := Parse(nil); err == nil || errors.Is(err, flag.ErrHelp) {
		t.Errorf("Parse(nil) error = %v, want the env parse failure", err)
	}
}

func TestUsageListsEveryFlagWithItsDefault(t *testing.T) {
	var buf bytes.Buffer
	Usage(&buf)
	out := buf.String()
	// Double hyphen, matching the README, CLAUDE.md and the systemd unit. Go's flag
	// package accepts both forms, so this is purely about presenting one convention.
	for _, name := range []string{"listen", "otbr-url", "poll-interval", "discovery-interval", "log-level", "data-dir", "otbr-socket"} {
		if !strings.Contains(out, "--"+name) {
			t.Errorf("usage is missing the --%s flag", name)
		}
	}
	if strings.Contains(out, "\n  -listen") {
		t.Error("usage prints a single-hyphen flag name; the project documents double")
	}
	for _, def := range []string{`":8088"`, `"http://127.0.0.1:8081"`, "5s", "5m0s", `"info"`} {
		if !strings.Contains(out, def) {
			t.Errorf("usage is missing the default %s", def)
		}
	}
	// Every flag should name its environment variable, or the two can drift.
	for _, pair := range envVars {
		if !strings.Contains(out, pair[0]) {
			t.Errorf("usage is missing %s", pair[0])
		}
	}
	// envVars and bindFlags are separate lists that must not drift apart; the flag
	// count is derived from bindFlags itself so adding a flag without an env var
	// entry (or the reverse) fails here.
	registered := 0
	probe := flag.NewFlagSet("probe", flag.ContinueOnError)
	bindFlags(probe, &Config{})
	probe.VisitAll(func(*flag.Flag) { registered++ })
	if len(envVars) != registered {
		t.Errorf("envVars has %d entries but bindFlags registers %d flags", len(envVars), registered)
	}
}

func TestBothHyphenFormsParse(t *testing.T) {
	// Go's flag package treats -x and --x identically; help shows only the double
	// form, so make sure that presentation choice never becomes a parsing rule.
	for _, args := range [][]string{{"-listen", ":9101"}, {"--listen", ":9101"}} {
		cfg, err := Parse(args)
		if err != nil {
			t.Fatalf("Parse(%v) error = %v", args, err)
		}
		if cfg.Listen != ":9101" {
			t.Errorf("Parse(%v) Listen = %q", args, cfg.Listen)
		}
	}
}
