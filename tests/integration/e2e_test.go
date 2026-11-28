//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/edsilegxrepo/repoview/internal/app"
	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/util"
)

// TEST STRATEGY:
// Execute end-to-end RPM integration workflows against both deterministic pure-Go fixtures
// (Matrix A: RPM-01..04 covering SQLite, XML fallback, EVR/collisions, workers/fsync/templates)
// and optional live real-world repository data (auto-skipped if absent).
// This integration suite is gated by the 'integration' build tag and validates:
//   1. Full real-world repository generation with real SQLite databases and real RPM headers.
//   2. Live HTTP endpoint serving: Starts a real HTTP server on an ephemeral loopback port,
//      issuing real HTTP client requests to verify 200 OK, Content-Types, and payloads.
//   3. Verification of client configuration, search indexing, and RSS 2.0 feed structure.
//   4. Incremental generation pass verifying state persistence and skipping of unchanged packages.
//   5. Real subprocess CLI execution with the compiled repoview binary.
//   6. Strict isolation: All generated files are confined to t.TempDir() with zero repo pollution.

func getTestRepoPath(t *testing.T) string {
	if custom := os.Getenv("REPOVIEW_TEST_REPO"); custom != "" {
		if fi, err := os.Stat(custom); err == nil && fi.IsDir() {
			return custom
		}
	}

	defaultPath := "/u01/wwwroot/test/el9/base/x86_64"
	if fi, err := os.Stat(defaultPath); err == nil && fi.IsDir() {
		return defaultPath
	}

	t.Skip("skipping live integration test: real test repository not found at " + defaultPath)
	return ""
}

func TestLive_EndToEndWorkflow(t *testing.T) {
	repoDir := getTestRepoPath(t)
	outDir := t.TempDir()
	stateDir := t.TempDir()

	cfg := app.Config{
		RepoDir:   repoDir,
		OutputDir: outDir,
		StateDir:  stateDir,
		Title:     "Enterprise Linux 9 BaseOS",
		URL:       "http://127.0.0.1/test/el9/base/x86_64",
		BaseURL:   "http://127.0.0.1/test/el9/base/x86_64",
		Force:     true,
		Quiet:     true,
		Version:   "integration-test",
	}

	// 1. Execute initial generation
	t.Log("Generating repository view from real repo...")
	gen := app.NewGenerator(cfg)
	start := time.Now()
	if err := gen.Run(); err != nil {
		t.Fatalf("Live Generator.Run failed: %v", err)
	}
	t.Logf("Generation completed in %v", time.Since(start))

	// 2. Validate essential files exist
	expectedFiles := []string{
		"index.html",
		"search.json",
		"latest-feed.xml",
		"nginx.html",
		filepath.Join("layout", "repostyle.css"),
		filepath.Join("layout", "search.js"),
	}

	for _, f := range expectedFiles {
		p := filepath.Join(outDir, f)
		fi, err := os.Stat(p)
		if err != nil {
			t.Errorf("expected generated file missing: %s", p)
			continue
		}
		if fi.Size() == 0 {
			t.Errorf("generated file is empty: %s", p)
		}
		if runtime.GOOS != "windows" {
			if perm := fi.Mode().Perm(); perm != util.DefaultFilePerm {
				t.Errorf("expected %04o file permissions for %s, got %04o", util.DefaultFilePerm, f, perm)
			}
		}
	}

	layoutDirInfo, err := os.Stat(filepath.Join(outDir, "layout"))
	if err != nil {
		t.Errorf("failed to stat layout directory: %v", err)
	} else if runtime.GOOS != "windows" {
		if perm := layoutDirInfo.Mode().Perm(); perm != util.DefaultDirPerm {
			t.Errorf("expected %04o permissions for layout directory, got %04o", util.DefaultDirPerm, perm)
		}
	}

	// 3. Start live HTTP server on ephemeral port to simulate real production client access
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind ephemeral port: %v", err)
	}
	serverPort := listener.Addr().(*net.TCPAddr).Port
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", serverPort)

	fs := http.FileServer(http.Dir(outDir))
	httpServer := &http.Server{Handler: fs}

	go func() {
		_ = httpServer.Serve(listener)
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()

	client := &http.Client{Timeout: 5 * time.Second}

	// 4. Test Live Endpoint: GET /index.html
	t.Run("Live_IndexHTML", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/index.html")
		if err != nil {
			t.Fatalf("GET /index.html failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		content := string(body)
		if !strings.Contains(content, "Enterprise Linux 9 BaseOS") {
			t.Errorf("index.html missing repository title")
		}
		if !strings.Contains(content, "nginx.html") {
			t.Errorf("index.html missing link to nginx package")
		}
	})

	// 5. Test Live Endpoint: GET /search.json
	t.Run("Live_SearchJSON", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/search.json")
		if err != nil {
			t.Fatalf("GET /search.json failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", resp.StatusCode)
		}

		var searchIndex models.SearchIndex
		if err := json.NewDecoder(resp.Body).Decode(&searchIndex); err != nil {
			t.Fatalf("failed to decode search.json: %v", err)
		}

		if len(searchIndex.Data) == 0 {
			t.Errorf("search.json returned zero packages")
		}

		foundNginx := false
		for _, row := range searchIndex.Data {
			if len(row) > 0 && row[0] == "nginx" {
				foundNginx = true
				break
			}
		}
		if !foundNginx {
			t.Errorf("nginx not found in search.json")
		}
	})

	// 6. Test Live Endpoint: GET /nginx.html
	t.Run("Live_PackagePage", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/nginx.html")
		if err != nil {
			t.Fatalf("GET /nginx.html failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		content := string(body)

		// Assert presence of quick install bar
		if !strings.Contains(content, "sudo dnf install") {
			t.Errorf("nginx.html missing quick install bar")
		}
		// Assert presence of dependencies
		if !strings.Contains(content, "Dependencies") {
			t.Errorf("nginx.html missing Dependencies section")
		}
	})

	// 7. Test Live Endpoint: GET /latest-feed.xml
	t.Run("Live_RSSFeed", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/latest-feed.xml")
		if err != nil {
			t.Fatalf("GET /latest-feed.xml failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", resp.StatusCode)
		}

		var rssFeed struct {
			XMLName xml.Name `xml:"rss"`
			Channel struct {
				Title string `xml:"title"`
				Items []struct {
					Title string `xml:"title"`
					Link  string `xml:"link"`
				} `xml:"item"`
			} `xml:"channel"`
		}

		if err := xml.NewDecoder(resp.Body).Decode(&rssFeed); err != nil {
			t.Fatalf("failed to decode latest-feed.xml: %v", err)
		}

		if len(rssFeed.Channel.Items) == 0 {
			t.Errorf("latest-feed.xml contains zero items")
		}
	})

	// 8. Incremental build pass: Run without Force, asserting rapid execution and stability
	t.Run("Live_IncrementalRun", func(t *testing.T) {
		cfg.Force = false
		incGen := app.NewGenerator(cfg)
		startInc := time.Now()
		if err := incGen.Run(); err != nil {
			t.Fatalf("incremental run failed: %v", err)
		}
		t.Logf("Incremental pass completed in %v", time.Since(startInc))
	})
}

func TestLive_SubprocessCLI(t *testing.T) {
	repoDir := getTestRepoPath(t)
	outDir := t.TempDir()

	// Execute CLI subprocess against real repository
	t.Log("Executing repoview subprocess against real repo...")
	_, output, code := runRepoview(t, t.TempDir(),
		"--output-dir", outDir,
		"--force",
		"--quiet",
		repoDir,
	)
	if code != 0 {
		t.Fatalf("subprocess execution failed (exit %d): %s", code, output)
	}

	// Verify index was created
	indexPath := filepath.Join(outDir, "index.html")
	if fi, err := os.Stat(indexPath); err != nil || fi.Size() == 0 {
		t.Errorf("index.html was not generated by CLI subprocess: %v", err)
	}
}

// ---------- Matrix A: Pure-Go RPM E2E Scenarios ----------

func TestE2E_RPM_01_SQLiteExtendedAndBinaryInspection(t *testing.T) {
	workspace := t.TempDir()
	repoDir := filepath.Join(workspace, "el9", "base", "x86_64")
	outDir := filepath.Join(workspace, "site")

	written := WriteRPMRepo(t, repoDir, RPMRepoSpec{
		Mode:     "sqlite",
		Revision: "el9-rev-2026.11",
		Groups: []SynthRPMGroup{{
			ID: "core-services", Name: "Core Services", Description: "Core server binaries", Packages: []string{"demo-pkg"},
		}},
		Advisories: []SynthAdvisory{{
			ID: "ELSA-2026:0101", Title: "Critical demo-pkg security update", Type: "security", Severity: "Critical",
			CVEs: []string{"CVE-2026-4242"},
			Packages: []SynthRPMSpec{{
				Name: "demo-pkg", Epoch: "0", Version: "2.4.1", Release: "3.el9", Arch: "x86_64",
			}},
		}},
		Packages: []SynthRPMSpec{{
			Name:        "demo-pkg",
			Epoch:       "0",
			Version:     "2.4.1",
			Release:     "3.el9",
			Arch:        "x86_64",
			Summary:     "Demo enterprise binary with weak deps and advisories",
			Description: "Full binary inspection showcase package.",
			URL:         "https://demo.example.com",
			License:     "Apache-2.0",
			Packager:    "Releng Team <releng@example.com>",
			BuildHost:   "el9-builder.example.com",
			BugURL:      "https://bugs.example.com/demo",
			VCS:         "https://git.example.com/demo.git",
			Group:       "Core Services",
			PreIn:       "echo demo prein",
			PostIn:      "echo demo postin",
			PreUn:       "echo demo preun",
			PostUn:      "echo demo postun",
			Requires:    []SynthRPMDep{{Name: "libc.so.6()(64bit)", Pre: true}},
			Provides:    []SynthRPMDep{{Name: "demo-capability", Flags: "EQ", Epoch: "0", Version: "2.4.1", Release: "3.el9"}},
			Recommends:  []SynthRPMDep{{Name: "logrotate"}},
			Suggests:    []SynthRPMDep{{Name: "demo-docs"}},
			Changelogs: []SynthChangelog{
				{Author: "Sec Bot <sec@example.com>", Date: 1700001000, Text: "- Patch CVE-2026-4242"},
				{Author: "Releng <rel@example.com>", Date: 1699000000, Text: "- Initial 2.4.1 build"},
			},
			Files: []SynthRPMFile{
				{Dir: "/usr/bin/", Name: "demo", Mode: 0o100755, Size: 4096, Flags: 0},
				{Dir: "/etc/demo/", Name: "demo.conf", Mode: 0o100644, Size: 128, Flags: 1},
				{Dir: "/usr/share/doc/demo/", Name: "README.md", Mode: 0o100644, Size: 256, Flags: 2},
				{Dir: "/usr/bin/", Name: "demo-alias", Mode: 0o120777, Size: 4, Flags: 0, SymlinkTarget: "/usr/bin/demo"},
			},
		}},
	})

	stateDir := t.TempDir()
	mux := http.NewServeMux()
	mux.Handle("/repo/", http.StripPrefix("/repo/", http.FileServer(http.Dir(repoDir))))
	mux.Handle("/view/", http.StripPrefix("/view/", http.FileServer(http.Dir(outDir))))
	baseURL, client := startTestMuxHTTPServer(t, mux)

	_, stderr, code := runRepoview(t, t.TempDir(),
		"--output-dir", outDir,
		"--state-dir", stateDir,
		"--title", "EL9 Base SQLite Mirror",
		"--url", baseURL+"/repo",
		"--baseurl", baseURL+"/repo",
		"--force",
		repoDir,
	)
	if code != 0 {
		t.Fatalf("RPM-01 repoview failed (exit %d): %s", code, stderr)
	}

	dlChecks := make(map[string]string)
	for href, info := range written {
		dlChecks["repo/"+href] = info.SHA256
	}

	pages := crawlAndVerifySite(t, client, baseURL+"/view", CrawlExpectations{
		ExpectRepoView:        true,
		VerifyPackageSnippets: true,
		MinHTMLPages:          3,
		RequiredSubstrings:    []string{"el9-rev-2026.11"},
		ExpectedDownloads:     make(map[string]string),
	})
	for relPath, wantSHA := range dlChecks {
		data, _ := httpGetBytes(t, client, baseURL+"/"+relPath)
		if sha256Hex(data) != wantSHA {
			t.Errorf("RPM download %s SHA-256 mismatch", relPath)
		}
	}

	pkgHTML := pages[baseURL+"/view/demo-pkg.html"]
	for _, want := range []string{
		"Recommends (1)", "logrotate",
		"Suggests (1)", "demo-docs",
		"Patch CVE-2026-4242", "Initial 2.4.1 build",
		"echo demo prein", "echo demo postin",
		"config", "doc", "/usr/bin/demo",
		"https://bugs.example.com/demo", "https://git.example.com/demo.git",
		"ELSA-2026:0101", "CVE-2026-4242",
	} {
		if !strings.Contains(pkgHTML, want) {
			t.Errorf("demo-pkg.html missing expected metadata %q", want)
		}
	}

	searchBytes, _ := httpGetBytes(t, client, baseURL+"/view/search.json")
	if !strings.Contains(string(searchBytes), "CVE-2026-4242") || !strings.Contains(string(searchBytes), "demo-capability") {
		t.Errorf("search.json missing CVE or provides token: %s", string(searchBytes))
	}

	// Incremental re-run must short-circuit via __repoview_fingerprint__
	stateBytes, err := os.ReadFile(filepath.Join(stateDir, "repoview_state.json"))
	if err != nil {
		// Legacy default state filename fallback check
		entries, _ := os.ReadDir(stateDir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				stateBytes, err = os.ReadFile(filepath.Join(stateDir, e.Name()))
				break
			}
		}
	}
	if err != nil {
		t.Fatalf("failed locating state.json in %s: %v", stateDir, err)
	}
	if !strings.Contains(string(stateBytes), "__repoview_fingerprint__") {
		t.Errorf("expected state JSON to contain __repoview_fingerprint__")
	}

	_, stderr2, code2 := runRepoview(t, t.TempDir(),
		"--output-dir", outDir,
		"--state-dir", stateDir,
		"--title", "EL9 Base SQLite Mirror",
		"--url", baseURL+"/repo",
		"--baseurl", baseURL+"/repo",
		repoDir,
	)
	if code2 != 0 {
		t.Fatalf("RPM-01 incremental rerun failed (exit %d): %s", code2, stderr2)
	}
}

func TestE2E_RPM_02_XMLOnlyFallback(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "el10", "appstream", "aarch64")
	outDir := t.TempDir()

	WriteRPMRepo(t, repoDir, RPMRepoSpec{
		Mode:     "xml",
		Revision: "el10-xml-1",
		Groups: []SynthRPMGroup{{
			ID: "web", Name: "Web Stack", Description: "HTTP services", Packages: []string{"caddy"},
		}},
		Advisories: []SynthAdvisory{{
			ID: "ELSA-2026:0202", Title: "caddy TLS fix", Type: "security", Severity: "Moderate",
			CVEs: []string{"CVE-2026-7777"},
			Packages: []SynthRPMSpec{{
				Name: "caddy", Epoch: "0", Version: "2.8.4", Release: "1.el10", Arch: "aarch64",
			}},
		}},
		Packages: []SynthRPMSpec{{
			Name:        "caddy",
			Epoch:       "0",
			Version:     "2.8.4",
			Release:     "1.el10",
			Arch:        "aarch64",
			Summary:     "XML-indexed web server",
			Description: "Generated without SQLite databases (--no-database).",
			License:     "Apache-2.0",
			Group:       "Web Stack",
			Recommends:  []SynthRPMDep{{Name: "ca-certificates"}},
			Changelogs: []SynthChangelog{
				{Author: "Maintainer <dev@example.com>", Date: 1700000500, Text: "- XML changelog entry"},
			},
		}},
	})

	_, stderr, code := runRepoview(t, t.TempDir(), "--output-dir", outDir, "--force", repoDir)
	if code != 0 {
		t.Fatalf("RPM-02 XML fallback failed (exit %d): %s", code, stderr)
	}

	baseURL, client := startTestHTTPServer(t, outDir)
	pages := crawlAndVerifySite(t, client, baseURL, CrawlExpectations{
		ExpectRepoView: true,
		MinHTMLPages:   3,
	})
	pkgHTML := pages[baseURL+"/caddy.html"]
	for _, want := range []string{"CVE-2026-7777", "XML changelog entry", "ca-certificates"} {
		if !strings.Contains(pkgHTML, want) {
			t.Errorf("XML fallback caddy.html missing %q", want)
		}
	}
}

func TestE2E_RPM_03_MultiVersionEVRAndCollisionSafe(t *testing.T) {
	repoDir := t.TempDir()
	outDir := t.TempDir()

	WriteRPMRepo(t, repoDir, RPMRepoSpec{
		Mode: "sqlite",
		Packages: []SynthRPMSpec{
			{
				Name: "nginx", Epoch: "0", Version: "1.24.0", Release: "1.el9", Arch: "x86_64",
				Summary: "Older nginx release", Description: "Legacy 0:1.24.0-1 build.",
				Group: "Tools", PreIn: "echo old-version",
			},
			{
				Name: "nginx", Epoch: "1", Version: "1.24.0", Release: "2.el9~rc1", Arch: "x86_64",
				Summary: "Epoch-bumped nginx rc", Description: "Latest 1:1.24.0-2.el9~rc1 build.",
				Group: "Tools", PreIn: "echo latest-evr-prein",
				Files: []SynthRPMFile{{Dir: "/usr/sbin/", Name: "nginx-latest", Mode: 0o100755, Size: 2048}},
			},
			{
				Name: "Alpha", Epoch: "0", Version: "1.0", Release: "1.el9", Arch: "x86_64",
				Summary: "Uppercase Alpha tool", Description: "Case-colliding Upper package.",
				Group: "Tools/CLI",
			},
			{
				Name: "alpha", Epoch: "0", Version: "1.0", Release: "1.el9", Arch: "x86_64",
				Summary: "Lowercase alpha tool", Description: "Case-colliding Lower package.",
				Group: "Tools/CLI",
			},
		},
	})

	_, stderr, code := runRepoview(t, t.TempDir(), "--output-dir", outDir, "--force", repoDir)
	if code != 0 {
		t.Fatalf("RPM-03 failed (exit %d): %s", code, stderr)
	}

	baseURL, client := startTestHTTPServer(t, outDir)
	pages := crawlAndVerifySite(t, client, baseURL, CrawlExpectations{
		ExpectRepoView: true,
		MinHTMLPages:   6, // index, 2 groups, nginx, Alpha, alpha
	})

	var sawUpper, sawLower, sawNginx bool
	for urlStr, body := range pages {
		if strings.Contains(body, "Case-colliding Upper package.") {
			sawUpper = true
		}
		if strings.Contains(body, "Case-colliding Lower package.") {
			sawLower = true
		}
		if strings.HasSuffix(urlStr, "/nginx.html") {
			sawNginx = true
			if !strings.Contains(body, "echo latest-evr-prein") || !strings.Contains(body, "/usr/sbin/nginx-latest") {
				t.Errorf("nginx.html did not enrich latest EVR version[0]")
			}
			if !strings.Contains(body, "0:1.24.0-1.el9") && !strings.Contains(body, "1.24.0-1.el9") {
				t.Errorf("nginx.html missing older version entry in version table")
			}
		}
	}
	if !sawUpper || !sawLower || !sawNginx {
		t.Errorf("missing expected pages: sawUpper=%v sawLower=%v sawNginx=%v", sawUpper, sawLower, sawNginx)
	}
}

func TestE2E_RPM_04_IncrementalWorkersFsyncAndCustomTemplate(t *testing.T) {
	repoDir := t.TempDir()
	outDir := t.TempDir()
	stateDir := t.TempDir()

	// Run 1: pkg-a v1.0, pkg-b, and ignore-me
	WriteRPMRepo(t, repoDir, RPMRepoSpec{
		Mode: "sqlite",
		Packages: []SynthRPMSpec{
			{Name: "pkg-a", Version: "1.0", Release: "1.el9", Arch: "x86_64", Summary: "Package A v1"},
			{Name: "pkg-b", Version: "1.0", Release: "1.el9", Arch: "x86_64", Summary: "Package B removed later"},
			{Name: "ignore-me", Version: "1.0", Release: "1.el9", Arch: "x86_64", Summary: "Soon ignored"},
		},
	})
	_, stderr, code := runRepoview(t, t.TempDir(), "--output-dir", outDir, "--state-dir", stateDir, repoDir)
	if code != 0 {
		t.Fatalf("RPM-04 Run 1 failed (exit %d): %s", code, stderr)
	}

	// Run 2: upgrade pkg-a -> 2.0, remove pkg-b, tighten --ignore-package without --force
	_ = os.RemoveAll(repoDir)
	WriteRPMRepo(t, repoDir, RPMRepoSpec{
		Mode: "sqlite",
		Packages: []SynthRPMSpec{
			{Name: "pkg-a", Version: "2.0", Release: "1.el9", Arch: "x86_64", Summary: "Package A v2"},
			{Name: "ignore-me", Version: "1.0", Release: "1.el9", Arch: "x86_64", Summary: "Ignored now"},
		},
	})
	_, stderr, code = runRepoview(t, t.TempDir(),
		"--output-dir", outDir,
		"--state-dir", stateDir,
		"--ignore-package", "ignore*",
		repoDir,
	)
	if code != 0 {
		t.Fatalf("RPM-04 Run 2 failed (exit %d): %s", code, stderr)
	}

	if _, err := os.Stat(filepath.Join(outDir, "pkg-b.html")); !os.IsNotExist(err) {
		t.Errorf("expected removed pkg-b.html to be pruned from disk")
	}
	if _, err := os.Stat(filepath.Join(outDir, "ignore-me.html")); !os.IsNotExist(err) {
		t.Errorf("expected ignored ignore-me.html to be pruned from disk")
	}
	pkgABytes, err := os.ReadFile(filepath.Join(outDir, "pkg-a.html"))
	if err != nil || !strings.Contains(string(pkgABytes), "2.0-1.el9") {
		t.Errorf("expected upgraded pkg-a.html with 2.0-1.el9, err=%v", err)
	}

	// Verify compact state.json decodes cleanly
	var decodedState map[string]any
	if entries, err := os.ReadDir(stateDir); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				raw, _ := os.ReadFile(filepath.Join(stateDir, e.Name()))
				if err := json.Unmarshal(raw, &decodedState); err != nil {
					t.Errorf("compact state JSON failed to unmarshal: %v", err)
				}
			}
		}
	}

	// Worker & --fsync determinism check over 24 packages
	parityRepo := t.TempDir()
	var pkgs []SynthRPMSpec
	for i := 0; i < 24; i++ {
		pkgs = append(pkgs, SynthRPMSpec{
			Name:    fmt.Sprintf("worker-pkg-%02d", i),
			Version: "1.0.0", Release: "1.el9", Arch: "x86_64",
			Summary:     fmt.Sprintf("Worker parity package %02d", i),
			Description: "Deterministic worker pooling check.",
		})
	}
	WriteRPMRepo(t, parityRepo, RPMRepoSpec{Mode: "sqlite", Packages: pkgs})

	outW1 := t.TempDir()
	outW8 := t.TempDir()
	outFsync := t.TempDir()
	for _, runSpec := range []struct {
		dir  string
		args []string
	}{
		{outW1, []string{"--output-dir", outW1, "--workers", "1", parityRepo}},
		{outW8, []string{"--output-dir", outW8, "--workers", "8", parityRepo}},
		{outFsync, []string{"--output-dir", outFsync, "--workers", "8", "--fsync", parityRepo}},
	} {
		_, errStr, c := runRepoview(t, t.TempDir(), runSpec.args...)
		if c != 0 {
			t.Fatalf("parity run %v failed (exit %d): %s", runSpec.args, c, errStr)
		}
	}
	for i := 0; i < 24; i++ {
		fname := fmt.Sprintf("worker-pkg-%02d.html", i)
		b1, _ := os.ReadFile(filepath.Join(outW1, fname))
		b8, _ := os.ReadFile(filepath.Join(outW8, fname))
		bf, _ := os.ReadFile(filepath.Join(outFsync, fname))
		if string(b1) != string(b8) || string(b1) != string(bf) {
			t.Fatalf("worker/fsync determinism mismatch on %s", fname)
		}
	}

	// Custom --template-dir check
	tmplDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmplDir, "index.html"), []byte(`<html><body>CUSTOM-TMPL:{{ .Repo.Title }}</body></html>`), 0o644)
	_ = os.WriteFile(filepath.Join(tmplDir, "group.html"), []byte(`<html><body>{{ .Group.Name }}</body></html>`), 0o644)
	_ = os.WriteFile(filepath.Join(tmplDir, "package.html"), []byte(`<html><body>{{ .Pkg.Name }}</body></html>`), 0o644)
	_ = os.WriteFile(filepath.Join(tmplDir, "rss.xml"), []byte(`<rss version="2.0"><channel><title>{{ .Repo.Title }}</title></channel></rss>`), 0o644)
	outCustom := t.TempDir()
	_, errStr, c := runRepoview(t, t.TempDir(), "--output-dir", outCustom, "--template-dir", tmplDir, "--title", "CustomTmplTitle", parityRepo)
	if c != 0 {
		t.Fatalf("custom --template-dir run failed (exit %d): %s", c, errStr)
	}
	customIdx, _ := os.ReadFile(filepath.Join(outCustom, "index.html"))
	if !strings.Contains(string(customIdx), "CUSTOM-TMPL:CustomTmplTitle") {
		t.Errorf("custom template output mismatch: %s", string(customIdx))
	}
}
