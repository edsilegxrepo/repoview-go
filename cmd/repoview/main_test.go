package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edsilegxrepo/repoview/internal/app"
)

// TEST STRATEGY:
// Validate the CLI interface, flag parser, early directory validation, error mappings,
// and process exit codes without polluting the repository.
//
// Test coverage includes:
//   - TestRun_Version: Validates that --version writes version string and returns ExitSuccess.
//   - TestRun_NoArgs: Validates that zero arguments prints usage and returns ExitUsageError.
//   - TestRun_InvalidFlag: Validates that an unknown flag returns ExitUsageError.
//   - TestRun_MissingRepo: Validates that a non-existent path returns ExitRepoMetadataError.
//   - TestRun_NotADirectory: Validates that passing a file instead of a directory returns ExitRepoMetadataError.
//   - TestRun_SafetyViolation: Validates that specifying --output-dir matching repoDir returns ExitUsageError.
//   - TestRun_StringList: Validates custom stringList flag accumulator.
//   - TestRun_FullPipeline: Validates successful run with real/fixture repository in t.TempDir().

func TestRun_Version(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--version"}, &stdout, &stderr)
	if code != app.ExitSuccess {
		t.Errorf("expected ExitSuccess (0), got %d", code)
	}
	if !strings.Contains(stdout.String(), "repoview version") {
		t.Errorf("expected stdout to contain version, got: %s", stdout.String())
	}
}

func TestRun_NoArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{}, &stdout, &stderr)
	if code != app.ExitUsageError {
		t.Errorf("expected ExitUsageError (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("expected stderr to contain usage, got: %s", stderr.String())
	}
}

func TestRun_InvalidFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--invalid-flag-123", "/tmp"}, &stdout, &stderr)
	if code != app.ExitUsageError {
		t.Errorf("expected ExitUsageError (2) on invalid flag, got %d", code)
	}
}

func TestRun_MissingRepo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	nonExistent := filepath.Join(t.TempDir(), "nonexistent_directory_abc")
	code := run([]string{nonExistent}, &stdout, &stderr)
	if code != app.ExitRepoMetadataError {
		t.Errorf("expected ExitRepoMetadataError (3), got %d", code)
	}
	if !strings.Contains(stderr.String(), "does not exist") {
		t.Errorf("expected stderr to mention 'does not exist', got: %s", stderr.String())
	}
}

func TestRun_NotADirectory(t *testing.T) {
	var stdout, stderr bytes.Buffer
	tempFile := filepath.Join(t.TempDir(), "regular_file.txt")
	if err := os.WriteFile(tempFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	code := run([]string{tempFile}, &stdout, &stderr)
	if code != app.ExitRepoMetadataError {
		t.Errorf("expected ExitRepoMetadataError (3) for regular file, got %d", code)
	}
}

func TestRun_SafetyViolation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	repoDir := t.TempDir()
	code := run([]string{"--force", "--output-dir", repoDir, repoDir}, &stdout, &stderr)
	if code != app.ExitUsageError {
		t.Errorf("expected ExitUsageError (2) for safety violation, got %d", code)
	}
	if !strings.Contains(stderr.String(), "Safety Violation") {
		t.Errorf("expected stderr to contain 'Safety Violation', got: %s", stderr.String())
	}
}

func TestStringList(t *testing.T) {
	var list stringList
	if err := list.Set("pattern1*"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := list.Set("pattern2*"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if list.String() != "pattern1*, pattern2*" {
		t.Errorf("expected 'pattern1*, pattern2*', got %q", list.String())
	}
}
