package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TEST STRATEGY:
// Verify the catastrophic repository deletion safety guards implemented in
// validateOutputDirSafety().
//
// Test coverage includes:
//   - Disallowing outputDir == repoDir (prevents wiping source packages when --force is passed).
//   - Disallowing outputDir as an ancestor/parent of repoDir (prevents wiping parent trees).
//   - Disallowing outputDir as the filesystem root ("/" or drive root).
//   - Disallowing outputDir if it already contains a "repodata/" directory (guards against misconfigured paths).
//   - Allowing safe, isolated output directories (e.g. repoDir/repoview or distinct temp directories).

func TestValidateOutputDirSafety(t *testing.T) {
	tempDir := t.TempDir()

	repoDir := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}

	// 1. Same directory must fail with safety error
	if err := validateOutputDirSafety(repoDir, repoDir); err == nil {
		t.Errorf("expected error when outputDir == repoDir, got nil")
	}

	// 2. Parent directory must fail with safety error
	if err := validateOutputDirSafety(repoDir, tempDir); err == nil {
		t.Errorf("expected error when outputDir is parent of repoDir, got nil")
	}

	// 3. Root directory must fail with safety error
	if err := validateOutputDirSafety(repoDir, "/"); err == nil {
		t.Errorf("expected error when outputDir is root '/', got nil")
	}

	// 4. Directory containing repodata must fail with safety error
	fakeRepo := filepath.Join(tempDir, "other_repo")
	_ = os.MkdirAll(filepath.Join(fakeRepo, "repodata"), 0o755)
	if err := validateOutputDirSafety(repoDir, fakeRepo); err == nil {
		t.Errorf("expected error when outputDir contains repodata, got nil")
	}

	// 5. Normal output directory must succeed
	validOut := filepath.Join(repoDir, "repoview")
	if err := validateOutputDirSafety(repoDir, validOut); err != nil {
		t.Errorf("expected valid outputDir to succeed, got: %v", err)
	}
}
