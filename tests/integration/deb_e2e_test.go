//go:build integration

package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
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

	"github.com/edsilegxrepo/repoview/internal/app"
	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/util"
)

// Helper to build an in-memory tar.gz for control.tar.gz or data.tar.gz
func makeTarGz(files map[string][]byte) []byte {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for name, content := range files {
		hdr := &tar.Header{
			Name:    name,
			Mode:    0o755,
			Size:    int64(len(content)),
			ModTime: time.Now(),
			Uname:   "root",
			Gname:   "root",
		}
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write(content)
	}

	_ = tw.Close()
	_ = gw.Close()
	return buf.Bytes()
}

// Helper to build an in-memory .deb file (standard Unix ar archive)
func makeDebArchive(controlTarGz, dataTarGz []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("!<arch>\n")

	writeArMember := func(name string, data []byte) {
		hdr := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8o%-10d`\n",
			name, time.Now().Unix(), 0, 0, 0o644, len(data))
		buf.WriteString(hdr)
		buf.Write(data)
		if len(data)%2 != 0 {
			buf.WriteByte('\n')
		}
	}

	writeArMember("debian-binary", []byte("2.0\n"))
	writeArMember("control.tar.gz", controlTarGz)
	writeArMember("data.tar.gz", dataTarGz)

	return buf.Bytes()
}

// createSyntheticDebianRepo builds an authentic Debian repository tree in t.TempDir()
func createSyntheticDebianRepo(t *testing.T) string {
	repoDir := t.TempDir()

	// 1. Prepare changelog text
	rawChangelog := `nginx (1.24.0-2) bookworm; urgency=medium

  * Real Debian E2E Integration test release.

 -- Debian Nginx Maintainers <pkg-nginx-maintainers@example.com>  Thu, 17 Sep 2026 12:00:00 +0000
`
	var gzChangelog bytes.Buffer
	gw := gzip.NewWriter(&gzChangelog)
	_, _ = gw.Write([]byte(rawChangelog))
	_ = gw.Close()

	// 2. Build nginx_1.24.0-2_amd64.deb
	nginxControl := map[string][]byte{
		"./control":  []byte("Package: nginx\nVersion: 1.24.0-2\nArchitecture: amd64\nMaintainer: Debian Nginx Maintainers <pkg-nginx-maintainers@example.com>\nInstalled-Size: 1500\nDepends: libc6 (>= 2.34), libssl3 (>= 3.0.0)\nRecommends: logrotate\nSuggests: nginx-doc\nConflicts: nginx-light\nBreaks: nginx-full (<< 1.20)\nReplaces: nginx-common\nSection: httpd\nDescription: high-performance web server\n Small and fast web server.\n"),
		"./preinst":  []byte("#!/bin/sh\necho 'nginx preinst running'\n"),
		"./postinst": []byte("#!/bin/sh\necho 'nginx postinst running'\n"),
		"./prerm":    []byte("#!/bin/sh\necho 'nginx prerm running'\n"),
		"./postrm":   []byte("#!/bin/sh\necho 'nginx postrm running'\n"),
	}
	nginxData := map[string][]byte{
		"./usr/sbin/nginx":                          []byte("#!/bin/sh\necho nginx binary\n"),
		"./etc/nginx/nginx.conf":                    []byte("worker_processes 1;\n"),
		"./usr/share/doc/nginx/changelog.Debian.gz": gzChangelog.Bytes(),
	}
	nginxDebBytes := makeDebArchive(makeTarGz(nginxControl), makeTarGz(nginxData))

	// 3. Build curl_8.5.0-1_amd64.deb
	curlControl := map[string][]byte{
		"./control": []byte("Package: curl\nVersion: 8.5.0-1\nArchitecture: amd64\nMaintainer: Debian cURL Team <curl@example.com>\nInstalled-Size: 450\nDepends: libc6 (>= 2.34), libcurl4 (= 8.5.0-1)\nSection: web\nDescription: command line tool for transferring data\n curl transfers data using URL syntax.\n"),
	}
	curlData := map[string][]byte{
		"./usr/bin/curl": []byte("#!/bin/sh\necho curl binary\n"),
	}
	curlDebBytes := makeDebArchive(makeTarGz(curlControl), makeTarGz(curlData))

	// 4. Create pool hierarchy: pool/main/n/nginx and pool/main/c/curl
	nginxPoolDir := filepath.Join(repoDir, "pool", "main", "n", "nginx")
	curlPoolDir := filepath.Join(repoDir, "pool", "main", "c", "curl")
	if err := os.MkdirAll(nginxPoolDir, 0o755); err != nil {
		t.Fatalf("failed to create nginx pool dir: %v", err)
	}
	if err := os.MkdirAll(curlPoolDir, 0o755); err != nil {
		t.Fatalf("failed to create curl pool dir: %v", err)
	}

	nginxDebPath := filepath.Join(nginxPoolDir, "nginx_1.24.0-2_amd64.deb")
	curlDebPath := filepath.Join(curlPoolDir, "curl_8.5.0-1_amd64.deb")
	if err := os.WriteFile(nginxDebPath, nginxDebBytes, 0o644); err != nil {
		t.Fatalf("failed to write nginx.deb: %v", err)
	}
	if err := os.WriteFile(curlDebPath, curlDebBytes, 0o644); err != nil {
		t.Fatalf("failed to write curl.deb: %v", err)
	}

	// 5. Create dists hierarchy: dists/bookworm/main/binary-amd64
	binDir := filepath.Join(repoDir, "dists", "bookworm", "main", "binary-amd64")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("failed to create binary-amd64 dir: %v", err)
	}

	packagesContent := fmt.Sprintf(`Package: nginx
Version: 1.24.0-2
Architecture: amd64
Maintainer: Debian Nginx Maintainers <pkg-nginx-maintainers@example.com>
Installed-Size: 1500
Depends: libc6 (>= 2.34), libssl3 (>= 3.0.0)
Recommends: logrotate
Suggests: nginx-doc
Conflicts: nginx-light
Breaks: nginx-full (<< 1.20)
Replaces: nginx-common
Section: httpd
Filename: pool/main/n/nginx/nginx_1.24.0-2_amd64.deb
Size: %d
SHA256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
Description: high-performance web server
 Small and fast web server.

Package: curl
Version: 8.5.0-1
Architecture: amd64
Maintainer: Debian cURL Team <curl@example.com>
Installed-Size: 450
Depends: libc6 (>= 2.34), libcurl4 (= 8.5.0-1)
Section: web
Filename: pool/main/c/curl/curl_8.5.0-1_amd64.deb
Size: %d
SHA256: abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789
Description: command line tool for transferring data
 curl transfers data using URL syntax.
`, len(nginxDebBytes), len(curlDebBytes))

	pkgGzPath := filepath.Join(binDir, "Packages.gz")
	pkgGzFile, err := os.Create(pkgGzPath)
	if err != nil {
		t.Fatalf("failed to create Packages.gz: %v", err)
	}
	pgw := gzip.NewWriter(pkgGzFile)
	_, _ = pgw.Write([]byte(packagesContent))
	_ = pgw.Close()
	_ = pkgGzFile.Close()

	// 6. Release file
	releaseDir := filepath.Join(repoDir, "dists", "bookworm")
	releaseContent := `Origin: Debian
Label: Debian
Suite: bookworm
Codename: bookworm
Components: main
Architectures: amd64
`
	if err := os.WriteFile(filepath.Join(releaseDir, "Release"), []byte(releaseContent), 0o644); err != nil {
		t.Fatalf("failed to write Release: %v", err)
	}

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
		if perm := fi.Mode().Perm(); perm != util.DefaultFilePerm {
			t.Errorf("expected %04o file permissions for %s, got %04o", util.DefaultFilePerm, f, perm)
		}
	}

	layoutDirInfo, err := os.Stat(filepath.Join(outDir, "layout"))
	if err != nil {
		t.Errorf("failed to stat layout directory: %v", err)
	} else if perm := layoutDirInfo.Mode().Perm(); perm != util.DefaultDirPerm {
		t.Errorf("expected %04o permissions for layout directory, got %04o", util.DefaultDirPerm, perm)
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
		defer resp.Body.Close()

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
		defer resp.Body.Close()

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
		defer resp.Body.Close()

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
		defer resp.Body.Close()

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
	tempBin := filepath.Join(t.TempDir(), "repoview")

	// 1. Compile binary
	t.Log("Compiling repoview binary...")
	buildCmd := exec.Command("go", "build", "-o", tempBin, "../../cmd/repoview/main.go")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\nOutput: %s", err, string(out))
	}

	// 2. Execute CLI subprocess with explicit --format deb
	t.Log("Executing repoview subprocess with --format deb...")
	runCmd := exec.Command(tempBin,
		"--output-dir", outDir,
		"--format", "deb",
		"--force",
		"--quiet",
		repoDir,
	)

	output, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess execution with --format deb failed: %v\nOutput: %s", err, string(output))
	}

	// 3. Verify index was created
	indexPath := filepath.Join(outDir, "index.html")
	if fi, err := os.Stat(indexPath); err != nil || fi.Size() == 0 {
		t.Errorf("index.html was not generated by CLI subprocess: %v", err)
	}

	// 4. Execute CLI subprocess with --format auto (testing auto-detection of Debian repository)
	outDirAuto := t.TempDir()
	t.Log("Executing repoview subprocess with auto-detection on Debian repo...")
	runCmdAuto := exec.Command(tempBin,
		"--output-dir", outDirAuto,
		"--format", "auto",
		"--force",
		"--quiet",
		repoDir,
	)

	outputAuto, err := runCmdAuto.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess execution with auto-detection failed: %v\nOutput: %s", err, string(outputAuto))
	}

	indexPathAuto := filepath.Join(outDirAuto, "index.html")
	if fi, err := os.Stat(indexPathAuto); err != nil || fi.Size() == 0 {
		t.Errorf("index.html was not generated by CLI auto-detection: %v", err)
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
		defer resp.Body.Close()

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
		defer resp.Body.Close()

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
		defer resp.Body.Close()

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
		defer resp.Body.Close()

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
		defer resp.Body.Close()

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
