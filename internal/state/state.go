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
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/edsilegxrepo/repoview/internal/util"
)

// MetadataKeyRepoFingerprint is stored in StateStore to short-circuit unchanged incremental runs.
const MetadataKeyRepoFingerprint = "__repoview_fingerprint__"

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

// CheckChanged computes the SHA-256 digest of content and reports whether it differs
// from the previously persisted hash without mutating state until persistence succeeds.
func (s *StateStore) CheckChanged(filename string, content []byte) (string, bool) {
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])

	s.mu.RLock()
	oldHash, exists := s.state[filename]
	s.mu.RUnlock()

	if !exists || oldHash != hash {
		return hash, true
	}
	return hash, false
}

// MarkWritten commits the SHA-256 digest for a successfully written file and marks state dirty.
func (s *StateStore) MarkWritten(filename, hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if oldHash, exists := s.state[filename]; !exists || oldHash != hash {
		s.state[filename] = hash
		s.dirty = true
	}
}

// HasChanged checks if the content has changed compared to the stored state.
// If it has changed, it updates the state and marks dirty.
// Returns true if changed (or new).
// Concurrency optimization: SHA256 is computed outside the lock to minimize lock contention.
func (s *StateStore) HasChanged(filename string, content []byte) bool {
	hash, changed := s.CheckChanged(filename, content)
	if changed {
		s.MarkWritten(filename, hash)
		return true
	}
	return false
}

// Get retrieves a tracked hash or metadata value by key.
func (s *StateStore) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.state[key]
	return val, ok
}

// Set stores a metadata value by key and marks the state store dirty when changed.
func (s *StateStore) Set(key, val string) {
	s.MarkWritten(key, val)
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

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(s.state); err != nil {
		return fmt.Errorf("failed to encode state json: %w", err)
	}

	// #nosec G304 -- internal state cache written atomically with 0600 permissions to protect build metadata
	if err := util.AtomicWriteFile(s.path, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("failed to persist state file: %w", err)
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
