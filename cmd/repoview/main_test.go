package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/edsilegxrepo/repoview/internal/app"
	"github.com/edsilegxrepo/repoview/internal/models"
	_ "github.com/mattn/go-sqlite3"
)

// TEST STRATEGY:
// Validate the CLI interface, flag parser, early directory validation, error mappings,
// and process exit codes without polluting the repository.
//
// Test coverage includes:
//   - TestRun_Version: Validates that --version writes version string and returns ExitSuccess.
//   - TestRun_NoArgs: Validates that zero arguments prints usage and returns ExitUsageError.
//   - TestRun_InvalidFlag: Validates that an unknown flag returns ExitUsageError.
//   - TestRun_MissingRepo: Validates that a non-existent path returns ExitRepoMetadataError.
//   - TestRun_NotADirectory: Validates that passing a file instead of a directory returns ExitRepoMetadataError.
//   - TestRun_SafetyViolation: Validates that specifying --output-dir matching repoDir returns ExitUsageError.
//   - TestRun_StringList: Validates custom stringList flag accumulator.
//   - TestRun_FullPipeline: Validates successful run with real/fixture repository in t.TempDir().

func TestRun_Version(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--version"}, &stdout, &stderr)
	if code != app.ExitSuccess {
		t.Errorf("expected ExitSuccess (0), got %d", code)
	}
	if !strings.Contains(stdout.String(), "repoview version") {
		t.Errorf("expected stdout to contain version, got: %s", stdout.String())
	}
}

func TestRun_NoArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{}, &stdout, &stderr)
	if code != app.ExitUsageError {
		t.Errorf("expected ExitUsageError (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("expected stderr to contain usage, got: %s", stderr.String())
	}
}

func TestRun_InvalidFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--invalid-flag-123", "/tmp"}, &stdout, &stderr)
	if code != app.ExitUsageError {
		t.Errorf("expected ExitUsageError (2) on invalid flag, got %d", code)
	}
}

func TestRun_MissingRepo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	nonExistent := filepath.Join(t.TempDir(), "nonexistent_directory_abc")
	code := run([]string{nonExistent}, &stdout, &stderr)
	if code != app.ExitRepoMetadataError {
		t.Errorf("expected ExitRepoMetadataError (3), got %d", code)
	}
	if !strings.Contains(stderr.String(), "does not exist") {
		t.Errorf("expected stderr to mention 'does not exist', got: %s", stderr.String())
	}
}

func TestRun_NotADirectory(t *testing.T) {
	var stdout, stderr bytes.Buffer
	tempFile := filepath.Join(t.TempDir(), "regular_file.txt")
	if err := os.WriteFile(tempFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	code := run([]string{tempFile}, &stdout, &stderr)
	if code != app.ExitRepoMetadataError {
		t.Errorf("expected ExitRepoMetadataError (3) for regular file, got %d", code)
	}
}

func TestRun_SafetyViolation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	repoDir := t.TempDir()
	code := run([]string{"--force", "--output-dir", repoDir, repoDir}, &stdout, &stderr)
	if code != app.ExitSafetyError {
		t.Errorf("expected ExitSafetyError (%d) for safety violation, got %d", app.ExitSafetyError, code)
	}
	if !strings.Contains(stderr.String(), "Safety Violation") {
		t.Errorf("expected stderr to contain 'Safety Violation', got: %s", stderr.String())
	}
}

func TestStringList(t *testing.T) {
	var list stringList
	if err := list.Set("pattern1*"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := list.Set("pattern2*"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if list.String() != "pattern1*, pattern2*" {
		t.Errorf("expected 'pattern1*, pattern2*', got %q", list.String())
	}
}

func TestRun_FormatFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	repoDir := t.TempDir()

	// 1. Invalid format
	code := run([]string{"--format", "invalid_format", repoDir}, &stdout, &stderr)
	if code != app.ExitUsageError {
		t.Errorf("expected ExitUsageError (2) for invalid format, got %d", code)
	}
	if !strings.Contains(stderr.String(), "invalid repository format") {
		t.Errorf("expected stderr to contain 'invalid repository format', got: %s", stderr.String())
	}

	// 2. Valid format flags (deb and rpm) with missing repo metadata
	stderr.Reset()
	codeDeb := run([]string{"--format", "deb", repoDir}, &stdout, &stderr)
	if codeDeb != app.ExitRepoMetadataError {
		t.Errorf("expected ExitRepoMetadataError (3) for empty repo dir, got %d", codeDeb)
	}

	stderr.Reset()
	codeRPM := run([]string{"--format", "rpm", repoDir}, &stdout, &stderr)
	if codeRPM != app.ExitRepoMetadataError {
		t.Errorf("expected ExitRepoMetadataError (3) for empty repo dir, got %d", codeRPM)
	}
}

func TestRun_PortalURLFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	repoDir := t.TempDir()

	// Verify that passing --portal-url with valid values parses properly
	for _, portalOpt := range []string{"auto", "none", "off", "https://repo.example.com", "../index.html"} {
		stderr.Reset()
		code := run([]string{"--portal-url", portalOpt, repoDir}, &stdout, &stderr)
		if code != app.ExitRepoMetadataError {
			t.Errorf("expected ExitRepoMetadataError (3) for empty repo dir with portal-url=%s, got %d", portalOpt, code)
		}
	}
}

func createMockRPMRepo(t *testing.T, targetDir string) {
	repodataDir := filepath.Join(targetDir, "repodata")
	if err := os.MkdirAll(repodataDir, 0o755); err != nil {
		t.Fatalf("failed to create repodata dir: %v", err)
	}

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
	VALUES (1, 'mockapp', '0', '1.0', '1.el9', 'x86_64', 'Mock app summary', 'Mock description', 'http://example.com', 1600000000, 'MIT', 'mockapp-1.0-1.src.rpm', 1048576, 'Packages/m/mockapp-1.0-1.el9.x86_64.rpm', 'Mock Inc', 'Applications/Internet', 'builder.local', 2097152);
	`
	if _, err := pdb.Exec(insertPkg); err != nil {
		t.Fatalf("failed to insert package: %v", err)
	}

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
}

func TestRunPortal_Version(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"portal", "--version"}, &stdout, &stderr)
	if code != app.ExitSuccess {
		t.Errorf("expected ExitSuccess (0), got %d", code)
	}
	if !strings.Contains(stdout.String(), "repoview version") {
		t.Errorf("expected stdout to contain version, got: %s", stdout.String())
	}
}

func TestRunPortal_InvalidFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"portal", "--bogus-flag-xyz", "/tmp"}, &stdout, &stderr)
	if code != app.ExitUsageError {
		t.Errorf("expected ExitUsageError (2), got %d", code)
	}
}

func TestRunPortal_MissingDir(t *testing.T) {
	var stdout, stderr bytes.Buffer
	nonExistent := filepath.Join(t.TempDir(), "does_not_exist_portal_dir")
	code := run([]string{"portal", nonExistent}, &stdout, &stderr)
	if code != app.ExitRepoMetadataError {
		t.Errorf("expected ExitRepoMetadataError (3), got %d", code)
	}
	if !strings.Contains(stderr.String(), "does not exist") {
		t.Errorf("expected stderr to mention 'does not exist', got: %s", stderr.String())
	}
}

func TestRunPortal_NotADirectory(t *testing.T) {
	var stdout, stderr bytes.Buffer
	tempFile := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(tempFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	code := run([]string{"portal", tempFile}, &stdout, &stderr)
	if code != app.ExitRepoMetadataError {
		t.Errorf("expected ExitRepoMetadataError (3), got %d", code)
	}
}

func TestRunPortal_PureDiscovery_EmptyDir(t *testing.T) {
	var stdout, stderr bytes.Buffer
	tempDir := t.TempDir()

	code := run([]string{"portal", "--in-place", tempDir}, &stdout, &stderr)
	if code != app.ExitSuccess {
		t.Fatalf("expected ExitSuccess (0), got %d, stderr: %s", code, stderr.String())
	}

	indexPath := filepath.Join(tempDir, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		t.Errorf("expected index.html to be created: %v", err)
	}

	feedPath := filepath.Join(tempDir, "portal-feed.xml")
	if _, err := os.Stat(feedPath); err != nil {
		t.Errorf("expected portal-feed.xml to be created: %v", err)
	}

	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("failed to read index.html: %v", err)
	}
	if !strings.Contains(string(data), "RepoView-Portal") {
		t.Errorf("expected index.html to contain RepoView-Portal generator signature")
	}
}

func TestRunPortal_CustomOptions(t *testing.T) {
	var stdout, stderr bytes.Buffer
	tempDir := t.TempDir()
	outDir := filepath.Join(t.TempDir(), "custom_html")

	code := run([]string{
		"portal",
		"--title", "Custom Cloud Portal",
		"--description", "Multi-distribution mirror matrix",
		"--portal-url", "https://mirrors.corp.internal",
		"--portal-home", outDir,
		"--workers", "4",
		tempDir,
	}, &stdout, &stderr)

	if code != app.ExitSuccess {
		t.Fatalf("expected ExitSuccess (0), got %d, stderr: %s", code, stderr.String())
	}

	indexPath := filepath.Join(outDir, "index.html")
	feedPath := filepath.Join(outDir, "portal-feed.xml")

	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("failed to read index.html: %v", err)
	}
	if !strings.Contains(string(indexData), "Custom Cloud Portal") {
		t.Errorf("expected index.html to contain 'Custom Cloud Portal'")
	}

	feedData, err := os.ReadFile(feedPath)
	if err != nil {
		t.Fatalf("failed to read portal-feed.xml: %v", err)
	}
	if !strings.Contains(string(feedData), "https://mirrors.corp.internal") {
		t.Errorf("expected feed to contain custom baseurl")
	}
}

func TestRunPortal_DumpConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	tempDir := t.TempDir()
	dumpPath := filepath.Join(t.TempDir(), "starter.yaml")

	code := run([]string{"portal", "--dump-config", dumpPath, tempDir}, &stdout, &stderr)
	if code != app.ExitSuccess {
		t.Fatalf("expected ExitSuccess (0), got %d, stderr: %s", code, stderr.String())
	}

	// Verify dump file exists and contains starter YAML
	data, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("failed to read dumped config: %v", err)
	}
	if !strings.Contains(string(data), "auto_discovery") {
		t.Errorf("expected dumped config to contain 'auto_discovery', got: %s", string(data))
	}

	// Verify index.html was NOT generated in dump mode
	indexPath := filepath.Join(tempDir, "index.html")
	if _, err := os.Stat(indexPath); err == nil {
		t.Errorf("dump-config should not generate index.html")
	}
}

func TestRunPortal_ConfigOverrides(t *testing.T) {
	var stdout, stderr bytes.Buffer
	tempDir := t.TempDir()

	yamlContent := `title: "YAML Configured Title"
description: "Configured from portal.yaml"
auto_discovery:
  enabled: true
  max_depth: 3
`
	configPath := filepath.Join(tempDir, "portal.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// 1. Run using portal.yaml (without CLI title flag)
	code := run([]string{"portal", "--in-place", "--config", configPath, tempDir}, &stdout, &stderr)
	if code != app.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d, stderr: %s", code, stderr.String())
	}
	data, _ := os.ReadFile(filepath.Join(tempDir, "index.html"))
	if !strings.Contains(string(data), "YAML Configured Title") {
		t.Errorf("expected index.html to contain 'YAML Configured Title'")
	}

	// 2. Run with CLI flag overriding the YAML title
	stdout.Reset()
	stderr.Reset()
	codeCLI := run([]string{"portal", "--in-place", "--config", configPath, "--title", "CLI Supreme Title", tempDir}, &stdout, &stderr)
	if codeCLI != app.ExitSuccess {
		t.Fatalf("expected ExitSuccess with flag override, got %d, stderr: %s", codeCLI, stderr.String())
	}
	dataCLI, _ := os.ReadFile(filepath.Join(tempDir, "index.html"))
	if !strings.Contains(string(dataCLI), "CLI Supreme Title") {
		t.Errorf("expected index.html to contain overridden title 'CLI Supreme Title'")
	}

	// 3. Invalid config path returns ExitConfigError
	stderr.Reset()
	codeInvalid := run([]string{"portal", "--config", "/nonexistent/portal.yaml", tempDir}, &stdout, &stderr)
	if codeInvalid != app.ExitConfigError {
		t.Errorf("expected ExitConfigError (%d) on invalid config path, got %d", app.ExitConfigError, codeInvalid)
	}
}

func TestRunPortal_SafetyOverwriteGuard(t *testing.T) {
	var stdout, stderr bytes.Buffer
	tempDir := t.TempDir()

	foreignHTML := "<!DOCTYPE html><html><head><title>My Handcrafted Site</title></head><body>Do not delete</body></html>"
	indexPath := filepath.Join(tempDir, "index.html")
	if err := os.WriteFile(indexPath, []byte(foreignHTML), 0o644); err != nil {
		t.Fatalf("failed to create foreign index.html: %v", err)
	}

	// 1. Without --force: must fail with ExitSafetyError and report Safety Violation
	code := run([]string{"portal", "--in-place", tempDir}, &stdout, &stderr)
	if code != app.ExitSafetyError {
		t.Errorf("expected ExitSafetyError (%d) for safety violation, got %d", app.ExitSafetyError, code)
	}
	if !strings.Contains(stderr.String(), "Safety Violation") {
		t.Errorf("expected stderr to report Safety Violation, got: %s", stderr.String())
	}

	// Verify original file remained intact
	currentData, _ := os.ReadFile(indexPath)
	if !strings.Contains(string(currentData), "My Handcrafted Site") {
		t.Errorf("original file was improperly modified")
	}

	// 2. With --force: should overwrite successfully
	stderr.Reset()
	stdout.Reset()
	codeForce := run([]string{"portal", "--in-place", "--force", tempDir}, &stdout, &stderr)
	if codeForce != app.ExitSuccess {
		t.Fatalf("expected ExitSuccess with --force, got %d, stderr: %s", codeForce, stderr.String())
	}

	overwrittenData, _ := os.ReadFile(indexPath)
	if !strings.Contains(string(overwrittenData), "RepoView-Portal") {
		t.Errorf("expected index.html to be overwritten with RepoView portal")
	}
}

func TestRunPortal_RequireRendered(t *testing.T) {
	var stdout, stderr bytes.Buffer
	tempDir := t.TempDir()

	// Repo 1: Rendered
	renderedRepo := filepath.Join(tempDir, "centos9-rendered", "x86_64")
	_ = os.MkdirAll(filepath.Join(renderedRepo, "repodata"), 0o755)
	repoviewDir := filepath.Join(renderedRepo, "repoview")
	_ = os.MkdirAll(repoviewDir, 0o755)
	_ = os.WriteFile(filepath.Join(repoviewDir, "index.html"), []byte("<html>Rendered Centos9</html>"), 0o644)
	desc := models.RepoDescriptor{
		Title:        "Enterprise Linux 9 Rendered",
		Format:       "rpm",
		Arch:         "x86_64",
		PackageCount: 15,
		LastBuild:    time.Now().UTC(),
	}
	descBytes, _ := json.Marshal(desc)
	_ = os.WriteFile(filepath.Join(repoviewDir, "repoview.json"), descBytes, 0o644)

	// Repo 2: Unrendered raw repo
	rawRepo := filepath.Join(tempDir, "fedora-raw", "x86_64")
	_ = os.MkdirAll(filepath.Join(rawRepo, "repodata"), 0o755)

	// Run with --require-rendered
	code := run([]string{"portal", "--in-place", "--require-rendered", tempDir}, &stdout, &stderr)
	if code != app.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d, stderr: %s", code, stderr.String())
	}

	portalHTML, _ := os.ReadFile(filepath.Join(tempDir, "index.html"))
	portalContent := string(portalHTML)

	if !strings.Contains(portalContent, "Enterprise Linux 9 Rendered") {
		t.Errorf("expected portal to contain rendered repo title")
	}
	if strings.Contains(portalContent, "fedora-raw") {
		t.Errorf("portal should NOT contain unrendered fedora-raw repo when --require-rendered is enabled")
	}
}

func TestRunPortal_RenderMissing_Parallel(t *testing.T) {
	var stdout, stderr bytes.Buffer
	tempDir := t.TempDir()

	// Create 2 unrendered raw repos with valid SQLite metadata
	repo1 := filepath.Join(tempDir, "el9", "x86_64")
	createMockRPMRepo(t, repo1)

	repo2 := filepath.Join(tempDir, "el9", "aarch64")
	createMockRPMRepo(t, repo2)

	// Run portal with --render-missing and --workers 4
	code := run([]string{"portal", "--in-place", "--render-missing", "--workers", "4", tempDir}, &stdout, &stderr)
	if code != app.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d, stderr: %s", code, stderr.String())
	}

	// Verify both child repositories have been rendered
	for _, repo := range []string{repo1, repo2} {
		repoviewHTML := filepath.Join(repo, "repoview", "index.html")
		if fi, err := os.Stat(repoviewHTML); err != nil || fi.Size() == 0 {
			t.Errorf("expected rendered %s to exist and be non-empty", repoviewHTML)
		}

		repoviewJSON := filepath.Join(repo, "repoview", "repoview.json")
		if fi, err := os.Stat(repoviewJSON); err != nil || fi.Size() == 0 {
			t.Errorf("expected descriptor %s to exist", repoviewJSON)
		}

		// Check portal back link
		data, err := os.ReadFile(repoviewHTML)
		if err != nil {
			t.Fatalf("failed to read child index: %v", err)
		}
		if !strings.Contains(string(data), "portal-back-link") {
			t.Errorf("expected child index in %s to contain portal-back-link breadcrumb", repo)
		}
	}

	// Verify top-level portal index.html exists and links to the repos
	portalHTML, err := os.ReadFile(filepath.Join(tempDir, "index.html"))
	if err != nil {
		t.Fatalf("failed to read portal index.html: %v", err)
	}
	portalContent := string(portalHTML)
	if !strings.Contains(portalContent, "mockapp") && !strings.Contains(portalContent, "x86_64") {
		t.Errorf("portal index should contain discovered repo info")
	}
}

func TestRunPortal_RenderAll_And_TitlePropagation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	tempDir := t.TempDir()

	// 1. Create a raw repo (el9/base/x86_64)
	repo1 := filepath.Join(tempDir, "el9", "base", "x86_64")
	createMockRPMRepo(t, repo1)

	// 2. Create another repo (fedora/updates/x86_64) that is initially pre-rendered with an old title
	repo2 := filepath.Join(tempDir, "fedora", "updates", "x86_64")
	createMockRPMRepo(t, repo2)

	// Initial render of repo2
	code := run([]string{"--title", "Old Fedora Title", repo2}, &stdout, &stderr)
	if code != app.ExitSuccess {
		t.Fatalf("failed initial render of repo2: %s", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()

	// 3. Write a portal.yaml with custom titles
	cfgContent := `
title: "Enterprise Custom Portal"
overrides:
  "el9/base/x86_64":
    title: "Enterprise Linux 9 BaseOS"
  "fedora/updates/x86_64":
    title: "Fedora Modern Updates"
`
	cfgPath := filepath.Join(tempDir, "portal.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatalf("failed to write portal.yaml: %v", err)
	}

	// 4. Run portal with --render-all, --force, --workers 2
	code = run([]string{"portal", "--in-place", "--render-all", "--force", "--workers", "2", tempDir}, &stdout, &stderr)
	if code != app.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d, stderr: %s", code, stderr.String())
	}

	// 5. Verify both repos have their overridden titles in repoview.json and index.html
	checkRepoTitle := func(repoPath, wantTitle string) {
		jsonPath := filepath.Join(repoPath, "repoview", "repoview.json")
		jsonData, err := os.ReadFile(jsonPath)
		if err != nil {
			t.Fatalf("failed to read %s: %v", jsonPath, err)
		}
		if !strings.Contains(string(jsonData), wantTitle) {
			t.Errorf("expected %s descriptor to contain title %q, got: %s", repoPath, wantTitle, string(jsonData))
		}

		htmlPath := filepath.Join(repoPath, "repoview", "index.html")
		htmlData, err := os.ReadFile(htmlPath)
		if err != nil {
			t.Fatalf("failed to read %s: %v", htmlPath, err)
		}
		if !strings.Contains(string(htmlData), wantTitle) {
			t.Errorf("expected %s HTML to contain title %q", repoPath, wantTitle)
		}
	}

	checkRepoTitle(repo1, "Enterprise Linux 9 BaseOS")
	checkRepoTitle(repo2, "Fedora Modern Updates")

	// 6. Verify portal index.html has both titles and catalog title
	portalHTML, err := os.ReadFile(filepath.Join(tempDir, "index.html"))
	if err != nil {
		t.Fatalf("failed to read portal index.html: %v", err)
	}
	content := string(portalHTML)
	if !strings.Contains(content, "Enterprise Custom Portal") {
		t.Errorf("portal index missing catalog title")
	}
	if !strings.Contains(content, "Enterprise Linux 9 BaseOS") {
		t.Errorf("portal index missing repo1 title")
	}
	if !strings.Contains(content, "Fedora Modern Updates") {
		t.Errorf("portal index missing repo2 title")
	}
}

func TestRunPortal_RequiresPortalHomeOrInPlace(t *testing.T) {
	var stdout, stderr bytes.Buffer
	tempDir := t.TempDir()

	// 1. Omission of both --portal-home and --in-place must fail with ExitUsageError (2)
	code := run([]string{"portal", tempDir}, &stdout, &stderr)
	if code != app.ExitUsageError {
		t.Errorf("expected ExitUsageError (2) when omitting both flags, got %d", code)
	}
	if !strings.Contains(stderr.String(), "--portal-home") || !strings.Contains(stderr.String(), "--in-place") {
		t.Errorf("expected error message mentioning --portal-home and --in-place, got: %s", stderr.String())
	}

	// 2. Supplying both --portal-home and --in-place must fail with ExitUsageError (2)
	stdout.Reset()
	stderr.Reset()
	outDir := filepath.Join(t.TempDir(), "portal_out")
	codeBoth := run([]string{"portal", "--portal-home", outDir, "--in-place", tempDir}, &stdout, &stderr)
	if codeBoth != app.ExitUsageError {
		t.Errorf("expected ExitUsageError (2) when specifying both flags, got %d", codeBoth)
	}
	if !strings.Contains(stderr.String(), "cannot specify both") {
		t.Errorf("expected mutual exclusion error message, got: %s", stderr.String())
	}
}

func TestRunPortal_DetachedMode_CleanRepo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	tempDir := t.TempDir()
	reposRoot := filepath.Join(tempDir, "repos")
	portalHome := filepath.Join(tempDir, "portal_home")

	repo1 := filepath.Join(reposRoot, "el9", "base", "x86_64")
	createMockRPMRepo(t, repo1)

	// Record list of files in reposRoot before generation
	var filesBefore []string
	_ = filepath.Walk(reposRoot, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(reposRoot, path)
			filesBefore = append(filesBefore, rel)
		}
		return nil
	})
	sort.Strings(filesBefore)

	// Run detached portal generation
	code := run([]string{"portal", "--portal-home", portalHome, "--render-all", reposRoot}, &stdout, &stderr)
	if code != app.ExitSuccess {
		t.Fatalf("expected ExitSuccess (0), got %d, stderr: %s", code, stderr.String())
	}

	// Verify portal top-level artifacts
	if _, err := os.Stat(filepath.Join(portalHome, "index.html")); err != nil {
		t.Fatalf("expected portal index.html to exist in portalHome: %v", err)
	}
	if _, err := os.Stat(filepath.Join(portalHome, "portal-feed.xml")); err != nil {
		t.Fatalf("expected portal-feed.xml to exist in portalHome: %v", err)
	}

	// Verify child repository generated inside portalHome shadow tree
	childIndex := filepath.Join(portalHome, "el9", "base", "x86_64", "index.html")
	if _, err := os.Stat(childIndex); err != nil {
		t.Fatalf("expected child repo index.html to exist in shadow tree: %v", err)
	}
	childDesc := filepath.Join(portalHome, "el9", "base", "x86_64", "repoview.json")
	if _, err := os.Stat(childDesc); err != nil {
		t.Fatalf("expected child repo repoview.json in shadow tree: %v", err)
	}
	childPkg := filepath.Join(portalHome, "el9", "base", "x86_64", "mockapp.html")
	if _, err := os.Stat(childPkg); err != nil {
		t.Fatalf("expected child package page mockapp.html in shadow tree: %v", err)
	}

	// Verify raw reposRoot remained completely UNTOUCHED
	var filesAfter []string
	_ = filepath.Walk(reposRoot, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(reposRoot, path)
			filesAfter = append(filesAfter, rel)
		}
		return nil
	})
	sort.Strings(filesAfter)

	if len(filesBefore) != len(filesAfter) {
		t.Fatalf("reposRoot file count changed: before=%d, after=%d", len(filesBefore), len(filesAfter))
	}
	for i := range filesBefore {
		if filesBefore[i] != filesAfter[i] {
			t.Errorf("reposRoot file mismatch at %d: before=%s, after=%s", i, filesBefore[i], filesAfter[i])
		}
	}
}

func TestRunPortal_RepoURL_And_PortalURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	tempDir := t.TempDir()
	reposRoot := filepath.Join(tempDir, "repos")
	portalHome := filepath.Join(tempDir, "portal_home")

	repo1 := filepath.Join(reposRoot, "el9", "base", "x86_64")
	createMockRPMRepo(t, repo1)

	code := run([]string{
		"portal",
		"--portal-home", portalHome,
		"--portal-url", "https://portal.example.com",
		"--repo-url", "https://mirrors.example.com/repos",
		"--render-all",
		reposRoot,
	}, &stdout, &stderr)
	if code != app.ExitSuccess {
		t.Fatalf("expected ExitSuccess (0), got %d, stderr: %s", code, stderr.String())
	}

	// 1. Verify child repo package download links in mockapp.html
	childPkgHTML, err := os.ReadFile(filepath.Join(portalHome, "el9", "base", "x86_64", "mockapp.html"))
	if err != nil {
		t.Fatalf("failed to read mockapp.html: %v", err)
	}
	pkgContent := string(childPkgHTML)
	wantDownload := `href="https://mirrors.example.com/repos/el9/base/x86_64/Packages/m/mockapp-1.0-1.el9.x86_64.rpm"`
	if !strings.Contains(pkgContent, wantDownload) {
		t.Errorf("expected package download link %q in child package page, got:\n%s", wantDownload, pkgContent)
	}
	wantDataHref := `data-href="https://mirrors.example.com/repos/el9/base/x86_64/Packages/m/mockapp-1.0-1.el9.x86_64.rpm"`
	if !strings.Contains(pkgContent, wantDataHref) {
		t.Errorf("expected install tab data-href %q in child package page", wantDataHref)
	}

	// 2. Verify child repo .repo configuration in index.html
	childIndexHTML, err := os.ReadFile(filepath.Join(portalHome, "el9", "base", "x86_64", "index.html"))
	if err != nil {
		t.Fatalf("failed to read child index.html: %v", err)
	}
	indexContent := string(childIndexHTML)
	wantBaseURL := `baseurl=https://mirrors.example.com/repos/el9/base/x86_64`
	if !strings.Contains(indexContent, wantBaseURL) {
		t.Errorf("expected %q in child repository index.html", wantBaseURL)
	}

	// 3. Verify portal catalog card uses repo-url snippet and links to child repo in portal
	portalIndexHTML, err := os.ReadFile(filepath.Join(portalHome, "index.html"))
	if err != nil {
		t.Fatalf("failed to read portal index.html: %v", err)
	}
	portalContent := string(portalIndexHTML)
	if !strings.Contains(portalContent, wantBaseURL) {
		t.Errorf("expected portal catalog card snippet to contain %q", wantBaseURL)
	}
	wantChildLink := `href="el9/base/x86_64/index.html"`
	if !strings.Contains(portalContent, wantChildLink) {
		t.Errorf("expected portal catalog to link to child repo with %q", wantChildLink)
	}
}

func TestRun_HelpAndGranularExitCodes(t *testing.T) {
	// 1. --help and portal --help must return ExitSuccess (0)
	for _, helpArgs := range [][]string{{"--help"}, {"portal", "--help"}} {
		var stdout, stderr bytes.Buffer
		if code := run(helpArgs, &stdout, &stderr); code != app.ExitSuccess {
			t.Errorf("run(%v) returned %d; want ExitSuccess (%d)", helpArgs, code, app.ExitSuccess)
		}
	}

	// 2. Broken custom template returns ExitTemplateError (4)
	repoDir := t.TempDir()
	createMockRPMRepo(t, repoDir)
	badTmpl := t.TempDir()
	_ = os.WriteFile(filepath.Join(badTmpl, "index.html"), []byte("{{ .Broken "), 0o644)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--template-dir", badTmpl, "--output-dir", t.TempDir(), repoDir}, &stdout, &stderr); code != app.ExitTemplateError {
		t.Errorf("expected ExitTemplateError (%d) on broken template, got %d, stderr=%s", app.ExitTemplateError, code, stderr.String())
	}

	// 3. Portal partial child failure returns ExitPartialFailure (6)
	portalRoot := t.TempDir()
	portalHome := t.TempDir()
	// Healthy child repo
	goodRepo := filepath.Join(portalRoot, "el9", "base", "x86_64")
	createMockRPMRepo(t, goodRepo)
	// Broken child repo (missing SQLite DBs)
	badRepo := filepath.Join(portalRoot, "el9", "broken", "x86_64")
	_ = os.MkdirAll(filepath.Join(badRepo, "repodata"), 0o755)
	_ = os.WriteFile(filepath.Join(badRepo, "repodata", "repomd.xml"), []byte("<repomd/>"), 0o644)

	stdout.Reset()
	stderr.Reset()
	codePartial := run([]string{"portal", "--portal-home", portalHome, "--render-all", portalRoot}, &stdout, &stderr)
	if codePartial != app.ExitPartialFailure {
		t.Errorf("expected ExitPartialFailure (%d) on portal partial child failure, got %d, stderr=%s", app.ExitPartialFailure, codePartial, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(portalHome, "index.html")); err != nil {
		t.Errorf("expected portal index.html to still be generated for healthy repos: %v", err)
	}
}

func TestRun_RepoYAMLAndSuiteArchFlags(t *testing.T) {
	repoDir := t.TempDir()
	outDir := t.TempDir()
	createMockRPMRepo(t, repoDir)

	// repoview.yaml supplies default title & description; CLI --title overrides repoview.yaml
	yamlCfg := "title: From Repo YAML\ndescription: Custom Repo Description\n"
	if err := os.WriteFile(filepath.Join(repoDir, "repoview.yaml"), []byte(yamlCfg), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"--output-dir", outDir, "--title", "CLI Override Title", "--suite", "custom-suite", "--arch", "x86_64", repoDir}, &stdout, &stderr)
	if code != app.ExitSuccess {
		t.Fatalf("run returned %d; stderr=%s", code, stderr.String())
	}

	indexHTML, err := os.ReadFile(filepath.Join(outDir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(indexHTML), "CLI Override Title") {
		t.Errorf("expected CLI --title to override repoview.yaml title")
	}
	if !strings.Contains(string(indexHTML), "Custom Repo Description") {
		t.Errorf("expected repoview.yaml description in index.html")
	}
}

func TestRun_ExitCodeMatrix(t *testing.T) {
	if app.ExitSuccess != 0 || app.ExitGeneralError != 1 || app.ExitUsageError != 2 ||
		app.ExitRepoMetadataError != 3 || app.ExitTemplateError != 4 || app.ExitIOError != 5 ||
		app.ExitPartialFailure != 6 || app.ExitConfigError != 7 || app.ExitSafetyError != 8 {
		t.Fatalf("unexpected exit code constant values")
	}

	var stdout, stderr bytes.Buffer

	// 0: ExitSuccess
	if code := run([]string{"--version"}, &stdout, &stderr); code != app.ExitSuccess {
		t.Errorf("expected ExitSuccess (0), got %d", code)
	}
	// 2: ExitUsageError (missing args & invalid format)
	if code := run([]string{"--format", "invalid", t.TempDir()}, &stdout, &stderr); code != app.ExitUsageError {
		t.Errorf("expected ExitUsageError (2), got %d", code)
	}
	// 3: ExitRepoMetadataError (non-existent repo dir)
	if code := run([]string{filepath.Join(t.TempDir(), "missing-repo")}, &stdout, &stderr); code != app.ExitRepoMetadataError {
		t.Errorf("expected ExitRepoMetadataError (3), got %d", code)
	}
	// 5: ExitIOError (--state-dir pointing to regular file)
	rpmDir := t.TempDir()
	createMockRPMRepo(t, rpmDir)
	blockerFile := filepath.Join(t.TempDir(), "not-a-dir")
	_ = os.WriteFile(blockerFile, []byte("x"), 0o644)
	if code := run([]string{"--state-dir", blockerFile, "--output-dir", t.TempDir(), rpmDir}, &stdout, &stderr); code != app.ExitIOError {
		t.Errorf("expected ExitIOError (5), got %d", code)
	}
	// 7: ExitSafetyError (--output-dir same as repoDir)
	if code := run([]string{"--output-dir", rpmDir, rpmDir}, &stdout, &stderr); code != app.ExitSafetyError {
		t.Errorf("expected ExitSafetyError (7), got %d", code)
	}
	// 8: ExitConfigError (invalid repoview.yaml syntax)
	badCfgRepo := t.TempDir()
	createMockRPMRepo(t, badCfgRepo)
	_ = os.WriteFile(filepath.Join(badCfgRepo, "repoview.yaml"), []byte("title: [unclosed"), 0o644)
	if code := run([]string{"--output-dir", t.TempDir(), badCfgRepo}, &stdout, &stderr); code != app.ExitConfigError {
		t.Errorf("expected ExitConfigError (8), got %d", code)
	}
}

