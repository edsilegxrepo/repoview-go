package util

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveContainedPath(t *testing.T) {
	baseDir := t.TempDir()
	outsideDir := t.TempDir()

	// 1. Valid nested path (even when file/subdir does not exist yet)
	got, err := ResolveContainedPath(baseDir, "repodata/primary.sqlite.gz")
	if err != nil {
		t.Fatalf("expected valid path to succeed, got: %v", err)
	}
	want := filepath.Join(baseDir, "repodata", "primary.sqlite.gz")
	if !SamePath(got, want) {
		t.Errorf("got %q; want %q", got, want)
	}

	// 2. Lexical path traversal
	for _, evil := range []string{"../escape.html", "a/../../escape.html", "", "/etc/passwd"} {
		if _, err := ResolveContainedPath(baseDir, evil); err == nil {
			t.Errorf("expected path %q to be rejected", evil)
		}
	}

	// 3. Symlink escape (skipped on platforms without symlink privilege)
	secretFile := filepath.Join(outsideDir, "secret.txt")
	_ = os.WriteFile(secretFile, []byte("secret"), 0o600)
	symlinkPath := filepath.Join(baseDir, "linked_out")
	if err := os.Symlink(outsideDir, symlinkPath); err == nil {
		if _, err := ResolveContainedPath(baseDir, "linked_out/secret.txt"); err == nil {
			t.Errorf("expected symlink escape to be rejected")
		} else if !strings.Contains(err.Error(), "security violation") {
			t.Errorf("expected security violation error, got: %v", err)
		}
	}
}

func TestIsPathContained_And_SamePath(t *testing.T) {
	base := filepath.Join(t.TempDir(), "root")
	child := filepath.Join(base, "sub", "file.html")
	sibling := filepath.Join(filepath.Dir(base), "root2")

	if !IsPathContained(base, base) {
		t.Errorf("expected base to be contained in itself")
	}
	if !IsPathContained(base, child) {
		t.Errorf("expected child to be contained in base")
	}
	if IsPathContained(base, sibling) {
		t.Errorf("expected sibling not to be contained in base")
	}
	if !SamePath(base, base+string(filepath.Separator)) {
		t.Errorf("expected SamePath to treat trailing separator as identical")
	}
}
