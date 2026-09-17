package logic

import (
	"testing"

	"github.com/edsilegxrepo/repoview/internal/models"
)

// TEST STRATEGY:
// Verify hierarchical group tree construction, automatic group inference accuracy,
// and package group updates across all package instances.
//
// Test coverage includes:
//   - TestBuildGroupTree:
//       * Decomposes multi-level categories ("applications/archiving", "applications/internet").
//       * Preserves and orders standalone leaf categories ("documentation").
//       * Calculates cumulative package counts per parent category.
//       * Validates HasActive() matching across nested subgroups and leaf nodes.
//   - TestInferGroupForPackage:
//       * Validates functional mapping across specific stems: SSH, Git, PostgreSQL, PHP,
//         Python, GlusterFS, ClamAV, diffutils, tmux, and web proxies.
//   - TestGetRpmGroups_Inference:
//       * Confirms that empty or "Unspecified" RPM group tags are completely eliminated.
//       * Confirms that Group and RpmGroup fields on all package version structs are updated.

func TestBuildGroupTree(t *testing.T) {
	groups := []*GroupData{
		{
			ID:       "applications_archiving",
			Name:     "applications/archiving",
			Filename: "applications.archiving.group.html",
			Packages: []*models.Package{{Name: "tar"}, {Name: "zip"}},
		},
		{
			ID:       "applications_internet",
			Name:     "applications/internet",
			Filename: "applications.internet.group.html",
			Packages: []*models.Package{{Name: "firefox"}},
		},
		{
			ID:       "development_tools",
			Name:     "development/tools",
			Filename: "development.tools.group.html",
			Packages: []*models.Package{{Name: "make"}, {Name: "gcc"}, {Name: "gdb"}},
		},
		{
			ID:       "documentation",
			Name:     "documentation",
			Filename: "documentation.group.html",
			Packages: []*models.Package{{Name: "man-pages"}},
		},
	}

	tree := BuildGroupTree(groups)
	if len(tree) != 3 {
		t.Fatalf("expected 3 top-level categories, got %d", len(tree))
	}

	// 1. Applications category
	if tree[0].Name != "applications" {
		t.Errorf("expected category 'applications', got '%s'", tree[0].Name)
	}
	if tree[0].TotalPackages != 3 {
		t.Errorf("expected 3 total packages in applications, got %d", tree[0].TotalPackages)
	}
	if len(tree[0].Subgroups) != 2 {
		t.Errorf("expected 2 subgroups in applications, got %d", len(tree[0].Subgroups))
	}
	if tree[0].Subgroups[0].Name != "archiving" || tree[0].Subgroups[1].Name != "internet" {
		t.Errorf("subgroups not sorted properly: %v, %v", tree[0].Subgroups[0].Name, tree[0].Subgroups[1].Name)
	}
	if !tree[0].HasActive("applications.internet.group.html") {
		t.Errorf("expected HasActive to be true for applications.internet.group.html")
	}
	if tree[0].HasActive("other.html") {
		t.Errorf("expected HasActive to be false for other.html")
	}

	// 2. Development category
	if tree[1].Name != "development" {
		t.Errorf("expected category 'development', got '%s'", tree[1].Name)
	}
	if tree[1].TotalPackages != 3 {
		t.Errorf("expected 3 total packages in development, got %d", tree[1].TotalPackages)
	}

	// 3. Documentation (standalone leaf)
	if tree[2].Name != "documentation" {
		t.Errorf("expected category 'documentation', got '%s'", tree[2].Name)
	}
	if !tree[2].IsLeaf {
		t.Errorf("expected documentation to be a leaf category")
	}
	if tree[2].TotalPackages != 1 {
		t.Errorf("expected 1 total package in documentation, got %d", tree[2].TotalPackages)
	}
	if !tree[2].HasActive("documentation.group.html") {
		t.Errorf("expected HasActive to be true for documentation.group.html")
	}
}

func TestInferGroupForPackage(t *testing.T) {
	tests := []struct {
		pkg      *models.Package
		expected string
	}{
		{
			pkg:      &models.Package{Name: "openssh-server", Summary: "Open source SSH server daemon"},
			expected: "productivity/networking/ssh",
		},
		{
			pkg:      &models.Package{Name: "git", Summary: "Fast Version Control System"},
			expected: "development/tools",
		},
		{
			pkg:      &models.Package{Name: "git-daemon", Summary: "Git protocol daemon"},
			expected: "system environment/daemons",
		},
		{
			pkg:      &models.Package{Name: "pg_activity", Summary: "PostgreSQL server activity monitoring"},
			expected: "applications/databases",
		},
		{
			pkg:      &models.Package{Name: "pgbouncer", Summary: "Connection pooler for PostgreSQL"},
			expected: "applications/databases",
		},
		{
			pkg:      &models.Package{Name: "php-pdo", Summary: "Database abstraction module for PHP"},
			expected: "applications/databases",
		},
		{
			pkg:      &models.Package{Name: "php", Summary: "PHP scripting language"},
			expected: "development/languages",
		},
		{
			pkg:      &models.Package{Name: "python310", Summary: "Interpreter of the Python programming language"},
			expected: "development/languages",
		},
		{
			pkg:      &models.Package{Name: "python310-libs", Summary: "Python runtime libraries"},
			expected: "development/libraries",
		},
		{
			pkg:      &models.Package{Name: "glusterfs-server", Summary: "Distributed file-system server"},
			expected: "system environment/daemons",
		},
		{
			pkg:      &models.Package{Name: "libgfapi0", Summary: "GlusterFS api library"},
			expected: "system environment/libraries",
		},
		{
			pkg:      &models.Package{Name: "clamd", Summary: "The Clam AntiVirus Daemon"},
			expected: "system environment/daemons",
		},
		{
			pkg:      &models.Package{Name: "clamav", Summary: "End-user tools for the Clam Antivirus scanner"},
			expected: "system environment/security",
		},
		{
			pkg:      &models.Package{Name: "diffutils", Summary: "GNU collection of diff utilities"},
			expected: "applications/text",
		},
		{
			pkg:      &models.Package{Name: "tmux", Summary: "A terminal multiplexer"},
			expected: "system/shells",
		},
		{
			pkg:      &models.Package{Name: "myproxy", Summary: "A high-performance HTTP web reverse proxy"},
			expected: "applications/internet",
		},
	}

	for _, tt := range tests {
		got := InferGroupForPackage(tt.pkg)
		if got != tt.expected {
			t.Errorf("InferGroupForPackage(%s): expected %q, got %q", tt.pkg.Name, tt.expected, got)
		}
	}
}

func TestGetRpmGroups_Inference(t *testing.T) {
	pkgs := []*models.Package{
		{
			Name:     "git",
			RpmGroup: "Unspecified",
			Summary:  "Fast Version Control System",
		},
		{
			Name:     "openssh-server",
			RpmGroup: "",
			Summary:  "OpenSSH server daemon",
		},
		{
			Name:     "nginx",
			RpmGroup: "Applications/Internet",
			Summary:  "High performance web server",
		},
	}

	service := NewGroupingService(pkgs, nil)
	groups, err := service.GetGroups()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	groupNames := make(map[string]bool)
	for _, g := range groups {
		groupNames[g.Name] = true
	}

	// Should not have "unspecified" group
	if groupNames["unspecified"] || groupNames["Unspecified"] {
		t.Errorf("unspecified group should not exist when all packages were inferred")
	}

	// Inferred groups should exist
	if !groupNames["development/tools"] {
		t.Errorf("expected development/tools group to exist for git")
	}
	if !groupNames["productivity/networking/ssh"] {
		t.Errorf("expected productivity/networking/ssh group to exist for openssh-server")
	}
	if !groupNames["applications/internet"] {
		t.Errorf("expected applications/internet group to exist for nginx")
	}

	// Package objects should have updated Group and RpmGroup fields
	for _, p := range pkgs {
		if p.Name == "git" {
			if p.Group != "development/tools" || p.RpmGroup != "development/tools" {
				t.Errorf("git Group/RpmGroup not updated: Group=%q, RpmGroup=%q", p.Group, p.RpmGroup)
			}
		}
		if p.Name == "openssh-server" {
			if p.Group != "productivity/networking/ssh" || p.RpmGroup != "productivity/networking/ssh" {
				t.Errorf("openssh-server Group/RpmGroup not updated: Group=%q, RpmGroup=%q", p.Group, p.RpmGroup)
			}
		}
	}
}

func TestGetLetterGroups(t *testing.T) {
	pkgs := []*models.Package{
		{Name: "bash", Version: "5.1", Release: "1"},
		{Name: "bind", Version: "9.16", Release: "1"},
		{Name: "curl", Version: "7.76", Release: "1"},
		{Name: "", Version: "1.0", Release: "1"}, // Empty name should be skipped
	}

	service := NewGroupingService(pkgs, nil)
	letterGroups := service.GetLetterGroups()

	if len(letterGroups) != 2 {
		t.Fatalf("expected 2 letter groups (B and C), got %d", len(letterGroups))
	}

	if letterGroups[0].Name != "Letter B" || len(letterGroups[0].Packages) != 2 {
		t.Errorf("expected Letter B with 2 packages, got %+v", letterGroups[0])
	}
	if letterGroups[1].Name != "Letter C" || len(letterGroups[1].Packages) != 1 {
		t.Errorf("expected Letter C with 1 package, got %+v", letterGroups[1])
	}
}

func TestCompsGroups(t *testing.T) {
	comps := &models.Comps{
		Groups: []models.CompsGroup{
			{
				ID:          "web",
				Name:        "Web Server",
				Description: "HTTP daemons",
				Packagelist: []string{"httpd", "nginx", "missing-pkg"},
			},
			{
				ID:          "empty-group",
				Name:        "Empty Group",
				Packagelist: []string{"nonexistent"},
			},
		},
	}

	pkgs := []*models.Package{
		{Name: "nginx", Version: "1.20", Release: "1"},
		{Name: "nginx", Version: "1.22", Release: "1"}, // Newer version
		{Name: "httpd", Version: "2.4", Release: "1"},
	}

	service := NewGroupingService(pkgs, comps)
	groups, err := service.GetGroups()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Empty group should be filtered out
	if len(groups) != 1 {
		t.Fatalf("expected 1 group (empty-group filtered out), got %d", len(groups))
	}

	if groups[0].ID != "web" {
		t.Errorf("expected group 'web', got %q", groups[0].ID)
	}

	// Should contain httpd and latest nginx (1.22)
	if len(groups[0].Packages) != 2 {
		t.Fatalf("expected 2 packages in web group, got %d", len(groups[0].Packages))
	}
	var nginxPkg *models.Package
	for _, p := range groups[0].Packages {
		if p.Name == "nginx" {
			nginxPkg = p
		}
	}
	if nginxPkg == nil || nginxPkg.Version != "1.22" {
		t.Errorf("expected latest nginx version 1.22, got %v", nginxPkg)
	}
}
