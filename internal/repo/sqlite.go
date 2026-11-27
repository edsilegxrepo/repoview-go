package repo

import (
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/util"

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
	PrimaryDB      *sql.DB
	repoDir        string
	updateInfoPath string
	warnWriter     io.Writer
	changelogStmt  *sql.Stmt // Cached prepared statement for fast changelog lookups
	reqStmt        *sql.Stmt // Cached prepared statement for package requires
	provStmt       *sql.Stmt // Cached prepared statement for package provides
	confStmt       *sql.Stmt // Cached prepared statement for package conflicts
	obsStmt        *sql.Stmt // Cached prepared statement for package obsoletes
	recStmt        *sql.Stmt // Optional prepared statement for RPM weak recommends
	sugStmt        *sql.Stmt // Optional prepared statement for RPM weak suggests
	enhStmt        *sql.Stmt // Optional prepared statement for RPM weak enhances
	supStmt        *sql.Stmt // Optional prepared statement for RPM weak supplements
}

// SetRepoDir configures the base repository path used by EnrichPackageDetails.
func (r *RepositoryAccess) SetRepoDir(repoDir string) {
	r.repoDir = repoDir
}

// SetUpdateInfoPath configures an optional repodata updateinfo.xml* path for CVE/errata enrichment.
func (r *RepositoryAccess) SetUpdateInfoPath(path string) {
	r.updateInfoPath = path
}

// SetWarningWriter injects a diagnostic writer for non-fatal SQLite warnings.
func (r *RepositoryAccess) SetWarningWriter(w io.Writer) {
	r.warnWriter = w
}

func (r *RepositoryAccess) warnf(format string, args ...interface{}) {
	if r.warnWriter != nil {
		_, _ = fmt.Fprintf(r.warnWriter, format, args...)
	}
}

// sqliteReadOnlyURI builds an escaped file: URI with read-only concurrency and query_only parameters.
func sqliteReadOnlyURI(dbPath string) (string, error) {
	absPath, err := filepath.Abs(filepath.Clean(dbPath))
	if err != nil {
		return "", err
	}
	slashed := filepath.ToSlash(absPath)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Set("cache", "shared")
	q.Set("_busy_timeout", "5000")
	q.Set("_query_only", "1")
	u := &url.URL{
		Scheme:   "file",
		Path:     slashed,
		RawQuery: q.Encode(),
	}
	return u.String(), nil
}

// NewRepositoryAccess creates a new accessor for the given database files.
// It opens connections in read-only mode with connection pooling and prepares statements for high-frequency queries.
func NewRepositoryAccess(primaryPath, otherPath string) (*RepositoryAccess, error) {
	dsn, err := sqliteReadOnlyURI(primaryPath)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid primary db path: %w", models.ErrRepoMetadata, err)
	}
	otherURI, err := sqliteReadOnlyURI(otherPath)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid other db path: %w", models.ErrRepoMetadata, err)
	}

	pdb, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to open primary db: %w", models.ErrRepoMetadata, err)
	}

	var closers []func() error
	closers = append(closers, pdb.Close)
	success := false
	defer func() {
		if !success {
			for i := len(closers) - 1; i >= 0; i-- {
				_ = closers[i]()
			}
		}
	}()

	maxConns := runtime.NumCPU() * 2
	if maxConns < 4 {
		maxConns = 4
	}
	if maxConns > 16 {
		maxConns = 16
	}
	pdb.SetMaxOpenConns(maxConns)
	pdb.SetMaxIdleConns(maxConns)

	// Optimization: Attach 'other' DB via read-only file URI to primary connection
	if _, err := pdb.Exec("ATTACH DATABASE ? AS other", otherURI); err != nil {
		return nil, fmt.Errorf("%w: failed to attach other db: %w", models.ErrRepoMetadata, err)
	}

	// Enforce read-only safety and read performance pragmas
	pragmas := []string{
		"PRAGMA query_only = ON",
		"PRAGMA trusted_schema = OFF",
		"PRAGMA temp_store = MEMORY",
		"PRAGMA cache_size = -65536",
	}
	if runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64" {
		pragmas = append(pragmas, "PRAGMA mmap_size = 268435456")
	}
	for _, stmtSQL := range pragmas {
		_, _ = pdb.Exec(stmtSQL)
	}

	// Prepare statements once for high-frequency queries to avoid recompiling SQL 40k+ times
	stmt, err := pdb.Prepare(`SELECT author, date, changelog FROM other.changelog WHERE pkgKey = ? ORDER BY date DESC LIMIT 10`)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to prepare changelog statement: %w", models.ErrRepoMetadata, err)
	}
	closers = append(closers, stmt.Close)

	reqStmt, err := pdb.Prepare(`SELECT name, flags, epoch, version, release, pre FROM requires WHERE pkgKey = ? ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to prepare requires statement: %w", models.ErrRepoMetadata, err)
	}
	closers = append(closers, reqStmt.Close)

	provStmt, err := pdb.Prepare(`SELECT name, flags, epoch, version, release FROM provides WHERE pkgKey = ? ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to prepare provides statement: %w", models.ErrRepoMetadata, err)
	}
	closers = append(closers, provStmt.Close)

	confStmt, err := pdb.Prepare(`SELECT name, flags, epoch, version, release FROM conflicts WHERE pkgKey = ? ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to prepare conflicts statement: %w", models.ErrRepoMetadata, err)
	}
	closers = append(closers, confStmt.Close)

	obsStmt, err := pdb.Prepare(`SELECT name, flags, epoch, version, release FROM obsoletes WHERE pkgKey = ? ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to prepare obsoletes statement: %w", models.ErrRepoMetadata, err)
	}
	closers = append(closers, obsStmt.Close)

	// Detect optional RPM weak dependency tables cleanly via sqlite_master
	existingTables := inspectTables(pdb)
	prepareOptionalDep := func(table string) *sql.Stmt {
		if !existingTables[table] {
			return nil
		}
		var q string
		switch table {
		case "recommends":
			q = `SELECT name, flags, epoch, version, release FROM recommends WHERE pkgKey = ? ORDER BY name`
		case "suggests":
			q = `SELECT name, flags, epoch, version, release FROM suggests WHERE pkgKey = ? ORDER BY name`
		case "enhances":
			q = `SELECT name, flags, epoch, version, release FROM enhances WHERE pkgKey = ? ORDER BY name`
		case "supplements":
			q = `SELECT name, flags, epoch, version, release FROM supplements WHERE pkgKey = ? ORDER BY name`
		default:
			return nil
		}
		s, prepErr := pdb.Prepare(q)
		if prepErr != nil {
			return nil
		}
		closers = append(closers, s.Close)
		return s
	}

	recStmt := prepareOptionalDep("recommends")
	sugStmt := prepareOptionalDep("suggests")
	enhStmt := prepareOptionalDep("enhances")
	supStmt := prepareOptionalDep("supplements")

	success = true
	return &RepositoryAccess{
		PrimaryDB:     pdb,
		repoDir:       filepath.Dir(filepath.Dir(primaryPath)),
		changelogStmt: stmt,
		reqStmt:       reqStmt,
		provStmt:      provStmt,
		confStmt:      confStmt,
		obsStmt:       obsStmt,
		recStmt:       recStmt,
		sugStmt:       sugStmt,
		enhStmt:       enhStmt,
		supStmt:       supStmt,
	}, nil
}

func inspectTables(db *sql.DB) map[string]bool {
	tables := make(map[string]bool)
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return tables
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			tables[strings.ToLower(name)] = true
		}
	}
	return tables
}

// Close closes the database connections and all cached prepared statements
func (r *RepositoryAccess) Close() error {
	for _, s := range []*sql.Stmt{r.changelogStmt, r.reqStmt, r.provStmt, r.confStmt, r.obsStmt, r.recStmt, r.sugStmt, r.enhStmt, r.supStmt} {
		if s != nil {
			_ = s.Close()
		}
	}
	if r.PrimaryDB != nil {
		return r.PrimaryDB.Close()
	}
	return nil
}

func inspectPackagesColumns(db *sql.DB) map[string]bool {
	cols := make(map[string]bool)
	rows, err := db.Query(`PRAGMA table_info(packages)`)
	if err != nil {
		return cols
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			cid        int
			name, ctyp string
			notnull    int
			dflt       sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &ctyp, &notnull, &dflt, &pk); err == nil {
			cols[strings.ToLower(name)] = true
		}
	}
	return cols
}

// GetAllPackages retrieves all packages from the primary database, including optional extended columns when present.
func (r *RepositoryAccess) GetAllPackages() ([]*models.Package, error) {
	cols := inspectPackagesColumns(r.PrimaryDB)

	pkgIDExpr := "''"
	if cols["pkgid"] {
		pkgIDExpr = "pkgId"
	}
	checksumTypeExpr := "''"
	if cols["checksum_type"] {
		checksumTypeExpr = "checksum_type"
	}
	packagerExpr := "''"
	if cols["rpm_packager"] {
		packagerExpr = "rpm_packager"
	}
	timeFileExpr := "0"
	if cols["time_file"] {
		timeFileExpr = "time_file"
	}
	sizeArchiveExpr := "0"
	if cols["size_archive"] {
		sizeArchiveExpr = "size_archive"
	}

	// #nosec G201 -- optional expressions strictly select hardcoded literal column names verified via PRAGMA table_info
	query := fmt.Sprintf(`
		SELECT pkgKey, name, epoch, version, release, arch,
		       summary, description, url, time_build, rpm_license,
		       rpm_sourcerpm, size_package, location_href, rpm_vendor, rpm_group,
		       rpm_buildhost, size_installed,
		       %s, %s, %s, %s, %s
		FROM packages`, pkgIDExpr, checksumTypeExpr, packagerExpr, timeFileExpr, sizeArchiveExpr)

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
		var epoch, summary, description, urlStr, license, sourceRPM, vendor, group, buildHost sql.NullString
		var pkgID, checksumType, packager sql.NullString
		var installedSize, timeFile, sizeArchive sql.NullInt64

		err := rows.Scan(
			&p.PkgKey, &p.Name, &epoch, &p.Version, &p.Release, &p.Arch,
			&summary, &description, &urlStr, &p.TimeBuild, &license,
			&sourceRPM, &p.SizePackage, &p.LocationHref, &vendor, &group,
			&buildHost, &installedSize,
			&pkgID, &checksumType, &packager, &timeFile, &sizeArchive,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan package row: %w", err)
		}

		// Handle nullable fields
		p.Epoch = epoch.String
		p.Summary = summary.String
		p.Description = description.String
		p.URL = urlStr.String
		p.License = license.String
		p.SourceRPM = sourceRPM.String
		p.SourcePackage = sourceRPM.String
		p.Vendor = vendor.String
		p.RpmGroup = group.String
		p.BuildHost = buildHost.String
		p.InstalledSize = installedSize.Int64
		p.Packager = strings.TrimSpace(packager.String)
		p.TimeFile = timeFile.Int64
		p.SizeArchive = sizeArchive.Int64
		p.ChecksumType = strings.TrimSpace(checksumType.String)
		sum := strings.TrimSpace(pkgID.String)
		if sum != "" && (strings.EqualFold(p.ChecksumType, "sha256") || p.ChecksumType == "" || len(sum) == 64) {
			p.SHA256 = sum
		}
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
			r.warnf("Warning: failed to enrich changelog batch: %v\n", err)
			return err
		}
	}
	return nil
}

func (r *RepositoryAccess) enrichBatch(pkgs []*models.Package) error {
	ids := make([]interface{}, len(pkgs))
	pkgMap := make(map[int64]*models.Package, len(pkgs))

	for i, p := range pkgs {
		ids[i] = p.PkgKey
		pkgMap[p.PkgKey] = p
	}

	placeholders := strings.Repeat("?,", len(ids)-1) + "?"

	// Fetch changelog history per pkgKey ordered newest first, capping per-package entries at 10 in Go.
	// #nosec G201 -- placeholders is generated from slice length and contains only '?' tokens; package keys are bound via args
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query
	query := fmt.Sprintf(`
		SELECT pkgKey, author, date, changelog
		FROM other.changelog
		WHERE pkgKey IN (%s)
		ORDER BY pkgKey, date DESC
	`, placeholders)

	rows, err := r.PrimaryDB.Query(query, ids...)
	if err != nil {
		return fmt.Errorf("failed to query changelogs batch: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	const maxChangelogsPerPkg = 10
	for rows.Next() {
		var pkgKey int64
		var author, changelog sql.NullString
		var date int64

		if err := rows.Scan(&pkgKey, &author, &date, &changelog); err != nil {
			return fmt.Errorf("scan error: %w", err)
		}

		p, ok := pkgMap[pkgKey]
		if !ok || len(p.Changelogs) >= maxChangelogsPerPkg {
			continue
		}
		entry := &models.ChangelogEntry{
			Author:    cleanAuthor(author.String),
			Date:      date,
			Changelog: changelog.String,
		}
		p.Changelogs = append(p.Changelogs, entry)
		if p.Changelog == nil {
			p.Changelog = entry
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

// GetPackageDependencies fetches mandatory and optional weak dependency vectors for pkgKey.
func (r *RepositoryAccess) GetPackageDependencies(pkgKey int64) (*models.PackageDependencies, error) {
	deps := &models.PackageDependencies{}
	specs := []struct {
		kind     string
		stmt     *sql.Stmt
		fallback string
		hasPre   bool
		required bool
		target   *[]*models.DependencyEntry
	}{
		{"requires", r.reqStmt, `SELECT name, flags, epoch, version, release, pre FROM requires WHERE pkgKey = ? ORDER BY name`, true, true, &deps.Requires},
		{"provides", r.provStmt, `SELECT name, flags, epoch, version, release FROM provides WHERE pkgKey = ? ORDER BY name`, false, true, &deps.Provides},
		{"conflicts", r.confStmt, `SELECT name, flags, epoch, version, release FROM conflicts WHERE pkgKey = ? ORDER BY name`, false, true, &deps.Conflicts},
		{"obsoletes", r.obsStmt, `SELECT name, flags, epoch, version, release FROM obsoletes WHERE pkgKey = ? ORDER BY name`, false, true, &deps.Obsoletes},
		{"recommends", r.recStmt, "", false, false, &deps.Recommends},
		{"suggests", r.sugStmt, "", false, false, &deps.Suggests},
		{"enhances", r.enhStmt, "", false, false, &deps.Enhances},
		{"supplements", r.supStmt, "", false, false, &deps.Supplements},
	}

	for _, sp := range specs {
		if !sp.required && sp.stmt == nil {
			continue
		}
		entries, err := r.queryDepEntries(sp.stmt, sp.fallback, pkgKey, sp.hasPre)
		if err != nil {
			return nil, fmt.Errorf("failed to query %s for pkgKey %d: %w", sp.kind, pkgKey, err)
		}
		*sp.target = entries
	}

	return deps, nil
}

func (r *RepositoryAccess) queryDepEntries(stmt *sql.Stmt, fallbackSQL string, pkgKey int64, hasPre bool) ([]*models.DependencyEntry, error) {
	var rows *sql.Rows
	var err error
	if stmt != nil {
		rows, err = stmt.Query(pkgKey)
	} else {
		rows, err = r.PrimaryDB.Query(fallbackSQL, pkgKey)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", models.ErrRepoMetadata, err)
	}
	defer func() { _ = rows.Close() }()

	var entries []*models.DependencyEntry
	for rows.Next() {
		var name, flags, epoch, version, release sql.NullString
		var pre bool
		if hasPre {
			err = rows.Scan(&name, &flags, &epoch, &version, &release, &pre)
		} else {
			err = rows.Scan(&name, &flags, &epoch, &version, &release)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: scan dependency row: %w", models.ErrRepoMetadata, err)
		}
		entries = append(entries, &models.DependencyEntry{
			Name:    name.String,
			Flags:   flags.String,
			Epoch:   epoch.String,
			Version: version.String,
			Release: release.String,
			Pre:     pre,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate dependency rows: %w", models.ErrRepoMetadata, err)
	}
	return entries, nil
}

// EnrichPackagesWithAdvisories attaches errata advisories when updateinfo.xml* is present.
func (r *RepositoryAccess) EnrichPackagesWithAdvisories(pkgs []*models.Package) error {
	if r.updateInfoPath == "" || len(pkgs) == 0 {
		return nil
	}
	path, cleanup, err := DecompressFile(r.updateInfoPath)
	if err != nil {
		return err
	}
	defer cleanup()
	recs, err := ParseUpdateInfo(path)
	if err != nil {
		return err
	}
	AttachAdvisories(pkgs, recs)
	return nil
}

// EnrichPackageDetails concurrently parses the local RPM files for all packages
// and populates deep inspection details (scriptlets, signatures, bug/vcs links).
func (r *RepositoryAccess) EnrichPackageDetails(pkgs []*models.Package) {
	EnrichPackagesWithRPMDetails(r.repoDir, pkgs)
}

// ReadPackageFiles extracts on-demand file manifests during page render.
func (r *RepositoryAccess) ReadPackageFiles(repoDir string, pkg *models.Package) ([]models.PackageFile, error) {
	if pkg.LocationHref == "" {
		return nil, nil
	}
	rpmPath, err := util.ResolveContainedPath(repoDir, pkg.LocationHref)
	if err != nil {
		return nil, fmt.Errorf("invalid package location %s: %w", pkg.LocationHref, err)
	}
	return ReadRPMFiles(rpmPath)
}

var (
	_ RepoReader        = (*RepositoryAccess)(nil)
	_ ChangelogEnricher = (*RepositoryAccess)(nil)
	_ DependencyReader  = (*RepositoryAccess)(nil)
	_ AdvisoryEnricher  = (*RepositoryAccess)(nil)
)
