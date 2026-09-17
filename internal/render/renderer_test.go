package render

import (
	"bytes"
	"os"
	"path/filepath"
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

	// 4. Test WriteAssets
	if err := r.WriteAssets(); err != nil {
		t.Fatalf("WriteAssets failed: %v", err)
	}

	layoutDirInfo, err := os.Stat(filepath.Join(r.OutDir, "layout"))
	if err != nil {
		t.Fatalf("failed to stat layout dir: %v", err)
	}
	if perm := layoutDirInfo.Mode().Perm(); perm != util.DefaultDirPerm {
		t.Errorf("expected layout dir to have %04o permissions, got %04o", util.DefaultDirPerm, perm)
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
	if perm := fileInfo.Mode().Perm(); perm != util.DefaultFilePerm {
		t.Errorf("expected written file to have %04o permissions, got %04o", util.DefaultFilePerm, perm)
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
