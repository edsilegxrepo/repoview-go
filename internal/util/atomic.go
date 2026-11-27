package util

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// AtomicWriteFile writes content to an ephemeral sibling temporary file in destPath's directory,
// sets permissions, flushes via fsync, closes, and atomically renames into place.
// On Windows, it falls back to remove-and-rename if an existing destination blocks direct replacement.
func AtomicWriteFile(destPath string, content []byte, perm os.FileMode) error {
	cleanDest := filepath.Clean(destPath)
	dir := filepath.Dir(cleanDest)

	tmpFile, err := os.CreateTemp(dir, "."+filepath.Base(cleanDest)+".*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file for %s: %w", cleanDest, err)
	}
	tmpPath := tmpFile.Name()
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			_ = os.Remove(tmpPath)
		}
	}()

	if runtime.GOOS != "windows" {
		// #nosec G302 -- callers pass explicit permissions (0644 for public web assets, 0600 for private state)
		if err := os.Chmod(tmpPath, perm); err != nil {
			_ = tmpFile.Close()
			return fmt.Errorf("failed to chmod temp file for %s: %w", cleanDest, err)
		}
	}

	if _, err := tmpFile.Write(content); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to write temp file for %s: %w", cleanDest, err)
	}

	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to sync temp file for %s: %w", cleanDest, err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file for %s: %w", cleanDest, err)
	}

	if err := os.Rename(tmpPath, cleanDest); err != nil {
		if runtime.GOOS == "windows" {
			_ = os.Remove(cleanDest)
			if err2 := os.Rename(tmpPath, cleanDest); err2 == nil {
				cleanupTemp = false
				return nil
			}
		}
		return fmt.Errorf("failed to atomically replace %s: %w", cleanDest, err)
	}

	cleanupTemp = false
	return nil
}
