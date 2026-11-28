package app

// OBJECTIVES:
// Verify single-repository starter config inference (--dump-config) and safe non-clobber
// / --force merge semantics for both RPM and Debian repositories.
//
// TEST STRATEGY:
// Exercises BuildStarterRepoConfig and WriteStarterRepoConfig against synthetic RPM/DEB trees
// in t.TempDir(), verifying inferred metadata, collision rejection on force=false, and preservation
// of manual edits when merging with force=true.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildAndWriteStarterRepoConfig_RPMAndSafeMerge(t *testing.T) {
	parent := t.TempDir()
	repoDir := filepath.Join(parent, "el9", "base", "x86_64")
	createXMLMockRPMRepo(t, repoDir)

	cfg := Config{
		RepoDir:     repoDir,
		BaseURL:     "https://mirrors.example.com/el9/base/x86_64",
		IgnoreList:  []string{"*-debuginfo"},
		ExcludeArch: []string{"i686"},
		PortalURL:   "auto",
	}

	starter, err := BuildStarterRepoConfig(cfg)
	if err != nil {
		t.Fatalf("BuildStarterRepoConfig failed: %v", err)
	}
	if starter.Format != "rpm" || starter.Distro != "el9" || starter.Channel != "base" || starter.Arch != "x86_64" {
		t.Errorf("unexpected inferred starter config: %+v", starter)
	}
	if starter.PortalURL != "" {
		t.Errorf("expected 'auto' portal_url omitted from starter config, got %q", starter.PortalURL)
	}
	if starter.Icon != "redhat" {
		t.Errorf("expected icon 'redhat', got %q", starter.Icon)
	}

	outPath := filepath.Join(t.TempDir(), "repoview.yaml")
	if err := WriteStarterRepoConfig(outPath, starter, false); err != nil {
		t.Fatalf("WriteStarterRepoConfig failed: %v", err)
	}

	// Non-clobber without Force
	if err := WriteStarterRepoConfig(outPath, starter, false); err == nil {
		t.Fatalf("expected non-clobber error when output file exists and force=false")
	}

	// Hand-edit output file and verify Force=true merges without losing custom settings
	if err := os.WriteFile(outPath, []byte("title: Custom Kept Title\nignore_package:\n  - custom-ignore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteStarterRepoConfig(outPath, starter, true); err != nil {
		t.Fatalf("WriteStarterRepoConfig(force=true) failed: %v", err)
	}

	merged, err := LoadRepoConfig(outPath)
	if err != nil {
		t.Fatalf("LoadRepoConfig after merge failed: %v", err)
	}
	if merged.Title != "Custom Kept Title" {
		t.Errorf("expected existing title preserved on merge, got %q", merged.Title)
	}
	if len(merged.IgnorePackage) != 1 || merged.IgnorePackage[0] != "custom-ignore" {
		t.Errorf("expected existing ignore_package preserved on merge, got %v", merged.IgnorePackage)
	}
	if merged.Format != "rpm" || merged.Arch != "x86_64" || merged.BaseURL != cfg.BaseURL {
		t.Errorf("expected missing fields filled from inferred starter config, got %+v", merged)
	}
}

func TestBuildStarterRepoConfig_DEB(t *testing.T) {
	parent := t.TempDir()
	repoDir := filepath.Join(parent, "deb13")
	suiteDir := filepath.Join(repoDir, "dists", "trixie")
	leaf := filepath.Join(suiteDir, "main", "binary-amd64")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "Release"), []byte("Origin: Debian\nSuite: trixie\nCodename: trixie\nArchitectures: amd64\nComponents: main\nDescription: Debian 13 Repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leaf, "Packages"), []byte("Package: jq\nVersion: 1.7-1\nArchitecture: amd64\nMaintainer: Debian <dev@debian.org>\nDescription: JSON processor\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	starter, err := BuildStarterRepoConfig(Config{
		RepoDir: repoDir,
	})
	if err != nil {
		t.Fatalf("BuildStarterRepoConfig(DEB) failed: %v", err)
	}
	if starter.Format != "deb" || starter.Suite != "trixie" || starter.Arch != "amd64" || !strings.Contains(starter.Description, "Debian 13 Repo") {
		t.Errorf("unexpected inferred DEB starter config: %+v", starter)
	}
}
