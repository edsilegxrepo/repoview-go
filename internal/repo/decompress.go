package repo

import (
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// OBJECTIVES:
// Provide unified archive decompression across all modern Linux compression algorithms
// (gzip, bzip2, xz, zstd) with strict resource exhaustion defenses.
//
// CORE COMPONENTS:
//   - MaxDecompressedSize: Hard upper ceiling (4 GiB) preventing zip/decompression bomb attacks.
//   - DecompressFile: Format-detecting archive decompressor returning temp file path and cleanup hook.
//   - IsCompressed: Helper detecting archive extensions.
//
// FUNCTIONALITY:
//   - Automatically determines compressor by file extension (.gz, .bz2, .xz, .zst, .zstd).
//   - Streams uncompressed bytes to an OS temporary file using io.LimitReader.
//   - Aborts and deletes temporary artifacts if decompressed payload exceeds MaxDecompressedSize.
//   - Returns an idempotent cleanup closure to ensure temporary files are deleted upon completion.
//
// DATA FLOW:
//   Compressed archive path -> Format detector -> Limited decompression -> Uncompressed temp path + cleanup()

// MaxDecompressedSize defines the maximum size (4 GiB) allowed for decompressed repodata
// to protect against decompression bomb / zip-bomb denial of service attacks.
const MaxDecompressedSize = 4 * 1024 * 1024 * 1024 // 4 GiB

// DecompressFile takes a path to a file, detects its compression type (gz, bz2, xz, zst),
// and decompresses it to a temporary file.
// If the file is not compressed, it returns the original path.
// It returns the path to the decompressed file, a cleanup function to remove the temp file, and any error.
func DecompressFile(path string) (string, func(), error) {
	ext := filepath.Ext(path)
	var r io.Reader

	// #nosec G304 -- path is discovered from repomd.xml and validated against path traversal
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", nil, err
	}
	// Don't defer close here immediately, as we might need it for the reader.
	// We'll close it in the wrapper or after reading.

	switch ext {
	case ".gz":
		gz, err := gzip.NewReader(f)
		if err != nil {
			_ = f.Close()
			return "", nil, err
		}
		defer func() {
			_ = gz.Close()
		}()
		r = gz
	case ".bz2":
		r = bzip2.NewReader(f)
	case ".xz":
		xzr, err := xz.NewReader(f)
		if err != nil {
			_ = f.Close()
			return "", nil, err
		}
		r = xzr
	case ".zst", ".zstd":
		zr, err := zstd.NewReader(f)
		if err != nil {
			_ = f.Close()
			return "", nil, err
		}
		defer zr.Close()
		r = zr
	default:
		_ = f.Close()
		return path, func() {}, nil
	}

	// Create temp file
	tmp, err := os.CreateTemp("", "repoview-decomp-*")
	if err != nil {
		_ = f.Close()
		return "", nil, err
	}

	// Decompress with hard limit to prevent disk exhaustion
	copied, err := io.Copy(tmp, io.LimitReader(r, MaxDecompressedSize+1))
	_ = tmp.Close()
	_ = f.Close()

	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", nil, err
	}
	if copied > MaxDecompressedSize {
		_ = os.Remove(tmp.Name())
		return "", nil, fmt.Errorf("decompressed size for %s exceeds maximum safety ceiling of 4 GiB (potential decompression bomb)", path)
	}

	cleanup := func() {
		_ = os.Remove(tmp.Name())
	}

	return tmp.Name(), cleanup, nil
}

// IsCompressed checks if a file extension indicates compression
func IsCompressed(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".gz" || ext == ".bz2" || ext == ".xz" || ext == ".zst" || ext == ".zstd"
}
