package util

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPermissions_ConstantsAndHelpers(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Verify Constants
	if DefaultUmask != 0o022 {
		t.Errorf("expected DefaultUmask 0022, got %04o", DefaultUmask)
	}
	if DefaultDirPerm != 0o755 {
		t.Errorf("expected DefaultDirPerm 0755, got %04o", DefaultDirPerm)
	}
	if DefaultFilePerm != 0o644 {
		t.Errorf("expected DefaultFilePerm 0644, got %04o", DefaultFilePerm)
	}

	// 1b. Test SetUmask
	oldUmask := SetUmask(DefaultUmask)
	defer SetUmask(oldUmask)

	// 2. Test EnsureDir
	subDir := filepath.Join(tempDir, "a", "b", "c")
	if err := EnsureDir(subDir); err != nil {
		t.Fatalf("EnsureDir failed: %v", err)
	}
	dirInfo, err := os.Stat(subDir)
	if err != nil {
		t.Fatalf("failed to stat created dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != DefaultDirPerm {
		t.Errorf("EnsureDir created perm %04o; want %04o", perm, DefaultDirPerm)
	}

	// 3. Test WriteWebFile
	filePath := filepath.Join(subDir, "test.txt")
	testData := []byte("web content")
	if err := WriteWebFile(filePath, testData); err != nil {
		t.Fatalf("WriteWebFile failed: %v", err)
	}
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("failed to stat written file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != DefaultFilePerm {
		t.Errorf("WriteWebFile created perm %04o; want %04o", perm, DefaultFilePerm)
	}

	// 4. Test EnsureDir error branch (e.g. attempting to create dir on top of file)
	if err := EnsureDir(filePath); err == nil {
		t.Errorf("expected error when creating dir over existing file, got nil")
	}

	// 5. Test WriteWebFile error branch (e.g. writing into a non-existent parent directory)
	badPath := filepath.Join(tempDir, "missing-parent", "test.txt")
	if err := WriteWebFile(badPath, testData); err == nil {
		t.Errorf("expected error writing to non-existent parent dir, got nil")
	}
}
