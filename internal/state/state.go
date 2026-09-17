// Package state provides persistent, incremental build state tracking for repoview-go.
//
// OBJECTIVES:
// Track SHA-256 content hashes of all generated HTML pages, RSS feeds, and search indices
// across runs. This enables incremental regeneration (skipping unchanged package pages)
// and automated pruning of orphaned/stale files from previous runs.
//
// CORE COMPONENTS:
//   - StateStore: Thread-safe, atomic disk-persisted key-value store mapping output filenames
//     to their SHA-256 hex digests.
//
// FUNCTIONALITY:
//   - Concurrency safety: RWMutex guards state mutations across parallel rendering workers.
//   - Performance optimization: SHA-256 digests are computed prior to acquiring write locks,
//     minimizing lock contention during high-volume generation.
//   - Fault tolerance & atomicity: State JSON files are written to a temporary sibling file (.tmp),
//     flushed to disk via fsync, and atomically renamed into place to prevent 0-byte state
//     corruption on abrupt process termination.
//   - Stale file discovery: Compares active files generated in the current run against historical
//     state records to identify removed or renamed packages.
//
// DATA FLOW:
//
//	Generated page bytes -> SHA-256 hash -> StateStore.HasChanged() -> Render/Skip -> StateStore.Save() -> state.json
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// StateStore manages the incremental build state to optimize subsequent runs.
// It maps filenames to their SHA256 hashes to detect content changes.
// It is safe for concurrent access, allowing parallel workers to check state without races.
type StateStore struct {
	path  string            // Filesystem destination path for the JSON state file
	state map[string]string // filename -> sha256 hash
	mu    sync.RWMutex      // Guards concurrent access to state map and dirty flag
	dirty bool              // Indicates unpersisted changes exist in memory
}

// NewStateStore loads an existing state file or initializes a new one.
// If the file doesn't exist or is corrupted, it starts with an empty state.
func NewStateStore(path string) (*StateStore, error) {
	s := &StateStore{
		path:  path,
		state: make(map[string]string),
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return s, nil
	}

	// #nosec G304 -- path is validated destination for internal state cache
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("failed to open state file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	if err := json.NewDecoder(f).Decode(&s.state); err != nil {
		// If corrupted, return empty state and mark dirty so it regenerates cleanly
		s.dirty = true
		return s, nil
	}

	return s, nil
}

// HasChanged checks if the content has changed compared to the stored state.
// If it has changed, it updates the state and marks dirty.
// Returns true if changed (or new).
// Concurrency optimization: SHA256 is computed outside the lock to minimize lock contention.
func (s *StateStore) HasChanged(filename string, content []byte) bool {
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])

	s.mu.Lock()
	defer s.mu.Unlock()

	oldHash, exists := s.state[filename]
	if !exists || oldHash != hash {
		s.state[filename] = hash
		s.dirty = true
		return true
	}

	return false
}

// Save persists the current state map to the JSON file on disk using atomic write-then-rename.
// It only writes if the state is marked as dirty (i.e., changes occurred).
// It writes to a temporary file first and flushes/syncs to prevent corrupt 0-byte state files on interruption.
func (s *StateStore) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.dirty {
		return nil
	}

	tmpPath := s.path + ".tmp"
	// #nosec G304 -- tmpPath is internal state store path; permissions 0600 prevent unauthorized state access
	f, err := os.OpenFile(filepath.Clean(tmpPath), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create temporary state file: %w", err)
	}

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(s.state); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to encode state json: %w", err)
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to sync state file: %w", err)
	}
	_ = f.Close()

	// Atomic rename to destination
	if err := os.Rename(tmpPath, s.path); err != nil {
		// Fallback for Windows if destination exists and cannot be replaced directly
		_ = os.Remove(s.path)
		if err2 := os.Rename(tmpPath, s.path); err2 != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("failed to atomically replace state file: %w", err2)
		}
	}

	s.dirty = false
	return nil
}

// GetStaleFiles identifies files that are present in the state database
// but were not generated in the current run. These files should be deleted.
func (s *StateStore) GetStaleFiles(kept []string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keptMap := make(map[string]bool, len(kept))
	for _, k := range kept {
		keptMap[k] = true
	}

	var stale []string
	for filename := range s.state {
		if !keptMap[filename] {
			stale = append(stale, filename)
		}
	}
	return stale
}

// Remove deletes an entry from the state map and marks the store as dirty.
// This is typically called after deleting a stale file from disk.
func (s *StateStore) Remove(filename string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state, filename)
	s.dirty = true
}
