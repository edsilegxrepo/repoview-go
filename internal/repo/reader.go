package repo

// OBJECTIVES:
// Define the format-agnostic data access contracts decoupling upstream generator
// orchestration from underlying RPM and Debian metadata storage engines.
//
// CORE COMPONENTS:
//   - RepoReader: Required baseline contract (GetAllPackages, EnrichPackageDetails, ReadPackageFiles, Close).
//   - Optional Enricher Interfaces: ChangelogEnricher, DependencyReader, DependencyEnricher,
//     PackageRenderEnricher, and AdvisoryEnricher for backend-specific optimizations.
//
// FUNCTIONALITY:
//   - Enables single-pass binary archive enrichment (PackageRenderEnricher) and batched SQLite
//     queries (DependencyEnricher) via clean interface assertions in app.Generator.
//
// DATA FLOW:
//   app.Generator -> RepoReader (+ optional interface assertions) -> []*models.Package

import "github.com/edsilegxrepo/repoview/internal/models"

// --- Core Storage & Capability Contracts ---

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

// DependencyEnricher is an optional capability interface implemented by backends that can
// bulk-load dependency graphs across a slice of packages prior to parallel page rendering.
type DependencyEnricher interface {
	EnrichPackagesWithDependencies(pkgs []*models.Package) error
}

// PackageRenderEnricher populates archive details (scriptlets, signatures, conffiles, timestamps)
// and on-demand file manifests in a single file open right before rendering a package page.
type PackageRenderEnricher interface {
	EnrichForRender(repoDir string, pkg *models.Package) error
}

// AdvisoryEnricher is an optional capability interface implemented by backends supporting updateinfo errata.
type AdvisoryEnricher interface {
	EnrichPackagesWithAdvisories(pkgs []*models.Package) error
}
