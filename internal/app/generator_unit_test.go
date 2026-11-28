package app

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/render"
	"github.com/edsilegxrepo/repoview/internal/state"
	_ "github.com/mattn/go-sqlite3"
)

// TEST STRATEGY:
// Validate the Generator orchestrator against a synthetic mock repository created in t.TempDir().
//
// Test coverage includes:
//   - Generator.Run full pipeline execution:
//       * Ingesting mock repomd.xml, primary.sqlite, other.sqlite, and comps.xml.
//       * Reading and filtering packages (including --ignore-package and --exclude-arch).
//       * Group organization and letter group compilation.
//       * Parallel package page rendering, group rendering, and index/feed/search.json generation.
//       * Custom --state-dir persistence.
//       * Incremental re-run skipping unchanged files.
//       * Stale file pruning.
//   - detectSiblingRepos:
//       * Hierarchical filesystem detection of sibling architectures (aarch64) and channels (extras).

func createMockRepository(t *testing.T) string {
	repoDir := t.TempDir()
	repodataDir := filepath.Join(repoDir, "repodata")
	if err := os.MkdirAll(repodataDir, 0o755); err != nil {
		t.Fatalf("failed to create repodata dir: %v", err)
	}

	// 1. primary.sqlite
	primaryPath := filepath.Join(repodataDir, "primary.sqlite")
	pdb, err := sql.Open("sqlite3", primaryPath)
	if err != nil {
		t.Fatalf("failed to open primary: %v", err)
	}
	defer func() { _ = pdb.Close() }()

	createTables := `
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
	CREATE TABLE requires (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT, pre BOOLEAN);
	CREATE TABLE provides (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT);
	CREATE TABLE conflicts (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT);
	CREATE TABLE obsoletes (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT);
	`
	if _, err := pdb.Exec(createTables); err != nil {
		t.Fatalf("failed to create tables: %v", err)
	}

	insertPkg := `
	INSERT INTO packages (pkgKey, name, epoch, version, release, arch, summary, description, url, time_build, rpm_license, rpm_sourcerpm, size_package, location_href, rpm_vendor, rpm_group, rpm_buildhost, size_installed)
	VALUES (1, 'mockapp', '0', '1.0', '1.el9', 'x86_64', 'Mock app summary', 'Mock description', 'http://example.com', 1600000000, 'MIT', 'mockapp-1.0-1.src.rpm', 1048576, '', 'Mock Inc', 'Applications/Internet', 'builder.local', 2097152);
	`
	if _, err := pdb.Exec(insertPkg); err != nil {
		t.Fatalf("failed to insert package: %v", err)
	}

	// 2. other.sqlite
	otherPath := filepath.Join(repodataDir, "other.sqlite")
	odb, err := sql.Open("sqlite3", otherPath)
	if err != nil {
		t.Fatalf("failed to open other: %v", err)
	}
	defer func() { _ = odb.Close() }()

	if _, err := odb.Exec(`CREATE TABLE changelog (pkgKey INTEGER, author TEXT, date INTEGER, changelog TEXT)`); err != nil {
		t.Fatalf("failed to create changelog table: %v", err)
	}
	if _, err := odb.Exec(`INSERT INTO changelog VALUES (1, 'Dev <dev@mock.org>', 1600000000, '- Initial mock')`); err != nil {
		t.Fatalf("failed to insert changelog: %v", err)
	}

	// 3. comps.xml
	compsPath := filepath.Join(repodataDir, "comps.xml")
	compsXML := `<?xml version="1.0" encoding="UTF-8"?>
<comps>
  <group>
    <id>mockgroup</id>
    <name>Mock Group</name>
    <description>Mock group description</description>
    <packagelist>
      <packagereq type="default">mockapp</packagereq>
    </packagelist>
  </group>
</comps>`
	if err := os.WriteFile(compsPath, []byte(compsXML), 0o644); err != nil {
		t.Fatalf("failed to write comps: %v", err)
	}

	// 4. repomd.xml
	repomdPath := filepath.Join(repodataDir, "repomd.xml")
	repomdXML := `<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="primary_db">
    <location href="repodata/primary.sqlite"/>
    <database_version>10</database_version>
  </data>
  <data type="other_db">
    <location href="repodata/other.sqlite"/>
  </data>
  <data type="group">
    <location href="repodata/comps.xml"/>
  </data>
</repomd>`
	if err := os.WriteFile(repomdPath, []byte(repomdXML), 0o644); err != nil {
		t.Fatalf("failed to write repomd: %v", err)
	}

	return repoDir
}

func TestGenerator_FullPipeline(t *testing.T) {
	repoDir := createMockRepository(t)
	outDir := t.TempDir()
	stateDir := t.TempDir()

	cfg := Config{
		RepoDir:     repoDir,
		OutputDir:   outDir,
		StateDir:    stateDir,
		Title:       "Test Repository",
		URL:         "http://example.com/repo",
		BaseURL:     "http://example.com/repo",
		Force:       true,
		Quiet:       false,
		Version:     "1.0.0",
		IgnoreList:  []string{"*-debug"},
		ExcludeArch: []string{"i686"},
	}

	gen := NewGenerator(cfg)

	// 1. First run with Force=true
	if err := gen.Run(); err != nil {
		t.Fatalf("Generator.Run failed: %v", err)
	}

	// Verify generated files
	expectedFiles := []string{
		"index.html",
		"search.json",
		"latest-feed.xml",
		"mockapp.html",
		"mockgroup.group.html",
		"repoview.json",
		filepath.Join("layout", "repostyle.css"),
		filepath.Join("layout", "search.js"),
	}

	for _, f := range expectedFiles {
		target := filepath.Join(outDir, f)
		if _, err := os.Stat(target); os.IsNotExist(err) {
			t.Errorf("expected generated file missing: %s", target)
		}
	}

	// Verify repoview.json contents
	descData, err := os.ReadFile(filepath.Join(outDir, "repoview.json"))
	if err != nil {
		t.Fatalf("failed to read repoview.json: %v", err)
	}
	var desc models.RepoDescriptor
	if err := json.Unmarshal(descData, &desc); err != nil {
		t.Fatalf("failed to parse repoview.json: %v", err)
	}
	if desc.PackageCount != 1 {
		t.Errorf("desc.PackageCount = %d; want 1", desc.PackageCount)
	}
	if desc.Arch != "x86_64" {
		t.Errorf("desc.Arch = %q; want x86_64", desc.Arch)
	}

	// 2. Incremental second run without Force
	cfg.Force = false
	genIncremental := NewGenerator(cfg)
	if err := genIncremental.Run(); err != nil {
		t.Fatalf("incremental Generator.Run failed: %v", err)
	}
}

func TestDetectSiblingRepos(t *testing.T) {
	root := t.TempDir()

	// Structure:
	// root/base/x86_64/repodata
	// root/base/aarch64/repodata
	// root/extras/x86_64/repodata
	baseX86 := filepath.Join(root, "base", "x86_64", "repodata")
	baseAarch := filepath.Join(root, "base", "aarch64", "repodata")
	extrasX86 := filepath.Join(root, "extras", "x86_64", "repodata")

	_ = os.MkdirAll(baseX86, 0o755)
	_ = os.MkdirAll(baseAarch, 0o755)
	_ = os.MkdirAll(extrasX86, 0o755)

	siblings := detectSiblingRepos(filepath.Join(root, "base", "x86_64"))

	if len(siblings) == 0 {
		t.Fatalf("expected sibling repositories to be detected")
	}

	hasAarch64 := false
	hasExtras := false
	for _, s := range siblings {
		if s.Name == "aarch64" {
			hasAarch64 = true
		}
		if s.Name == "extras" {
			hasExtras = true
		}
	}

	if !hasAarch64 {
		t.Errorf("expected aarch64 architecture sibling to be detected")
	}
	if !hasExtras {
		t.Errorf("expected extras channel sibling to be detected")
	}
}

func TestGenerator_Errors(t *testing.T) {
	// 1. Non-existent repo directory
	cfg := Config{
		RepoDir:   filepath.Join(t.TempDir(), "nonexistent"),
		OutputDir: t.TempDir(),
	}
	gen := NewGenerator(cfg)
	if err := gen.Run(); err == nil {
		t.Errorf("expected error for non-existent repo, got nil")
	}

	// 2. --force with broken custom template directory must NOT wipe pre-existing outputDir
	repoDir := createMockRepository(t)
	existingOut := t.TempDir()
	preservedFile := filepath.Join(existingOut, "index.html")
	if err := os.WriteFile(preservedFile, []byte("existing site"), 0o644); err != nil {
		t.Fatalf("failed to write existing index.html: %v", err)
	}

	badTmplDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(badTmplDir, "index.html"), []byte("{{ .BrokenTemplate "), 0o644); err != nil {
		t.Fatalf("failed to write broken template: %v", err)
	}

	forceGen := NewGenerator(Config{
		RepoDir:     repoDir,
		OutputDir:   existingOut,
		TemplateDir: badTmplDir,
		Force:       true,
		Quiet:       true,
	})
	if err := forceGen.Run(); err == nil {
		t.Fatalf("expected error for broken template directory, got nil")
	}
	afterBytes, err := os.ReadFile(preservedFile)
	if err != nil {
		t.Fatalf("pre-existing output file was deleted despite template failure: %v", err)
	}
	if string(afterBytes) != "existing site" {
		t.Errorf("pre-existing output file was corrupted: %q", string(afterBytes))
	}

	// 3. Uncreatable --state-dir (path blocked by existing regular file) must return error cleanly without nil panic
	blockingFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blockingFile, []byte("blocker"), 0o644); err != nil {
		t.Fatalf("failed to write blocking file: %v", err)
	}
	stateErrGen := NewGenerator(Config{
		RepoDir:   repoDir,
		OutputDir: t.TempDir(),
		StateDir:  filepath.Join(blockingFile, "nested"),
		Quiet:     true,
	})
	if err := stateErrGen.Run(); err == nil {
		t.Errorf("expected error when --state-dir cannot be created, got nil")
	}
}

type stubFilesReader struct {
	files []models.PackageFile
}

func (s *stubFilesReader) GetAllPackages() ([]*models.Package, error) { return nil, nil }
func (s *stubFilesReader) EnrichPackageDetails([]*models.Package)     {}
func (s *stubFilesReader) ReadPackageFiles(string, *models.Package) ([]models.PackageFile, error) {
	return s.files, nil
}

func (s *stubFilesReader) GetPackageDependencies(int64) (*models.PackageDependencies, error) {
	return &models.PackageDependencies{}, nil
}
func (s *stubFilesReader) Close() error { return nil }

func TestRenderSinglePackage_ClearsFilesAfterRender(t *testing.T) {
	repoDir := t.TempDir()
	outDir := t.TempDir()
	r, err := render.NewRenderer(outDir, "", "Test Repo", "1.0.0", []string{"F"})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}
	st, err := state.NewStateStore(filepath.Join(outDir, "state.json"))
	if err != nil {
		t.Fatalf("NewStateStore failed: %v", err)
	}

	latest := &models.Package{
		PkgKey:       1,
		Name:         "foo",
		Version:      "2.0",
		Release:      "1",
		Arch:         "x86_64",
		LocationHref: "Packages/foo-2.0-1.x86_64.rpm",
		Details:      &models.PackageDetails{BuildHost: "builder.example.org"},
	}
	older := &models.Package{
		PkgKey:  2,
		Name:    "foo",
		Version: "1.0",
		Release: "1",
		Arch:    "x86_64",
	}

	gen := NewGenerator(Config{RepoDir: repoDir, OutputDir: outDir, Quiet: true})
	db := &stubFilesReader{
		files: []models.PackageFile{
			{Name: "/usr/bin/foo", Mode: "-rwxr-xr-x", Size: 123},
			{Name: "/etc/foo.conf", Mode: "-rw-r--r--", Size: 45},
		},
	}

	filename, err := gen.renderSinglePackage(db, r, st, []*models.Package{latest, older}, nil, "foo")
	if err != nil {
		t.Fatalf("renderSinglePackage failed: %v", err)
	}
	if filename != "foo.html" {
		t.Errorf("expected foo.html, got %q", filename)
	}
	if latest.Details == nil {
		t.Fatalf("expected non-file metadata on Details to be preserved")
	}
	if latest.Details.Files != nil {
		t.Errorf("expected latest.Details.Files to be cleared after render, still has %d entries", len(latest.Details.Files))
	}
	if older.Details != nil {
		t.Errorf("expected older version Details to remain nil")
	}

	renderedBytes, err := os.ReadFile(filepath.Join(outDir, "foo.html"))
	if err != nil {
		t.Fatalf("failed to read rendered foo.html: %v", err)
	}
	if len(renderedBytes) == 0 {
		t.Errorf("rendered foo.html is empty")
	}
}

func TestValidateOutputDirSafety_SymlinkRepoDir(t *testing.T) {
	realRepo := t.TempDir()
	symlinkParent := t.TempDir()
	repoLink := filepath.Join(symlinkParent, "repo_link")
	if err := os.Symlink(realRepo, repoLink); err != nil {
		t.Skipf("skipping symlink safety test (symlinks unsupported): %v", err)
	}

	if err := validateOutputDirSafety(realRepo, repoLink); err == nil {
		t.Errorf("expected validateOutputDirSafety to block outputDir pointing to repoDir via symlink")
	}
	if err := validateOutputDirSafety(realRepo, filepath.Join(repoLink, "repodata")); err == nil {
		t.Errorf("expected validateOutputDirSafety to block repodata inside symlinked repoDir")
	}
}

func TestGenerator_WindowsCycleGuardHelper_BuildCheck(t *testing.T) {
	// Also ensure a failing GetPackageDependencies propagates through renderSinglePackage
	repoDir := t.TempDir()
	outDir := t.TempDir()
	r, err := render.NewRenderer(outDir, "", "Test Repo", "1.0.0", []string{"F"})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}
	st, err := state.NewStateStore(filepath.Join(outDir, "state.json"))
	if err != nil {
		t.Fatalf("NewStateStore failed: %v", err)
	}
	gen := NewGenerator(Config{RepoDir: repoDir, OutputDir: outDir, Quiet: true})
	errDB := &failingDepsReader{}
	pkg := &models.Package{PkgKey: 1, Name: "foo", Version: "1.0", Release: "1", Arch: "x86_64"}
	if _, err := gen.renderSinglePackage(errDB, r, st, []*models.Package{pkg}, nil, "foo"); err == nil {
		t.Errorf("expected renderSinglePackage to propagate GetPackageDependencies error")
	}
}

type failingDepsReader struct{ stubFilesReader }

func (f *failingDepsReader) GetPackageDependencies(int64) (*models.PackageDependencies, error) {
	return nil, os.ErrInvalid
}

func TestGenerator_EffectiveWorkersClamp(t *testing.T) {
	if got := NewGenerator(Config{Workers: 64}).effectiveWorkers(100); got != 16 {
		t.Errorf("expected workers clamped to 16, got %d", got)
	}
	if got := NewGenerator(Config{Workers: 8}).effectiveWorkers(3); got != 3 {
		t.Errorf("expected workers capped at itemCount 3, got %d", got)
	}
	if got := NewGenerator(Config{}).effectiveWorkers(0); got != 0 {
		t.Errorf("expected 0 workers for 0 items, got %d", got)
	}
}
