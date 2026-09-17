package repo

import (
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/edsilegxrepo/repoview/internal/models"

	_ "github.com/mattn/go-sqlite3"
)

// OBJECTIVES:
// Provide high-throughput, thread-safe, and read-only querying of repository SQLite
// databases (primary.sqlite for package definitions and dependencies, other.sqlite for changelogs).
//
// CORE COMPONENTS:
//   - RepositoryAccess: Centralized SQLite client holding connection pools and pre-compiled statements.
//   - NewRepositoryAccess: Initializes database connections with read-only concurrency and attached databases.
//   - GetAllPackages: Queries all rows from the primary packages table.
//   - EnrichPackagesWithChangelogs: Bulk-fetches latest changelogs using SQL windowing/joins in batches of 500.
//   - GetPackageDependencies: Queries all 4 dependency vectors (requires, provides, conflicts, obsoletes) using prepared statements.
//
// FUNCTIONALITY:
//   - Configures SQLite DSN with mode=ro, cache=shared, and _busy_timeout=5000 to prevent locking under concurrency.
//   - Tunes connection pooling with SetMaxOpenConns and SetMaxIdleConns scaled to runtime.NumCPU() * 2.
//   - Attaches other.sqlite as an attached database to execute cross-database joins without duplicate connections.
//   - Pre-compiles SQL prepared statements for all high-frequency lookups, eliminating 40,000+ dynamic query compilations.
//   - Sanitizes author strings to match legacy repoview output conventions.
//
// DATA FLOW:
//   primary.sqlite + other.sqlite -> RepositoryAccess -> models.Package / models.PackageDependencies

// RepositoryAccess handles interactions with the SQLite databases.
// It manages connections to both 'primary' (packages) and 'other' (changelogs)
// databases and provides methods to query them efficiently.
type RepositoryAccess struct {
	PrimaryDB     *sql.DB
	changelogStmt *sql.Stmt // Cached prepared statement for fast changelog lookups
	reqStmt       *sql.Stmt // Cached prepared statement for package requires
	provStmt      *sql.Stmt // Cached prepared statement for package provides
	confStmt      *sql.Stmt // Cached prepared statement for package conflicts
	obsStmt       *sql.Stmt // Cached prepared statement for package obsoletes
}

// NewRepositoryAccess creates a new accessor for the given database files.
// It opens connections in read-only mode with connection pooling and prepares statements for high-frequency queries.
func NewRepositoryAccess(primaryPath, otherPath string) (*RepositoryAccess, error) {
	dsn := primaryPath
	if !strings.Contains(dsn, "?") {
		dsn += "?mode=ro&cache=shared&_busy_timeout=5000"
	} else {
		dsn += "&mode=ro&cache=shared&_busy_timeout=5000"
	}

	pdb, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open primary db: %w", err)
	}

	maxConns := runtime.NumCPU() * 2
	if maxConns < 4 {
		maxConns = 4
	}
	pdb.SetMaxOpenConns(maxConns)
	pdb.SetMaxIdleConns(maxConns)

	// Optimization: Attach 'other' DB to 'primary' connection in read-only mode
	if _, err := pdb.Exec("ATTACH DATABASE ? AS other", otherPath); err != nil {
		_ = pdb.Close()
		return nil, fmt.Errorf("failed to attach other db: %w", err)
	}

	// Prepare statements once for high-frequency queries to avoid recompiling SQL 40k+ times
	stmt, err := pdb.Prepare(`SELECT author, date, changelog FROM other.changelog WHERE pkgKey = ? ORDER BY date DESC LIMIT 1`)
	if err != nil {
		_ = pdb.Close()
		return nil, fmt.Errorf("failed to prepare changelog statement: %w", err)
	}

	reqStmt, err := pdb.Prepare(`SELECT name, flags, epoch, version, release, pre FROM requires WHERE pkgKey = ? ORDER BY name`)
	if err != nil {
		_ = stmt.Close()
		_ = pdb.Close()
		return nil, fmt.Errorf("failed to prepare requires statement: %w", err)
	}

	provStmt, err := pdb.Prepare(`SELECT name, flags, epoch, version, release FROM provides WHERE pkgKey = ? ORDER BY name`)
	if err != nil {
		_ = reqStmt.Close()
		_ = stmt.Close()
		_ = pdb.Close()
		return nil, fmt.Errorf("failed to prepare provides statement: %w", err)
	}

	confStmt, err := pdb.Prepare(`SELECT name, flags, epoch, version, release FROM conflicts WHERE pkgKey = ? ORDER BY name`)
	if err != nil {
		_ = provStmt.Close()
		_ = reqStmt.Close()
		_ = stmt.Close()
		_ = pdb.Close()
		return nil, fmt.Errorf("failed to prepare conflicts statement: %w", err)
	}

	obsStmt, err := pdb.Prepare(`SELECT name, flags, epoch, version, release FROM obsoletes WHERE pkgKey = ? ORDER BY name`)
	if err != nil {
		_ = confStmt.Close()
		_ = provStmt.Close()
		_ = reqStmt.Close()
		_ = stmt.Close()
		_ = pdb.Close()
		return nil, fmt.Errorf("failed to prepare obsoletes statement: %w", err)
	}

	return &RepositoryAccess{
		PrimaryDB:     pdb,
		changelogStmt: stmt,
		reqStmt:       reqStmt,
		provStmt:      provStmt,
		confStmt:      confStmt,
		obsStmt:       obsStmt,
	}, nil
}

// Close closes the database connections and all cached prepared statements
func (r *RepositoryAccess) Close() error {
	if r.changelogStmt != nil {
		_ = r.changelogStmt.Close()
	}
	if r.reqStmt != nil {
		_ = r.reqStmt.Close()
	}
	if r.provStmt != nil {
		_ = r.provStmt.Close()
	}
	if r.confStmt != nil {
		_ = r.confStmt.Close()
	}
	if r.obsStmt != nil {
		_ = r.obsStmt.Close()
	}
	if r.PrimaryDB != nil {
		return r.PrimaryDB.Close()
	}
	return nil
}

// GetAllPackages retrieves all packages from the primary database
func (r *RepositoryAccess) GetAllPackages() ([]*models.Package, error) {
	// We select all columns needed to populate the models.Package struct
	query := `
		SELECT pkgKey, name, epoch, version, release, arch, 
		       summary, description, url, time_build, rpm_license, 
		       rpm_sourcerpm, size_package, location_href, rpm_vendor, rpm_group,
		       rpm_buildhost, size_installed 
		FROM packages`

	rows, err := r.PrimaryDB.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query packages: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var packages []*models.Package

	for rows.Next() {
		var p models.Package
		var epoch, summary, description, url, license, sourceRPM, vendor, group, buildHost sql.NullString
		var installedSize sql.NullInt64

		err := rows.Scan(
			&p.PkgKey, &p.Name, &epoch, &p.Version, &p.Release, &p.Arch,
			&summary, &description, &url, &p.TimeBuild, &license,
			&sourceRPM, &p.SizePackage, &p.LocationHref, &vendor, &group,
			&buildHost, &installedSize,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan package row: %w", err)
		}

		// Handle nullable fields
		p.Epoch = epoch.String
		p.Summary = summary.String
		p.Description = description.String
		p.URL = url.String
		p.License = license.String
		p.SourceRPM = sourceRPM.String
		p.SourcePackage = sourceRPM.String
		p.Vendor = vendor.String
		p.RpmGroup = group.String
		p.BuildHost = buildHost.String
		p.InstalledSize = installedSize.Int64
		p.Format = models.FormatRPM

		packages = append(packages, &p)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating packages: %w", err)
	}

	return packages, nil
}

// GetChangelogForPackage retrieves the latest changelog entry for a specific package key
func (r *RepositoryAccess) GetChangelogForPackage(pkgKey int64) (*models.ChangelogEntry, error) {
	var c models.ChangelogEntry
	var author, changelog sql.NullString

	err := r.changelogStmt.QueryRow(pkgKey).Scan(&author, &c.Date, &changelog)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // No changelog is not an error
		}
		return nil, fmt.Errorf("failed to scan changelog: %w", err)
	}

	c.Author = cleanAuthor(author.String)
	c.Changelog = changelog.String

	return &c, nil
}

// EnrichPackagesWithChangelogs populates the Changelog field for a slice of packages.
// It uses a bulk query optimization via the attached database.
func (r *RepositoryAccess) EnrichPackagesWithChangelogs(pkgs []*models.Package) error {
	if len(pkgs) == 0 {
		return nil
	}

	// Batch processing to avoid hitting SQLite variable limits (999 usually, but we use 500 to be safe)
	batchSize := 500
	for i := 0; i < len(pkgs); i += batchSize {
		end := i + batchSize
		if end > len(pkgs) {
			end = len(pkgs)
		}
		batch := pkgs[i:end]
		if err := r.enrichBatch(batch); err != nil {
			// We log and return the error, caller can decide whether to proceed
			log.Printf("Warning: failed to enrich batch: %v", err)
			return err
		}
	}
	return nil
}

func (r *RepositoryAccess) enrichBatch(pkgs []*models.Package) error {
	ids := make([]interface{}, len(pkgs))
	pkgMap := make(map[int64]*models.Package)

	for i, p := range pkgs {
		ids[i] = p.PkgKey
		pkgMap[p.PkgKey] = p
	}

	placeholders := strings.Repeat("?,", len(ids)-1) + "?"

	// Query to fetch latest changelog for each package in the batch.
	// PERFORMANCE NOTE:
	// We use a subquery to find the MAX(date) for each pkgKey to ensure we get the latest entry.
	// This avoids fetching all changelogs (which can be MBs of text) and filtering in Go.
	// This turns N queries into N/BatchSize queries, solving the N+1 performance bottleneck.
	// #nosec G201 -- placeholders is generated from slice length and contains only '?' tokens; package keys are bound via args
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query
	query := fmt.Sprintf(`
		SELECT t1.pkgKey, t1.author, t1.date, t1.changelog 
		FROM other.changelog t1
		INNER JOIN (
			SELECT pkgKey, MAX(date) as max_date 
			FROM other.changelog 
			WHERE pkgKey IN (%s) 
			GROUP BY pkgKey
		) t2 ON t1.pkgKey = t2.pkgKey AND t1.date = t2.max_date
	`, placeholders)

	rows, err := r.PrimaryDB.Query(query, ids...)
	if err != nil {
		return fmt.Errorf("failed to query changelogs batch: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var pkgKey int64
		var author, changelog sql.NullString
		var date int64

		if err := rows.Scan(&pkgKey, &author, &date, &changelog); err != nil {
			return fmt.Errorf("scan error: %w", err)
		}

		p, ok := pkgMap[pkgKey]
		// We assign the changelog if not already set.
		// The query guarantees one row per pkgKey (due to GROUP BY in subquery and join).
		// Note: In case of duplicate max_date, we might get duplicates, so we check if nil.
		if ok && p.Changelog == nil {
			p.Changelog = &models.ChangelogEntry{
				Author:    cleanAuthor(author.String),
				Date:      date,
				Changelog: changelog.String,
			}
		}
	}
	return rows.Err()
}

// cleanAuthor strips the email address from the author string to match legacy repoview behavior.
// Example: "John Doe <john@example.com>" -> "John Doe"
func cleanAuthor(author string) string {
	if idx := strings.Index(author, "<"); idx != -1 {
		return strings.TrimSpace(author[:idx])
	}
	return author
}

// GetPackageDependencies fetches requires, provides, conflicts, and obsoletes for a given package key.
func (r *RepositoryAccess) GetPackageDependencies(pkgKey int64) (*models.PackageDependencies, error) {
	deps := &models.PackageDependencies{}

	// 1. Requires
	var reqRows *sql.Rows
	var err error
	if r.reqStmt != nil {
		reqRows, err = r.reqStmt.Query(pkgKey)
	} else {
		reqRows, err = r.PrimaryDB.Query(`SELECT name, flags, epoch, version, release, pre FROM requires WHERE pkgKey = ? ORDER BY name`, pkgKey)
	}
	if err == nil {
		defer func() { _ = reqRows.Close() }()
		for reqRows.Next() {
			var name, flags, epoch, version, release sql.NullString
			var pre bool
			if err := reqRows.Scan(&name, &flags, &epoch, &version, &release, &pre); err == nil {
				deps.Requires = append(deps.Requires, &models.DependencyEntry{
					Name:    name.String,
					Flags:   flags.String,
					Epoch:   epoch.String,
					Version: version.String,
					Release: release.String,
					Pre:     pre,
				})
			}
		}
	}

	// 2. Provides
	var provRows *sql.Rows
	if r.provStmt != nil {
		provRows, err = r.provStmt.Query(pkgKey)
	} else {
		provRows, err = r.PrimaryDB.Query(`SELECT name, flags, epoch, version, release FROM provides WHERE pkgKey = ? ORDER BY name`, pkgKey)
	}
	if err == nil {
		defer func() { _ = provRows.Close() }()
		for provRows.Next() {
			var name, flags, epoch, version, release sql.NullString
			if err := provRows.Scan(&name, &flags, &epoch, &version, &release); err == nil {
				deps.Provides = append(deps.Provides, &models.DependencyEntry{
					Name:    name.String,
					Flags:   flags.String,
					Epoch:   epoch.String,
					Version: version.String,
					Release: release.String,
				})
			}
		}
	}

	// 3. Conflicts
	var confRows *sql.Rows
	if r.confStmt != nil {
		confRows, err = r.confStmt.Query(pkgKey)
	} else {
		confRows, err = r.PrimaryDB.Query(`SELECT name, flags, epoch, version, release FROM conflicts WHERE pkgKey = ? ORDER BY name`, pkgKey)
	}
	if err == nil {
		defer func() { _ = confRows.Close() }()
		for confRows.Next() {
			var name, flags, epoch, version, release sql.NullString
			if err := confRows.Scan(&name, &flags, &epoch, &version, &release); err == nil {
				deps.Conflicts = append(deps.Conflicts, &models.DependencyEntry{
					Name:    name.String,
					Flags:   flags.String,
					Epoch:   epoch.String,
					Version: version.String,
					Release: release.String,
				})
			}
		}
	}

	// 4. Obsoletes
	var obsRows *sql.Rows
	if r.obsStmt != nil {
		obsRows, err = r.obsStmt.Query(pkgKey)
	} else {
		obsRows, err = r.PrimaryDB.Query(`SELECT name, flags, epoch, version, release FROM obsoletes WHERE pkgKey = ? ORDER BY name`, pkgKey)
	}
	if err == nil {
		defer func() { _ = obsRows.Close() }()
		for obsRows.Next() {
			var name, flags, epoch, version, release sql.NullString
			if err := obsRows.Scan(&name, &flags, &epoch, &version, &release); err == nil {
				deps.Obsoletes = append(deps.Obsoletes, &models.DependencyEntry{
					Name:    name.String,
					Flags:   flags.String,
					Epoch:   epoch.String,
					Version: version.String,
					Release: release.String,
				})
			}
		}
	}

	return deps, nil
}

// EnrichPackageDetails concurrently parses the local RPM files for all packages
// and populates deep inspection details (scriptlets, signatures, files).
func (r *RepositoryAccess) EnrichPackageDetails(repoDir string, pkgs []*models.Package) {
	EnrichPackagesWithRPMDetails(repoDir, pkgs)
}

// ReadPackageFiles extracts on-demand file manifests during page render.
func (r *RepositoryAccess) ReadPackageFiles(repoDir string, pkg *models.Package) ([]models.PackageFile, error) {
	if pkg.LocationHref == "" {
		return nil, nil
	}
	rpmPath := filepath.Join(repoDir, pkg.LocationHref)
	rel, err := filepath.Rel(repoDir, rpmPath)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil, fmt.Errorf("invalid package location: %s", pkg.LocationHref)
	}
	return ReadRPMFiles(rpmPath)
}

var _ RepoReader = (*RepositoryAccess)(nil)
