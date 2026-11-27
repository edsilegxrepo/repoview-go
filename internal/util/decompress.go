package util

import (
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// MaxDecompressedSize defines the maximum size (4 GiB) allowed for decompressed metadata
// to protect against decompression bomb / zip-bomb denial of service attacks.
const MaxDecompressedSize = 4 * 1024 * 1024 * 1024

var (
	// ErrDecompressBomb indicates a compressed stream exceeded MaxDecompressedSize.
	ErrDecompressBomb = errors.New("decompressed payload exceeds safety ceiling")
	// ErrDecompressIO indicates a filesystem error creating temporary scratch storage.
	ErrDecompressIO = errors.New("decompression scratch I/O error")
)

// DecompressFile inspects path by extension (.gz, .bz2, .xz, .zst, .zstd) and streams
// decompressed bytes to an OS temporary file up to MaxDecompressedSize. Uncompressed files
// return the original path and a no-op cleanup function.
func DecompressFile(path string) (string, func(), error) {
	ext := strings.ToLower(filepath.Ext(path))

	// #nosec G304 -- caller validates path against repository directory boundaries
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", nil, fmt.Errorf("failed to open %s: %w", path, err)
	}

	var r io.Reader
	switch ext {
	case ".gz":
		gz, err := gzip.NewReader(f)
		if err != nil {
			_ = f.Close()
			return "", nil, fmt.Errorf("failed to open gzip reader for %s: %w", path, err)
		}
		defer func() { _ = gz.Close() }()
		r = gz
	case ".bz2":
		r = bzip2.NewReader(f)
	case ".xz":
		xzr, err := xz.NewReader(f)
		if err != nil {
			_ = f.Close()
			return "", nil, fmt.Errorf("failed to open xz reader for %s: %w", path, err)
		}
		r = xzr
	case ".zst", ".zstd":
		zr, err := zstd.NewReader(f)
		if err != nil {
			_ = f.Close()
			return "", nil, fmt.Errorf("failed to open zstd reader for %s: %w", path, err)
		}
		defer zr.Close()
		r = zr
	default:
		_ = f.Close()
		return path, func() {}, nil
	}

	tmp, err := os.CreateTemp("", "repoview-decomp-*")
	if err != nil {
		_ = f.Close()
		return "", nil, fmt.Errorf("%w: failed to create temporary file: %w", ErrDecompressIO, err)
	}

	copied, err := io.Copy(tmp, io.LimitReader(r, MaxDecompressedSize+1))
	_ = tmp.Close()
	_ = f.Close()

	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", nil, fmt.Errorf("decompression failed for %s: %w", path, err)
	}
	if copied > MaxDecompressedSize {
		_ = os.Remove(tmp.Name())
		return "", nil, fmt.Errorf("%w: %s exceeds 4 GiB limit", ErrDecompressBomb, path)
	}

	cleanup := func() {
		_ = os.Remove(tmp.Name())
	}
	return tmp.Name(), cleanup, nil
}

// IsCompressed reports whether path ends with a recognized compression extension.
func IsCompressed(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".gz" || ext == ".bz2" || ext == ".xz" || ext == ".zst" || ext == ".zstd"
}
