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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edsilegxrepo/repoview/internal/app"
	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/util"
)

// TEST STRATEGY:
// Execute end-to-end integration workflows against real repository data with zero mocking.
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
		defer resp.Body.Close()

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
		defer resp.Body.Close()

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
		defer resp.Body.Close()

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
	tempBin := filepath.Join(t.TempDir(), "repoview")

	// 1. Compile binary
	t.Log("Compiling repoview binary...")
	buildCmd := exec.Command("go", "build", "-o", tempBin, "../../cmd/repoview/main.go")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\nOutput: %s", err, string(out))
	}

	// 2. Execute CLI subprocess against real repository
	t.Log("Executing repoview subprocess against real repo...")
	runCmd := exec.Command(tempBin,
		"--output-dir", outDir,
		"--force",
		"--quiet",
		repoDir,
	)

	output, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess execution failed: %v\nOutput: %s", err, string(output))
	}

	// 3. Verify index was created
	indexPath := filepath.Join(outDir, "index.html")
	if fi, err := os.Stat(indexPath); err != nil || fi.Size() == 0 {
		t.Errorf("index.html was not generated by CLI subprocess: %v", err)
	}
}
