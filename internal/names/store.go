// Package names provides a small, file-backed store of user-assigned device
// labels keyed by Thread extended address. It is the only component that
// persists local state; OTBR itself is never written to.
package names

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

// MaxNameLength bounds a stored label so a single entry cannot grow unbounded.
const MaxNameLength = 48

var extPattern = regexp.MustCompile(`^[0-9a-f]{1,32}$`)

// ErrDisabled is returned by mutating methods when no data directory is
// configured, so naming degrades to read-only rather than failing loudly.
var ErrDisabled = errors.New("device naming is disabled: no data directory configured")

// Store is a concurrency-safe map of extended address to display name backed by
// a JSON file. The zero value is not usable; call New.
type Store struct {
	mu      sync.RWMutex
	path    string
	names   map[string]string
	enabled bool
}

// New creates a store. When dataDir is empty the store is disabled: reads
// return no names and writes return ErrDisabled.
func New(dataDir string) *Store {
	s := &Store{names: map[string]string{}}
	if strings.TrimSpace(dataDir) != "" {
		s.path = filepath.Join(dataDir, "names.json")
		s.enabled = true
	}
	return s
}

// Enabled reports whether the store can persist names.
func (s *Store) Enabled() bool { return s.enabled }

// Load reads the backing file into memory. A missing file is not an error.
func (s *Store) Load() error {
	if !s.enabled {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read names file: %w", err)
	}
	stored := map[string]string{}
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("decode names file: %w", err)
	}
	cleaned := make(map[string]string, len(stored))
	for ext, name := range stored {
		key, keyErr := normalizeExt(ext)
		value, valErr := normalizeName(name)
		if keyErr != nil || valErr != nil {
			continue
		}
		cleaned[key] = value
	}
	s.mu.Lock()
	s.names = cleaned
	s.mu.Unlock()
	return nil
}

// Snapshot returns a copy of the current extended-address to name mapping.
func (s *Store) Snapshot() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.names))
	for ext, name := range s.names {
		out[ext] = name
	}
	return out
}

// Set assigns name to the given extended address and persists the change. An
// empty name deletes the entry.
func (s *Store) Set(ext, name string) error {
	if !s.enabled {
		return ErrDisabled
	}
	key, err := normalizeExt(ext)
	if err != nil {
		return err
	}
	value, err := normalizeName(name)
	if err != nil {
		return err
	}
	if value == "" {
		return s.Delete(ext)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.names[key] = value
	return s.persistLocked()
}

// Delete removes any name assigned to the given extended address.
func (s *Store) Delete(ext string) error {
	if !s.enabled {
		return ErrDisabled
	}
	key, err := normalizeExt(ext)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.names[key]; !ok {
		return nil
	}
	delete(s.names, key)
	return s.persistLocked()
}

// persistLocked writes the map to disk atomically. Callers must hold s.mu.
func (s *Store) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	data, err := json.MarshalIndent(s.names, "", "  ")
	if err != nil {
		return fmt.Errorf("encode names: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "names-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp names file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp names file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp names file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace names file: %w", err)
	}
	return nil
}

// normalizeExt lowercases an extended address and strips an optional 0x prefix.
func normalizeExt(ext string) (string, error) {
	clean := strings.ToLower(strings.TrimSpace(ext))
	clean = strings.TrimPrefix(clean, "0x")
	if !extPattern.MatchString(clean) {
		return "", fmt.Errorf("invalid extended address %q", ext)
	}
	return clean, nil
}

// normalizeName trims a label, rejects control characters, and enforces the
// length limit. A blank result is valid and signals deletion.
func normalizeName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", nil
	}
	if len([]rune(trimmed)) > MaxNameLength {
		return "", fmt.Errorf("name exceeds %d characters", MaxNameLength)
	}
	for _, r := range trimmed {
		if r != '\t' && unicode.IsControl(r) {
			return "", errors.New("name contains control characters")
		}
	}
	return trimmed, nil
}
