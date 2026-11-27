package deb

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/util"
	"pault.ag/go/debian/control"
)

// DebRepository implements repo.RepoReader for Debian package repositories.
type DebRepository struct {
	locs    *DebRepoLocations
	repoDir string
	cleanup func()
}

// NewDebRepository creates a new DebRepository instance.
func NewDebRepository(locs *DebRepoLocations, cleanup func()) *DebRepository {
	var base string
	if locs != nil {
		base = locs.BaseDir
	}
	return &DebRepository{
		locs:    locs,
		repoDir: base,
		cleanup: cleanup,
	}
}

// SetRepoDir sets the root directory used for resolving relative .deb paths when overriding locs.BaseDir.
func (r *DebRepository) SetRepoDir(repoDir string) {
	r.repoDir = repoDir
}

// GetAllPackages parses Packages using control.ParseBinaryIndex and maps to models.Package.
// It supports multi-component aggregation if multiple Packages files were discovered.
func (r *DebRepository) GetAllPackages() ([]*models.Package, error) {
	if r.locs == nil || (r.locs.PackagesFile == "" && len(r.locs.PackagesFiles) == 0) {
		return nil, fmt.Errorf("%w: no Packages file available", models.ErrRepoMetadata)
	}

	filesToParse := r.locs.PackagesFiles
	if len(filesToParse) == 0 {
		filesToParse = []string{r.locs.PackagesFile}
	}

	var releaseHashes map[string]string
	var releaseDir string
	if r.locs.ReleaseFile != "" {
		var err error
		releaseHashes, err = parseReleaseSHA256(r.locs.ReleaseFile)
		if err != nil {
			return nil, err
		}
		releaseDir = filepath.Dir(r.locs.ReleaseFile)
	}

	var packages []*models.Package
	seen := make(map[string]bool)

	for _, pkgFile := range filesToParse {
		if len(releaseHashes) > 0 {
			if rel, err := filepath.Rel(releaseDir, pkgFile); err == nil {
				relKey := filepath.ToSlash(strings.TrimPrefix(rel, "./"))
				if expected, ok := releaseHashes[relKey]; ok {
					if err := verifyFileSHA256(pkgFile, expected); err != nil {
						return nil, err
					}
				}
			}
		}
		err := func(fPath string) error {
			packagesPath, cleanupDecomp, err := util.DecompressFile(fPath)
			if err != nil {
				if errors.Is(err, util.ErrDecompressBomb) {
					return fmt.Errorf("%w: %w", models.ErrIntegrity, err)
				}
				return fmt.Errorf("%w: failed to decompress packages file %s: %w", models.ErrRepoMetadata, fPath, err)
			}
			defer cleanupDecomp()

			// #nosec G304 -- packagesPath is verified decompressed repository index from repo discovery
			f, err := os.Open(filepath.Clean(packagesPath))
			if err != nil {
				return fmt.Errorf("%w: failed to open packages file %s: %w", models.ErrRepoMetadata, packagesPath, err)
			}
			defer func() { _ = f.Close() }()

			indices, err := control.ParseBinaryIndex(bufio.NewReader(f))
			if err != nil {
				return fmt.Errorf("%w: failed to parse Debian binary index %s: %w", models.ErrRepoMetadata, fPath, err)
			}

			for _, idx := range indices {
				p := models.PackageFromBinaryIndex(idx)

				dedupKey := p.LocationHref
				if dedupKey == "" {
					dedupKey = p.Name + "\x00" + p.EVR() + "\x00" + p.Arch
				}
				if seen[dedupKey] {
					continue
				}
				seen[dedupKey] = true

				p.PkgKey = int64(len(packages) + 1)
				packages = append(packages, p)
			}
			return nil
		}(pkgFile)
		if err != nil {
			return nil, err
		}
	}

	return packages, nil
}

// EnrichPackageDetails concurrently parses the local .deb files for all packages
// and populates deep inspection details (maintainer scriptlets, conffiles, and ar build timestamps) in one pass.
func (r *DebRepository) EnrichPackageDetails(pkgs []*models.Package) {
	baseDir := r.repoDir
	if baseDir == "" && r.locs != nil {
		baseDir = r.locs.BaseDir
	}
	util.ForEachConcurrent(pkgs, 0, func(_ int, p *models.Package) {
		if p == nil || p.LocationHref == "" {
			return
		}
		debPath := r.resolveDebPath(baseDir, p.LocationHref)
		if debPath == "" {
			return
		}

		details, ts, err := ReadDebDetailsWithTimestamp(debPath)
		if p.TimeBuild <= 0 && ts > 0 {
			p.TimeBuild = ts
		}
		if err != nil {
			return
		}
		p.Details = details
	})
}

// ReadPackageFiles extracts on-demand file manifests from data.tar inside the .deb archive
// and enriches changelog and fallback license fields in a single streaming pass.
func (r *DebRepository) ReadPackageFiles(repoDir string, pkg *models.Package) ([]models.PackageFile, error) {
	if pkg.LocationHref == "" {
		return nil, nil
	}

	debPath := r.resolveDebPath(repoDir, pkg.LocationHref)
	if debPath == "" {
		return nil, fmt.Errorf("package file not found for %s", pkg.LocationHref)
	}

	var conffiles map[string]bool
	if pkg.Details != nil {
		conffiles = pkg.Details.Conffiles
	}
	files, cl, detectedLicense, err := ReadDebFilesMetadata(debPath, conffiles)
	if err != nil {
		return nil, err
	}
	if pkg.Changelog == nil && cl != nil {
		pkg.Changelog = cl
		pkg.Changelogs = []*models.ChangelogEntry{cl}
	}
	if pkg.License == "" && detectedLicense != "" {
		pkg.License = detectedLicense
	}
	if pkg.TimeBuild <= 0 && cl != nil && cl.Date > 0 {
		pkg.TimeBuild = cl.Date
	}
	return files, nil
}

// resolveDebPath locates the .deb file on disk, checking repoDir and locs.BaseDir
// with security guards against directory traversal.
func (r *DebRepository) resolveDebPath(repoDir, href string) string {
	candidates := []string{repoDir}
	if r.locs != nil && r.locs.BaseDir != "" && r.locs.BaseDir != repoDir {
		candidates = append(candidates, r.locs.BaseDir)
	}

	cleanHref := filepath.Clean(href)

	for _, base := range candidates {
		target, err := util.ResolveContainedPath(base, cleanHref)
		if err != nil {
			continue
		}
		if _, err := os.Stat(target); err == nil {
			return target
		}
	}

	return ""
}

// Close releases any allocated resources.
func (r *DebRepository) Close() error {
	if r.cleanup != nil {
		r.cleanup()
	}
	return nil
}

// parseReleaseSHA256 extracts the SHA256 digest map (relative path -> hex digest) from a Debian Release/InRelease file.
func parseReleaseSHA256(releasePath string) (map[string]string, error) {
	const maxReleaseBytes = 5 * 1024 * 1024 // 5 MiB ceiling
	// #nosec G304 -- releasePath was resolved by deb.Discover inside repository root
	f, err := os.Open(filepath.Clean(releasePath))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to open Debian Release file %s: %w", models.ErrRepoMetadata, releasePath, err)
	}
	defer func() { _ = f.Close() }()

	hashes := make(map[string]string)
	scanner := bufio.NewScanner(io.LimitReader(f, maxReleaseBytes))
	inSHA256 := false
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if !inSHA256 {
			if trimmed == "SHA256:" {
				inSHA256 = true
			}
			continue
		}
		if line == "" || (line[0] != ' ' && line[0] != '\t') {
			break
		}
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			relPath := filepath.ToSlash(strings.TrimPrefix(fields[2], "./"))
			hashes[relPath] = strings.ToLower(fields[0])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: failed to scan Debian Release file %s: %w", models.ErrRepoMetadata, releasePath, err)
	}
	return hashes, nil
}

// verifyFileSHA256 validates that targetPath matches expectedHex SHA256 digest.
func verifyFileSHA256(targetPath, expectedHex string) error {
	// #nosec G304 -- targetPath was resolved inside repository metadata directory
	f, err := os.Open(filepath.Clean(targetPath))
	if err != nil {
		return fmt.Errorf("failed to open %s for SHA256 verification: %w", targetPath, err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("%w: failed to hash %s: %w", models.ErrIO, targetPath, err)
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(actual, strings.TrimSpace(expectedHex)) {
		return fmt.Errorf("%w: SHA256 mismatch for %s: expected %s, got %s", models.ErrIntegrity, targetPath, expectedHex, actual)
	}
	return nil
}
