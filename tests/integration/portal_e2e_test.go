//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/edsilegxrepo/repoview/internal/models"
	_ "github.com/mattn/go-sqlite3"
)

// TEST STRATEGY:
// Validate Multi-Repository Portal & Enterprise Security matrices end-to-end across:
//   1. Topology 1: Nested Hierarchical Trees (el8/base/x86_64, el9/base/x86_64).
//   2. Topology 2: Flat / Mixed Format Slugs (el-9-x86_64, ubuntu-24.04-x86_64).
//   3. Topology 3: Dedicated Repoview Tree (el10-base.x86_64).
//   4. Topology 4: Debian Multi-Suite / Multi-Component Pools (ubuntu/dists/noble/... with pool/ branch pruning).
//   5. Live HTTP Server testing on loopback: Verifying 200 OK for /index.html, /portal-feed.xml, and /portal-search.json.
//   6. Real Subprocess CLI execution with '--render-missing', '--render-all', detached '--portal-home', '--dump-config' merge, and bounded worker pool.
//   7. Matrices D & E: Cross-distro RPM/DEB/MIX portals, custom taxonomy rules, XSS/traversal defenses, and CLI exit codes 0-8.

func setupMockRPMData(t *testing.T, repoDir, pkgName string) {
	repodataDir := filepath.Join(repoDir, "repodata")
	if err := os.MkdirAll(repodataDir, 0o755); err != nil {
		t.Fatalf("failed to create repodata dir: %v", err)
	}

	primaryPath := filepath.Join(repodataDir, "primary.sqlite")
	pdb, err := sql.Open("sqlite3", primaryPath)
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	defer func() { _ = pdb.Close() }()

	createTables := `
	CREATE TABLE packages (
		pkgKey INTEGER PRIMARY KEY,
		name TEXT, epoch TEXT, version TEXT, release TEXT, arch TEXT,
		summary TEXT, description TEXT, url TEXT, time_build INTEGER,
		rpm_license TEXT, rpm_sourcerpm TEXT, size_package INTEGER,
		location_href TEXT, rpm_vendor TEXT, rpm_group TEXT,
		rpm_buildhost TEXT, size_installed INTEGER
	);
	CREATE TABLE requires (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT, pre BOOLEAN);
	CREATE TABLE provides (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT);
	CREATE TABLE conflicts (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT);
	CREATE TABLE obsoletes (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT);
	`
	if _, err := pdb.Exec(createTables); err != nil {
		t.Fatalf("failed to create tables: %v", err)
	}

	insertPkg := fmt.Sprintf(`
	INSERT INTO packages (pkgKey, name, epoch, version, release, arch, summary, description, url, time_build, rpm_license, rpm_sourcerpm, size_package, location_href, rpm_vendor, rpm_group, rpm_buildhost, size_installed)
	VALUES (1, '%s', '0', '1.0', '1.el9', 'x86_64', 'Summary for %s', 'Description', 'http://example.com', 1600000000, 'MIT', '%s-1.0-1.src.rpm', 1048576, '', 'Vendor Inc', 'Applications/System', 'builder.local', 2097152);
	`, pkgName, pkgName, pkgName)
	if _, err := pdb.Exec(insertPkg); err != nil {
		t.Fatalf("failed to insert package: %v", err)
	}

	otherPath := filepath.Join(repodataDir, "other.sqlite")
	odb, err := sql.Open("sqlite3", otherPath)
	if err != nil {
		t.Fatalf("failed to open other.sqlite: %v", err)
	}
	defer func() { _ = odb.Close() }()

	if _, err := odb.Exec(`CREATE TABLE changelog (pkgKey INTEGER, author TEXT, date INTEGER, changelog TEXT)`); err != nil {
		t.Fatalf("failed to create changelog: %v", err)
	}

	compsXML := `<?xml version="1.0" encoding="UTF-8"?><comps></comps>`
	_ = os.WriteFile(filepath.Join(repodataDir, "comps.xml"), []byte(compsXML), 0o644)

	repomdXML := `<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="primary_db"><location href="repodata/primary.sqlite"/><database_version>10</database_version></data>
  <data type="other_db"><location href="repodata/other.sqlite"/></data>
</repomd>`
	_ = os.WriteFile(filepath.Join(repodataDir, "repomd.xml"), []byte(repomdXML), 0o644)
}

func setupMockDebData(t *testing.T, repoDir, suite, component, arch, pkgName string) {
	leaf := filepath.Join(repoDir, "dists", suite, component, "binary-"+arch)
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatalf("failed to create deb leaf: %v", err)
	}

	pkgBlock := fmt.Sprintf("Package: %s\nVersion: 1.0.0-1\nArchitecture: %s\nMaintainer: Builder <dev@test.org>\nDescription: Test package\nFilename: pool/%s/%s_1.0.0-1_%s.deb\nSize: 1024\n\n",
		pkgName, arch, component, pkgName, arch)
	if err := os.WriteFile(filepath.Join(leaf, "Packages"), []byte(pkgBlock), 0o644); err != nil {
		t.Fatalf("failed to write Packages: %v", err)
	}
}

func TestLive_Portal_TopologiesAndLiveServer(t *testing.T) {
	homePath := t.TempDir()

	// 1. Topology 1: Nested Hierarchical Trees
	topo1_el8 := filepath.Join(homePath, "el8", "base", "x86_64")
	setupMockRPMData(t, topo1_el8, "kernel-core")
	topo1_el9 := filepath.Join(homePath, "el9", "base", "x86_64")
	setupMockRPMData(t, topo1_el9, "systemd")

	// 2. Topology 2: Flat / Mixed Format Slugs
	topo2_rpm := filepath.Join(homePath, "el-9-x86_64")
	setupMockRPMData(t, topo2_rpm, "nginx")

	topo2_deb := filepath.Join(homePath, "ubuntu-24.04-x86_64")
	_ = os.MkdirAll(topo2_deb, 0o755)
	_ = os.WriteFile(filepath.Join(topo2_deb, "Packages"), []byte("Package: redis\nVersion: 7.0-1\nArchitecture: amd64\n\n"), 0o644)

	// 3. Topology 3: Dedicated Repoview Tree
	topo3_dir := filepath.Join(homePath, "el10-base.x86_64")
	_ = os.MkdirAll(topo3_dir, 0o755)
	_ = os.WriteFile(filepath.Join(topo3_dir, "index.html"), []byte(`<!DOCTYPE html><html><head><meta name="generator" content="RepoView"></head><body>Dedicated Repoview Tree</body></html>`), 0o644)
	desc3 := models.RepoDescriptor{
		Title:        "Enterprise Linux 10 BaseOS",
		Format:       "rpm",
		Arch:         "x86_64",
		Distro:       "el10",
		Channel:      "base",
		PackageCount: 850,
		LastBuild:    time.Now().UTC(),
	}
	desc3Bytes, _ := json.Marshal(desc3)
	_ = os.WriteFile(filepath.Join(topo3_dir, "repoview.json"), desc3Bytes, 0o644)

	// 4. Topology 4: Debian Multi-Suite with pool/ branch pruning
	topo4_root := filepath.Join(homePath, "ubuntu")
	setupMockDebData(t, topo4_root, "noble", "main", "amd64", "glibc")
	setupMockDebData(t, topo4_root, "noble", "universe", "amd64", "htop")

	// Create massive pool/ directory that must be pruned by scanner
	poolDir := filepath.Join(topo4_root, "pool", "main", "g", "glibc")
	_ = os.MkdirAll(poolDir, 0o755)
	for i := 0; i < 20; i++ {
		_ = os.WriteFile(filepath.Join(poolDir, fmt.Sprintf("dummy_libc6_%d.deb", i)), []byte("dummy binary payload"), 0o644)
	}

	// Build binary
	binDir := t.TempDir()
	binName := "repoview"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(binDir, binName)
	buildCmd := exec.Command("go", "build", "-o", binPath, "github.com/edsilegxrepo/repoview/cmd/repoview")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build repoview binary: %v\nOutput: %s", err, string(out))
	}

	// Run portal command with --in-place, --render-missing and --workers 4
	cmd := exec.Command(binPath, "portal", "--in-place", "--title", "Enterprise Portal Matrix", "--render-missing", "--workers", "4", homePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("repoview portal failed: %v\nOutput: %s", err, string(output))
	}

	portalHTMLPath := filepath.Join(homePath, "index.html")
	portalFeedPath := filepath.Join(homePath, "portal-feed.xml")

	if _, err := os.Stat(portalHTMLPath); err != nil {
		t.Fatalf("expected portal index.html to exist: %v", err)
	}
	if _, err := os.Stat(portalFeedPath); err != nil {
		t.Fatalf("expected portal-feed.xml to exist: %v", err)
	}

	// Spin up ephemeral HTTP server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind ephemeral listener: %v", err)
	}
	server := &http.Server{
		Handler:           http.FileServer(http.Dir(homePath)),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Shutdown(context.Background()) }()

	baseURL := fmt.Sprintf("http://%s", listener.Addr().String())

	// Test 1: GET /index.html
	resp, err := http.Get(baseURL + "/index.html")
	if err != nil {
		t.Fatalf("failed to GET /index.html: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	content := string(body)

	// Verify Header Metrics Bar & Title
	if !strings.Contains(content, "Enterprise Portal Matrix") {
		t.Errorf("expected portal title 'Enterprise Portal Matrix'")
	}
	if !strings.Contains(content, "RepoView-Portal") {
		t.Errorf("expected generator signature RepoView-Portal")
	}

	// Verify Topologies are represented
	// Topology 1 (el8, el9)
	if !strings.Contains(content, "el8") && !strings.Contains(content, "el9") {
		t.Errorf("expected Topology 1 repos in portal")
	}
	// Topology 2 (el-9-x86_64, ubuntu-24.04)
	if !strings.Contains(content, "el-9-x86_64") {
		t.Errorf("expected Topology 2 repo in portal")
	}
	// Topology 3 (el10-base)
	if !strings.Contains(content, "Enterprise Linux 10 BaseOS") {
		t.Errorf("expected Topology 3 repo in portal")
	}
	// Topology 4 (Ubuntu noble with components aggregated)
	if !strings.Contains(content, "noble") {
		t.Errorf("expected Debian suite noble in portal")
	}
	if !strings.Contains(content, "main") && !strings.Contains(content, "universe") {
		t.Errorf("expected aggregated components in noble card")
	}

	// Verify Quick Setup buttons
	if !strings.Contains(content, "btn-copy-config") {
		t.Errorf("expected quick-copy configuration buttons")
	}

	// Test 2: GET /portal-feed.xml
	feedResp, err := http.Get(baseURL + "/portal-feed.xml")
	if err != nil {
		t.Fatalf("failed to GET /portal-feed.xml: %v", err)
	}
	defer func() { _ = feedResp.Body.Close() }()

	if feedResp.StatusCode != http.StatusOK {
		t.Errorf("expected feed status 200, got %d", feedResp.StatusCode)
	}

	feedBody, _ := io.ReadAll(feedResp.Body)
	var parsedFeed struct {
		XMLName xml.Name `xml:"rss"`
		Channel struct {
			Title string `xml:"title"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(feedBody, &parsedFeed); err != nil {
		t.Fatalf("failed to unmarshal portal RSS feed: %v", err)
	}
	if !strings.Contains(parsedFeed.Channel.Title, "Enterprise Portal Matrix") {
		t.Errorf("expected feed channel title to contain portal title, got: %s", parsedFeed.Channel.Title)
	}

	// Test 3: Verify Child Portal Breadcrumbs (Two-way navigation)
	childIndex := filepath.Join(topo1_el9, "repoview", "index.html")
	if childData, err := os.ReadFile(childIndex); err == nil {
		if !strings.Contains(string(childData), "portal-back-link") {
			t.Errorf("expected child index.html to contain 'portal-back-link'")
		}
	} else {
		t.Errorf("failed to read child index %s: %v", childIndex, err)
	}
}

func TestLive_Portal_RealRepository_TestDirectory(t *testing.T) {
	testDir := "/u01/wwwroot/test"
	if fi, err := os.Stat(testDir); err != nil || !fi.IsDir() {
		t.Skipf("skipping live test: %s is not available", testDir)
	}

	// 1. Render detached portal strictly inside t.TempDir() (never mutate /u01/wwwroot/test)
	portalOut := t.TempDir()
	portalTitle := "Enterprise Production Mirror Portal"
	portalDesc := "Unified package catalog for Enterprise Linux 9 and Ubuntu 24.04 repositories"
	_, stderr, code := runRepoview(t, t.TempDir(), "portal",
		"--portal-home", portalOut,
		"--render-all",
		"--title", portalTitle,
		"--description", portalDesc,
		"--force",
		testDir,
	)
	if code != 0 {
		t.Fatalf("repoview portal failed on %s (exit %d): %s", testDir, code, stderr)
	}

	portalHTMLPath := filepath.Join(portalOut, "index.html")
	portalFeedPath := filepath.Join(portalOut, "portal-feed.xml")

	// 2. Verify physical artifacts exist with world-readable permissions (0644)
	fiHTML, err := os.Stat(portalHTMLPath)
	if err != nil {
		t.Fatalf("expected portal index.html to exist in %s: %v", portalOut, err)
	}
	if fiHTML.Mode().Perm()&0o444 != 0o444 {
		t.Errorf("expected index.html to be world-readable (0644), got mode: %v", fiHTML.Mode())
	}

	fiFeed, err := os.Stat(portalFeedPath)
	if err != nil {
		t.Fatalf("expected portal-feed.xml to exist in %s: %v", portalOut, err)
	}
	if fiFeed.Mode().Perm()&0o444 != 0o444 {
		t.Errorf("expected portal-feed.xml to be world-readable (0644), got mode: %v", fiFeed.Mode())
	}

	// 3. Verify HTML content
	htmlBytes, err := os.ReadFile(portalHTMLPath)
	if err != nil {
		t.Fatalf("failed to read generated index.html: %v", err)
	}
	htmlContent := string(htmlBytes)

	if !strings.Contains(htmlContent, portalTitle) {
		t.Errorf("expected portal HTML to contain title %q", portalTitle)
	}
	if !strings.Contains(htmlContent, "RepoView-Portal") {
		t.Errorf("expected portal HTML to contain generator signature 'RepoView-Portal'")
	}
	if !strings.Contains(htmlContent, "el9/base/x86_64") && !strings.Contains(htmlContent, "el9") {
		t.Errorf("expected el9 base repository to appear in portal HTML")
	}
	if !strings.Contains(htmlContent, "el9/extras/x86_64") && !strings.Contains(htmlContent, "extras") {
		t.Errorf("expected el9 extras repository to appear in portal HTML")
	}
	if !strings.Contains(htmlContent, "ubu24/custom") && !strings.Contains(htmlContent, "custom") {
		t.Errorf("expected ubu24 custom repository to appear in portal HTML")
	}

	if !strings.Contains(htmlContent, `data-arch="amd64"`) {
		t.Errorf("expected ubu24/custom to be detected as amd64")
	}
	if strings.Contains(htmlContent, `data-arch="unknown"`) {
		t.Errorf("portal HTML unexpectedly contains data-arch=\"unknown\"")
	}
	if !strings.Contains(htmlContent, "btn-copy-config") {
		t.Errorf("expected .btn-copy-config quick copy buttons in portal HTML")
	}

	// 4. Spin up ephemeral HTTP server serving detached portalOut
	baseURL, client := startTestHTTPServer(t, portalOut)
	crawlAndVerifySite(t, client, baseURL, CrawlExpectations{
		ExpectPortal:     true,
		RequireKnownArch: true,
		ExpectFormats:    []string{"rpm", "deb"},
	})

	// Test GET detached child repo index pages served under portalOut
	childTests := []string{
		"/el9/base/x86_64/index.html",
		"/el9/extras/x86_64/index.html",
		"/ubu24/custom/index.html",
	}
	for _, childPath := range childTests {
		_, _ = httpGetBytes(t, client, baseURL+childPath)
	}
}

func TestPortal_RenderAll_E2E(t *testing.T) {
	tempRoot := t.TempDir()
	homePath := filepath.Join(tempRoot, "mirror")
	_ = os.MkdirAll(homePath, 0o755)

	// Create 2 RPM repos
	repo1 := filepath.Join(homePath, "el9", "base", "x86_64")
	setupMockRPMData(t, repo1, "kernel")

	repo2 := filepath.Join(homePath, "el9", "extras", "x86_64")
	setupMockRPMData(t, repo2, "ansible")

	// Pre-render repo1 with an old title
	binDir := t.TempDir()
	binName := "repoview"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(binDir, binName)
	buildCmd := exec.Command("go", "build", "-o", binPath, "github.com/edsilegxrepo/repoview/cmd/repoview")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build repoview binary: %v\nOutput: %s", err, string(out))
	}

	preRenderCmd := exec.Command(binPath, "--title", "Old Core Title", repo1)
	if out, err := preRenderCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed pre-render: %v\nOutput: %s", err, string(out))
	}

	// Create portal.yaml with title overrides
	portalYAML := `
title: "E2E Render All Catalog"
overrides:
  "el9/base/x86_64":
    title: "Enterprise Linux 9 Core"
  "el9/extras/x86_64":
    title: "Enterprise Linux 9 Addons"
`
	if err := os.WriteFile(filepath.Join(homePath, "portal.yaml"), []byte(portalYAML), 0o644); err != nil {
		t.Fatalf("failed to write portal.yaml: %v", err)
	}

	// Run portal --in-place --render-all --force --workers 2
	portalCmd := exec.Command(binPath, "portal", "--in-place", "--render-all", "--force", "--workers", "2", homePath)
	if out, err := portalCmd.CombinedOutput(); err != nil {
		t.Fatalf("portal --render-all failed: %v\nOutput: %s", err, string(out))
	}

	// Verify both repos contain new titles
	for repoPath, wantTitle := range map[string]string{
		repo1: "Enterprise Linux 9 Core",
		repo2: "Enterprise Linux 9 Addons",
	} {
		jsonBytes, err := os.ReadFile(filepath.Join(repoPath, "repoview", "repoview.json"))
		if err != nil {
			t.Fatalf("failed to read descriptor for %s: %v", repoPath, err)
		}
		if !strings.Contains(string(jsonBytes), wantTitle) {
			t.Errorf("expected %s descriptor to contain %q, got %s", repoPath, wantTitle, string(jsonBytes))
		}

		htmlBytes, err := os.ReadFile(filepath.Join(repoPath, "repoview", "index.html"))
		if err != nil {
			t.Fatalf("failed to read index.html for %s: %v", repoPath, err)
		}
		if !strings.Contains(string(htmlBytes), wantTitle) {
			t.Errorf("expected %s index.html to contain %q", repoPath, wantTitle)
		}
	}

	// Verify top-level portal index
	portalHTML, err := os.ReadFile(filepath.Join(homePath, "index.html"))
	if err != nil {
		t.Fatalf("failed to read portal index: %v", err)
	}
	content := string(portalHTML)
	if !strings.Contains(content, "E2E Render All Catalog") {
		t.Errorf("portal missing title")
	}
	if !strings.Contains(content, "Enterprise Linux 9 Core") || !strings.Contains(content, "Enterprise Linux 9 Addons") {
		t.Errorf("portal missing repo titles")
	}
}

func TestPortal_Detached_ReadOnlySource_E2E(t *testing.T) {
	// 1. Build repoview binary
	binDir := t.TempDir()
	binName := "repoview"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(binDir, binName)
	buildCmd := exec.Command("go", "build", "-o", binPath, "github.com/edsilegxrepo/repoview/cmd/repoview")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build repoview binary: %v\nOutput: %s", err, string(out))
	}

	// 2. Prepare mock RPM repos
	reposDir := filepath.Join(t.TempDir(), "source_repos")
	repo1 := filepath.Join(reposDir, "el9", "base", "x86_64")
	setupMockRPMData(t, repo1, "app-one")
	repo2 := filepath.Join(reposDir, "el9", "extras", "x86_64")
	setupMockRPMData(t, repo2, "app-two")

	// 3. Make the entire source_repos tree strictly READ-ONLY
	_ = filepath.Walk(reposDir, func(path string, info os.FileInfo, err error) error {
		if err == nil {
			if info.IsDir() {
				_ = os.Chmod(path, 0o555)
			} else {
				_ = os.Chmod(path, 0o444)
			}
		}
		return nil
	})
	// Restore write permissions on test cleanup so os.RemoveAll inside t.TempDir succeeds
	t.Cleanup(func() {
		_ = filepath.Walk(reposDir, func(path string, info os.FileInfo, err error) error {
			if err == nil {
				if info.IsDir() {
					_ = os.Chmod(path, 0o755)
				} else {
					_ = os.Chmod(path, 0o644)
				}
			}
			return nil
		})
	})

	// 4. Detached Portal Home
	portalHome := filepath.Join(t.TempDir(), "portal_home")

	// 5. Run repoview portal in detached mode against read-only repositories
	portalCmd := exec.Command(binPath, "portal",
		"--portal-home", portalHome,
		"--portal-url", "https://portal.corp.local",
		"--repo-url", "https://mirrors.corp.local/packages",
		"--render-all",
		"--workers", "2",
		reposDir,
	)
	out, err := portalCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("portal in detached mode failed against read-only repo: %v\nOutput: %s", err, string(out))
	}

	// 6. Verify top-level portal artifacts
	portalHTMLPath := filepath.Join(portalHome, "index.html")
	if _, err := os.Stat(portalHTMLPath); err != nil {
		t.Fatalf("expected portal index.html in %s: %v", portalHome, err)
	}
	portalFeedPath := filepath.Join(portalHome, "portal-feed.xml")
	if _, err := os.Stat(portalFeedPath); err != nil {
		t.Fatalf("expected portal-feed.xml in %s: %v", portalHome, err)
	}

	// 7. Verify child repositories rendered into detached shadow tree
	for _, sub := range []string{"el9/base/x86_64", "el9/extras/x86_64"} {
		childHTML := filepath.Join(portalHome, filepath.FromSlash(sub), "index.html")
		if _, err := os.Stat(childHTML); err != nil {
			t.Fatalf("expected child index in %s: %v", childHTML, err)
		}
		childJSON := filepath.Join(portalHome, filepath.FromSlash(sub), "repoview.json")
		if _, err := os.Stat(childJSON); err != nil {
			t.Fatalf("expected child descriptor in %s: %v", childJSON, err)
		}

		htmlBytes, err := os.ReadFile(childHTML)
		if err != nil {
			t.Fatalf("failed to read child index: %v", err)
		}
		content := string(htmlBytes)
		wantBaseURL := fmt.Sprintf("baseurl=https://mirrors.corp.local/packages/%s", sub)
		if !strings.Contains(content, wantBaseURL) {
			t.Errorf("child repo %s missing configured baseurl %q in index.html", sub, wantBaseURL)
		}
	}

	// 8. Re-run with --render-missing: Scanner must recognize child repos in portalHome as already rendered
	reRunCmd := exec.Command(binPath, "portal",
		"--portal-home", portalHome,
		"--portal-url", "https://portal.corp.local",
		"--repo-url", "https://mirrors.corp.local/packages",
		"--render-missing",
		reposDir,
	)
	reOut, reErr := reRunCmd.CombinedOutput()
	if reErr != nil {
		t.Fatalf("portal re-run failed: %v\nOutput: %s", reErr, string(reOut))
	}
	if strings.Contains(string(reOut), "Discovered 2 unrendered") {
		t.Errorf("portal re-run should not attempt to re-render already rendered repos: %s", string(reOut))
	}
}

// ---------- RPM-05, DEB-03, MIX-01..MIX-05 & Matrix E ----------

func TestE2E_RPM_05_MultiDistroRPMPortal(t *testing.T) {
	info := BuildEnterpriseMatrix(t, EnterpriseMirrorOpts{
		IncludeEL8:      true,
		IncludeEL9:      true,
		IncludeEL10Slug: true,
		IncludeSymlinks: true,
	})
	makeReadOnlyMirror(t, info.RootDir)
	portalOut := t.TempDir()

	mux := http.NewServeMux()
	mux.Handle("/mirror/", http.StripPrefix("/mirror/", http.FileServer(http.Dir(info.RootDir))))
	mux.Handle("/portal/", http.StripPrefix("/portal/", http.FileServer(http.Dir(portalOut))))
	baseURL, client := startTestMuxHTTPServer(t, mux)

	_, stderr, code := runRepoview(t, t.TempDir(), "portal",
		"--portal-home", portalOut,
		"--render-all",
		"--title", "Enterprise RPM Catalog",
		"--repo-url", baseURL+"/mirror",
		info.RootDir,
	)
	if code != 0 {
		t.Fatalf("RPM-05 portal failed (exit %d): %s", code, stderr)
	}

	pages := crawlAndVerifySite(t, client, baseURL+"/portal", CrawlExpectations{
		ExpectPortal:       true,
		RequireKnownArch:   true,
		ExpectFormats:      []string{"rpm"},
		ForbidFormats:      []string{"deb"},
		VerifyRepoSnippets: true,
		MinHTMLPages:       10,
	})

	portalHTML := pages[baseURL+"/portal/index.html"]
	if !strings.Contains(portalHTML, "redhat") {
		t.Errorf("RPM-05 portal missing redhat distro icons")
	}
	if strings.Contains(portalHTML, "el9/latest") {
		t.Errorf("symlink alias el9/latest should be deduplicated instead of rendered as a card")
	}
	// Sibling switching inside child page
	childHTML := pages[baseURL+"/portal/el9/appstream/x86_64/index.html"]
	if !strings.Contains(childHTML, "sibling-chip") || !strings.Contains(childHTML, "aarch64") {
		t.Errorf("RPM-05 child repo missing sibling arch switcher")
	}
	// Verify downloadable .rpm SHA-256
	for relHref, arc := range info.RPMArchives {
		data, _ := httpGetBytes(t, client, baseURL+"/mirror/"+relHref)
		if sha256Hex(data) != arc.SHA256 {
			t.Errorf("RPM-05 archive %s SHA-256 mismatch", relHref)
		}
		break
	}
}

func TestE2E_DEB_03_MultiDistroDEBPortal(t *testing.T) {
	info := BuildEnterpriseMatrix(t, EnterpriseMirrorOpts{
		IncludeUbuntu24: true,
		IncludeUbuntu26: true,
		IncludeDebian12: true,
		IncludeDebian13: true,
	})
	makeReadOnlyMirror(t, info.RootDir)
	portalOut := t.TempDir()

	mux := http.NewServeMux()
	mux.Handle("/mirror/", http.StripPrefix("/mirror/", http.FileServer(http.Dir(info.RootDir))))
	mux.Handle("/portal/", http.StripPrefix("/portal/", http.FileServer(http.Dir(portalOut))))
	baseURL, client := startTestMuxHTTPServer(t, mux)

	_, stderr, code := runRepoview(t, t.TempDir(), "portal",
		"--portal-home", portalOut,
		"--render-all",
		"--title", "Enterprise DEB Catalog",
		"--repo-url", baseURL+"/mirror",
		info.RootDir,
	)
	if code != 0 {
		t.Fatalf("DEB-03 portal failed (exit %d): %s", code, stderr)
	}

	pages := crawlAndVerifySite(t, client, baseURL+"/portal", CrawlExpectations{
		ExpectPortal:       true,
		RequireKnownArch:   true,
		ExpectFormats:      []string{"deb"},
		ForbidFormats:      []string{"rpm"},
		VerifyRepoSnippets: true,
		MinHTMLPages:       8,
	})

	portalHTML := pages[baseURL+"/portal/index.html"]
	for _, want := range []string{"noble", "trixie", "bookworm", "ubu26", "ubuntu", "debian", "URIs: " + baseURL + "/mirror/deb13/", "URIs: " + baseURL + "/mirror/ubu26/custom/"} {
		if !strings.Contains(portalHTML, want) {
			t.Errorf("DEB-03 portal missing expected marker/URI %q", want)
		}
	}
}

func TestE2E_MIX_01_EnterpriseMixedDetachedPortal(t *testing.T) {
	info := BuildEnterpriseMatrix(t, EnterpriseMirrorOpts{
		IncludeEL8:      true,
		IncludeEL9:      true,
		IncludeEL10Slug: true,
		IncludeUbuntu24: true,
		IncludeUbuntu26: true,
		IncludeDebian13: true,
		IncludeDDEB:     true,
	})
	makeReadOnlyMirror(t, info.RootDir)
	portalOut := t.TempDir()

	mux := http.NewServeMux()
	mux.Handle("/mirror/", http.StripPrefix("/mirror/", http.FileServer(http.Dir(info.RootDir))))
	mux.Handle("/portal/", http.StripPrefix("/portal/", http.FileServer(http.Dir(portalOut))))
	baseURL, client := startTestMuxHTTPServer(t, mux)

	_, stderr, code := runRepoview(t, t.TempDir(), "portal",
		"--portal-home", portalOut,
		"--render-all",
		"--title", "Enterprise Mixed Catalog",
		"--repo-url", baseURL+"/mirror",
		info.RootDir,
	)
	if code != 0 {
		t.Fatalf("MIX-01 portal failed (exit %d): %s", code, stderr)
	}

	pages := crawlAndVerifySite(t, client, baseURL+"/portal", CrawlExpectations{
		ExpectPortal:       true,
		RequireKnownArch:   true,
		ExpectFormats:      []string{"rpm", "deb"},
		VerifyRepoSnippets: true,
		MinHTMLPages:       20,
	})

	portalHTML := pages[baseURL+"/portal/index.html"]
	for _, want := range []string{"el8", "el9", "el10", "ubu24", "ubu26", "deb13", "main/debug", "x86_64", "aarch64", "amd64"} {
		if !strings.Contains(portalHTML, want) {
			t.Errorf("MIX-01 portal HTML missing %q", want)
		}
	}

	// Second --render-missing run must skip already rendered detached repos
	stdout2, stderr2, code2 := runRepoview(t, t.TempDir(), "portal",
		"--portal-home", portalOut,
		"--render-missing",
		"--repo-url", baseURL+"/mirror",
		info.RootDir,
	)
	if code2 != 0 {
		t.Fatalf("MIX-01 --render-missing failed (exit %d): %s", code2, stderr2)
	}
	if strings.Contains(stdout2, "unrendered repositories") {
		t.Errorf("MIX-01 second --render-missing should skip already rendered repos, got: %s", stdout2)
	}
}

func TestE2E_MIX_02_InPlaceLegacyAndSymlinkDedup(t *testing.T) {
	info := BuildEnterpriseMatrix(t, EnterpriseMirrorOpts{
		IncludeEL9:      true,
		IncludeUbuntu24: true,
		IncludeDDEB:     true,
		IncludeSymlinks: true,
	})
	portalYAML := `title: "Legacy In-Place Portal"
description: "Unified in-place catalog"
`
	if err := os.WriteFile(filepath.Join(info.RootDir, "portal.yaml"), []byte(portalYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runRepoview(t, t.TempDir(), "portal",
		"--in-place",
		"--render-missing",
		"--force",
		info.RootDir,
	)
	if code != 0 {
		t.Fatalf("MIX-02 --in-place failed (exit %d): %s", code, stderr)
	}

	baseURL, client := startTestHTTPServer(t, info.RootDir)
	pages := crawlAndVerifySite(t, client, baseURL, CrawlExpectations{
		ExpectPortal:     true,
		RequireKnownArch: true,
		ExpectFormats:    []string{"rpm", "deb"},
		ForbiddenSubstrings: []string{
			"el9/latest",
		},
	})
	if strings.Contains(pages[baseURL+"/index.html"], "pool/") {
		t.Errorf("MIX-02 pool/ directory should be pruned from portal")
	}

	// Verify child in-place repoview has working sibling switching and portal-back-link
	crawlAndVerifySite(t, client, baseURL+"/el9/appstream/x86_64/repoview", CrawlExpectations{
		ExpectRepoView:      true,
		CheckPortalBackLink: true,
		RequiredSubstrings:  []string{"sibling-chip", "aarch64"},
	})
}

func TestE2E_MIX_03_ConfigPrecedenceDumpConfigAndSafeMerge(t *testing.T) {
	info := BuildEnterpriseMatrix(t, EnterpriseMirrorOpts{
		IncludeEL8:      true,
		IncludeEL9:      true,
		IncludeUbuntu24: true,
	})
	workDir := t.TempDir()
	childCfgPath := filepath.Join(workDir, "child-repoview.yaml")
	portalCfgPath := filepath.Join(workDir, "portal.yaml")

	// 1. Single-repo --dump-config + non-clobber guard (exit 8)
	el9Repo := filepath.Join(info.RootDir, "el9", "baseos", "x86_64")
	_, stderr, code := runRepoview(t, workDir, "--dump-config", childCfgPath, el9Repo)
	if code != 0 {
		t.Fatalf("child --dump-config failed (exit %d): %s", code, stderr)
	}
	_, _, codeClobberChild := runRepoview(t, workDir, "--dump-config", childCfgPath, el9Repo)
	if codeClobberChild != 8 {
		t.Errorf("expected ExitSafetyError (8) clobbering child config without --force, got %d", codeClobberChild)
	}

	// Verify custom hidden override in MIX-03 hides el8/extras/x86_64 from catalog & portalHTML
	// 2. Portal --dump-config + non-clobber guard (exit 8)
	_, stderrP, codeP := runRepoview(t, workDir, "portal",
		"--dump-config", portalCfgPath,
		"--portal-url", "https://portal.corp.test",
		"--repo-url", "https://mirrors.corp.test",
		info.RootDir,
	)
	if codeP != 0 {
		t.Fatalf("portal --dump-config failed (exit %d): %s", codeP, stderrP)
	}
	_, _, codeClobberPortal := runRepoview(t, workDir, "portal", "--dump-config", portalCfgPath, info.RootDir)
	if codeClobberPortal != 8 {
		t.Errorf("expected ExitSafetyError (8) clobbering portal.yaml without --force, got %d", codeClobberPortal)
	}

	rawCfgBytes, err := os.ReadFile(portalCfgPath)
	if err != nil {
		t.Fatal(err)
	}
	rawCfg := string(rawCfgBytes)
	for _, wantField := range []string{"portal_url: https://portal.corp.test", "repo_url: https://mirrors.corp.test", "suite: noble", "components:", "arch: x86_64", "format: rpm"} {
		if !strings.Contains(rawCfg, wantField) {
			t.Errorf("dumped portal.yaml missing inferred field %q:\n%s", wantField, rawCfg)
		}
	}

	// 3. Inject pinned + hidden + custom title into portal.yaml and verify --dump-config --force safe merge preserves all three
	customPortalYAML := `title: "Enterprise Config Catalog"
portal_url: https://portal.corp.test
repo_url: https://mirrors.corp.test
overrides:
  "el8/extras/x86_64":
    hidden: true
  "el9/baseos/x86_64":
    pinned: true
    title: "Pinned EL9 Core Override"
    description: "Custom description from override"
`
	if err := os.WriteFile(portalCfgPath, []byte(customPortalYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderrMerge, codeMerge := runRepoview(t, workDir, "portal", "--dump-config", portalCfgPath, "--force", info.RootDir)
	if codeMerge != 0 {
		t.Fatalf("portal --dump-config --force merge failed (exit %d): %s", codeMerge, stderrMerge)
	}
	mergedBytes, _ := os.ReadFile(portalCfgPath)
	mergedStr := string(mergedBytes)
	for _, mustKeep := range []string{"hidden: true", "pinned: true", "Pinned EL9 Core Override", "el8/base/x86_64"} {
		if !strings.Contains(mergedStr, mustKeep) {
			t.Errorf("portal --dump-config --force lost %q in merged YAML:\n%s", mustKeep, mergedStr)
		}
	}

	// 4. Render with --config portalCfgPath & verify pinned + hidden + portal-search.json over HTTP
	portalOut := t.TempDir()
	_, stderrRender, codeRender := runRepoview(t, workDir, "portal",
		"--config", portalCfgPath,
		"--portal-home", portalOut,
		"--render-all",
		info.RootDir,
	)
	if codeRender != 0 {
		t.Fatalf("MIX-03 portal render failed (exit %d): %s", codeRender, stderrRender)
	}

	if _, err := os.Stat(filepath.Join(portalOut, "el8", "extras", "x86_64", "index.html")); !os.IsNotExist(err) {
		t.Errorf("hidden repo el8/extras/x86_64 should be skipped during --render-all")
	}

	baseURL, client := startTestHTTPServer(t, portalOut)
	crawlAndVerifySite(t, client, baseURL, CrawlExpectations{
		ExpectPortal:        true,
		ExpectPinned:        true,
		RequiredSubstrings:  []string{"Pinned EL9 Core Override", "Custom description from override"},
		ForbiddenSubstrings: []string{"el8/extras/x86_64"},
	})
}

func TestE2E_MIX_04_CustomEnterpriseTaxonomyRules(t *testing.T) {
	mirrorRoot := t.TempDir()
	corpRepo := filepath.Join(mirrorRoot, "corp", "platform", "custombrand", "stable", "arm64")
	WriteRPMRepo(t, corpRepo, RPMRepoSpec{
		Mode: "sqlite",
		Packages: []SynthRPMSpec{{
			Name: "corp-agent", Version: "3.1.0", Release: "1", Arch: "arm64",
			Summary: "Corp telemetry agent", Description: "Custom taxonomy enterprise RPM.",
		}},
	})

	cfgPath := filepath.Join(t.TempDir(), "portal.yaml")
	cfgYAML := `title: "Custom Taxonomy Portal"
taxonomy:
  path_patterns:
    - pattern: "corp/*/{distro}/{channel}/{arch}"
  distro_aliases:
    custombrand: "almalinux"
`
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	portalOut := t.TempDir()
	_, stderr, code := runRepoview(t, t.TempDir(), "portal",
		"--config", cfgPath,
		"--portal-home", portalOut,
		"--render-all",
		mirrorRoot,
	)
	if code != 0 {
		t.Fatalf("MIX-04 failed (exit %d): %s", code, stderr)
	}

	baseURL, client := startTestHTTPServer(t, portalOut)
	pages := crawlAndVerifySite(t, client, baseURL, CrawlExpectations{
		ExpectPortal:     true,
		RequireKnownArch: true,
	})
	portalHTML := pages[baseURL+"/index.html"]
	for _, want := range []string{"custombrand", "stable", `data-arch="arm64"`, "almalinux"} {
		if !strings.Contains(portalHTML, want) {
			t.Errorf("MIX-04 portal HTML missing mapped taxonomy attribute %q", want)
		}
	}
}

func TestE2E_MIX_05_SingleRootRepoDetachedMode(t *testing.T) {
	rootRepo := t.TempDir()
	WriteRPMRepo(t, rootRepo, RPMRepoSpec{
		Mode: "sqlite",
		Packages: []SynthRPMSpec{{
			Name: "root-app", Version: "1.0.0", Release: "1.el9", Arch: "x86_64",
			Summary: "Root level RPM package", Description: "RelPath == . test package.",
		}},
	})

	portalOut := t.TempDir()
	_, stderr, code := runRepoview(t, t.TempDir(), "portal",
		"--portal-home", portalOut,
		"--render-all",
		"--title", "Single Root Detached Portal",
		rootRepo,
	)
	if code != 0 {
		t.Fatalf("MIX-05 failed (exit %d): %s", code, stderr)
	}

	if _, err := os.Stat(filepath.Join(portalOut, "repoview", "index.html")); err != nil {
		t.Fatalf("MIX-05 expected child index under portalOut/repoview/index.html: %v", err)
	}

	baseURL, client := startTestHTTPServer(t, portalOut)
	crawlAndVerifySite(t, client, baseURL, CrawlExpectations{
		ExpectPortal:       true,
		RequireKnownArch:   true,
		RequiredSubstrings: []string{"repoview/index.html"},
	})
}

// ---------- Matrix E: Adversarial Security, Escape & Exit Code Subprocess Matrix ----------

func TestE2E_MatrixE_SecurityAndExitCodes(t *testing.T) {
	// SEC-01: Adversarial HTML/JS/Shell Escaping
	sec1Repo := t.TempDir()
	sec1Out := t.TempDir()
	WriteRPMRepo(t, sec1Repo, RPMRepoSpec{
		Mode: "sqlite",
		Packages: []SynthRPMSpec{{
			Name:        "evilpkg;id;$(whoami)",
			Version:     "1.0",
			Release:     "1.el9",
			Arch:        "x86_64",
			Summary:     `Hello <script>alert(1)</script> world`,
			Description: `Body <img src=x onerror=alert(2)> test`,
			URL:         `javascript:alert(1)`,
		}},
	})
	_, stderr, code := runRepoview(t, t.TempDir(), "--output-dir", sec1Out, "--force", sec1Repo)
	if code != 0 {
		t.Fatalf("SEC-01 repoview failed (exit %d): %s", code, stderr)
	}
	baseURL, client := startTestHTTPServer(t, sec1Out)
	pages := crawlAndVerifySite(t, client, baseURL, CrawlExpectations{
		ExpectRepoView:      true,
		ForbiddenSubstrings: []string{"<script>alert(1)</script>"},
	})
	var sawEscaped bool
	for _, body := range pages {
		if strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
			sawEscaped = true
		}
		if strings.Contains(body, `href="javascript:alert(1)"`) {
			t.Errorf("SEC-01 rendered dangerous javascript: href")
		}
	}
	if !sawEscaped {
		t.Errorf("SEC-01 expected escaped script tag inside rendered pages")
	}

	// SEC-02: Path traversal rejection in repomd.xml (../../secret.sqlite)
	sec2Parent := t.TempDir()
	sec2Repo := filepath.Join(sec2Parent, "repo")
	_ = os.MkdirAll(filepath.Join(sec2Repo, "repodata"), 0o755)
	_ = os.WriteFile(filepath.Join(sec2Parent, "secret.sqlite"), []byte("secret"), 0o644)
	traversalRepomd := `<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="primary_db"><location href="../../secret.sqlite"/><checksum type="sha256">0000</checksum></data>
</repomd>`
	_ = os.WriteFile(filepath.Join(sec2Repo, "repodata", "repomd.xml"), []byte(traversalRepomd), 0o644)
	_, _, sec2Code := runRepoview(t, t.TempDir(), "--output-dir", t.TempDir(), sec2Repo)
	if sec2Code == 0 {
		t.Errorf("SEC-02 expected non-zero exit when repomd.xml traverses outside repo root")
	}

	// Exit codes 0..8
	// 0: ExitSuccess (--version)
	_, _, c0 := runRepoview(t, t.TempDir(), "--version")
	if c0 != 0 {
		t.Errorf("expected ExitSuccess (0) on --version, got %d", c0)
	}
	// 2: ExitUsageError (--format bogus)
	_, _, c2 := runRepoview(t, t.TempDir(), "--format", "bogus", sec1Repo)
	if c2 != 2 {
		t.Errorf("expected ExitUsageError (2), got %d", c2)
	}
	// 3: ExitRepoMetadataError (missing path)
	_, _, c3 := runRepoview(t, t.TempDir(), filepath.Join(t.TempDir(), "missing-repo"))
	if c3 != 3 {
		t.Errorf("expected ExitRepoMetadataError (3), got %d", c3)
	}
	// 4: ExitTemplateError (invalid template must fail before wiping output)
	badTmpl := t.TempDir()
	_ = os.WriteFile(filepath.Join(badTmpl, "index.html"), []byte("{{ .Broken "), 0o644)
	_, _, c4 := runRepoview(t, t.TempDir(), "--template-dir", badTmpl, "--output-dir", t.TempDir(), sec1Repo)
	if c4 != 4 {
		t.Errorf("expected ExitTemplateError (4), got %d", c4)
	}
	// 5: ExitIOError (--state-dir pointing to an existing regular file)
	regFile := filepath.Join(t.TempDir(), "state-as-file")
	_ = os.WriteFile(regFile, []byte("x"), 0o644)
	_, _, c5 := runRepoview(t, t.TempDir(), "--state-dir", regFile, "--output-dir", t.TempDir(), sec1Repo)
	if c5 != 5 {
		t.Errorf("expected ExitIOError (5), got %d", c5)
	}
	// 6: ExitPartialFailure (portal --render-all with 2 healthy + 1 corrupt child)
	partialMirror := t.TempDir()
	WriteRPMRepo(t, filepath.Join(partialMirror, "good1", "base", "x86_64"), RPMRepoSpec{Packages: []SynthRPMSpec{{Name: "g1", Version: "1.0", Release: "1"}}})
	WriteRPMRepo(t, filepath.Join(partialMirror, "good2", "base", "x86_64"), RPMRepoSpec{Packages: []SynthRPMSpec{{Name: "g2", Version: "1.0", Release: "1"}}})
	corruptChild := filepath.Join(partialMirror, "broken", "base", "x86_64")
	_ = os.MkdirAll(filepath.Join(corruptChild, "repodata"), 0o755)
	_ = os.WriteFile(filepath.Join(corruptChild, "repodata", "repomd.xml"), []byte("<corrupt"), 0o644)
	partialOut := t.TempDir()
	_, _, c6 := runRepoview(t, t.TempDir(), "portal", "--portal-home", partialOut, "--render-all", partialMirror)
	if c6 != 6 {
		t.Errorf("expected ExitPartialFailure (6), got %d", c6)
	}
	if _, err := os.Stat(filepath.Join(partialOut, "index.html")); err != nil {
		t.Errorf("expected portal index.html despite partial child failure: %v", err)
	}
	// 7: ExitConfigError (malformed repoview.yaml on single-repo AND portal --config bad.yaml)
	badCfgRepo := t.TempDir()
	WriteRPMRepo(t, badCfgRepo, RPMRepoSpec{Packages: []SynthRPMSpec{{Name: "b1", Version: "1.0", Release: "1"}}})
	_ = os.WriteFile(filepath.Join(badCfgRepo, "repoview.yaml"), []byte("title: [unterminated\n"), 0o644)
	_, _, c7Repo := runRepoview(t, t.TempDir(), "--output-dir", t.TempDir(), badCfgRepo)
	if c7Repo != 7 {
		t.Errorf("expected ExitConfigError (7) for malformed repoview.yaml, got %d", c7Repo)
	}
	badYAML := filepath.Join(t.TempDir(), "bad.yaml")
	_ = os.WriteFile(badYAML, []byte("title: [unterminated\n"), 0o644)
	_, _, c7Portal := runRepoview(t, t.TempDir(), "portal", "--config", badYAML, "--portal-home", t.TempDir(), partialMirror)
	if c7Portal != 7 {
		t.Errorf("expected ExitConfigError (7) for malformed portal --config, got %d", c7Portal)
	}
	// 8: ExitSafetyError (single-repo --output-dir == repoDir AND portal overwriting foreign index.html without --force)
	_, _, c8Repo := runRepoview(t, t.TempDir(), "--output-dir", sec1Repo, sec1Repo)
	if c8Repo != 8 {
		t.Errorf("expected ExitSafetyError (8) on --output-dir == repoDir, got %d", c8Repo)
	}
	foreignDir := t.TempDir()
	foreignSentinel := "<html><body>Foreign Site Must Not Be Clobbered</body></html>"
	_ = os.WriteFile(filepath.Join(foreignDir, "index.html"), []byte(foreignSentinel), 0o644)
	_, _, c8Portal := runRepoview(t, t.TempDir(), "portal", "--portal-home", foreignDir, partialMirror)
	if c8Portal != 8 {
		t.Errorf("expected ExitSafetyError (8) overwriting foreign index.html without --force, got %d", c8Portal)
	}
	afterSentinel, _ := os.ReadFile(filepath.Join(foreignDir, "index.html"))
	if string(afterSentinel) != foreignSentinel {
		t.Errorf("foreign index.html was modified despite ExitSafetyError")
	}
}
