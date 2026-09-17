package repo

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/edsilegxrepo/repoview/internal/models"

	_ "github.com/mattn/go-sqlite3"
)

// TEST STRATEGY:
// Validate SQLite repository access layer against mock SQLite databases in t.TempDir().
//
// Test coverage includes:
//   - Schema creation (packages, requires, provides, conflicts, obsoletes, other.changelog).
//   - GetAllPackages: Querying, row scanning, null value handling.
//   - GetChangelogForPackage: Querying latest changelog note, cleanAuthor email stripping.
//   - EnrichPackagesWithChangelogs: Bulk batch enrichment across package slices.
//   - GetPackageDependencies: Extracting requires, provides, conflicts, and obsoletes with prepared statements.
//   - RepositoryAccess.Close: Clean teardown of statements and database handles.

func setupTestDBs(t *testing.T) (string, string) {
	tempDir := t.TempDir()
	primaryPath := filepath.Join(tempDir, "primary.sqlite")
	otherPath := filepath.Join(tempDir, "other.sqlite")

	// 1. Setup primary.sqlite
	pdb, err := sql.Open("sqlite3", primaryPath)
	if err != nil {
		t.Fatalf("failed to open primary sqlite: %v", err)
	}
	defer func() { _ = pdb.Close() }()

	createPrimary := `
	CREATE TABLE packages (
		pkgKey INTEGER PRIMARY KEY,
		name TEXT,
		epoch TEXT,
		version TEXT,
		release TEXT,
		arch TEXT,
		summary TEXT,
		description TEXT,
		url TEXT,
		time_build INTEGER,
		rpm_license TEXT,
		rpm_sourcerpm TEXT,
		size_package INTEGER,
		location_href TEXT,
		rpm_vendor TEXT,
		rpm_group TEXT,
		rpm_buildhost TEXT,
		size_installed INTEGER
	);
	CREATE TABLE requires (
		pkgKey INTEGER,
		name TEXT,
		flags TEXT,
		epoch TEXT,
		version TEXT,
		release TEXT,
		pre BOOLEAN
	);
	CREATE TABLE provides (
		pkgKey INTEGER,
		name TEXT,
		flags TEXT,
		epoch TEXT,
		version TEXT,
		release TEXT
	);
	CREATE TABLE conflicts (
		pkgKey INTEGER,
		name TEXT,
		flags TEXT,
		epoch TEXT,
		version TEXT,
		release TEXT
	);
	CREATE TABLE obsoletes (
		pkgKey INTEGER,
		name TEXT,
		flags TEXT,
		epoch TEXT,
		version TEXT,
		release TEXT
	);
	`
	if _, err := pdb.Exec(createPrimary); err != nil {
		t.Fatalf("failed to create primary schema: %v", err)
	}

	// Insert test package
	insertPkg := `
	INSERT INTO packages (pkgKey, name, epoch, version, release, arch, summary, description, url, time_build, rpm_license, rpm_sourcerpm, size_package, location_href, rpm_vendor, rpm_group, rpm_buildhost, size_installed)
	VALUES (1, 'demo', '0', '1.0', '1.el9', 'x86_64', 'Demo package', 'Demo description', 'http://example.com', 1600000000, 'MIT', 'demo-1.0-1.src.rpm', 1048576, 'demo-1.0.rpm', 'Demo Inc', 'Applications/System', 'build.local', 2097152);
	`
	if _, err := pdb.Exec(insertPkg); err != nil {
		t.Fatalf("failed to insert package: %v", err)
	}

	// Insert dependencies
	if _, err := pdb.Exec(`INSERT INTO requires (pkgKey, name, flags, epoch, version, release, pre) VALUES (1, 'glibc', 'GE', '0', '2.34', '1', 0)`); err != nil {
		t.Fatalf("failed to insert requires: %v", err)
	}
	if _, err := pdb.Exec(`INSERT INTO provides (pkgKey, name, flags, epoch, version, release) VALUES (1, 'demo-cap', 'EQ', '0', '1.0', '1')`); err != nil {
		t.Fatalf("failed to insert provides: %v", err)
	}
	if _, err := pdb.Exec(`INSERT INTO conflicts (pkgKey, name, flags, epoch, version, release) VALUES (1, 'old-demo', 'LT', '0', '1.0', '0')`); err != nil {
		t.Fatalf("failed to insert conflicts: %v", err)
	}
	if _, err := pdb.Exec(`INSERT INTO obsoletes (pkgKey, name, flags, epoch, version, release) VALUES (1, 'legacy-demo', 'LE', '0', '0.9', '1')`); err != nil {
		t.Fatalf("failed to insert obsoletes: %v", err)
	}

	// 2. Setup other.sqlite
	odb, err := sql.Open("sqlite3", otherPath)
	if err != nil {
		t.Fatalf("failed to open other sqlite: %v", err)
	}
	defer func() { _ = odb.Close() }()

	createOther := `
	CREATE TABLE changelog (
		pkgKey INTEGER,
		author TEXT,
		date INTEGER,
		changelog TEXT
	);
	`
	if _, err := odb.Exec(createOther); err != nil {
		t.Fatalf("failed to create other schema: %v", err)
	}

	insertChangelog := `
	INSERT INTO changelog (pkgKey, author, date, changelog)
	VALUES (1, 'Maintainer <maint@example.com>', 1600000000, '- New upstream release');
	`
	if _, err := odb.Exec(insertChangelog); err != nil {
		t.Fatalf("failed to insert changelog: %v", err)
	}

	return primaryPath, otherPath
}

func TestRepositoryAccess_Lifecycle(t *testing.T) {
	primaryPath, otherPath := setupTestDBs(t)

	repoAccess, err := NewRepositoryAccess(primaryPath, otherPath)
	if err != nil {
		t.Fatalf("NewRepositoryAccess failed: %v", err)
	}
	defer repoAccess.Close()

	// 1. Test GetAllPackages
	pkgs, err := repoAccess.GetAllPackages()
	if err != nil {
		t.Fatalf("GetAllPackages failed: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 package, got %d", len(pkgs))
	}
	pkg := pkgs[0]
	if pkg.Name != "demo" || pkg.Version != "1.0" || pkg.InstalledSize != 2097152 {
		t.Errorf("unexpected package data: %+v", pkg)
	}

	// 2. Test GetChangelogForPackage
	ch, err := repoAccess.GetChangelogForPackage(pkg.PkgKey)
	if err != nil {
		t.Fatalf("GetChangelogForPackage failed: %v", err)
	}
	if ch == nil || ch.Author != "Maintainer" {
		t.Errorf("expected clean author 'Maintainer', got %+v", ch)
	}

	// 3. Test EnrichPackagesWithChangelogs
	pkg.Changelog = nil
	if err := repoAccess.EnrichPackagesWithChangelogs([]*models.Package{pkg}); err != nil {
		t.Fatalf("EnrichPackagesWithChangelogs failed: %v", err)
	}
	if pkg.Changelog == nil || pkg.Changelog.Author != "Maintainer" {
		t.Errorf("expected Changelog to be populated, got: %+v", pkg.Changelog)
	}

	// 4. Test GetPackageDependencies
	deps, err := repoAccess.GetPackageDependencies(pkg.PkgKey)
	if err != nil {
		t.Fatalf("GetPackageDependencies failed: %v", err)
	}
	if len(deps.Requires) != 1 || deps.Requires[0].Name != "glibc" {
		t.Errorf("unexpected requires: %+v", deps.Requires)
	}
	if len(deps.Provides) != 1 || deps.Provides[0].Name != "demo-cap" {
		t.Errorf("unexpected provides: %+v", deps.Provides)
	}
	if len(deps.Conflicts) != 1 || deps.Conflicts[0].Name != "old-demo" {
		t.Errorf("unexpected conflicts: %+v", deps.Conflicts)
	}
	if len(deps.Obsoletes) != 1 || deps.Obsoletes[0].Name != "legacy-demo" {
		t.Errorf("unexpected obsoletes: %+v", deps.Obsoletes)
	}
}

func TestCleanAuthor(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"John Doe <john@example.com>", "John Doe"},
		{"Alice Cooper <alice@music.org>", "Alice Cooper"},
		{"Plain Author", "Plain Author"},
	}

	for _, tt := range tests {
		got := cleanAuthor(tt.input)
		if got != tt.expected {
			t.Errorf("cleanAuthor(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}
