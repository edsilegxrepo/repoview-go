package app

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edsilegxrepo/repoview/internal/models"
)

const mockDebianPackages = `Package: webapp
Version: 2.1.0-1
Architecture: amd64
Maintainer: WebApp Team <webapp@example.org>
Installed-Size: 12500
Depends: libc6 (>= 2.34), libssl3 (>= 3.0.0)
Recommends: webapp-doc
Suggests: nginx
Conflicts: webapp-legacy
Breaks: webapp-old (<< 2.0)
Replaces: webapp-old
Section: web
Filename: pool/main/w/webapp/webapp_2.1.0-1_amd64.deb
Size: 4500000
SHA256: 1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff
Homepage: https://webapp.example.org
Description: modern scalable web application platform
 WebApp is a production-grade web platform designed for
 high throughput and microservice architectures.

Package: dbtools
Version: 1.0.4-2
Architecture: amd64
Maintainer: Database Team <db@example.org>
Installed-Size: 3200
Depends: libc6 (>= 2.34)
Section: database
Filename: pool/main/d/dbtools/dbtools_1.0.4-2_amd64.deb
Size: 1200000
SHA256: 9999888877776666555544443333222211110000aaaabbbbccccddddeeeeffff
Description: lightweight database management command-line utilities
 DBTools provides fast command line utilities to inspect,
 dump, and restore database clusters.
`

func createMockDebianRepository(t *testing.T, isPool bool) (string, string) {
	repoDir := t.TempDir()

	if isPool {
		binDir := filepath.Join(repoDir, "dists", "noble", "main", "binary-amd64")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatalf("failed to create binDir: %v", err)
		}

		// Write Packages.gz
		pkgGzPath := filepath.Join(binDir, "Packages.gz")
		f, err := os.Create(pkgGzPath)
		if err != nil {
			t.Fatalf("failed to create Packages.gz: %v", err)
		}
		gw := gzip.NewWriter(f)
		if _, err := gw.Write([]byte(mockDebianPackages)); err != nil {
			t.Fatalf("failed to write gzip: %v", err)
		}
		_ = gw.Close()
		_ = f.Close()

		// Write Release file
		releasePath := filepath.Join(repoDir, "dists", "noble", "Release")
		releaseContent := `Origin: TestDebian
Label: TestDebian
Suite: noble
Codename: noble
Architectures: amd64
Components: main
Description: Test Debian Repository
`
		if err := os.WriteFile(releasePath, []byte(releaseContent), 0o644); err != nil {
			t.Fatalf("failed to write Release: %v", err)
		}

		return repoDir, binDir
	}

	// Flat repository
	pkgPath := filepath.Join(repoDir, "Packages")
	if err := os.WriteFile(pkgPath, []byte(mockDebianPackages), 0o644); err != nil {
		t.Fatalf("failed to write flat Packages: %v", err)
	}

	return repoDir, repoDir
}

func TestGenerator_DebianPipeline_PoolDists(t *testing.T) {
	repoDir, _ := createMockDebianRepository(t, true)
	outDir := t.TempDir()
	stateDir := t.TempDir()

	cfg := Config{
		RepoDir:   repoDir,
		OutputDir: outDir,
		StateDir:  stateDir,
		Title:     "Noble Test Repository",
		URL:       "http://repo.example.com/noble",
		BaseURL:   "http://repo.example.com/noble",
		Format:    models.FormatDEB,
		Force:     true,
		Quiet:     true,
		Version:   "1.0-test",
	}

	gen := NewGenerator(cfg)
	if err := gen.Run(); err != nil {
		t.Fatalf("Generator.Run failed for Debian pool repository: %v", err)
	}

	// Verify generated files exist
	expectedFiles := []string{
		"index.html",
		"search.json",
		"latest-feed.xml",
		"webapp.html",
		"dbtools.html",
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

	// Verify package page content (webapp.html)
	webappHTML, err := os.ReadFile(filepath.Join(outDir, "webapp.html"))
	if err != nil {
		t.Fatalf("failed to read webapp.html: %v", err)
	}
	webappContent := string(webappHTML)

	if !strings.Contains(webappContent, "sudo apt install webapp") {
		t.Errorf("expected 'sudo apt install webapp' in webapp.html")
	}
	if !strings.Contains(webappContent, "WebApp Team") {
		t.Errorf("expected maintainer 'WebApp Team' in webapp.html")
	}
	if !strings.Contains(webappContent, "webapp_2.1.0-1_amd64.deb") {
		t.Errorf("expected Debian archive filename in webapp.html")
	}
	if !strings.Contains(webappContent, "Available DEB Packages") {
		t.Errorf("expected 'Available DEB Packages' section in webapp.html")
	}

	// Verify search.json contains Debian packages
	searchJSON, err := os.ReadFile(filepath.Join(outDir, "search.json"))
	if err != nil {
		t.Fatalf("failed to read search.json: %v", err)
	}
	var searchIdx models.SearchIndex
	if err := json.Unmarshal(searchJSON, &searchIdx); err != nil {
		t.Fatalf("failed to parse search.json: %v", err)
	}
	if len(searchIdx.Data) != 2 {
		t.Errorf("expected 2 packages in search.json, got %d", len(searchIdx.Data))
	}

	// Verify index.html contains .sources configuration
	indexHTML, err := os.ReadFile(filepath.Join(outDir, "index.html"))
	if err != nil {
		t.Fatalf("failed to read index.html: %v", err)
	}
	indexContent := string(indexHTML)
	if !strings.Contains(indexContent, "Types: deb") {
		t.Errorf("expected 'Types: deb' snippet in index.html")
	}
	if !strings.Contains(indexContent, "Configure APT (.sources)") {
		t.Errorf("expected 'Configure APT (.sources)' button in index.html")
	}

	// Test incremental generation (skip unchanged)
	cfg.Force = false
	gen2 := NewGenerator(cfg)
	if err := gen2.Run(); err != nil {
		t.Fatalf("Incremental Generator.Run failed: %v", err)
	}
}

func TestGenerator_DebianPipeline_FlatRepo_AutoDetect(t *testing.T) {
	repoDir, _ := createMockDebianRepository(t, false)
	outDir := t.TempDir()

	cfg := Config{
		RepoDir:   repoDir,
		OutputDir: outDir,
		Title:     "Flat Debian Repo",
		Format:    "", // auto-detect
		Force:     true,
		Quiet:     true,
		Version:   "1.0-test",
	}

	gen := NewGenerator(cfg)
	if err := gen.Run(); err != nil {
		t.Fatalf("Generator.Run failed for flat Debian repository: %v", err)
	}

	if _, err := os.Stat(filepath.Join(outDir, "webapp.html")); err != nil {
		t.Errorf("expected webapp.html to be generated in flat repo test: %v", err)
	}
}

func TestDetectDebianSiblings(t *testing.T) {
	tmpDir := t.TempDir()

	// Create dists/noble/main/binary-amd64 and dists/noble/main/binary-arm64
	amd64Dir := filepath.Join(tmpDir, "dists", "noble", "main", "binary-amd64")
	arm64Dir := filepath.Join(tmpDir, "dists", "noble", "main", "binary-arm64")
	_ = os.MkdirAll(amd64Dir, 0o755)
	_ = os.MkdirAll(arm64Dir, 0o755)

	siblings := detectDebianSiblings(amd64Dir)
	if len(siblings) != 2 {
		t.Fatalf("expected 2 sibling architectures, got %d", len(siblings))
	}

	hasAmd64 := false
	hasArm64 := false
	for _, s := range siblings {
		if s.Name == "amd64" {
			hasAmd64 = true
			if !s.IsActive {
				t.Errorf("expected amd64 to be active")
			}
		}
		if s.Name == "arm64" {
			hasArm64 = true
			if s.IsActive {
				t.Errorf("expected arm64 to be inactive")
			}
		}
	}

	if !hasAmd64 || !hasArm64 {
		t.Errorf("expected both amd64 and arm64 siblings to be detected")
	}
}

func TestDetectDebianSiblings_Components(t *testing.T) {
	tmpDir := t.TempDir()

	// Create dists/bookworm/main/binary-amd64 and dists/bookworm/universe/binary-amd64
	mainDir := filepath.Join(tmpDir, "dists", "bookworm", "main", "binary-amd64")
	universeDir := filepath.Join(tmpDir, "dists", "bookworm", "universe", "binary-amd64")
	_ = os.MkdirAll(mainDir, 0o755)
	_ = os.MkdirAll(universeDir, 0o755)

	siblings := detectDebianSiblings(mainDir)
	if len(siblings) != 2 {
		t.Fatalf("expected 2 component siblings, got %d", len(siblings))
	}

	foundMain := false
	foundUniverse := false
	for _, s := range siblings {
		if s.Name == "main" {
			foundMain = true
			if !s.IsActive {
				t.Errorf("expected main to be active")
			}
		}
		if s.Name == "universe" {
			foundUniverse = true
			if s.IsActive {
				t.Errorf("expected universe to be inactive")
			}
		}
	}
	if !foundMain || !foundUniverse {
		t.Errorf("expected both main and universe components to be found")
	}
}
