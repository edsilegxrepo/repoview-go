package render

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/edsilegxrepo/repoview/internal/logic"
	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/util"
)

// TEST STRATEGY:
// Validate the complete rendering pipeline across HTML templates, XML feeds,
// static web asset deployment, atomic file persistence, and path traversal security.
//
// Test coverage includes:
//   - TestTemplateRendering:
//       * RenderIndex: Validates generation of index.html with group summaries and recent updates.
//       * RenderGroup: Validates group.html generation with package listings.
//       * RenderPackage: Validates package.html generation with EVR, changelog, and metadata.
//       * WriteAssets: Validates extraction and writing of layout/ CSS and JS files from embedded FS.
//       * WriteToFile (Atomic Write): Validates successful write via temporary file and atomic rename.
//       * WriteToFile (Path Traversal Protection): Confirms immediate rejection when attempting
//         to write to "../escaped.html" outside the target output directory boundary.

func TestTemplateRendering(t *testing.T) {
	letters := []string{"A", "B", "C"}
	r, err := NewRenderer(t.TempDir(), "", "Test Repo", "1.0.0", letters)
	if err != nil {
		t.Fatalf("failed to create renderer: %v", err)
	}

	pkg1 := &models.Package{
		Name:         "testpkg",
		Version:      "1.0",
		Release:      "1.el9",
		Arch:         "x86_64",
		Summary:      "A test package summary",
		Description:  "Detailed test description for the package.",
		URL:          "https://example.com/testpkg",
		License:      "MIT",
		Vendor:       "Example Inc",
		TimeBuild:    time.Now().Unix(),
		LocationHref: "testpkg-1.0-1.el9.x86_64.rpm",
		SizePackage:  1024 * 1024 * 5,
		Changelog: &models.ChangelogEntry{
			Author:    "Dev <dev@example.com>",
			Date:      time.Now().Unix(),
			Changelog: "- Initial release",
		},
	}
	pkg1.AllVersions = []*models.Package{pkg1}

	group := &logic.GroupData{
		ID:          "test_group",
		Name:        "Applications/Test",
		Description: "A test group description",
		Filename:    "test_group.group.html",
		Packages:    []*models.Package{pkg1},
	}

	groups := []*logic.GroupData{group}
	r.SetGroups(groups)

	// 1. Test RenderIndex
	indexHTML, err := r.RenderIndex(groups, []*models.Package{pkg1}, "http://example.com/repo")
	if err != nil {
		t.Fatalf("RenderIndex failed: %v", err)
	}
	if len(indexHTML) == 0 {
		t.Errorf("RenderIndex returned empty content")
	}

	// 2. Test RenderGroup
	groupHTML, err := r.RenderGroup(group)
	if err != nil {
		t.Fatalf("RenderGroup failed: %v", err)
	}
	if len(groupHTML) == 0 {
		t.Errorf("RenderGroup returned empty content")
	}

	// 3. Test RenderPackage
	pkgHTML, err := r.RenderPackage(pkg1, group)
	if err != nil {
		t.Fatalf("RenderPackage failed: %v", err)
	}
	if len(pkgHTML) == 0 {
		t.Errorf("RenderPackage returned empty content")
	}

	// 3a. Verify default dark theme across templates
	for name, htmlBytes := range map[string][]byte{"index": indexHTML, "group": groupHTML, "package": pkgHTML} {
		content := string(htmlBytes)
		if !strings.Contains(content, `data-theme="dark"`) {
			t.Errorf("expected %s HTML to declare data-theme=\"dark\" default", name)
		}
		if !strings.Contains(content, `localStorage.getItem("repoview-theme") || "dark"`) {
			t.Errorf("expected %s HTML to default to \"dark\" theme in script", name)
		}
	}

	// 3b. Verify repo-hero in index, sidebar brand, and breadcrumb repo titles
	if !strings.Contains(string(indexHTML), `class="repo-hero"`) || !strings.Contains(string(indexHTML), `<h1 class="repo-hero-title">`) {
		t.Errorf("expected repo-hero heading in index HTML")
	}
	if !strings.Contains(string(indexHTML), `<span>RepoView</span>`) {
		t.Errorf("expected generic RepoView in sidebar brand")
	}
	if !strings.Contains(string(groupHTML), `<a href="index.html">Test Repo</a>`) {
		t.Errorf("expected repo title breadcrumb in group HTML")
	}
	if !strings.Contains(string(pkgHTML), `<a href="index.html">Test Repo</a>`) {
		t.Errorf("expected repo title breadcrumb in package HTML")
	}

	// 3c. Test PortalURL rendering
	r.SetPortalURL("../../../index.html")
	portalIndex, err := r.RenderIndex(groups, []*models.Package{pkg1}, "http://example.com/repo")
	if err != nil || !strings.Contains(string(portalIndex), "portal-back-link") || !strings.Contains(string(portalIndex), "../../../index.html") {
		t.Errorf("expected portal-back-link in index HTML with portal URL")
	}

	portalGroup, err := r.RenderGroup(group)
	if err != nil || !strings.Contains(string(portalGroup), "portal-back-link") || !strings.Contains(string(portalGroup), "../../../index.html") {
		t.Errorf("expected portal-back-link in group HTML with portal URL")
	}

	portalPkg, err := r.RenderPackage(pkg1, group)
	if err != nil || !strings.Contains(string(portalPkg), "portal-back-link") || !strings.Contains(string(portalPkg), "../../../index.html") {
		t.Errorf("expected portal-back-link in package HTML with portal URL")
	}

	// 4. Test WriteAssets
	if err := r.WriteAssets(); err != nil {
		t.Fatalf("WriteAssets failed: %v", err)
	}

	layoutDirInfo, err := os.Stat(filepath.Join(r.OutDir, "layout"))
	if err != nil {
		t.Fatalf("failed to stat layout dir: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := layoutDirInfo.Mode().Perm(); perm != util.DefaultDirPerm {
			t.Errorf("expected layout dir to have %04o permissions, got %04o", util.DefaultDirPerm, perm)
		}
	}

	// 5. Test Atomic WriteToFile with permissions check
	testContent := []byte("<!DOCTYPE html><html><body>atomic content</body></html>")
	if err := r.WriteToFile("testpage.html", testContent); err != nil {
		t.Fatalf("WriteToFile failed: %v", err)
	}

	fileInfo, err := os.Stat(filepath.Join(r.OutDir, "testpage.html"))
	if err != nil {
		t.Fatalf("failed to stat written testpage.html: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := fileInfo.Mode().Perm(); perm != util.DefaultFilePerm {
			t.Errorf("expected written file to have %04o permissions, got %04o", util.DefaultFilePerm, perm)
		}
	} else if got, readErr := os.ReadFile(filepath.Join(r.OutDir, "testpage.html")); readErr != nil || !bytes.Equal(got, testContent) {
		t.Errorf("expected written content on Windows, got err=%v len=%d", readErr, len(got))
	}

	// 6. Test Path Traversal Protection
	if err := r.WriteToFile("../escaped.html", testContent); err == nil {
		t.Errorf("expected path traversal write to fail, but it succeeded")
	}
}

func TestRenderer_RSS_And_SearchIndex(t *testing.T) {
	outDir := t.TempDir()
	r, err := NewRenderer(outDir, "", "Test Repo", "1.0", []string{"A", "B"})
	if err != nil {
		t.Fatalf("failed to create renderer: %v", err)
	}

	r.SetRepoMeta("my-repo", "http://example.com/repo")
	r.SetSiblings([]*logic.SiblingRepo{{Name: "extras", RelURL: "../extras", IsActive: false}})

	pkgs := []*models.Package{
		{
			Name:         "nginx",
			Epoch:        "1",
			Version:      "1.20",
			Release:      "1",
			Arch:         "x86_64",
			Summary:      "Web server",
			TimeBuild:    time.Now().Unix(),
			LocationHref: "nginx-1.20-1.rpm",
		},
	}

	// 1. RSS rendering
	rssBytes, err := r.RenderRSS(pkgs, "http://example.com/repo")
	if err != nil {
		t.Fatalf("RenderRSS failed: %v", err)
	}
	if len(rssBytes) == 0 {
		t.Errorf("RenderRSS produced empty bytes")
	}

	// 2. Search Index rendering
	searchBytes, err := r.RenderSearchIndex(pkgs)
	if err != nil {
		t.Fatalf("RenderSearchIndex failed: %v", err)
	}
	if len(searchBytes) == 0 {
		t.Errorf("RenderSearchIndex produced empty bytes")
	}
}

func TestRenderer_CustomTemplates_And_Assets(t *testing.T) {
	tempCustomDir := t.TempDir()
	outDir := t.TempDir()

	// Write custom templates
	customHTML := `{{define "index.html"}}<html><body>Custom Index {{.Repo.Title}}</body></html>{{end}}`
	if err := os.WriteFile(filepath.Join(tempCustomDir, "index.html"), []byte(customHTML), 0o644); err != nil {
		t.Fatalf("failed to write custom html: %v", err)
	}

	customXML := `{{define "rss.xml"}}<rss><channel><title>{{.Repo.Title}}</title></channel></rss>{{end}}`
	if err := os.WriteFile(filepath.Join(tempCustomDir, "rss.xml"), []byte(customXML), 0o644); err != nil {
		t.Fatalf("failed to write custom xml: %v", err)
	}

	// Create custom layout dir
	customLayout := filepath.Join(tempCustomDir, "layout")
	if err := os.MkdirAll(customLayout, 0o755); err != nil {
		t.Fatalf("failed to create custom layout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(customLayout, "custom.css"), []byte("body { color: red; }"), 0o644); err != nil {
		t.Fatalf("failed to write custom css: %v", err)
	}

	r, err := NewRenderer(outDir, tempCustomDir, "Custom Repo", "2.0", []string{"A"})
	if err != nil {
		t.Fatalf("failed to create renderer with custom templates: %v", err)
	}

	// Verify custom index render
	idxBytes, err := r.RenderIndex(nil, nil, "")
	if err != nil {
		t.Fatalf("failed to render custom index: %v", err)
	}
	if !strings.Contains(string(idxBytes), "Custom Index Custom Repo") {
		t.Errorf("custom index output mismatch: %s", string(idxBytes))
	}

	// Verify custom assets copy
	if err := r.WriteAssets(); err != nil {
		t.Fatalf("failed to write custom assets: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "layout", "custom.css")); err != nil {
		t.Errorf("custom.css was not copied to layout dir: %v", err)
	}
}

func TestRenderer_TemplateFunctions(t *testing.T) {
	outDir := t.TempDir()
	r, err := NewRenderer(outDir, "", "Test", "1.0", nil)
	if err != nil {
		t.Fatalf("failed to create renderer: %v", err)
	}

	// Test template execution with compressionSaved and paragraphs
	customTmpl := `{{compressionSaved 500 1000}}|{{compressionSaved 1000 500}}|{{compressionSaved 0 0}}|{{paragraphs "Para 1\n\nPara 2"}}`
	tmpl, err := r.HTMLTmpls.New("test-helpers").Parse(customTmpl)
	if err != nil {
		t.Fatalf("failed to parse helper template: %v", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, nil); err != nil {
		t.Fatalf("failed to execute helper template: %v", err)
	}

	res := buf.String()
	if !strings.Contains(res, "50.0%") {
		t.Errorf("expected 50.0%% in compression savings, got: %s", res)
	}
}

func TestRenderer_DebianRendering(t *testing.T) {
	outDir := t.TempDir()
	r, err := NewRenderer(outDir, "", "Debian Test Repo", "1.0", []string{"N"})
	if err != nil {
		t.Fatalf("failed to create renderer: %v", err)
	}
	r.SetFormat(models.FormatDEB)

	debPkg := &models.Package{
		Format:        models.FormatDEB,
		Name:          "nginx",
		Version:       "1.24.0",
		Release:       "2",
		Arch:          "amd64",
		Summary:       "small, powerful, scalable web/proxy server",
		Description:   "Detailed Debian package description.",
		Maintainer:    "Debian Nginx Maintainers <pkg-nginx-maintainers@example.com>",
		Section:       "httpd",
		SizePackage:   500000,
		InstalledSize: 1500000,
		LocationHref:  "pool/main/n/nginx/nginx_1.24.0-2_amd64.deb",
		Dependencies: &models.PackageDependencies{
			Requires: []*models.DependencyEntry{
				{Name: "libc6", Flags: ">=", Version: "2.34"},
			},
			Recommends: []*models.DependencyEntry{
				{Name: "logrotate"},
			},
		},
		Details: &models.PackageDetails{
			Scriptlets: &models.PackageScriptlets{
				PostIn: "/usr/sbin/service nginx start",
			},
		},
	}
	debPkg.AllVersions = []*models.Package{debPkg}

	group := &logic.GroupData{
		ID:       "httpd",
		Name:     "httpd",
		Filename: "httpd.group.html",
		Packages: []*models.Package{debPkg},
	}

	// 1. RenderPackage verification
	pkgHTML, err := r.RenderPackage(debPkg, group)
	if err != nil {
		t.Fatalf("RenderPackage failed for Debian: %v", err)
	}
	pkgStr := string(pkgHTML)

	if !strings.Contains(pkgStr, "sudo apt install nginx") {
		t.Errorf("expected 'sudo apt install nginx' in package HTML")
	}
	if !strings.Contains(pkgStr, "data-tool=\"apt\"") {
		t.Errorf("expected apt install tab in package HTML")
	}
	if !strings.Contains(pkgStr, "data-tool=\"dpkg\"") {
		t.Errorf("expected dpkg install tab in package HTML")
	}
	if !strings.Contains(pkgStr, "Debian Nginx Maintainers") {
		t.Errorf("expected maintainer in package HTML")
	}
	if !strings.Contains(pkgStr, "Depends (1)") {
		t.Errorf("expected 'Depends (1)' tab in package HTML")
	}
	if !strings.Contains(pkgStr, "Recommends (1)") {
		t.Errorf("expected 'Recommends (1)' tab in package HTML")
	}
	if !strings.Contains(pkgStr, "postinst maintainer script:") {
		t.Errorf("expected postinst maintainer script header in package HTML")
	}
	if !strings.Contains(pkgStr, "nginx_1.24.0-2_amd64.deb") {
		t.Errorf("expected debian archive filename in package HTML")
	}

	// 2. RenderIndex verification
	idxHTML, err := r.RenderIndex([]*logic.GroupData{group}, []*models.Package{debPkg}, "http://example.com/debian")
	if err != nil {
		t.Fatalf("RenderIndex failed for Debian: %v", err)
	}
	idxStr := string(idxHTML)

	if !strings.Contains(idxStr, "Types: deb") {
		t.Errorf("expected 'Types: deb' snippet in index HTML")
	}
	if !strings.Contains(idxStr, ".sources") {
		t.Errorf("expected '.sources' in index HTML")
	}
}

func TestRenderer_PackageBaseURL(t *testing.T) {
	tempDir := t.TempDir()
	r, err := NewRenderer(tempDir, "", "Test Repo", "1.0.0", []string{"N"})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	r.SetPackageBaseURL("https://mirrors.example.com/repos/el9/base/x86_64/")

	pkg := &models.Package{
		Name:         "nginx",
		Version:      "1.20",
		Release:      "1.el9",
		Arch:         "x86_64",
		LocationHref: "Packages/n/nginx-1.20-1.el9.x86_64.rpm",
		Format:       models.FormatRPM,
	}
	pkg.AllVersions = []*models.Package{pkg}

	group := &logic.GroupData{
		ID:       "web",
		Name:     "Web Servers",
		Packages: []*models.Package{pkg},
	}

	pkgHTML, err := r.RenderPackage(pkg, group)
	if err != nil {
		t.Fatalf("RenderPackage failed: %v", err)
	}
	pkgStr := string(pkgHTML)

	expectedDownload := `href="https://mirrors.example.com/repos/el9/base/x86_64/Packages/n/nginx-1.20-1.el9.x86_64.rpm"`
	if !strings.Contains(pkgStr, expectedDownload) {
		t.Errorf("expected package download link %q, got HTML:\n%s", expectedDownload, pkgStr)
	}

	expectedDataHref := `data-href="https://mirrors.example.com/repos/el9/base/x86_64/Packages/n/nginx-1.20-1.el9.x86_64.rpm"`
	if !strings.Contains(pkgStr, expectedDataHref) {
		t.Errorf("expected install tab data-href %q, got HTML:\n%s", expectedDataHref, pkgStr)
	}

	// RSS feed
	rssBytes, err := r.RenderRSS([]*models.Package{pkg}, "https://portal.example.com")
	if err != nil {
		t.Fatalf("RenderRSS failed: %v", err)
	}
	rssStr := string(rssBytes)
	expectedRSSLink := `<a href="https://mirrors.example.com/repos/el9/base/x86_64/Packages/n/nginx-1.20-1.el9.x86_64.rpm">`
	if !strings.Contains(rssStr, expectedRSSLink) {
		t.Errorf("expected RSS package download link %q, got:\n%s", expectedRSSLink, rssStr)
	}
}

func TestRenderer_RSS_XMLAndCDATAInjectionDefense(t *testing.T) {
	outDir := t.TempDir()
	r, err := NewRenderer(outDir, "", `Repo <Title> & "Quotes"`, "1.0.0", []string{"A"})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	evilPkg := &models.Package{
		Name:         "evilpkg",
		Version:      "1.0",
		Release:      "1",
		Arch:         "x86_64",
		Summary:      `Summary ]]> <script>alert(1)</script>`,
		Description:  `Desc ]]> <injected>bad</injected>`,
		TimeBuild:    time.Now().Unix(),
		LocationHref: `javascript:alert(1)`,
		Changelog: &models.ChangelogEntry{
			Author:    `Hacker <h@x.org> ]]>`,
			Date:      time.Now().Unix(),
			Changelog: `- Fix ]]> <img src=x onerror=alert(1)>`,
		},
	}
	evilPkg.AllVersions = []*models.Package{evilPkg}

	rssBytes, err := r.RenderRSS([]*models.Package{evilPkg}, "javascript:alert(2)")
	if err != nil {
		t.Fatalf("RenderRSS failed: %v", err)
	}
	rssStr := string(rssBytes)

	// Must parse cleanly as valid XML with zero injected tags
	var parsed struct {
		XMLName xml.Name `xml:"rss"`
		Channel struct {
			Title string `xml:"title"`
			Link  string `xml:"link"`
			Items []struct {
				Title       string `xml:"title"`
				Description string `xml:"description"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(rssBytes, &parsed); err != nil {
		t.Fatalf("RSS output is not valid XML after adversarial payload: %v\nXML:\n%s", err, rssStr)
	}
	if parsed.Channel.Title != `Repo <Title> & "Quotes"` {
		t.Errorf("unexpected decoded channel title: %q", parsed.Channel.Title)
	}
	if strings.Contains(rssStr, "<script>") || strings.Contains(rssStr, "<injected>") {
		t.Errorf("unescaped HTML/XML tags leaked into RSS output:\n%s", rssStr)
	}
	if strings.Contains(rssStr, "javascript:") {
		t.Errorf("unsafe javascript: URI leaked into RSS output:\n%s", rssStr)
	}
}

func TestRenderer_WriteSymlinkRejection(t *testing.T) {
	outDir := t.TempDir()
	outsideDir := t.TempDir()
	r, err := NewRenderer(outDir, "", "Test Repo", "1.0.0", []string{"A"})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	outsideFile := filepath.Join(outsideDir, "victim.html")
	if err := os.WriteFile(outsideFile, []byte("original"), 0o600); err != nil {
		t.Fatalf("failed to create outside file: %v", err)
	}

	// 1. WriteToFile via symlinked subdirectory escaping outDir
	linkDir := filepath.Join(outDir, "sub")
	if err := os.Symlink(outsideDir, linkDir); err != nil {
		t.Skipf("skipping symlink rejection test (symlinks unsupported): %v", err)
	}
	if err := r.WriteToFile("sub/victim.html", []byte("pwned")); err == nil {
		t.Errorf("expected WriteToFile through symlinked subdir to fail")
	}

	// 2. WriteAssets with pre-placed symlinked layout directory
	layoutLink := filepath.Join(outDir, "layout")
	if err := os.Symlink(outsideDir, layoutLink); err != nil {
		t.Fatalf("failed to create layout symlink: %v", err)
	}
	if err := r.WriteAssets(); err == nil {
		t.Errorf("expected WriteAssets into symlinked layout dir to fail")
	}

	got, _ := os.ReadFile(outsideFile)
	if string(got) != "original" {
		t.Errorf("outside target file was modified through symlink: %q", string(got))
	}
}

func TestRenderer_SnippetAndCommandHardening(t *testing.T) {
	outDir := t.TempDir()
	r, err := NewRenderer(outDir, "", "Safe Title\ngpgcheck=0", "1.0.0", []string{"P"})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}
	r.SetRepoMeta("my\nrepo", "https://repo.example.com/el9\ngpgcheck=0")
	r.SetGPGKey("https://repo.example.com/RPM-GPG-KEY\nevil=1")

	idxBytes, err := r.RenderIndex(nil, nil, "")
	if err != nil {
		t.Fatalf("RenderIndex failed: %v", err)
	}
	idxStr := string(idxBytes)
	wantSnippet := "[myrepo]\nname=Safe Titlegpgcheck=0\nbaseurl=https://repo.example.com/el9gpgcheck=0\nenabled=1\ngpgcheck=1\ngpgkey=https://repo.example.com/RPM-GPG-KEYevil=1"
	if !strings.Contains(idxStr, wantSnippet) {
		t.Errorf("expected sanitized .repo snippet %q in index.html, got:\n%s", wantSnippet, idxStr)
	}

	evilPkg := &models.Package{
		Name:    "pkg;rm -rf /",
		Version: "1.0",
		Release: "1",
		Arch:    "x86_64",
		Details: &models.PackageDetails{},
	}
	evilPkg.AllVersions = []*models.Package{evilPkg}
	pkgBytes, err := r.RenderPackage(evilPkg, &logic.GroupData{Name: "System", Filename: "sys.group.html"})
	if err != nil {
		t.Fatalf("RenderPackage failed: %v", err)
	}
	pkgStr := string(pkgBytes)
	if !strings.Contains(pkgStr, "sudo dnf install &#39;pkg;rm -rf /&#39;") {
		t.Errorf("expected shell-quoted install command in package.html, got:\n%s", pkgStr)
	}
}

type mockAssetState struct {
	hashes     map[string]string
	writeCount int
}

func (m *mockAssetState) CheckChanged(filename string, content []byte) (string, bool) {
	h := string(content) // simple test digest sufficient for unit verification
	if prev, ok := m.hashes[filename]; ok && prev == h {
		return h, false
	}
	return h, true
}

func (m *mockAssetState) MarkWritten(filename, hash string) {
	m.hashes[filename] = hash
	m.writeCount++
}

func TestRenderer_WriteAssetsWithState_SkipsUnchanged(t *testing.T) {
	outDir := t.TempDir()
	r, err := NewRenderer(outDir, "", "Repo", "1.0.0", []string{"A"})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	st := &mockAssetState{hashes: make(map[string]string)}
	keys1, err := r.WriteAssetsWithState(st, false)
	if err != nil || len(keys1) == 0 {
		t.Fatalf("first WriteAssetsWithState failed: keys=%v err=%v", keys1, err)
	}
	initialWrites := st.writeCount

	// Second call with force=false must skip writing unchanged assets
	if _, err := r.WriteAssetsWithState(st, false); err != nil {
		t.Fatalf("second WriteAssetsWithState failed: %v", err)
	}
	if st.writeCount != initialWrites {
		t.Errorf("expected unchanged assets to be skipped, got writeCount=%d want=%d", st.writeCount, initialWrites)
	}

	// Third call with force=true must re-write assets
	if _, err := r.WriteAssetsWithState(st, true); err != nil {
		t.Fatalf("forced WriteAssetsWithState failed: %v", err)
	}
	if st.writeCount == initialWrites {
		t.Errorf("expected force=true to write assets, writeCount stayed at %d", st.writeCount)
	}
}

func TestRenderer_SettersSearchAndAtomicLocalSink(t *testing.T) {
	outDir := t.TempDir()
	r, err := NewRenderer(outDir, "", "Phase5 Repo", "1.2.3", []string{"A"})
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	sink := &AtomicLocalSink{RootDir: outDir}
	r.SetSink(sink)
	r.SetTotalPackages(42)
	r.SetRevision("rev-99")
	r.SetDescription("Custom repo description")

	pkgs := []*models.Package{
		{
			Name:     "alpha",
			Version:  "1.0",
			Release:  "1",
			Arch:     "x86_64",
			Summary:  "Alpha package",
			RpmGroup: "System/Base",
			License:  "MIT",
			Tags:     []string{"implemented-in::go", "role::program"},
			Advisories: []models.Advisory{
				{ID: "ELSA-2026:1000", CVEs: []string{"CVE-2026-1000"}},
			},
			Dependencies: &models.PackageDependencies{
				Provides: []*models.DependencyEntry{{Name: "alpha-cap"}, {Name: "libalpha.so"}},
			},
		},
	}
	searchJSON, err := r.RenderSearchIndex(pkgs)
	if err != nil {
		t.Fatalf("RenderSearchIndex failed: %v", err)
	}
	if !strings.Contains(string(searchJSON), "alpha-cap") || !strings.Contains(string(searchJSON), "CVE-2026-1000") {
		t.Errorf("expected provides and CVEs in search.json, got %s", string(searchJSON))
	}

	if err := r.WriteToFile("sub/test.html", []byte("<html>ok</html>")); err != nil {
		t.Fatalf("WriteToFile via AtomicLocalSink failed: %v", err)
	}
	if !sink.Exists("sub/test.html") {
		t.Errorf("expected AtomicLocalSink.Exists(\"sub/test.html\") to be true")
	}
	if err := sink.RemoveFile("sub/test.html"); err != nil {
		t.Fatalf("RemoveFile failed: %v", err)
	}
	if sink.Exists("sub/test.html") {
		t.Errorf("expected sub/test.html removed")
	}
	if err := sink.WriteFile("../escape.html", []byte("bad")); err == nil {
		t.Errorf("expected traversal write error")
	}
	if err := sink.RemoveFile("../escape.html"); err == nil {
		t.Errorf("expected traversal remove error")
	}
	if sink.Exists("../escape.html") {
		t.Errorf("expected Exists false on traversal path")
	}
}

