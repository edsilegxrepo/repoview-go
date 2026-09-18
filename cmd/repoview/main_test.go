package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
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
	if code != app.ExitUsageError {
		t.Errorf("expected ExitUsageError (2) for safety violation, got %d", code)
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
	VALUES (1, 'mockapp', '0', '1.0', '1.el9', 'x86_64', 'Mock app summary', 'Mock description', 'http://example.com', 1600000000, 'MIT', 'mockapp-1.0-1.src.rpm', 1048576, '', 'Mock Inc', 'Applications/Internet', 'builder.local', 2097152);
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

	code := run([]string{"portal", tempDir}, &stdout, &stderr)
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
		"--baseurl", "https://mirrors.corp.internal",
		"--output-dir", outDir,
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
	code := run([]string{"portal", "--config", configPath, tempDir}, &stdout, &stderr)
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
	codeCLI := run([]string{"portal", "--config", configPath, "--title", "CLI Supreme Title", tempDir}, &stdout, &stderr)
	if codeCLI != app.ExitSuccess {
		t.Fatalf("expected ExitSuccess with flag override, got %d, stderr: %s", codeCLI, stderr.String())
	}
	dataCLI, _ := os.ReadFile(filepath.Join(tempDir, "index.html"))
	if !strings.Contains(string(dataCLI), "CLI Supreme Title") {
		t.Errorf("expected index.html to contain overridden title 'CLI Supreme Title'")
	}

	// 3. Invalid config path returns ExitUsageError
	stderr.Reset()
	codeInvalid := run([]string{"portal", "--config", "/nonexistent/portal.yaml", tempDir}, &stdout, &stderr)
	if codeInvalid != app.ExitUsageError {
		t.Errorf("expected ExitUsageError (2) on invalid config path, got %d", codeInvalid)
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

	// 1. Without --force: must fail with ExitUsageError and report Safety Violation
	code := run([]string{"portal", tempDir}, &stdout, &stderr)
	if code != app.ExitUsageError {
		t.Errorf("expected ExitUsageError (2) for safety violation, got %d", code)
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
	codeForce := run([]string{"portal", "--force", tempDir}, &stdout, &stderr)
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
	code := run([]string{"portal", "--require-rendered", tempDir}, &stdout, &stderr)
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
	code := run([]string{"portal", "--render-missing", "--workers", "4", tempDir}, &stdout, &stderr)
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
