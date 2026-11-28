//go:build integration

package integration

// OBJECTIVES:
// Provide shared subprocess binary compilation (TestMain), ephemeral loopback HTTP serving,
// recursive link crawling, JSON/RSS validation, and exact SHA-256 package download helpers.
//
// CORE COMPONENTS:
//   - TestMain: Builds one temporary repoview binary shared across all integration subprocess tests.
//   - runRepoview / startTestMuxHTTPServer: Subprocess execution and dual repo/UI loopback serving.
//   - crawlAndVerifySite: Recursive HTML link checker validating CSS/JS/JSON/RSS and snippet URLs.
//
// DATA FLOW:
//   CLI invocation -> output dir -> net/http loopback FileServer -> HTTP link/schema/checksum verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/portal"
)

// e2eBinaryPath stores the path to the repoview executable built once in TestMain.
var e2eBinaryPath string

// TestMain builds the repoview CLI binary once in an isolated OS temp directory
// and cleans it up after the integration suite completes.
func TestMain(m *testing.M) {
	tmpBinDir, err := os.MkdirTemp("", "repoview-e2e-bin-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp binary dir: %v\n", err)
		os.Exit(1)
	}

	binName := "repoview"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	e2eBinaryPath = filepath.Join(tmpBinDir, binName)

	buildCmd := exec.Command("go", "build", "-trimpath", "-o", e2eBinaryPath, "../../cmd/repoview")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build repoview binary: %v\n%s\n", err, string(out))
		_ = os.RemoveAll(tmpBinDir)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(tmpBinDir)
	os.Exit(code)
}

// runRepoview executes the compiled repoview binary inside an isolated working directory,
// returning separated stdout, stderr, and the process exit code.
func runRepoview(t *testing.T, workDir string, args ...string) (string, string, int) {
	t.Helper()
	if workDir == "" {
		workDir = t.TempDir()
	}

	cmd := exec.Command(e2eBinaryPath, args...)
	cmd.Dir = workDir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("failed to start repoview %v: %v", args, err)
		}
	}
	return stdout.String(), stderr.String(), exitCode
}

// startTestHTTPServer binds an ephemeral 127.0.0.1 HTTP file server for docRoot with bounded timeouts.
func startTestHTTPServer(t *testing.T, docRoot string) (string, *http.Client) {
	t.Helper()
	return startTestMuxHTTPServer(t, http.FileServer(http.Dir(docRoot)))
}

// startTestMuxHTTPServer binds an ephemeral 127.0.0.1 HTTP server for any custom handler (e.g., mirror + portal).
func startTestMuxHTTPServer(t *testing.T, handler http.Handler) (string, *http.Client) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind ephemeral HTTP listener: %v", err)
	}

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	client := &http.Client{Timeout: 5 * time.Second}
	return "http://" + listener.Addr().String(), client
}

// makeReadOnlyMirror recursively applies read-only permissions (0555 dirs / 0444 files)
// and registers cleanup restoring writable modes before t.TempDir() teardown.
func makeReadOnlyMirror(t *testing.T, dir string) {
	t.Helper()

	var dirs []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		return os.Chmod(path, 0o444)
	})
	if err != nil {
		t.Fatalf("makeReadOnlyMirror failed marking files read-only under %s: %v", dir, err)
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], 0o555); err != nil {
			t.Fatalf("makeReadOnlyMirror failed marking dir read-only %s: %v", dirs[i], err)
		}
	}

	t.Cleanup(func() {
		var cleanDirs []string
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				// On POSIX, ensure directory is readable/writable if visited early
				_ = os.Chmod(path, 0o755)
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if d.IsDir() {
				_ = os.Chmod(path, 0o755)
				cleanDirs = append(cleanDirs, path)
				return nil
			}
			_ = os.Chmod(path, 0o644)
			return nil
		})
		for i := len(cleanDirs) - 1; i >= 0; i-- {
			_ = os.Chmod(cleanDirs[i], 0o755)
		}
	})
}

// CrawlExpectations configures site-wide HTTP verification checks run by crawlAndVerifySite.
type CrawlExpectations struct {
	RequireKnownArch      bool              // Fails if HTML renders data-arch="unknown"
	ExpectRepoView        bool              // Verifies single-repo UI markers, search.json, latest-feed.xml, repoview.json
	ExpectPortal          bool              // Verifies portal UI markers, portal-feed.xml, portal-search.json
	ExpectPinned          bool              // Requires .repo-card.pinned and #pinned-section
	ExpectFormats         []string          // Expected format filter values in portal HTML
	ForbidFormats         []string          // Format filter values that must NOT exist in portal HTML
	VerifyRepoSnippets    bool              // Parses emitted portal data-config snippets and verifies HTTP 200 on repo metadata
	VerifyPackageSnippets bool              // Parses #repoSnippetCode in single-repo index.html and verifies HTTP 200
	CheckPortalBackLink   bool              // Requires portal-back-link anchor resolving 200 OK
	MinHTMLPages          int               // Minimum crawled HTML pages
	ExpectedDownloads     map[string]string // URL path -> expected lowercase SHA-256 hex digest
	RequiredSubstrings    []string          // Strings required on the landing page (index.html)
	ForbiddenSubstrings   []string          // Strings forbidden on the landing page (index.html)
}

var (
	reScriptBlock = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	reAttrHref    = regexp.MustCompile(`(?i)\b(?:href|src)="([^"]+)"`)
	reDataConfig  = regexp.MustCompile(`data-config="([^"]+)"`)
	reSnippetCode = regexp.MustCompile(`(?s)<code id="repoSnippetCode"[^>]*>(.*?)</code>`)
	rePortalBack  = regexp.MustCompile(`<a href="([^"]+)" class="portal-back-link"`)
)

// crawlAndVerifySite crawls HTML pages reachable from baseURL + "index.html",
// validates UI markers, JSON schemas, RSS 2.0 XML feeds, package downloads, and client config URLs.
func crawlAndVerifySite(t *testing.T, client *http.Client, baseURL string, opts CrawlExpectations) map[string]string {
	t.Helper()

	baseURL = strings.TrimRight(baseURL, "/")
	startURL := baseURL + "/index.html"
	parsedBase, err := url.Parse(baseURL + "/")
	if err != nil {
		t.Fatalf("invalid baseURL %q: %v", baseURL, err)
	}

	visited := make(map[string]bool)
	pages := make(map[string]string)
	queue := []string{startURL}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		if visited[curr] {
			continue
		}
		visited[curr] = true

		bodyBytes, contentType := httpGetBytes(t, client, curr)
		if len(bodyBytes) == 0 {
			t.Errorf("empty HTTP 200 body for %s", curr)
			continue
		}

		isHTML := strings.Contains(strings.ToLower(contentType), "text/html") || strings.HasSuffix(strings.ToLower(curr), ".html")
		if !isHTML {
			continue
		}

		htmlStr := string(bodyBytes)
		pages[curr] = htmlStr

		if opts.RequireKnownArch && strings.Contains(htmlStr, `data-arch="unknown"`) {
			t.Errorf("unexpected data-arch=\"unknown\" rendered on %s", curr)
		}

		currParsed, parseErr := url.Parse(curr)
		if parseErr != nil {
			t.Errorf("invalid crawled URL %q: %v", curr, parseErr)
			continue
		}

		scrubbedHTML := reScriptBlock.ReplaceAllString(htmlStr, "")
		matches := reAttrHref.FindAllStringSubmatch(scrubbedHTML, -1)
		for _, m := range matches {
			rawRef := html.UnescapeString(strings.TrimSpace(m[1]))
			if rawRef == "" ||
				strings.Contains(rawRef, "${") ||
				strings.HasPrefix(rawRef, "#") ||
				strings.HasPrefix(rawRef, "mailto:") ||
				strings.HasPrefix(rawRef, "javascript:") ||
				strings.HasPrefix(rawRef, "data:") ||
				strings.HasPrefix(rawRef, "http://") ||
				strings.HasPrefix(rawRef, "https://") {
				continue
			}

			// Strip query/fragment before resolving
			if idx := strings.IndexAny(rawRef, "#?"); idx != -1 {
				rawRef = rawRef[:idx]
			}
			if rawRef == "" {
				continue
			}
			lowerRef := strings.ToLower(rawRef)
			if strings.HasSuffix(lowerRef, ".rpm") || strings.HasSuffix(lowerRef, ".deb") || strings.HasSuffix(lowerRef, ".ddeb") {
				continue
			}

			relParsed, err := url.Parse(rawRef)
			if err != nil {
				t.Errorf("invalid relative link %q on %s: %v", rawRef, curr, err)
				continue
			}
			resolved := currParsed.ResolveReference(relParsed)
			// Only verify targets hosted on our local test server
			if resolved.Host != parsedBase.Host {
				continue
			}
			targetURL := resolved.String()
			if !visited[targetURL] {
				queue = append(queue, targetURL)
			}
		}
	}

	landingHTML, ok := pages[startURL]
	if !ok {
		t.Fatalf("landing page %s was not fetched", startURL)
	}

	if opts.MinHTMLPages > 0 && len(pages) < opts.MinHTMLPages {
		t.Errorf("expected at least %d crawled HTML pages, got %d", opts.MinHTMLPages, len(pages))
	}

	for _, reqSub := range opts.RequiredSubstrings {
		if !strings.Contains(landingHTML, reqSub) {
			t.Errorf("landing page missing required substring %q", reqSub)
		}
	}
	for _, forbidSub := range opts.ForbiddenSubstrings {
		if strings.Contains(landingHTML, forbidSub) {
			t.Errorf("landing page unexpectedly contains %q", forbidSub)
		}
	}

	if opts.ExpectRepoView {
		verifyRepoViewSite(t, client, baseURL, landingHTML, opts)
	}
	if opts.ExpectPortal {
		verifyPortalSite(t, client, baseURL, landingHTML, opts)
	}
	for dlPath, wantSHA := range opts.ExpectedDownloads {
		dlURL := baseURL + "/" + strings.TrimLeft(dlPath, "/")
		data, _ := httpGetBytes(t, client, dlURL)
		sum := sha256.Sum256(data)
		gotSHA := hex.EncodeToString(sum[:])
		if wantSHA != "" && gotSHA != wantSHA {
			t.Errorf("download %s SHA-256 mismatch: want %s, got %s", dlURL, wantSHA, gotSHA)
		}
	}

	return pages
}

func verifyRepoViewSite(t *testing.T, client *http.Client, baseURL, landingHTML string, opts CrawlExpectations) {
	t.Helper()

	for _, marker := range []string{
		`id="pkgSearchClear"`,
		`id="groupTreeFilter"`,
		`id="sidebarToggleBtn"`,
		`Last Built:`,
	} {
		if !strings.Contains(landingHTML, marker) {
			t.Errorf("repo index.html missing expected UI marker %q", marker)
		}
	}

	if opts.CheckPortalBackLink {
		m := rePortalBack.FindStringSubmatch(landingHTML)
		if len(m) < 2 {
			t.Errorf("expected portal-back-link anchor in repo index.html")
		} else {
			landingParsed, _ := url.Parse(baseURL + "/index.html")
			refParsed, err := url.Parse(html.UnescapeString(m[1]))
			if err != nil {
				t.Errorf("invalid portal-back-link %q: %v", m[1], err)
			} else {
				backURL := landingParsed.ResolveReference(refParsed).String()
				backBody, _ := httpGetBytes(t, client, backURL)
				if !strings.Contains(string(backBody), "RepoView-Portal") {
					t.Errorf("portal-back-link target %s did not serve RepoView-Portal HTML", backURL)
				}
			}
		}
	}

	// Verify extended search.json schema
	searchBytes, _ := httpGetBytes(t, client, baseURL+"/search.json")
	var sIdx models.SearchIndex
	if err := json.Unmarshal(searchBytes, &sIdx); err != nil {
		t.Fatalf("failed to parse %s/search.json: %v", baseURL, err)
	}
	wantSchema := []string{"n", "v", "a", "s", "f", "g", "l", "p", "t"}
	if !slices.Equal(sIdx.Schema, wantSchema) {
		t.Errorf("search.json schema mismatch: want %v, got %v", wantSchema, sIdx.Schema)
	}
	for i, row := range sIdx.Data {
		if len(row) != len(wantSchema) {
			t.Errorf("search.json row %d width mismatch: want %d, got %d (%v)", i, len(wantSchema), len(row), row)
			continue
		}
		href := strings.TrimSpace(row[4])
		if href == "" {
			t.Errorf("search.json row %d has empty href", i)
		} else {
			_, _ = httpGetBytes(t, client, baseURL+"/"+strings.TrimLeft(href, "/"))
		}
	}

	// Verify repoview.json
	descBytes, _ := httpGetBytes(t, client, baseURL+"/repoview.json")
	var desc models.RepoDescriptor
	if err := json.Unmarshal(descBytes, &desc); err != nil {
		t.Errorf("failed to parse repoview.json: %v", err)
	}

	// Verify latest-feed.xml RSS 2.0
	verifyRSSFeed(t, client, baseURL+"/latest-feed.xml")

	// Verify embedded config snippet resolves metadata endpoint over HTTP
	if opts.VerifyPackageSnippets {
		m := reSnippetCode.FindStringSubmatch(landingHTML)
		if len(m) < 2 {
			t.Errorf("expected #repoSnippetCode in index.html")
		} else {
			snippet := html.UnescapeString(m[1])
			verifyClientConfigSnippetHTTP(t, client, baseURL, snippet)
		}
	}
}

func verifyPortalSite(t *testing.T, client *http.Client, baseURL, landingHTML string, opts CrawlExpectations) {
	t.Helper()

	for _, marker := range []string{
		`RepoView-Portal`,
		`id="catalog-sort"`,
		`id="btn-reset-filters"`,
		`id="portal-search-mode"`,
	} {
		if !strings.Contains(landingHTML, marker) {
			t.Errorf("portal index.html missing expected UI marker %q", marker)
		}
	}

	if opts.ExpectPinned {
		for _, marker := range []string{`id="pinned-section"`, `repo-card pinned`} {
			if !strings.Contains(landingHTML, marker) {
				t.Errorf("portal index.html missing pinned marker %q", marker)
			}
		}
	}

	for _, fmtName := range opts.ExpectFormats {
		wantAttr := fmt.Sprintf(`data-filter-format="%s"`, fmtName)
		if !strings.Contains(landingHTML, wantAttr) {
			t.Errorf("portal filter bar missing expected format pill %q", fmtName)
		}
	}
	for _, fmtName := range opts.ForbidFormats {
		forbidAttr := fmt.Sprintf(`data-filter-format="%s"`, fmtName)
		if strings.Contains(landingHTML, forbidAttr) {
			t.Errorf("portal filter bar unexpectedly contains forbidden format pill %q", fmtName)
		}
	}

	// Verify portal-feed.xml RSS 2.0
	verifyRSSFeed(t, client, baseURL+"/portal-feed.xml")

	// Verify portal-search.json schema & child links
	psBytes, _ := httpGetBytes(t, client, baseURL+"/portal-search.json")
	var psIdx portal.PortalSearchIndex
	if err := json.Unmarshal(psBytes, &psIdx); err != nil {
		t.Fatalf("failed to parse portal-search.json: %v", err)
	}
	wantPSchema := []string{"ri", "n", "v", "a", "s", "f", "l", "p", "c"}
	if !slices.Equal(psIdx.Schema, wantPSchema) {
		t.Errorf("portal-search.json schema mismatch: want %v, got %v", wantPSchema, psIdx.Schema)
	}
	baseParsed, _ := url.Parse(baseURL + "/")
	for i, row := range psIdx.Data {
		if len(row) != len(wantPSchema) {
			t.Errorf("portal-search.json row %d width mismatch: want %d, got %d", i, len(wantPSchema), len(row))
			continue
		}
		ri, err := strconv.Atoi(row[0])
		if err != nil || ri < 0 || ri >= len(psIdx.Repos) {
			t.Errorf("portal-search.json row %d invalid repo index %q", i, row[0])
			continue
		}
		baseHref := strings.TrimSuffix(psIdx.Repos[ri].BaseHref, "/")
		targetRef := baseHref + "/" + strings.TrimLeft(row[5], "/")
		relParsed, err := url.Parse(targetRef)
		if err != nil {
			t.Errorf("portal-search.json invalid link %q: %v", targetRef, err)
			continue
		}
		resolved := baseParsed.ResolveReference(relParsed).String()
		_, _ = httpGetBytes(t, client, resolved)
	}

	if opts.VerifyRepoSnippets {
		matches := reDataConfig.FindAllStringSubmatch(landingHTML, -1)
		if len(matches) == 0 {
			t.Errorf("expected data-config snippets on portal cards")
		}
		seen := make(map[string]bool)
		for _, m := range matches {
			snippet := html.UnescapeString(m[1])
			if seen[snippet] {
				continue
			}
			seen[snippet] = true
			verifyClientConfigSnippetHTTP(t, client, baseURL, snippet)
		}
	}
}

func verifyClientConfigSnippetHTTP(t *testing.T, client *http.Client, fallbackBaseURL, snippet string) {
	t.Helper()

	if strings.Contains(snippet, "baseurl=") {
		for _, line := range strings.Split(snippet, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "baseurl=") {
				repoURL := strings.TrimSuffix(strings.TrimPrefix(line, "baseurl="), "/")
				target := resolveTestURL(fallbackBaseURL, repoURL+"/repodata/repomd.xml")
				if target != "" {
					_, _ = httpGetBytes(t, client, target)
				}
				return
			}
		}
	}

	if strings.Contains(snippet, "Types: deb") {
		var uri, suite string
		for _, line := range strings.Split(snippet, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "URIs:") {
				uri = strings.TrimSpace(strings.TrimPrefix(line, "URIs:"))
			} else if strings.HasPrefix(line, "Suites:") {
				suite = strings.TrimSpace(strings.TrimPrefix(line, "Suites:"))
			}
		}
		uri = strings.TrimSuffix(uri, "/")
		if suite == "./" || suite == "" {
			// Flat repo: try Release or Packages(.gz/.zst/.bz2)
			for _, candidate := range []string{"Release", "InRelease", "Packages", "Packages.gz", "Packages.zst", "Packages.bz2"} {
				target := resolveTestURL(fallbackBaseURL, uri+"/"+candidate)
				if target != "" && tryHTTPHeadOrGet(client, target) {
					return
				}
			}
			t.Errorf("no Release or Packages reachable for flat DEB snippet URIs=%q Suites=%q", uri, suite)
			return
		}
		// dists/ suite repo: check Release or InRelease
		for _, candidate := range []string{"Release", "InRelease"} {
			target := resolveTestURL(fallbackBaseURL, uri+"/dists/"+suite+"/"+candidate)
			if target != "" && tryHTTPHeadOrGet(client, target) {
				return
			}
		}
		t.Errorf("no Release or InRelease reachable for DEB suite snippet URIs=%q Suites=%q", uri, suite)
	}
}

func resolveTestURL(fallbackBaseURL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://127.0.0.1") || strings.HasPrefix(raw, "http://localhost") {
		return raw
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		// Map path portion to ephemeral fallbackBaseURL if test configured a vanity --repo-url
		if u, err := url.Parse(raw); err == nil {
			return strings.TrimRight(fallbackBaseURL, "/") + "/" + strings.TrimLeft(u.Path, "/")
		}
		return ""
	}
	base, err := url.Parse(strings.TrimRight(fallbackBaseURL, "/") + "/")
	if err != nil {
		return ""
	}
	rel, err := url.Parse(strings.TrimLeft(raw, "./"))
	if err != nil {
		return ""
	}
	return base.ResolveReference(rel).String()
}

func tryHTTPHeadOrGet(client *http.Client, targetURL string) bool {
	resp, err := client.Get(targetURL)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func verifyRSSFeed(t *testing.T, client *http.Client, feedURL string) {
	t.Helper()

	feedBytes, _ := httpGetBytes(t, client, feedURL)
	var feed struct {
		XMLName xml.Name `xml:"rss"`
		Version string   `xml:"version,attr"`
		Channel struct {
			Title string `xml:"title"`
			Items []struct {
				Title string `xml:"title"`
				Link  string `xml:"link"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(feedBytes, &feed); err != nil {
		t.Fatalf("failed to unmarshal RSS XML from %s: %v", feedURL, err)
	}
	if feed.XMLName.Local != "rss" {
		t.Errorf("expected <rss> root element on %s, got <%s>", feedURL, feed.XMLName.Local)
	}
	if feed.Channel.Title == "" {
		t.Errorf("RSS feed %s has empty channel title", feedURL)
	}
	for i, item := range feed.Channel.Items {
		if item.Link == "" {
			t.Errorf("RSS item %d in %s has empty <link>", i, feedURL)
			continue
		}
		if !strings.HasPrefix(item.Link, "http://") && !strings.HasPrefix(item.Link, "https://") {
			feedParsed, _ := url.Parse(feedURL)
			relParsed, err := url.Parse(item.Link)
			if err != nil {
				t.Errorf("RSS item %d invalid relative link %q: %v", i, item.Link, err)
			} else {
				_, _ = httpGetBytes(t, client, feedParsed.ResolveReference(relParsed).String())
			}
		}
	}
}

func httpGetBytes(t *testing.T, client *http.Client, targetURL string) ([]byte, string) {
	t.Helper()

	resp, err := client.Get(targetURL)
	if err != nil {
		t.Fatalf("HTTP GET %s failed: %v", targetURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP GET %s returned status %d, want 200", targetURL, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed reading body from %s: %v", targetURL, err)
	}
	return data, resp.Header.Get("Content-Type")
}
