package util

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "test.txt")

	if err := AtomicWriteFile(target, []byte("hello v1"), DefaultFilePerm); err != nil {
		t.Fatalf("AtomicWriteFile failed: %v", err)
	}
	if err := AtomicWriteFile(target, []byte("hello v2"), DefaultFilePerm); err != nil {
		t.Fatalf("AtomicWriteFile overwrite failed: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello v2" {
		t.Errorf("expected 'hello v2', got %q", string(got))
	}

	if err := AtomicWriteFile(filepath.Join(dir, "missing", "child.txt"), []byte("x"), DefaultFilePerm); err == nil {
		t.Errorf("expected error writing into non-existent directory")
	}
}

func TestClampWorkersAndForEachConcurrent(t *testing.T) {
	if got := ClampWorkers(0, 0); got != 0 {
		t.Errorf("expected 0 for 0 items, got %d", got)
	}
	if got := ClampWorkers(99, 20); got != MaxDefaultWorkers {
		t.Errorf("expected MaxDefaultWorkers (%d), got %d", MaxDefaultWorkers, got)
	}
	if got := ClampWorkers(8, 3); got != 3 {
		t.Errorf("expected 3, got %d", got)
	}
	if got := ClampWorkers(-5, 100); got < 1 || got > MaxDefaultWorkers {
		t.Errorf("expected clamped default worker count, got %d", got)
	}

	ForEachConcurrent([]string(nil), 4, func(int, string) {
		t.Errorf("callback should not run for empty slice")
	})

	items := []string{"a", "b", "c", "d", "e"}
	var sum atomic.Int32
	ForEachConcurrent(items, 3, func(idx int, _ string) {
		sum.Add(int32(idx + 1))
	})
	if sum.Load() != 15 {
		t.Errorf("expected index sum 15, got %d", sum.Load())
	}
}

func TestIsCompressedAndDecompressFile(t *testing.T) {
	for _, p := range []string{"a.gz", "a.bz2", "a.xz", "a.zst", "a.zstd"} {
		if !IsCompressed(p) {
			t.Errorf("expected IsCompressed(%q) to be true", p)
		}
	}
	if IsCompressed("primary.xml") {
		t.Errorf("expected IsCompressed(\"primary.xml\") to be false")
	}

	dir := t.TempDir()
	payload := []byte("<repodata>sample payload</repodata>")

	// 1. Plain XML -> returns original path directly
	plainPath := filepath.Join(dir, "primary.xml")
	if err := os.WriteFile(plainPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	outPath, cleanup, err := DecompressFile(plainPath)
	if err != nil || outPath != plainPath {
		t.Fatalf("plain DecompressFile failed: out=%q err=%v", outPath, err)
	}
	cleanup()

	// 2. Gzip
	gzPath := filepath.Join(dir, "primary.xml.gz")
	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	_, _ = gw.Write(payload)
	_ = gw.Close()
	_ = os.WriteFile(gzPath, gzBuf.Bytes(), 0o644)
	if p, c, err := DecompressFile(gzPath); err != nil {
		t.Fatalf("gz DecompressFile failed: %v", err)
	} else {
		defer c()
		got, _ := os.ReadFile(p)
		if !bytes.Equal(got, payload) {
			t.Errorf("gz payload mismatch: %q", string(got))
		}
	}

	// 3. Zstd
	zstPath := filepath.Join(dir, "primary.xml.zst")
	var zstBuf bytes.Buffer
	zw, _ := zstd.NewWriter(&zstBuf)
	_, _ = zw.Write(payload)
	_ = zw.Close()
	_ = os.WriteFile(zstPath, zstBuf.Bytes(), 0o644)
	if p, c, err := DecompressFile(zstPath); err != nil {
		t.Fatalf("zst DecompressFile failed: %v", err)
	} else {
		defer c()
		got, _ := os.ReadFile(p)
		if !bytes.Equal(got, payload) {
			t.Errorf("zst payload mismatch: %q", string(got))
		}
	}

	// 4. XZ
	xzPath := filepath.Join(dir, "primary.xml.xz")
	var xzBuf bytes.Buffer
	xw, _ := xz.NewWriter(&xzBuf)
	_, _ = xw.Write(payload)
	_ = xw.Close()
	_ = os.WriteFile(xzPath, xzBuf.Bytes(), 0o644)
	if p, c, err := DecompressFile(xzPath); err != nil {
		t.Fatalf("xz DecompressFile failed: %v", err)
	} else {
		defer c()
		got, _ := os.ReadFile(p)
		if !bytes.Equal(got, payload) {
			t.Errorf("xz payload mismatch: %q", string(got))
		}
	}

	// 5. Corrupt gzip error handling
	corruptGZ := filepath.Join(dir, "corrupt.xml.gz")
	_ = os.WriteFile(corruptGZ, []byte("not valid gzip bytes"), 0o644)
	if _, _, err := DecompressFile(corruptGZ); err == nil {
		t.Errorf("expected error for corrupt gzip archive")
	}

	// 6. Missing file
	if _, _, err := DecompressFile(filepath.Join(dir, "missing.gz")); err == nil {
		t.Errorf("expected error for missing file")
	}
}

func TestHumanSizeAndMisc(t *testing.T) {
	if s := HumanSize(512); !strings.Contains(s, "512") {
		t.Errorf("unexpected HumanSize(512): %q", s)
	}
	if s := HumanSize(2048); !strings.Contains(s, "KB") && !strings.Contains(s, "KiB") {
		t.Errorf("unexpected HumanSize(2048): %q", s)
	}
	if s := HumanSize(5 * 1024 * 1024); !strings.Contains(s, "MB") && !strings.Contains(s, "MiB") {
		t.Errorf("unexpected HumanSize(5MB): %q", s)
	}
	if s := HumanSize(3 * 1024 * 1024 * 1024); !strings.Contains(s, "GB") && !strings.Contains(s, "GiB") {
		t.Errorf("unexpected HumanSize(3GB): %q", s)
	}
}
