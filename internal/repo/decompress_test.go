package repo

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// TEST STRATEGY:
// Verify multi-format archive decompression across gzip, xz, zstd, and uncompressed files,
// ensuring cleanup closures delete temporary files and format checks function accurately.

func TestDecompressFile_Gzip(t *testing.T) {
	tempDir := t.TempDir()
	gzPath := filepath.Join(tempDir, "test.txt.gz")

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	originalContent := "hello compressed world"
	if _, err := gw.Write([]byte(originalContent)); err != nil {
		t.Fatalf("failed to write gzip: %v", err)
	}
	_ = gw.Close()

	if err := os.WriteFile(gzPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	decompPath, cleanup, err := DecompressFile(gzPath)
	if err != nil {
		t.Fatalf("DecompressFile failed: %v", err)
	}

	content, err := os.ReadFile(decompPath)
	if err != nil {
		t.Fatalf("failed to read decompressed file: %v", err)
	}
	if string(content) != originalContent {
		t.Errorf("expected %q, got %q", originalContent, string(content))
	}

	// Verify cleanup
	cleanup()
	if _, err := os.Stat(decompPath); !os.IsNotExist(err) {
		t.Errorf("cleanup did not remove temporary file %s", decompPath)
	}
}

func TestDecompressFile_Zstd(t *testing.T) {
	tempDir := t.TempDir()
	zstPath := filepath.Join(tempDir, "test.txt.zst")

	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("failed to create zstd writer: %v", err)
	}
	originalContent := "zstd compressed content"
	if _, err := zw.Write([]byte(originalContent)); err != nil {
		t.Fatalf("failed to write zstd: %v", err)
	}
	_ = zw.Close()

	if err := os.WriteFile(zstPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	decompPath, cleanup, err := DecompressFile(zstPath)
	if err != nil {
		t.Fatalf("DecompressFile failed: %v", err)
	}
	defer cleanup()

	content, err := os.ReadFile(decompPath)
	if err != nil {
		t.Fatalf("failed to read decompressed file: %v", err)
	}
	if string(content) != originalContent {
		t.Errorf("expected %q, got %q", originalContent, string(content))
	}
}

func TestDecompressFile_XZ(t *testing.T) {
	tempDir := t.TempDir()
	xzPath := filepath.Join(tempDir, "test.txt.xz")

	var buf bytes.Buffer
	xzw, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatalf("failed to create xz writer: %v", err)
	}
	originalContent := "xz compressed content"
	if _, err := xzw.Write([]byte(originalContent)); err != nil {
		t.Fatalf("failed to write xz: %v", err)
	}
	_ = xzw.Close()

	if err := os.WriteFile(xzPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	decompPath, cleanup, err := DecompressFile(xzPath)
	if err != nil {
		t.Fatalf("DecompressFile failed: %v", err)
	}
	defer cleanup()

	content, err := os.ReadFile(decompPath)
	if err != nil {
		t.Fatalf("failed to read decompressed file: %v", err)
	}
	if string(content) != originalContent {
		t.Errorf("expected %q, got %q", originalContent, string(content))
	}
}

func TestDecompressFile_Uncompressed(t *testing.T) {
	tempDir := t.TempDir()
	plainPath := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(plainPath, []byte("plain"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	decompPath, cleanup, err := DecompressFile(plainPath)
	if err != nil {
		t.Fatalf("DecompressFile failed: %v", err)
	}
	defer cleanup()

	if decompPath != plainPath {
		t.Errorf("expected uncompressed path %q, got %q", plainPath, decompPath)
	}
}

func TestDecompressFile_Corrupt(t *testing.T) {
	tempDir := t.TempDir()
	corruptGz := filepath.Join(tempDir, "corrupt.gz")
	if err := os.WriteFile(corruptGz, []byte("not a gzip file"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, _, err := DecompressFile(corruptGz)
	if err == nil {
		t.Errorf("expected error when decompressing corrupt gzip, got nil")
	}
}

func TestIsCompressed(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"file.gz", true},
		{"file.bz2", true},
		{"file.xz", true},
		{"file.zst", true},
		{"file.zstd", true},
		{"file.sqlite", false},
		{"file.xml", false},
		{"file.rpm", false},
	}

	for _, tt := range tests {
		got := IsCompressed(tt.path)
		if got != tt.expected {
			t.Errorf("IsCompressed(%q) = %v; want %v", tt.path, got, tt.expected)
		}
	}
}
