package state

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TEST STRATEGY:
// Verify incremental state persistence, concurrency guarantees, and atomic file safety.
//
// Test coverage includes:
//   - TestStateStoreAtomicSave:
//       * Detection of new files as changed (HasChanged == true).
//       * Detection of identical content as unchanged (HasChanged == false).
//       * Detection of modified content as changed (HasChanged == true).
//       * Atomic persistence to disk via temporary file rename and temp file cleanup.
//       * State reloading from disk to confirm persistent hash fidelity.
//   - TestStateStoreConcurrency:
//       * 50 concurrent goroutines executing HasChanged() simultaneously to validate
//         thread safety and confirm zero race conditions under `go test -race`.

func TestStateStoreAtomicSave(t *testing.T) {
	tempDir := t.TempDir()

	statePath := filepath.Join(tempDir, "state.json")
	store, err := NewStateStore(statePath)
	if err != nil {
		t.Fatalf("NewStateStore failed: %v", err)
	}

	content := []byte("hello world")
	if !store.HasChanged("page1.html", content) {
		t.Errorf("expected HasChanged to return true for new file")
	}

	// Unchanged content should return false
	if store.HasChanged("page1.html", content) {
		t.Errorf("expected HasChanged to return false for identical content")
	}

	// Different content should return true
	if !store.HasChanged("page1.html", []byte("hello world modified")) {
		t.Errorf("expected HasChanged to return true for modified content")
	}

	// Save atomically
	if err := store.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify temporary file is cleaned up
	tmpPath := statePath + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("temporary state file was not cleaned up: %s", tmpPath)
	}

	// Reload state store from disk
	reloaded, err := NewStateStore(statePath)
	if err != nil {
		t.Fatalf("reloading state store failed: %v", err)
	}

	if reloaded.HasChanged("page1.html", []byte("hello world modified")) {
		t.Errorf("expected reloaded state to recognize unchanged content")
	}
}

func TestStateStoreConcurrency(t *testing.T) {
	tempDir := t.TempDir()

	statePath := filepath.Join(tempDir, "state.json")
	store, err := NewStateStore(statePath)
	if err != nil {
		t.Fatalf("NewStateStore failed: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			filename := "file.html"
			data := []byte("concurrent content")
			_ = store.HasChanged(filename, data)
		}()
	}
	wg.Wait()
}

func TestStateStore_StaleFiles_And_Remove(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "state.json")
	store, err := NewStateStore(statePath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	store.HasChanged("kept.html", []byte("kept content"))
	store.HasChanged("stale1.html", []byte("stale1 content"))
	store.HasChanged("stale2.html", []byte("stale2 content"))

	stale := store.GetStaleFiles([]string{"kept.html"})
	if len(stale) != 2 {
		t.Fatalf("expected 2 stale files, got %d: %v", len(stale), stale)
	}

	// Remove stale1 and re-verify
	store.Remove("stale1.html")
	staleAfter := store.GetStaleFiles([]string{"kept.html"})
	if len(staleAfter) != 1 || staleAfter[0] != "stale2.html" {
		t.Errorf("expected only stale2.html remaining, got: %v", staleAfter)
	}
}

func TestStateStore_CorruptJSON_Recovery(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "state.json")

	// Write invalid JSON
	if err := os.WriteFile(statePath, []byte("{invalid json corrupt"), 0o644); err != nil {
		t.Fatalf("failed to write corrupt file: %v", err)
	}

	store, err := NewStateStore(statePath)
	if err != nil {
		t.Fatalf("expected NewStateStore to recover cleanly from corrupted JSON, got: %v", err)
	}

	// Any new item should be recognized as changed
	if !store.HasChanged("recovered.html", []byte("content")) {
		t.Errorf("expected HasChanged to return true for recovered store")
	}

	// Save when dirty should overwrite corrupted file cleanly
	if err := store.Save(); err != nil {
		t.Fatalf("expected Save to succeed: %v", err)
	}

	// Save again when clean (not dirty) should be a no-op
	if err := store.Save(); err != nil {
		t.Fatalf("expected Save when clean to succeed: %v", err)
	}
}

func TestStateStore_CheckChangedAndMarkWritten(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	store, err := NewStateStore(statePath)
	if err != nil {
		t.Fatalf("NewStateStore failed: %v", err)
	}

	content := []byte("<html>page</html>")
	hash, changed := store.CheckChanged("page.html", content)
	if !changed || hash == "" {
		t.Fatalf("expected CheckChanged to report changed=true with hash, got changed=%v hash=%q", changed, hash)
	}
	// Must NOT mutate state until MarkWritten is explicitly called
	if _, stillChanged := store.CheckChanged("page.html", content); !stillChanged {
		t.Errorf("expected CheckChanged not to persist hash prior to MarkWritten")
	}

	store.MarkWritten("page.html", hash)
	if _, afterMarkChanged := store.CheckChanged("page.html", content); afterMarkChanged {
		t.Errorf("expected CheckChanged to report false after MarkWritten")
	}

	store.Set(MetadataKeyRepoFingerprint, "fp-123")
	if gotFP, ok := store.Get(MetadataKeyRepoFingerprint); !ok || gotFP != "fp-123" {
		t.Errorf("expected Get/Set fingerprint fp-123, got %q (ok=%v)", gotFP, ok)
	}

	// MarkWrittenBatch with empty slice + populated entries
	store.MarkWrittenBatch(nil)
	store.MarkWrittenBatch([]Entry{
		{Filename: "batch1.html", Hash: "h1"},
		{Filename: "batch2.html", Hash: "h2"},
	})
	if h, ok := store.Get("batch1.html"); !ok || h != "h1" {
		t.Errorf("expected batch1.html hash h1, got %q (ok=%v)", h, ok)
	}
	if err := store.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	reloaded, err := NewStateStore(statePath)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if h, ok := reloaded.Get("batch2.html"); !ok || h != "h2" {
		t.Errorf("expected persisted batch2.html hash h2, got %q (ok=%v)", h, ok)
	}
}
