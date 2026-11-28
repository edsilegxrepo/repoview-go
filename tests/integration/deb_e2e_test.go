//go:build integration

package integration

// OBJECTIVES:
// Execute end-to-end Debian (.deb) and debug symbol (.ddeb) integration workflows across
// pure-Go Matrix B/C specs as well as optional real APT mirror smoke tests.
//
// TEST STRATEGY:
// Synthesizes authentic Deb822 Packages (.gz/.bz2/.zst), clearsigned InRelease, .deb/.ddeb
// ar payloads (.tar.gz/.xz/.zst), and dists/.../debug trees in t.TempDir(), compiles/runs
// repoview subprocesses, crawls output over ephemeral HTTP servers, and verifies SHA-256 downloads.

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

func createSyntheticDebianRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	WriteDebRepo(t, repoDir, []DebIndexGroup{{
		Suite: "bookworm", Codename: "bookworm", Component: "main", Arch: "amd64", IndexComp: "gz",
		Origin: "Debian", Label: "Debian", Description: "Debian Bookworm Test Repo",
		Packages: []SynthDebSpec{
			{
				Name: "nginx", Version: "1.24.0-2", Arch: "amd64", Section: "httpd", InstalledSize: 1500,
				Maintainer:      "Debian Nginx Maintainers <pkg-nginx-maintainers@example.com>",
				Depends:         "libc6 (>= 2.34), libssl3 (>= 3.0.0)",
				Recommends:      "logrotate",
				Suggests:        "nginx-doc",
				Conflicts:       "nginx-light",
				Breaks:          "nginx-full (<< 1.20)",
				Replaces:        "nginx-common",
				Description:     "high-performance web server",
				LongDescription: "Small and fast web server.",
				PreInst:         "#!/bin/sh\necho 'nginx preinst running'\n",
				PostInst:        "#!/bin/sh\necho 'nginx postinst running'\n",
				PreRm:           "#!/bin/sh\necho 'nginx prerm running'\n",
				PostRm:          "#!/bin/sh\necho 'nginx postrm running'\n",
				Changelog:       "nginx (1.24.0-2) bookworm; urgency=medium\n\n  * Real Debian E2E Integration test release.\n\n -- Debian Nginx Maintainers <pkg-nginx-maintainers@example.com>  Thu, 17 Sep 2026 12:00:00 +0000\n",
				ExtraFiles: []SynthDebFileSpec{
					{Path: "./usr/sbin/nginx", Mode: 0o755, Content: []byte("#!/bin/sh\necho nginx\n")},
					{Path: "./etc/nginx/nginx.conf", Mode: 0o644, Content: []byte("worker_processes 1;\n")},
				},
			},
			{
				Name: "curl", Version: "8.5.0-1", Arch: "amd64", Section: "web", InstalledSize: 450,
				Maintainer:      "Debian cURL Team <curl@example.com>",
				Depends:         "libc6 (>= 2.34), libcurl4 (= 8.5.0-1)",
				Description:     "command line tool for transferring data",
				LongDescription: "curl transfers data using URL syntax.",
				ExtraFiles: []SynthDebFileSpec{
					{Path: "./usr/bin/curl", Mode: 0o755, Content: []byte("#!/bin/sh\necho curl\n")},
				},
			},
		},
	}})
	return repoDir
}

func TestLive_Debian_EndToEndWorkflow(t *testing.T) {
	repoDir := createSyntheticDebianRepo(t)
	outDir := t.TempDir()
	stateDir := t.TempDir()

	cfg := app.Config{
		RepoDir:   repoDir,
		OutputDir: outDir,
		StateDir:  stateDir,
		Title:     "Debian 12 (Bookworm) Main",
		URL:       "http://127.0.0.1/debian",
		BaseURL:   "http://127.0.0.1/debian",
		Format:    models.FormatDEB,
		Force:     true,
		Quiet:     true,
		Version:   "deb-e2e-test",
	}

	// 1. Execute initial generation
	t.Log("Generating repository view from Debian repository...")
	gen := app.NewGenerator(cfg)
	start := time.Now()
	if err := gen.Run(); err != nil {
		t.Fatalf("Live Debian Generator.Run failed: %v", err)
	}
	t.Logf("Debian generation completed in %v", time.Since(start))

	// 2. Validate essential files exist
	expectedFiles := []string{
		"index.html",
		"search.json",
		"latest-feed.xml",
		"nginx.html",
		"curl.html",
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

	// 3. Start live HTTP server on ephemeral port to simulate production client access
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
	t.Run("Live_Debian_IndexHTML", func(t *testing.T) {
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
		if !strings.Contains(content, "Debian 12 (Bookworm) Main") {
			t.Errorf("index.html missing repository title")
		}
		if !strings.Contains(content, "nginx.html") {
			t.Errorf("index.html missing link to nginx package")
		}
		if !strings.Contains(content, "curl.html") {
			t.Errorf("index.html missing link to curl package")
		}
		// Assert presence of deb822 source configuration snippet
		if !strings.Contains(content, "Types: deb") {
			t.Errorf("index.html missing Deb822 sources snippet")
		}
	})

	// 5. Test Live Endpoint: GET /search.json
	t.Run("Live_Debian_SearchJSON", func(t *testing.T) {
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

		if len(searchIndex.Data) < 2 {
			t.Errorf("search.json expected at least 2 packages, got %d", len(searchIndex.Data))
		}

		foundNginx := false
		foundCurl := false
		for _, row := range searchIndex.Data {
			if len(row) > 0 {
				if row[0] == "nginx" {
					foundNginx = true
				}
				if row[0] == "curl" {
					foundCurl = true
				}
			}
		}
		if !foundNginx {
			t.Errorf("nginx not found in search.json")
		}
		if !foundCurl {
			t.Errorf("curl not found in search.json")
		}
	})

	// 6. Test Live Endpoint: GET /nginx.html
	t.Run("Live_Debian_PackagePage", func(t *testing.T) {
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

		// Assert Debian quick install bar commands
		if !strings.Contains(content, "sudo apt install nginx") {
			t.Errorf("nginx.html missing 'sudo apt install nginx'")
		}
		if !strings.Contains(content, "data-tool=\"dpkg\"") {
			t.Errorf("nginx.html missing dpkg tool tab")
		}

		// Assert dependencies: Depends, Recommends, Suggests, Conflicts & Breaks
		if !strings.Contains(content, "Depends") {
			t.Errorf("nginx.html missing Depends section")
		}
		if !strings.Contains(content, "Recommends") {
			t.Errorf("nginx.html missing Recommends section")
		}
		if !strings.Contains(content, "Suggests") {
			t.Errorf("nginx.html missing Suggests section")
		}
		if !strings.Contains(content, "Conflicts & Breaks") {
			t.Errorf("nginx.html missing Conflicts & Breaks section")
		}

		// Assert maintainer scriptlets inspection
		if !strings.Contains(content, "preinst maintainer script") {
			t.Errorf("nginx.html missing preinst maintainer script")
		}
		if !strings.Contains(content, "nginx preinst running") {
			t.Errorf("nginx.html missing preinst script body")
		}
		if !strings.Contains(content, "postinst maintainer script") {
			t.Errorf("nginx.html missing postinst maintainer script")
		}

		// Assert on-demand installed file manifest
		if !strings.Contains(content, "/usr/sbin/nginx") {
			t.Errorf("nginx.html missing installed file /usr/sbin/nginx")
		}

		// Assert on-demand changelog entry
		if !strings.Contains(content, "Real Debian E2E Integration test release") {
			t.Errorf("nginx.html missing on-demand extracted changelog")
		}
	})

	// 7. Test Live Endpoint: GET /latest-feed.xml
	t.Run("Live_Debian_RSSFeed", func(t *testing.T) {
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
	t.Run("Live_Debian_IncrementalRun", func(t *testing.T) {
		cfg.Force = false
		incGen := app.NewGenerator(cfg)
		startInc := time.Now()
		if err := incGen.Run(); err != nil {
			t.Fatalf("incremental Debian run failed: %v", err)
		}
		t.Logf("Incremental pass completed in %v", time.Since(startInc))
	})
}

func TestLive_Debian_SubprocessCLI(t *testing.T) {
	repoDir := createSyntheticDebianRepo(t)
	outDir := t.TempDir()

	// 1. Execute CLI subprocess with explicit --format deb
	_, output, code := runRepoview(t, t.TempDir(),
		"--output-dir", outDir,
		"--format", "deb",
		"--force",
		"--quiet",
		repoDir,
	)
	if code != 0 {
		t.Fatalf("subprocess execution with --format deb failed (exit %d): %s", code, output)
	}

	indexPath := filepath.Join(outDir, "index.html")
	if fi, err := os.Stat(indexPath); err != nil || fi.Size() == 0 {
		t.Errorf("index.html was not generated by CLI subprocess: %v", err)
	}

	// 2. Execute CLI subprocess with --format auto
	outDirAuto := t.TempDir()
	_, outputAuto, codeAuto := runRepoview(t, t.TempDir(),
		"--output-dir", outDirAuto,
		"--format", "auto",
		"--force",
		"--quiet",
		repoDir,
	)
	if codeAuto != 0 {
		t.Fatalf("subprocess execution with auto-detection failed (exit %d): %s", codeAuto, outputAuto)
	}

	indexPathAuto := filepath.Join(outDirAuto, "index.html")
	if fi, err := os.Stat(indexPathAuto); err != nil || fi.Size() == 0 {
		t.Errorf("index.html was not generated by CLI auto-detection: %v", err)
	}
}

// ---------- Matrix B: Multi-Suite, Multi-Arch & Custom DEB Topologies ----------

func TestE2E_DEB_01_MultiSuiteArchAndPayloads(t *testing.T) {
	workspace := t.TempDir()
	ubuDir := filepath.Join(workspace, "ubu24")
	deb13Dir := filepath.Join(workspace, "deb13")

	writtenUbu := WriteDebRepo(t, ubuDir, []DebIndexGroup{
		{
			Suite: "noble", Codename: "noble", Component: "main", Arch: "amd64", IndexComp: "zst", UseInRelease: true,
			Origin: "Ubuntu", Description: "Ubuntu 24.04 Noble",
			Packages: []SynthDebSpec{{
				Name: "noble-only", Version: "24.04.1", Arch: "amd64", Section: "net",
				ControlComp: "zst", DataComp: "zst", Description: "Noble-only package",
			}},
		},
		{
			Suite: "jammy", Codename: "jammy", Component: "universe", Arch: "arm64", IndexComp: "zst", UseInRelease: true,
			Origin: "Ubuntu", Description: "Ubuntu 22.04 Jammy Universe",
			Packages: []SynthDebSpec{{
				Name: "jammy-arm-tool", Version: "2.1.0-1", Arch: "arm64", Section: "utils",
				Priority: "important", MultiArch: "foreign", BuiltUsing: "glibc (= 2.35-0ubuntu3)",
				Tag: "role::program, uitoolkit::cli", ControlComp: "zst", DataComp: "zst",
				Conffiles:   []string{"/etc/jammy-arm/app.conf"},
				Description: "Jammy ARM64 Zstd tool",
				Copyright:   "Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/\nLicense: Apache-2.0\n",
				ExtraFiles: []SynthDebFileSpec{
					{Path: "./usr/bin/jammy-arm", Mode: 0o755, Content: []byte("#!/bin/sh\necho arm\n")},
					{Path: "./etc/jammy-arm/app.conf", Mode: 0o644, Content: []byte("mode=prod\n")},
					{Path: "./usr/bin/jammy-link", SymlinkTarget: "/usr/bin/jammy-arm"},
				},
			}},
		},
	})

	WriteDebRepo(t, deb13Dir, []DebIndexGroup{
		{
			Suite: "trixie", Codename: "trixie", Component: "main", Arch: "amd64", IndexComp: "xz", UseInRelease: true,
			Origin: "Debian", Description: "Debian 13 Trixie Main",
			Packages: []SynthDebSpec{{
				Name: "trixie-core", Version: "13.0.1", Arch: "amd64", Section: "libs",
				ControlComp: "xz", DataComp: "xz", Description: "Debian 13 XZ core package",
				Copyright: "Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/\nLicense: GPL-3.0+\n",
			}},
		},
		{
			Suite: "trixie", Codename: "trixie", Component: "contrib", Arch: "amd64", IndexComp: "bz2", UseInRelease: true,
			Origin: "Debian", Description: "Debian 13 Trixie Contrib",
		},
	})

	// 1. Test --suite jammy --arch arm64 isolation on multi-suite ubu24
	outJammy := filepath.Join(workspace, "out-jammy")
	mux := http.NewServeMux()
	mux.Handle("/ubu24/", http.StripPrefix("/ubu24/", http.FileServer(http.Dir(ubuDir))))
	mux.Handle("/view-jammy/", http.StripPrefix("/view-jammy/", http.FileServer(http.Dir(outJammy))))
	baseURL, client := startTestMuxHTTPServer(t, mux)

	_, stderr, code := runRepoview(t, t.TempDir(),
		"--output-dir", outJammy,
		"--suite", "jammy",
		"--arch", "arm64",
		"--url", baseURL+"/ubu24",
		"--baseurl", baseURL+"/ubu24",
		"--force",
		ubuDir,
	)
	if code != 0 {
		t.Fatalf("DEB-01 jammy/arm64 failed (exit %d): %s", code, stderr)
	}

	pages := crawlAndVerifySite(t, client, baseURL+"/view-jammy", CrawlExpectations{
		ExpectRepoView:        true,
		VerifyPackageSnippets: true,
		MinHTMLPages:          3,
		ForbiddenSubstrings:   []string{"-----BEGIN PGP"},
	})
	if _, hasNoble := pages[baseURL+"/view-jammy/noble-only.html"]; hasNoble {
		t.Errorf("noble-only package leaked into --suite jammy --arch arm64 build")
	}
	pkgPage := pages[baseURL+"/view-jammy/jammy-arm-tool.html"]
	for _, want := range []string{"important", "Multi-Arch: foreign", "role::program", "Apache-2.0", "/usr/bin/jammy-arm", "/etc/jammy-arm/app.conf"} {
		if !strings.Contains(pkgPage, want) {
			t.Errorf("jammy-arm-tool.html missing expected attribute %q", want)
		}
	}
	for href, info := range writtenUbu {
		if strings.Contains(href, "jammy-arm-tool") {
			data, _ := httpGetBytes(t, client, baseURL+"/ubu24/"+href)
			if sha256Hex(data) != info.SHA256 {
				t.Errorf("jammy .deb download SHA-256 mismatch")
			}
		}
	}

	// 2. Test deb13 trixie with .tar.xz + InRelease + Packages.bz2
	outDeb13 := t.TempDir()
	_, stderr2, code2 := runRepoview(t, t.TempDir(),
		"--output-dir", outDeb13,
		"--suite", "trixie",
		"--arch", "amd64",
		"--force",
		deb13Dir,
	)
	if code2 != 0 {
		t.Fatalf("DEB-01 deb13 trixie failed (exit %d): %s", code2, stderr2)
	}
	for _, fname := range []string{"trixie-core.html", "bz2-demo.html"} {
		if _, err := os.Stat(filepath.Join(outDeb13, fname)); err != nil {
			t.Errorf("expected %s from deb13 trixie (xz + bz2): %v", fname, err)
		}
	}
}

func TestE2E_DEB_02_FlatAndTwoLevelTopologies(t *testing.T) {
	workspace := t.TempDir()

	// ubu24/custom (Packages.zst + Release), ubu26/custom (Packages.gz + Release), deb13/custom (Packages.bz2 + Release), flat roots
	targets := []struct {
		rel      string
		groups   []DebIndexGroup
		wantPkg  string
		wantText string
	}{
		{
			rel: "ubu24/custom",
			groups: []DebIndexGroup{{
				Arch: "amd64", IndexComp: "zst", Origin: "Ubuntu", Label: "Ubu24 Custom",
				Packages: []SynthDebSpec{{Name: "ubu24-svc", Version: "1.0-1", Arch: "amd64", ControlComp: "zst", DataComp: "zst", Description: "Ubu24 custom service"}},
			}},
			wantPkg: "ubu24-svc",
		},
		{
			rel: "ubu26/custom",
			groups: []DebIndexGroup{{
				Arch: "amd64", IndexComp: "gz", Origin: "Ubuntu", Label: "Ubu26 Custom",
				Packages: []SynthDebSpec{{Name: "ubu26-svc", Version: "2.0-1", Arch: "amd64", ControlComp: "zst", DataComp: "zst", Description: "Ubu26 custom service"}},
			}},
			wantPkg: "ubu26-svc",
		},
		{
			rel: "deb13/custom",
			groups: []DebIndexGroup{{
				Arch: "amd64", IndexComp: "bz2", Origin: "Debian", Label: "Deb13 Custom",
			}},
			wantPkg: "bz2-demo",
		},
		{
			rel: "ubuntu-26.04-amd64",
			groups: []DebIndexGroup{{
				Arch: "amd64", IndexComp: "gz", Origin: "Ubuntu", Label: "Flat 26.04",
				Packages: []SynthDebSpec{{Name: "flat-ubu26", Version: "1.0", Arch: "amd64", Description: "Flat Ubuntu 26.04"}},
			}},
			wantPkg: "flat-ubu26",
		},
		{
			rel: "debian-13-amd64",
			groups: []DebIndexGroup{{
				Arch: "amd64", IndexComp: "xz", Origin: "Debian", Label: "Flat Deb13",
				Packages: []SynthDebSpec{{Name: "flat-deb13", Version: "1.0", Arch: "amd64", ControlComp: "xz", DataComp: "xz", Description: "Flat Debian 13"}},
			}},
			wantPkg: "flat-deb13",
		},
	}

	for _, tc := range targets {
		repoDir := filepath.Join(workspace, filepath.FromSlash(tc.rel))
		WriteDebRepo(t, repoDir, tc.groups)
		outDir := t.TempDir()
		stateDir := t.TempDir()

		_, stderr, code := runRepoview(t, t.TempDir(), "--output-dir", outDir, "--state-dir", stateDir, repoDir)
		if code != 0 {
			t.Fatalf("DEB-02 %s failed (exit %d): %s", tc.rel, code, stderr)
		}
		if _, err := os.Stat(filepath.Join(outDir, tc.wantPkg+".html")); err != nil {
			t.Errorf("DEB-02 %s missing %s.html: %v", tc.rel, tc.wantPkg, err)
		}
		// Incremental short-circuit run
		_, stderr2, code2 := runRepoview(t, t.TempDir(), "--output-dir", outDir, "--state-dir", stateDir, repoDir)
		if code2 != 0 {
			t.Fatalf("DEB-02 %s incremental failed (exit %d): %s", tc.rel, code2, stderr2)
		}
	}
}

// ---------- Matrix C: DDEB Debian Debug Symbol Scenarios ----------

func TestE2E_DDEB_01_StandaloneFlatDDEB(t *testing.T) {
	workspace := t.TempDir()
	repoDir := filepath.Join(workspace, "ubu24-dbgsym")
	outDir := filepath.Join(workspace, "site")

	written := WriteDebRepo(t, repoDir, []DebIndexGroup{{
		Arch: "amd64", IndexComp: "zst", Origin: "Ubuntu-Debug", Label: "Ubuntu 24.04 dbgsym",
		Packages: []SynthDebSpec{{
			Name: "nginx-dbgsym", Version: "1.24.0-2", Arch: "amd64",
			Section: "debug", Extension: ".ddeb", ControlComp: "zst", DataComp: "zst",
			BuildIDs:    []string{"abcdef1234567890fedcba0987654321"},
			Description: "debug symbols for nginx",
		}},
	}})

	mux := http.NewServeMux()
	mux.Handle("/repo/", http.StripPrefix("/repo/", http.FileServer(http.Dir(repoDir))))
	mux.Handle("/view/", http.StripPrefix("/view/", http.FileServer(http.Dir(outDir))))
	baseURL, client := startTestMuxHTTPServer(t, mux)

	_, stderr, code := runRepoview(t, t.TempDir(),
		"--output-dir", outDir,
		"--url", baseURL+"/repo",
		"--baseurl", baseURL+"/repo",
		repoDir,
	)
	if code != 0 {
		t.Fatalf("DDEB-01 failed (exit %d): %s", code, stderr)
	}

	pages := crawlAndVerifySite(t, client, baseURL+"/view", CrawlExpectations{
		ExpectRepoView: true,
		MinHTMLPages:   3,
	})
	pkgHTML := pages[baseURL+"/view/nginx-dbgsym.html"]
	for _, want := range []string{
		"nginx-dbgsym_1.24.0-2_amd64.ddeb",
		"/usr/lib/debug/.build-id/ab/cdef1234567890fedcba0987654321.debug",
		"Section",
	} {
		if !strings.Contains(pkgHTML, want) {
			t.Errorf("nginx-dbgsym.html missing %q", want)
		}
	}
	for href, info := range written {
		data, _ := httpGetBytes(t, client, baseURL+"/repo/"+href)
		if sha256Hex(data) != info.SHA256 {
			t.Errorf(".ddeb download %s SHA-256 mismatch", href)
		}
	}
}

func TestE2E_DDEB_02_APTDebugTreeAndPortal(t *testing.T) {
	workspace := t.TempDir()
	mirrorRoot := filepath.Join(workspace, "mirror")
	ddebRepo := filepath.Join(mirrorRoot, "ddebs")

	written := WriteDebRepo(t, ddebRepo, []DebIndexGroup{
		{
			Suite: "noble", Component: "main", IsDebug: true, Arch: "amd64", IndexComp: "gz", OmitRelease: true,
			Packages: []SynthDebSpec{{
				Name: "nginx-dbgsym", Version: "1.24.0-2", Arch: "amd64", Section: "debug",
				Extension: ".ddeb", ControlComp: "zst", DataComp: "zst",
				BuildIDs:    []string{"11223344556677889900aabbccddeeff"},
				Description: "amd64 debug symbols for nginx",
			}},
		},
		{
			Suite: "noble", Component: "main", IsDebug: true, Arch: "arm64", IndexComp: "gz", OmitRelease: true,
			Packages: []SynthDebSpec{{
				Name: "nginx-dbgsym", Version: "1.24.0-2", Arch: "arm64", Section: "debug",
				Extension: ".ddeb", ControlComp: "zst", DataComp: "zst",
				BuildIDs:    []string{"ffeeddccbbaa00998877665544332211"},
				Description: "arm64 debug symbols for nginx",
			}},
		},
	})

	// 1. Standalone repoview targeting ddebs --suite noble --arch amd64 (even without Release!)
	standaloneOut := filepath.Join(workspace, "standalone")
	_, stderr, code := runRepoview(t, t.TempDir(),
		"--output-dir", standaloneOut,
		"--suite", "noble",
		"--arch", "amd64",
		ddebRepo,
	)
	if code != 0 {
		t.Fatalf("DDEB-02 standalone failed (exit %d): %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(standaloneOut, "nginx-dbgsym.html")); err != nil {
		t.Errorf("DDEB-02 standalone missing nginx-dbgsym.html: %v", err)
	}

	// 2. Portal detached rendering discovering both amd64 & arm64 debug trees (sparing dists/.../debug, pruning pool/)
	portalOut := filepath.Join(workspace, "portal-out")
	_, stderrP, codeP := runRepoview(t, t.TempDir(), "portal",
		"--portal-home", portalOut,
		"--render-all",
		"--title", "DDEB Debug Catalog",
		mirrorRoot,
	)
	if codeP != 0 {
		t.Fatalf("DDEB-02 portal failed (exit %d): %s", codeP, stderrP)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(portalOut)))
	baseURL, client := startTestMuxHTTPServer(t, mux)
	pages := crawlAndVerifySite(t, client, baseURL, CrawlExpectations{
		ExpectPortal:     true,
		RequireKnownArch: true,
		ExpectFormats:    []string{"deb"},
		ForbidFormats:    []string{"rpm"},
		MinHTMLPages:     3,
	})

	portalHTML := pages[baseURL+"/index.html"]
	if !strings.Contains(portalHTML, `data-arch="amd64"`) || !strings.Contains(portalHTML, `data-arch="arm64"`) {
		t.Errorf("DDEB-02 portal missing amd64 or arm64 debug card despite OmitRelease=true")
	}
	if len(written) != 2 {
		t.Errorf("expected 2 written .ddeb payloads, got %d", len(written))
	}
}

func getRealDebTestRepoPath(t *testing.T) string {
	if custom := os.Getenv("REPOVIEW_DEB_TEST_REPO"); custom != "" {
		if fi, err := os.Stat(custom); err == nil && fi.IsDir() {
			return custom
		}
	}

	defaultPath := "/u01/wwwroot/test/ubu24/custom"
	if fi, err := os.Stat(defaultPath); err == nil && fi.IsDir() {
		return defaultPath
	}

	t.Skip("skipping live real Debian repo test: repo not found at " + defaultPath)
	return ""
}

func TestLive_Debian_RealRepository_EndToEndWorkflow(t *testing.T) {
	repoDir := getRealDebTestRepoPath(t)
	outDir := t.TempDir()
	stateDir := t.TempDir()

	cfg := app.Config{
		RepoDir:   repoDir,
		OutputDir: outDir,
		StateDir:  stateDir,
		Title:     "Ubuntu 24.04 Custom DEB Repository",
		URL:       "http://127.0.0.1/ubu24/custom",
		BaseURL:   "http://127.0.0.1/ubu24/custom",
		Force:     true,
		Quiet:     true,
		Version:   "ubu24-custom-test",
	}

	// 1. Execute initial generation
	t.Logf("Generating repository view from real Debian repo at %s...", repoDir)
	gen := app.NewGenerator(cfg)
	start := time.Now()
	if err := gen.Run(); err != nil {
		t.Fatalf("Live Real Debian Generator.Run failed: %v", err)
	}
	t.Logf("Generation of %s completed in %v", repoDir, time.Since(start))

	// 2. Validate essential files exist
	expectedFiles := []string{
		"index.html",
		"search.json",
		"latest-feed.xml",
		"clamav.html",
		"gcsfuse.html",
		"proxysql.html",
		"rclone.html",
		"miller.html",
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
	t.Run("RealRepo_Debian_IndexHTML", func(t *testing.T) {
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
		if !strings.Contains(content, "Ubuntu 24.04 Custom DEB Repository") {
			t.Errorf("index.html missing repository title")
		}
		if !strings.Contains(content, "gcsfuse.html") {
			t.Errorf("index.html missing link to gcsfuse package")
		}
		if !strings.Contains(content, "rclone.html") {
			t.Errorf("index.html missing link to rclone package")
		}
		if !strings.Contains(content, "letter_c.group.html") {
			t.Errorf("index.html missing link to letter C group page")
		}
		if !strings.Contains(content, "letter_p.group.html") {
			t.Errorf("index.html missing link to letter P group page")
		}
		if !strings.Contains(content, "Types: deb") {
			t.Errorf("index.html missing Deb822 sources modal snippet")
		}
	})

	// 5. Test Live Endpoint: GET /search.json
	t.Run("RealRepo_Debian_SearchJSON", func(t *testing.T) {
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

		if len(searchIndex.Data) < 40 {
			t.Errorf("expected at least 40 packages in search index, got %d", len(searchIndex.Data))
		}

		foundClamav := false
		foundGcsfuse := false
		foundProxysql := false
		foundRclone := false
		for _, row := range searchIndex.Data {
			if len(row) > 0 {
				switch row[0] {
				case "clamav":
					foundClamav = true
				case "gcsfuse":
					foundGcsfuse = true
				case "proxysql":
					foundProxysql = true
				case "rclone":
					foundRclone = true
				}
			}
		}

		if !foundClamav || !foundGcsfuse || !foundProxysql || !foundRclone {
			t.Errorf("expected core uploaded packages in search index: clamav=%v, gcsfuse=%v, proxysql=%v, rclone=%v",
				foundClamav, foundGcsfuse, foundProxysql, foundRclone)
		}
	})

	// 6. Test Live Endpoint: GET /gcsfuse.html
	t.Run("RealRepo_Debian_GcsfusePage", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/gcsfuse.html")
		if err != nil {
			t.Fatalf("GET /gcsfuse.html failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		content := string(body)

		if !strings.Contains(content, "sudo apt install gcsfuse") {
			t.Errorf("gcsfuse.html missing 'sudo apt install gcsfuse'")
		}
		if !strings.Contains(content, "data-tool=\"dpkg\"") || !strings.Contains(content, "gcsfuse_3.11.3_amd64.deb") {
			t.Errorf("gcsfuse.html missing dpkg installation tab with exact .deb archive filename")
		}
		if !strings.Contains(content, "fuse") {
			t.Errorf("gcsfuse.html missing 'fuse' dependency")
		}
	})

	// 7. Test Live Endpoint: GET /proxysql.html
	t.Run("RealRepo_Debian_ProxysqlPage", func(t *testing.T) {
		resp, err := client.Get(baseURL + "/proxysql.html")
		if err != nil {
			t.Fatalf("GET /proxysql.html failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		content := string(body)

		if !strings.Contains(content, "sudo apt install proxysql") {
			t.Errorf("proxysql.html missing 'sudo apt install proxysql'")
		}
		if !strings.Contains(content, "data-tool=\"dpkg\"") || !strings.Contains(content, "proxysql_3.0.10-ubuntu24_amd64.deb") {
			t.Errorf("proxysql.html missing dpkg tab for proxysql")
		}
	})

	// 8. Test Live Endpoint: GET /latest-feed.xml
	t.Run("RealRepo_Debian_RSSFeed", func(t *testing.T) {
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

	// 9. Incremental build pass: Run without Force, asserting rapid execution and stability
	t.Run("RealRepo_Debian_IncrementalRun", func(t *testing.T) {
		cfg.Force = false
		incGen := app.NewGenerator(cfg)
		startInc := time.Now()
		if err := incGen.Run(); err != nil {
			t.Fatalf("incremental real Debian run failed: %v", err)
		}
		t.Logf("Incremental pass completed in %v", time.Since(startInc))
	})
}
