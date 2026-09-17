package deb

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/repo"
	"pault.ag/go/debian/control"
)

// DebRepository implements repo.RepoReader for Debian package repositories.
type DebRepository struct {
	locs    *DebRepoLocations
	cleanup func()
	depsMu  sync.RWMutex
	depsMap map[int64]*models.PackageDependencies
}

// NewDebRepository creates a new DebRepository instance.
func NewDebRepository(locs *DebRepoLocations, cleanup func()) repo.RepoReader {
	return &DebRepository{
		locs:    locs,
		cleanup: cleanup,
		depsMap: make(map[int64]*models.PackageDependencies),
	}
}

// GetAllPackages parses Packages using control.ParseBinaryIndex and maps to models.Package.
// It supports multi-component aggregation if multiple Packages files were discovered.
func (r *DebRepository) GetAllPackages() ([]*models.Package, error) {
	if r.locs == nil || (r.locs.PackagesFile == "" && len(r.locs.PackagesFiles) == 0) {
		return nil, fmt.Errorf("no Packages file available")
	}

	filesToParse := r.locs.PackagesFiles
	if len(filesToParse) == 0 {
		filesToParse = []string{r.locs.PackagesFile}
	}

	var packages []*models.Package
	seen := make(map[string]bool)

	r.depsMu.Lock()
	defer r.depsMu.Unlock()

	for _, pkgFile := range filesToParse {
		err := func(fPath string) error {
			packagesPath, cleanupDecomp, err := repo.DecompressFile(fPath)
			if err != nil {
				return fmt.Errorf("failed to decompress packages file %s: %w", fPath, err)
			}
			defer cleanupDecomp()

			// #nosec G304 -- packagesPath is verified decompressed repository index from repo discovery
			f, err := os.Open(filepath.Clean(packagesPath))
			if err != nil {
				return fmt.Errorf("failed to open packages file %s: %w", packagesPath, err)
			}
			defer func() { _ = f.Close() }()

			indices, err := control.ParseBinaryIndex(bufio.NewReader(f))
			if err != nil {
				return fmt.Errorf("failed to parse Debian binary index %s: %w", fPath, err)
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

				if p.LocationHref != "" && r.locs != nil {
					if debPath := r.resolveDebPath(r.locs.BaseDir, p.LocationHref); debPath != "" {
						p.TimeBuild = ReadDebTimestamp(debPath)
					}
				}

				if p.Dependencies != nil {
					r.depsMap[p.PkgKey] = p.Dependencies
				}

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

// GetPackageDependencies returns cached package dependencies for a given package key.
func (r *DebRepository) GetPackageDependencies(pkgKey int64) (*models.PackageDependencies, error) {
	r.depsMu.RLock()
	defer r.depsMu.RUnlock()
	return r.depsMap[pkgKey], nil
}

// GetChangelogForPackage retrieves a changelog entry for a specific package key (if cached).
func (r *DebRepository) GetChangelogForPackage(pkgKey int64) (*models.ChangelogEntry, error) {
	return nil, nil
}

// EnrichPackagesWithChangelogs is a no-op at bulk ingestion time for Debian packages
// because Debian repositories do not store changelogs in the Packages index.
// Changelogs can be extracted on-demand during package page rendering from the .deb archive.
func (r *DebRepository) EnrichPackagesWithChangelogs(pkgs []*models.Package) error {
	return nil
}

// EnrichPackageDetails concurrently parses the local .deb files for all packages
// and populates deep inspection details (maintainer scriptlets from control.tar).
func (r *DebRepository) EnrichPackageDetails(repoDir string, pkgs []*models.Package) {
	numWorkers := runtime.NumCPU() * 2
	if numWorkers < 4 {
		numWorkers = 4
	}

	pkgChan := make(chan *models.Package, len(pkgs))
	for _, p := range pkgs {
		pkgChan <- p
	}
	close(pkgChan)

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range pkgChan {
				if p.LocationHref == "" {
					continue
				}

				debPath := r.resolveDebPath(repoDir, p.LocationHref)
				if debPath == "" {
					continue
				}

				if p.TimeBuild <= 0 {
					p.TimeBuild = ReadDebTimestamp(debPath)
				}

				details, err := ReadDebDetails(debPath)
				if err != nil {
					continue
				}

				p.Details = details
			}
		}()
	}

	wg.Wait()
}

// ReadPackageFiles extracts on-demand file manifests from data.tar inside the .deb archive.
func (r *DebRepository) ReadPackageFiles(repoDir string, pkg *models.Package) ([]models.PackageFile, error) {
	if pkg.LocationHref == "" {
		return nil, nil
	}

	debPath := r.resolveDebPath(repoDir, pkg.LocationHref)
	if debPath == "" {
		return nil, fmt.Errorf("package file not found for %s", pkg.LocationHref)
	}

	files, cl, err := ReadDebFilesAndChangelog(debPath)
	if err != nil {
		return nil, err
	}
	if pkg.Changelog == nil && cl != nil {
		pkg.Changelog = cl
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
		target := filepath.Join(base, cleanHref)
		rel, err := filepath.Rel(base, target)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
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

var _ repo.RepoReader = (*DebRepository)(nil)
