package repo

import "github.com/edsilegxrepo/repoview/internal/models"

// RepoReader unifies package repository ingestion across RPM and DEB backends.
type RepoReader interface {
	// GetAllPackages extracts all package records from the repository index.
	GetAllPackages() ([]*models.Package, error)

	// EnrichPackagesWithChangelogs associates changelog records with packages.
	EnrichPackagesWithChangelogs(pkgs []*models.Package) error

	// EnrichPackageDetails populates scriptlets and signatures from package binaries.
	EnrichPackageDetails(repoDir string, pkgs []*models.Package)

	// ReadPackageFiles extracts on-demand file manifests during page render.
	ReadPackageFiles(repoDir string, pkg *models.Package) ([]models.PackageFile, error)

	// GetPackageDependencies fetches requires, provides, conflicts, obsoletes for a given package.
	GetPackageDependencies(pkgKey int64) (*models.PackageDependencies, error)

	// Close releases database connections, file handles, or ephemeral decompression scratchpads.
	Close() error
}
