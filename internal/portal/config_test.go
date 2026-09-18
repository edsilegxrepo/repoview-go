package portal

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/edsilegxrepo/repoview/internal/models"
)

// TEST STRATEGY:
// Verify YAML configuration parsing, default fallbacks, starter config dumping,
// metadata override merging, icon matching, external repo appending, and pinned ordering.

func TestDefaultPortalConfig(t *testing.T) {
	cfg := DefaultPortalConfig()
	if cfg.Title == "" {
		t.Errorf("expected non-empty default title")
	}
	if !cfg.AutoDiscovery.Enabled {
		t.Errorf("expected AutoDiscovery.Enabled=true")
	}
	if cfg.AutoDiscovery.MaxDepth != 5 {
		t.Errorf("expected MaxDepth=5, got %d", cfg.AutoDiscovery.MaxDepth)
	}
}

func TestLoadConfig_Valid(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "portal.yaml")

	content := `
title: "Custom Title"
description: "Custom Description"
base_url: "https://custom.repo.net/"
auto_discovery:
  enabled: true
  max_depth: 4
  require_rendered: true
  exclude:
    - "*debug*"
overrides:
  "el9/base/x86_64":
    title: "Overridden EL9"
    icon: "redhat"
    badge: "Production"
pinned:
  - "el9/base/x86_64"
external_repos:
  - title: "External Build"
    rel_path: "https://remote.example.com/"
    format: "rpm"
    distro: "fedora"
    arch: "x86_64"
    package_count: 50
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write portal.yaml: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Title != "Custom Title" {
		t.Errorf("cfg.Title = %q; want %q", cfg.Title, "Custom Title")
	}
	if cfg.AutoDiscovery.MaxDepth != 4 {
		t.Errorf("cfg.AutoDiscovery.MaxDepth = %d; want 4", cfg.AutoDiscovery.MaxDepth)
	}
	if !cfg.AutoDiscovery.RequireRendered {
		t.Errorf("expected RequireRendered=true")
	}
	if len(cfg.Overrides) != 1 {
		t.Errorf("expected 1 override, got %d", len(cfg.Overrides))
	}
	if len(cfg.ExternalRepos) != 1 {
		t.Errorf("expected 1 external repo, got %d", len(cfg.ExternalRepos))
	}
}

func TestLoadConfig_Errors(t *testing.T) {
	// 1. Non-existent file
	if _, err := LoadConfig("/nonexistent/portal.yaml"); err == nil {
		t.Errorf("expected error for non-existent file, got nil")
	}

	// 2. Corrupted YAML
	tempDir := t.TempDir()
	corruptPath := filepath.Join(tempDir, "corrupt.yaml")
	_ = os.WriteFile(corruptPath, []byte("title: [broken: yaml: :::"), 0o644)
	if _, err := LoadConfig(corruptPath); err == nil {
		t.Errorf("expected error for corrupted YAML, got nil")
	}
}

func TestDumpConfig(t *testing.T) {
	tempDir := t.TempDir()
	outputPath := filepath.Join(tempDir, "portal.yaml")

	repos := []*DiscoveredRepo{
		{
			Title:        "EL9 BaseOS",
			RelPath:      "el9/base/x86_64",
			Distro:       "el9",
			Arch:         "x86_64",
			Format:       models.FormatRPM,
			PackageCount: 120,
			LastBuild:    time.Now().UTC(),
		},
		{
			Title:        "Ubuntu 24.04 Noble",
			RelPath:      "ubuntu/dists/noble/main/binary-amd64",
			Distro:       "ubuntu",
			Arch:         "amd64",
			Format:       models.FormatDEB,
			PackageCount: 300,
			LastBuild:    time.Now().UTC(),
		},
	}

	catalog := BuildCatalog("Mirrors", "Company package mirrors", "https://mirror.org/", repos)

	if err := DumpConfig(outputPath, catalog); err != nil {
		t.Fatalf("DumpConfig failed: %v", err)
	}

	// Verify dumped file can be parsed back
	loaded, err := LoadConfig(outputPath)
	if err != nil {
		t.Fatalf("failed to reload dumped config: %v", err)
	}

	if loaded.Title != "Mirrors" {
		t.Errorf("loaded.Title = %q; want %q", loaded.Title, "Mirrors")
	}
	if len(loaded.Pinned) != 2 {
		t.Errorf("expected 2 pinned repos in starter config, got %d", len(loaded.Pinned))
	}
	if len(loaded.Overrides) != 2 {
		t.Errorf("expected 2 overrides in starter config, got %d", len(loaded.Overrides))
	}
}

func TestApplyConfig(t *testing.T) {
	repo1 := &DiscoveredRepo{
		Title:        "Raw Repo 1",
		RelPath:      "el9/base/x86_64",
		Distro:       "el9",
		Arch:         "x86_64",
		Format:       models.FormatRPM,
		PackageCount: 100,
	}
	repo2 := &DiscoveredRepo{
		Title:        "Raw Repo 2",
		RelPath:      "ubuntu/dists/noble/main/binary-amd64",
		Distro:       "ubuntu",
		Arch:         "amd64",
		Format:       models.FormatDEB,
		PackageCount: 200,
	}

	catalog := BuildCatalog("Default Title", "Default Desc", "", []*DiscoveredRepo{repo1, repo2})

	cfg := &PortalConfig{
		Title:       "Custom Catalog",
		Description: "Custom Description",
		BaseURL:     "https://repos.company.net/",
		Overrides: map[string]RepoOverrideConfig{
			"el9/base/x86_64": {
				Title: "Enterprise Linux 9",
				Badge: "Stable",
				Icon:  "redhat",
			},
		},
		Pinned: []string{"ubuntu/dists/noble/main/binary-amd64"},
		ExternalRepos: []*DiscoveredRepo{
			{
				Title:        "External Repo",
				RelPath:      "https://ext.org/",
				Distro:       "fedora",
				Arch:         "x86_64",
				Format:       models.FormatRPM,
				PackageCount: 50,
			},
		},
	}

	ApplyConfig(catalog, cfg)

	if catalog.Title != "Custom Catalog" {
		t.Errorf("catalog.Title = %q; want %q", catalog.Title, "Custom Catalog")
	}
	if catalog.TotalRepos != 3 {
		t.Errorf("catalog.TotalRepos = %d; want 3 (2 local + 1 external)", catalog.TotalRepos)
	}
	if catalog.TotalPkgs != 350 {
		t.Errorf("catalog.TotalPkgs = %d; want 350", catalog.TotalPkgs)
	}

	// Pinned repo (repo2: ubuntu) should be first
	if catalog.Repos[0].RelPath != "ubuntu/dists/noble/main/binary-amd64" {
		t.Errorf("first repo should be pinned ubuntu, got %s", catalog.Repos[0].RelPath)
	}

	// Check overrides on repo1
	var el9Repo *DiscoveredRepo
	for _, r := range catalog.Repos {
		if r.RelPath == "el9/base/x86_64" {
			el9Repo = r
			break
		}
	}
	if el9Repo == nil {
		t.Fatalf("missing el9 repo")
	}
	if el9Repo.Title != "Enterprise Linux 9" {
		t.Errorf("el9Repo.Title = %q; want %q", el9Repo.Title, "Enterprise Linux 9")
	}
	if el9Repo.Badge != "Stable" {
		t.Errorf("el9Repo.Badge = %q; want %q", el9Repo.Badge, "Stable")
	}
	if el9Repo.Icon != "redhat" {
		t.Errorf("el9Repo.Icon = %q; want %q", el9Repo.Icon, "redhat")
	}
}

func TestFindPortalConfigIn(t *testing.T) {
	tempRoot := t.TempDir()

	// portal.yaml at tempRoot
	configPath := filepath.Join(tempRoot, "portal.yaml")
	_ = os.WriteFile(configPath, []byte("title: Root\n"), 0o644)

	childDir := filepath.Join(tempRoot, "a", "b", "c")
	_ = os.MkdirAll(childDir, 0o755)

	found, err := FindPortalConfigIn(childDir)
	if err != nil {
		t.Fatalf("FindPortalConfigIn failed: %v", err)
	}
	if found != configPath {
		t.Errorf("FindPortalConfigIn = %q; want %q", found, configPath)
	}

	// Empty dir with no config
	otherDir := t.TempDir()
	if _, err := FindPortalConfigIn(otherDir); err == nil {
		t.Errorf("expected error when no config exists, got nil")
	}
}

func TestMatchDistroIcon(t *testing.T) {
	tests := []struct {
		distro string
		want   string
	}{
		{"el9", "redhat"},
		{"RHEL8", "redhat"},
		{"Fedora-40", "fedora"},
		{"rocky-linux", "rocky"},
		{"almalinux", "almalinux"},
		{"centos7", "centos"},
		{"ubuntu-24.04", "ubuntu"},
		{"debian-12", "debian"},
		{"archlinux", "arch"},
		{"opensuse-leap", "opensuse"},
		{"mycustomdistro", "generic"},
	}

	for _, tc := range tests {
		got := MatchDistroIcon(tc.distro)
		if got != tc.want {
			t.Errorf("MatchDistroIcon(%q) = %q; want %q", tc.distro, got, tc.want)
		}
	}
}
