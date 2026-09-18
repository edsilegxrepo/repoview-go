package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/edsilegxrepo/repoview/internal/models"
)

func TestFindParentPortal_YAML(t *testing.T) {
	tempRoot := t.TempDir()

	// Structure: tempRoot/portal.yaml
	//            tempRoot/el9/base/x86_64/repoview
	portalFile := filepath.Join(tempRoot, "portal.yaml")
	if err := os.WriteFile(portalFile, []byte("title: Root Portal\n"), 0o644); err != nil {
		t.Fatalf("failed to create portal.yaml: %v", err)
	}

	repoDir := filepath.Join(tempRoot, "el9", "base", "x86_64")
	outputDir := filepath.Join(repoDir, "repoview")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("failed to create outputDir: %v", err)
	}

	relURL := findParentPortal(outputDir, repoDir)
	expected := "../../../../index.html"
	if relURL != expected {
		t.Errorf("findParentPortal() = %q; want %q", relURL, expected)
	}
}

func TestFindParentPortal_YML(t *testing.T) {
	tempRoot := t.TempDir()

	// Structure: tempRoot/portal.yml
	//            tempRoot/repo/repoview
	portalFile := filepath.Join(tempRoot, "portal.yml")
	if err := os.WriteFile(portalFile, []byte("title: Root Portal\n"), 0o644); err != nil {
		t.Fatalf("failed to create portal.yml: %v", err)
	}

	repoDir := filepath.Join(tempRoot, "repo")
	outputDir := filepath.Join(repoDir, "repoview")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("failed to create outputDir: %v", err)
	}

	relURL := findParentPortal(outputDir, repoDir)
	expected := "../../index.html"
	if relURL != expected {
		t.Errorf("findParentPortal() = %q; want %q", relURL, expected)
	}
}

func TestFindParentPortal_IndexHTMLSignature(t *testing.T) {
	tempRoot := t.TempDir()

	// Structure: tempRoot/index.html with <meta name="generator" content="RepoView-Portal">
	//            tempRoot/ubuntu/repoview
	portalHTML := `<!DOCTYPE html>
<html>
<head>
    <meta name="generator" content="RepoView-Portal">
    <title>Portal</title>
</head>
<body><h1>Portal</h1></body>
</html>`
	if err := os.WriteFile(filepath.Join(tempRoot, "index.html"), []byte(portalHTML), 0o644); err != nil {
		t.Fatalf("failed to write portal index.html: %v", err)
	}

	repoDir := filepath.Join(tempRoot, "ubuntu")
	outputDir := filepath.Join(repoDir, "repoview")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("failed to create outputDir: %v", err)
	}

	relURL := findParentPortal(outputDir, repoDir)
	expected := "../../index.html"
	if relURL != expected {
		t.Errorf("findParentPortal() = %q; want %q", relURL, expected)
	}
}

func TestFindParentPortal_NotFound(t *testing.T) {
	tempRoot := t.TempDir()
	repoDir := filepath.Join(tempRoot, "repo")
	outputDir := filepath.Join(repoDir, "repoview")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("failed to create outputDir: %v", err)
	}

	// Normal non-portal index.html should not match
	if err := os.WriteFile(filepath.Join(tempRoot, "index.html"), []byte("<html><body>Hello</body></html>"), 0o644); err != nil {
		t.Fatalf("failed to write index.html: %v", err)
	}

	relURL := findParentPortal(outputDir, repoDir)
	if relURL != "" {
		t.Errorf("findParentPortal() expected empty, got %q", relURL)
	}
}

func TestResolvePortalURL(t *testing.T) {
	tempRoot := t.TempDir()
	portalFile := filepath.Join(tempRoot, "portal.yaml")
	_ = os.WriteFile(portalFile, []byte("title: P\n"), 0o644)

	repoDir := filepath.Join(tempRoot, "repo")
	outDir := filepath.Join(repoDir, "repoview")
	_ = os.MkdirAll(outDir, 0o755)

	tests := []struct {
		name      string
		portalURL string
		want      string
	}{
		{
			name:      "Explicit URL",
			portalURL: "https://hub.example.com",
			want:      "https://hub.example.com",
		},
		{
			name:      "Explicit relative path",
			portalURL: "../custom-portal.html",
			want:      "../custom-portal.html",
		},
		{
			name:      "Disabled via none",
			portalURL: "none",
			want:      "",
		},
		{
			name:      "Disabled via off",
			portalURL: "off",
			want:      "",
		},
		{
			name:      "Disabled via false",
			portalURL: "false",
			want:      "",
		},
		{
			name:      "Auto detection",
			portalURL: "auto",
			want:      "../../index.html",
		},
		{
			name:      "Empty defaults to auto",
			portalURL: "",
			want:      "../../index.html",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewGenerator(Config{
				RepoDir:   repoDir,
				OutputDir: outDir,
				PortalURL: tc.portalURL,
			})
			got := g.resolvePortalURL()
			if got != tc.want {
				t.Errorf("resolvePortalURL() = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestDetectPrimaryArch(t *testing.T) {
	tests := []struct {
		name string
		pkgs []*models.Package
		want string
	}{
		{
			name: "Empty package list",
			pkgs: []*models.Package{},
			want: "all",
		},
		{
			name: "Only noarch packages",
			pkgs: []*models.Package{
				{Arch: "noarch"},
				{Arch: "noarch"},
			},
			want: "noarch",
		},
		{
			name: "Only all packages",
			pkgs: []*models.Package{
				{Arch: "all"},
			},
			want: "all",
		},
		{
			name: "Mixed binary and noarch",
			pkgs: []*models.Package{
				{Arch: "noarch"},
				{Arch: "x86_64"},
				{Arch: "noarch"},
				{Arch: "x86_64"},
			},
			want: "x86_64",
		},
		{
			name: "Multi binary arches picks highest count",
			pkgs: []*models.Package{
				{Arch: "aarch64"},
				{Arch: "x86_64"},
				{Arch: "aarch64"},
			},
			want: "aarch64",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detectPrimaryArch(tc.pkgs)
			if got != tc.want {
				t.Errorf("detectPrimaryArch() = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestDetectArchFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/var/www/repos/el9/base/x86_64", want: "x86_64"},
		{path: "/var/www/repos/el9/base/aarch64/", want: "aarch64"},
		{path: "/var/www/repos/ubuntu/dists/noble/main/binary-amd64", want: "amd64"},
		{path: "/var/www/repos/ubuntu/dists/noble/main/binary-arm64", want: "arm64"},
		{path: "/var/www/repos/el10-base.x86_64", want: "x86_64"},
		{path: "/var/www/repos/fedora-40-updates.s390x", want: "s390x"},
		{path: "/var/www/repos/el-9-x86_64", want: "x86_64"},
		{path: "/var/www/repos/generic-repo", want: ""},
	}

	for _, tc := range tests {
		got := detectArchFromPath(tc.path)
		if got != tc.want {
			t.Errorf("detectArchFromPath(%q) = %q; want %q", tc.path, got, tc.want)
		}
	}
}

func TestInferDistroAndChannel(t *testing.T) {
	tests := []struct {
		path        string
		wantDistro  string
		wantChannel string
	}{
		{
			path:        "/var/www/repos/el9/base/x86_64",
			wantDistro:  "el9",
			wantChannel: "base",
		},
		{
			path:        "/var/www/repos/el8/extras/aarch64",
			wantDistro:  "el8",
			wantChannel: "extras",
		},
		{
			path:        "/var/www/repos/ubuntu/dists/noble/main/binary-amd64",
			wantDistro:  "ubuntu",
			wantChannel: "main",
		},
		{
			path:        "/var/www/repos/debian/dists/bookworm/universe",
			wantDistro:  "debian",
			wantChannel: "universe",
		},
		{
			path:        "/var/www/repos/el10-base.x86_64",
			wantDistro:  "el10",
			wantChannel: "base",
		},
		{
			path:        "/var/www/repos/fedora-40-updates.x86_64",
			wantDistro:  "fedora-40",
			wantChannel: "updates",
		},
		{
			path:        "/var/www/repos/custom-archive",
			wantDistro:  "custom-archive",
			wantChannel: "base",
		},
	}

	for _, tc := range tests {
		gotDistro, gotChannel := inferDistroAndChannel(tc.path)
		if gotDistro != tc.wantDistro || gotChannel != tc.wantChannel {
			t.Errorf("inferDistroAndChannel(%q) = (%q, %q); want (%q, %q)",
				tc.path, gotDistro, gotChannel, tc.wantDistro, tc.wantChannel)
		}
	}
}

func TestBuildDescriptor(t *testing.T) {
	g := NewGenerator(Config{
		RepoDir:   "/var/www/repos/el9/base/x86_64",
		OutputDir: "/var/www/repos/el9/base/x86_64/repoview",
		Title:     "Enterprise Linux 9 BaseOS",
		BaseURL:   "https://repo.example.com/el9/base/x86_64/",
		Format:    models.FormatRPM,
		Version:   "0.2.0",
	})

	pkgs := []*models.Package{
		{Name: "app1", Arch: "x86_64"},
		{Name: "app2", Arch: "noarch"},
	}

	data, err := g.buildDescriptor("../../../../index.html", pkgs)
	if err != nil {
		t.Fatalf("buildDescriptor failed: %v", err)
	}

	var desc models.RepoDescriptor
	if err := json.Unmarshal(data, &desc); err != nil {
		t.Fatalf("failed to unmarshal descriptor JSON: %v", err)
	}

	if desc.Title != "Enterprise Linux 9 BaseOS" {
		t.Errorf("desc.Title = %q; want %q", desc.Title, "Enterprise Linux 9 BaseOS")
	}
	if desc.Format != "rpm" {
		t.Errorf("desc.Format = %q; want %q", desc.Format, "rpm")
	}
	if desc.Arch != "x86_64" {
		t.Errorf("desc.Arch = %q; want %q", desc.Arch, "x86_64")
	}
	if desc.Distro != "el9" {
		t.Errorf("desc.Distro = %q; want %q", desc.Distro, "el9")
	}
	if desc.Channel != "base" {
		t.Errorf("desc.Channel = %q; want %q", desc.Channel, "base")
	}
	if desc.PackageCount != 2 {
		t.Errorf("desc.PackageCount = %d; want 2", desc.PackageCount)
	}
	if desc.BaseURL != "https://repo.example.com/el9/base/x86_64/" {
		t.Errorf("desc.BaseURL = %q; want %q", desc.BaseURL, "https://repo.example.com/el9/base/x86_64/")
	}
	if desc.PortalURL != "../../../../index.html" {
		t.Errorf("desc.PortalURL = %q; want %q", desc.PortalURL, "../../../../index.html")
	}
	if desc.RepoviewVersion != "0.2.0" {
		t.Errorf("desc.RepoviewVersion = %q; want %q", desc.RepoviewVersion, "0.2.0")
	}
	if desc.LastBuild.IsZero() {
		t.Errorf("desc.LastBuild should not be zero")
	}
}
