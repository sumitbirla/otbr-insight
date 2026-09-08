// Package backup persists a single snapshot of the previous Thread operational
// dataset (as TLV hex) so a destructive network change can be undone. The
// snapshot contains the network key in plaintext; it lives only in the local
// data directory alongside device names.
package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrDisabled is returned when no data directory is configured.
var ErrDisabled = errors.New("dataset backup is disabled: no data directory configured")

// Meta is the non-sensitive description of a stored snapshot (never the TLV).
type Meta struct {
	Present     bool
	NetworkName string
	SavedAt     time.Time
}

type snapshot struct {
	TLV         string    `json:"tlv"`
	NetworkName string    `json:"networkName"`
	SavedAt     time.Time `json:"savedAt"`
}

// Store holds at most one dataset snapshot, backed by a JSON file.
type Store struct {
	mu      sync.RWMutex
	path    string
	enabled bool
	snap    *snapshot
}

// New creates a store. An empty dataDir disables it.
func New(dataDir string) *Store {
	s := &Store{}
	if strings.TrimSpace(dataDir) != "" {
		s.path = filepath.Join(dataDir, "dataset-backup.json")
		s.enabled = true
	}
	return s
}

// Enabled reports whether snapshots can be persisted.
func (s *Store) Enabled() bool { return s.enabled }

// Load reads any existing snapshot. A missing file is not an error.
func (s *Store) Load() error {
	if !s.enabled {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read dataset backup: %w", err)
	}
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("decode dataset backup: %w", err)
	}
	if snap.TLV == "" {
		return nil
	}
	s.mu.Lock()
	s.snap = &snap
	s.mu.Unlock()
	return nil
}

// Save records the current dataset before a destructive change, replacing any
// prior snapshot. A blank TLV is ignored (nothing to back up).
func (s *Store) Save(tlv, networkName string) error {
	if !s.enabled {
		return ErrDisabled
	}
	if strings.TrimSpace(tlv) == "" {
		return nil
	}
	snap := &snapshot{TLV: strings.TrimSpace(tlv), NetworkName: networkName, SavedAt: time.Now().UTC()}
	if err := s.persist(snap); err != nil {
		return err
	}
	s.mu.Lock()
	s.snap = snap
	s.mu.Unlock()
	return nil
}

// Meta returns the snapshot description without the sensitive TLV.
func (s *Store) Meta() Meta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snap == nil {
		return Meta{Present: false}
	}
	return Meta{Present: true, NetworkName: s.snap.NetworkName, SavedAt: s.snap.SavedAt}
}

// TLV returns the stored dataset TLV for server-side restore. It is never sent
// on a read/list response.
func (s *Store) TLV() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snap == nil {
		return ""
	}
	return s.snap.TLV
}

func (s *Store) persist(snap *snapshot) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "dataset-backup-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp backup file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp backup file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp backup file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace backup file: %w", err)
	}
	return nil
}
