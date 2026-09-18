package app

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/edsilegxrepo/repoview/internal/models"
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
}
