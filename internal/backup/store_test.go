package backup

import "testing"

func TestSaveMetaTLVAndReload(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := s.Save("0e08abcd", "OpenThread-a1b2"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if m := s.Meta(); !m.Present || m.NetworkName != "OpenThread-a1b2" || m.SavedAt.IsZero() {
		t.Fatalf("Meta() = %+v", m)
	}
	if s.TLV() != "0e08abcd" {
		t.Errorf("TLV() = %q", s.TLV())
	}

	reloaded := New(dir)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if reloaded.TLV() != "0e08abcd" || !reloaded.Meta().Present {
		t.Error("snapshot did not survive reload")
	}
}

func TestSaveBlankTLVIgnored(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save("   ", "x"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if s.Meta().Present {
		t.Error("blank TLV should not create a snapshot")
	}
}

func TestSaveReplacesPrevious(t *testing.T) {
	s := New(t.TempDir())
	_ = s.Save("aaaa", "First")
	_ = s.Save("bbbb", "Second")
	if s.TLV() != "bbbb" || s.Meta().NetworkName != "Second" {
		t.Errorf("expected latest snapshot, got TLV=%q name=%q", s.TLV(), s.Meta().NetworkName)
	}
}

func TestDisabledWithoutDataDir(t *testing.T) {
	s := New("")
	if s.Enabled() {
		t.Fatal("store should be disabled without a data dir")
	}
	if err := s.Save("aaaa", "x"); err != ErrDisabled {
		t.Errorf("Save() error = %v, want ErrDisabled", err)
	}
	if s.Meta().Present || s.TLV() != "" {
		t.Error("disabled store should hold nothing")
	}
}
