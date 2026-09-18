package portal

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edsilegxrepo/repoview/internal/models"
)

// TEST STRATEGY:
// Validate HTML portal rendering, aggregated RSS 2.0 feed merging (Option C),
// atomic file writing, and Overwrite Safety Guard (Option 2).

func createMockCatalog() *Catalog {
	repos := []*DiscoveredRepo{
		{
			Title:        "Enterprise Linux 9 Base",
			RelPath:      "el9/base/x86_64",
			TargetURL:    "el9/base/x86_64/repoview/index.html",
			Format:       models.FormatRPM,
			Distro:       "el9",
			Channel:      "base",
			Arch:         "x86_64",
			PackageCount: 1500,
			LastBuild:    time.Now().UTC(),
			IsRendered:   true,
			Icon:         "redhat",
			Badge:        "Production",
		},
		{
			Title:        "Ubuntu 24.04 LTS",
			RelPath:      "ubuntu/dists/noble",
			TargetURL:    "ubuntu/dists/noble/main/binary-amd64/repoview/index.html",
			Format:       models.FormatDEB,
			Distro:       "ubuntu",
			Suite:        "noble",
			Components:   []string{"main", "universe"},
			Channel:      "main, universe",
			Arch:         "amd64",
			PackageCount: 3200,
			LastBuild:    time.Now().UTC(),
			IsRendered:   true,
			Icon:         "ubuntu",
			Badge:        "LTS",
		},
		{
			Title:        "Raw Nightly Mirror",
			RelPath:      "el10-nightly.x86_64",
			TargetURL:    "el10-nightly.x86_64",
			Format:       models.FormatRPM,
			Distro:       "el10",
			Channel:      "nightly",
			Arch:         "x86_64",
			PackageCount: 200,
			IsRendered:   false,
			Icon:         "generic",
		},
	}

	return BuildCatalog("Main Mirror Hub", "Central mirror portal", "https://mirror.example.com/", repos)
}

func TestRenderer_RenderHTML(t *testing.T) {
	r, err := NewRenderer("")
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	catalog := createMockCatalog()
	htmlBytes, err := r.RenderHTML(catalog)
	if err != nil {
		t.Fatalf("RenderHTML failed: %v", err)
	}

	content := string(htmlBytes)

	// Verify crucial elements
	if !strings.Contains(content, `<meta name="generator" content="RepoView-Portal">`) {
		t.Errorf("missing RepoView-Portal generator meta tag")
	}
	if !strings.Contains(content, "Main Mirror Hub") {
		t.Errorf("missing portal title in HTML")
	}
	if !strings.Contains(content, "Enterprise Linux 9 Base") {
		t.Errorf("missing EL9 repo card in HTML")
	}
	if !strings.Contains(content, "Ubuntu 24.04 LTS") {
		t.Errorf("missing Ubuntu repo card in HTML")
	}
	if !strings.Contains(content, "Raw Nightly Mirror") {
		t.Errorf("missing Raw mirror repo card in HTML")
	}
	if !strings.Contains(content, "id=\"catalog-search\"") {
		t.Errorf("missing search input in HTML")
	}
	if !strings.Contains(content, "id=\"theme-toggle\"") {
		t.Errorf("missing theme toggle in HTML")
	}
}

func TestRenderer_RenderFeed_Aggregated(t *testing.T) {
	tempRoot := t.TempDir()
	r, err := NewRenderer("")
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	// Create child repo latest-feed.xml
	childDir := filepath.Join(tempRoot, "el9", "base", "x86_64")
	childOut := filepath.Join(childDir, "repoview")
	_ = os.MkdirAll(childOut, 0o755)

	childFeed := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>EL9 Feed</title>
    <link>index.html</link>
    <description>Child Feed</description>
    <item>
      <title>nginx-1.24.0-1.el9.x86_64</title>
      <link>nginx.html</link>
      <description>High performance web server</description>
      <pubDate>` + time.Now().UTC().Format(time.RFC1123Z) + `</pubDate>
      <guid>nginx-1.24.0-1.el9.x86_64</guid>
    </item>
  </channel>
</rss>`
	_ = os.WriteFile(filepath.Join(childOut, "latest-feed.xml"), []byte(childFeed), 0o644)

	catalog := createMockCatalog()
	// Point first repo path to real childDir on disk
	catalog.Repos[0].Path = childDir

	feedBytes, err := r.RenderFeed(catalog, tempRoot)
	if err != nil {
		t.Fatalf("RenderFeed failed: %v", err)
	}

	// Verify valid XML
	var parsed rssFeed
	if err := xml.Unmarshal(feedBytes, &parsed); err != nil {
		t.Fatalf("failed to unmarshal aggregated feed XML: %v", err)
	}

	if parsed.Channel.Title != "Main Mirror Hub - Unified Feed" {
		t.Errorf("unexpected channel title: %s", parsed.Channel.Title)
	}

	// Should contain both repository updates and aggregated package item
	foundChildItem := false
	for _, item := range parsed.Channel.Items {
		if strings.Contains(item.Title, "nginx-1.24.0") {
			foundChildItem = true
			break
		}
	}
	if !foundChildItem {
		t.Errorf("aggregated child package item not found in feed")
	}
}

func TestRenderer_WritePortal_SafetyGuard(t *testing.T) {
	tempOut := t.TempDir()
	r, err := NewRenderer("")
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	catalog := createMockCatalog()

	// 1. Write fresh portal
	if err := r.WritePortal(tempOut, tempOut, catalog, false); err != nil {
		t.Fatalf("initial WritePortal failed: %v", err)
	}

	indexPath := filepath.Join(tempOut, "index.html")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		t.Fatalf("index.html was not written")
	}
	feedPath := filepath.Join(tempOut, "portal-feed.xml")
	if _, err := os.Stat(feedPath); os.IsNotExist(err) {
		t.Fatalf("portal-feed.xml was not written")
	}

	// 2. Overwrite existing RepoView-Portal index.html without force (should succeed)
	if err := r.WritePortal(tempOut, tempOut, catalog, false); err != nil {
		t.Fatalf("re-write of valid portal failed: %v", err)
	}

	// 3. Foreign non-RepoView index.html (Option 2 Safety Guard)
	foreignOut := t.TempDir()
	foreignIndex := filepath.Join(foreignOut, "index.html")
	_ = os.WriteFile(foreignIndex, []byte("<html><body>Welcome to My Nginx Server</body></html>"), 0o644)

	// Attempting write without force MUST fail with safety violation
	err = r.WritePortal(foreignOut, foreignOut, catalog, false)
	if err == nil {
		t.Fatalf("expected safety error when overwriting foreign index.html without force, got nil")
	}
	var safetyErr *ErrSafetyOverwrite
	if !errors.As(err, &safetyErr) {
		t.Errorf("expected error of type *ErrSafetyOverwrite, got: %T (%v)", err, err)
	}

	// Attempting write WITH force MUST succeed
	if err := r.WritePortal(foreignOut, foreignOut, catalog, true); err != nil {
		t.Fatalf("WritePortal with force=true failed: %v", err)
	}

	overwrittenData, _ := os.ReadFile(foreignIndex)
	if !strings.Contains(string(overwrittenData), "RepoView-Portal") {
		t.Errorf("foreign index was not replaced with portal HTML")
	}
}
