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
	"strings"
	"testing"
	"time"

	"github.com/edsilegxrepo/repoview/internal/models"
	_ "github.com/mattn/go-sqlite3"
)

// TEST STRATEGY:
// Validate the Multi-Repository Portal & Catalog Feature end-to-end as specified in docs/MULTIREPOS.md:
//   1. Topology 1: Nested Hierarchical Trees (el8/base/x86_64, el9/base/x86_64).
//   2. Topology 2: Flat / Mixed Format Slugs (el-9-x86_64, ubuntu-24.04-x86_64).
//   3. Topology 3: Dedicated Repoview Tree (el10-base.x86_64).
//   4. Topology 4: Debian Multi-Suite / Multi-Component Pools (ubuntu/dists/noble/... with pool/ branch pruning).
//   5. Live HTTP Server testing on loopback: Verifying 200 OK for /index.html and /portal-feed.xml.
//   6. Real Subprocess CLI execution with '--render-missing' and bounded worker pool.

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
	binPath := filepath.Join(binDir, "repoview")
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

	// 1. Build fresh repoview binary
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "repoview")
	buildCmd := exec.Command("go", "build", "-o", binPath, "github.com/edsilegxrepo/repoview/cmd/repoview")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build repoview binary: %v\nOutput: %s", err, string(out))
	}

	// 2. Run portal command targeting /u01/wwwroot/test
	portalTitle := "Enterprise Production Mirror Portal"
	portalDesc := "Unified package catalog for Enterprise Linux 9 and Ubuntu 24.04 repositories"
	cmd := exec.Command(binPath, "portal",
		"--in-place",
		"--title", portalTitle,
		"--description", portalDesc,
		"--force",
		testDir,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("repoview portal failed on %s: %v\nOutput: %s", testDir, err, string(output))
	}

	portalHTMLPath := filepath.Join(testDir, "index.html")
	portalFeedPath := filepath.Join(testDir, "portal-feed.xml")

	// 3. Verify physical artifacts exist with world-readable permissions (0644)
	fiHTML, err := os.Stat(portalHTMLPath)
	if err != nil {
		t.Fatalf("expected portal index.html to exist in %s: %v", testDir, err)
	}
	if fiHTML.Mode().Perm()&0o444 != 0o444 {
		t.Errorf("expected index.html to be world-readable (0644), got mode: %v", fiHTML.Mode())
	}

	fiFeed, err := os.Stat(portalFeedPath)
	if err != nil {
		t.Fatalf("expected portal-feed.xml to exist in %s: %v", testDir, err)
	}
	if fiFeed.Mode().Perm()&0o444 != 0o444 {
		t.Errorf("expected portal-feed.xml to be world-readable (0644), got mode: %v", fiFeed.Mode())
	}

	// 4. Verify HTML content
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
	// Check for discovered repositories
	if !strings.Contains(htmlContent, "el9/base/x86_64") && !strings.Contains(htmlContent, "el9") {
		t.Errorf("expected el9 base repository to appear in portal HTML")
	}
	if !strings.Contains(htmlContent, "el9/extras/x86_64") && !strings.Contains(htmlContent, "extras") {
		t.Errorf("expected el9 extras repository to appear in portal HTML")
	}
	if !strings.Contains(htmlContent, "ubu24/custom") && !strings.Contains(htmlContent, "custom") {
		t.Errorf("expected ubu24 custom repository to appear in portal HTML")
	}

	// Verify architecture detection: ubu24/custom must be amd64, no repositories should have unknown arch
	if !strings.Contains(htmlContent, `data-arch="amd64"`) {
		t.Errorf("expected ubu24/custom to be detected as amd64")
	}
	if strings.Contains(htmlContent, `data-arch="unknown"`) {
		t.Errorf("portal HTML unexpectedly contains data-arch=\"unknown\"")
	}

	// Verify Setup config buttons
	if !strings.Contains(htmlContent, "btn-copy-config") {
		t.Errorf("expected .btn-copy-config quick copy buttons in portal HTML")
	}

	// 5. Spin up ephemeral HTTP server serving /u01/wwwroot/test
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind ephemeral listener: %v", err)
	}
	server := &http.Server{
		Handler:           http.FileServer(http.Dir(testDir)),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Shutdown(context.Background()) }()

	baseURL := fmt.Sprintf("http://%s", listener.Addr().String())

	// Test GET /index.html
	resp, err := http.Get(baseURL + "/index.html")
	if err != nil {
		t.Fatalf("failed to GET /index.html: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for /index.html, got %d", resp.StatusCode)
	}

	// Test GET /portal-feed.xml
	feedResp, err := http.Get(baseURL + "/portal-feed.xml")
	if err != nil {
		t.Fatalf("failed to GET /portal-feed.xml: %v", err)
	}
	defer func() { _ = feedResp.Body.Close() }()
	if feedResp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for /portal-feed.xml, got %d", feedResp.StatusCode)
	}

	feedBytes, _ := io.ReadAll(feedResp.Body)
	var feedParsed struct {
		XMLName xml.Name `xml:"rss"`
		Channel struct {
			Title string `xml:"title"`
			Items []struct {
				Title string `xml:"title"`
				Link  string `xml:"link"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(feedBytes, &feedParsed); err != nil {
		t.Fatalf("failed to unmarshal portal feed XML: %v", err)
	}
	if len(feedParsed.Channel.Items) == 0 {
		t.Errorf("expected portal feed to contain aggregated items, got 0")
	}

	// Test GET child repo index pages through portal links
	childTests := []string{
		"/el9/base/x86_64/repoview/index.html",
		"/el9/extras/x86_64/repoview/index.html",
		"/ubu24/custom/repoview/index.html",
	}
	for _, childPath := range childTests {
		childResp, err := http.Get(baseURL + childPath)
		if err != nil {
			t.Errorf("failed to GET %s: %v", childPath, err)
			continue
		}
		_ = childResp.Body.Close()
		if childResp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK for child repo %s, got %d", childPath, childResp.StatusCode)
		}
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
	binPath := filepath.Join(binDir, "repoview")
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
	binPath := filepath.Join(binDir, "repoview")
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

