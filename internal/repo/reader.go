package repo

import "github.com/edsilegxrepo/repoview/internal/models"

// RepoReader unifies minimal package repository ingestion across RPM and DEB backends.
type RepoReader interface {
	// GetAllPackages extracts all package records from the repository index.
	GetAllPackages() ([]*models.Package, error)

	// EnrichPackageDetails populates scriptlets, signatures, conffiles, or timestamps from package archives.
	EnrichPackageDetails(pkgs []*models.Package)

	// ReadPackageFiles extracts on-demand file manifests during page render.
	ReadPackageFiles(repoDir string, pkg *models.Package) ([]models.PackageFile, error)

	// Close releases database connections, file handles, or ephemeral decompression scratchpads.
	Close() error
}

// ChangelogEnricher is an optional capability interface implemented by backends (such as RPM SQLite/XML)
// that store changelogs in repository-level index metadata rather than individual package archives.
type ChangelogEnricher interface {
	EnrichPackagesWithChangelogs(pkgs []*models.Package) error
}

// DependencyReader is an optional capability interface implemented by backends that lazy-load
// per-package dependency graphs on demand instead of attaching them in GetAllPackages.
type DependencyReader interface {
	GetPackageDependencies(pkgKey int64) (*models.PackageDependencies, error)
}

// AdvisoryEnricher is an optional capability interface implemented by backends supporting updateinfo errata.
type AdvisoryEnricher interface {
	EnrichPackagesWithAdvisories(pkgs []*models.Package) error
}
