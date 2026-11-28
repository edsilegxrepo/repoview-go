package repo

// OBJECTIVES:
// Expose repository metadata decompression to repoview-go's parser layer while mapping
// low-level decompression failures onto canonical domain sentinel errors.
//
// CORE COMPONENTS:
//   - MaxDecompressedSize: Shared 4 GiB ceiling inherited from internal/util.
//   - DecompressFile: Delegates format decoding (.gz, .bz2, .xz, .zst) and maps
//     ErrDecompressBomb -> models.ErrIntegrity and I/O errors -> models.ErrIO.
//   - IsCompressed: Checks recognized metadata compression extensions.
//
// FUNCTIONALITY:
//   - Preserves clean process exit code boundaries without duplicating streaming code.
//
// DATA FLOW:
//   metadata file path -> util.DecompressFile -> sentinel-wrapped error or scratch file path

import (
	"errors"
	"fmt"

	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/util"
)

// MaxDecompressedSize defines the maximum size (4 GiB) allowed for decompressed repodata.
const MaxDecompressedSize = util.MaxDecompressedSize

// DecompressFile decompresses .gz, .bz2, .xz, .zst/.zstd metadata files to an ephemeral file
// and wraps errors with domain-specific sentinel errors for deterministic CLI exit codes.
func DecompressFile(path string) (string, func(), error) {
	outPath, cleanup, err := util.DecompressFile(path)
	if err != nil {
		switch {
		case errors.Is(err, util.ErrDecompressBomb):
			return "", nil, fmt.Errorf("%w: %w", models.ErrIntegrity, err)
		case errors.Is(err, util.ErrDecompressIO):
			return "", nil, fmt.Errorf("%w: %w", models.ErrIO, err)
		default:
			return "", nil, fmt.Errorf("%w: %w", models.ErrRepoMetadata, err)
		}
	}
	return outPath, cleanup, nil
}

// IsCompressed checks if a file extension indicates compression.
func IsCompressed(path string) bool {
	return util.IsCompressed(path)
}
