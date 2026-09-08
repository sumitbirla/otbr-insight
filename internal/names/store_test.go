package names

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreSetGetPersist(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := s.Set("0x0708090A0B0C0D0E", "  Living room  "); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if got := s.Snapshot()["0708090a0b0c0d0e"]; got != "Living room" {
		t.Errorf("stored name = %q, want trimmed/lowercased-key %q", got, "Living room")
	}

	reloaded := New(dir)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := reloaded.Snapshot()["0708090a0b0c0d0e"]; got != "Living room" {
		t.Errorf("reloaded name = %q", got)
	}
}

func TestStoreEmptyNameDeletes(t *testing.T) {
	s := New(t.TempDir())
	_ = s.Set("aabb", "Temp")
	if err := s.Set("aabb", "   "); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if _, ok := s.Snapshot()["aabb"]; ok {
		t.Error("blank name should delete the entry")
	}
}

func TestStoreRejectsInvalidInput(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Set("nothex!", "Name"); err == nil {
		t.Error("expected error for non-hex extended address")
	}
	if err := s.Set("aabb", strings.Repeat("x", MaxNameLength+1)); err == nil {
		t.Error("expected error for over-long name")
	}
	if err := s.Set("aabb", "bad\x07bell"); err == nil {
		t.Error("expected error for control characters")
	}
}

func TestStoreDisabledWithoutDataDir(t *testing.T) {
	s := New("")
	if s.Enabled() {
		t.Fatal("store should be disabled without a data dir")
	}
	if err := s.Set("aabb", "Name"); err != ErrDisabled {
		t.Errorf("Set() error = %v, want ErrDisabled", err)
	}
	if err := s.Load(); err != nil {
		t.Errorf("Load() on disabled store = %v", err)
	}
}

func TestStoreLoadMissingFileIsOK(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "does-not-exist-yet"))
	if err := s.Load(); err != nil {
		t.Errorf("Load() with missing file = %v", err)
	}
}
