package logic

import (
	"testing"

	"github.com/edsilegxrepo/repoview/internal/models"
)

// TEST STRATEGY:
// Validate package filtering logic against architecture exclusions and package name / NVRA globs.
//
// Test coverage includes:
//   - FilterPackages with empty exclusion lists (passthrough).
//   - FilterPackages with invalid glob pattern syntax (error return).
//   - FilterPackages with architecture exclusion (excluding src, i686).
//   - FilterPackages with simple package name glob (*debuginfo*).
//   - FilterPackages with full NVRA glob (foo-0-1.0-1).

func TestFilterPackages_Passthrough(t *testing.T) {
	pkgs := []*models.Package{
		{Name: "pkg1", Arch: "x86_64"},
		{Name: "pkg2", Arch: "noarch"},
	}

	filtered, err := FilterPackages(pkgs, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(filtered) != 2 {
		t.Errorf("expected 2 packages, got %d", len(filtered))
	}
}

func TestFilterPackages_InvalidGlob(t *testing.T) {
	pkgs := []*models.Package{
		{Name: "pkg1", Arch: "x86_64"},
	}

	_, err := FilterPackages(pkgs, []string{"[-a"}, nil)
	if err == nil {
		t.Errorf("expected error on invalid glob pattern, got nil")
	}
}

func TestFilterPackages_ArchitectureExclusion(t *testing.T) {
	pkgs := []*models.Package{
		{Name: "kernel", Arch: "x86_64"},
		{Name: "kernel-src", Arch: "src"},
		{Name: "glibc32", Arch: "i686"},
		{Name: "python-docs", Arch: "noarch"},
	}

	filtered, err := FilterPackages(pkgs, nil, []string{"src", "i686"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(filtered) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(filtered))
	}
	if filtered[0].Name != "kernel" || filtered[1].Name != "python-docs" {
		t.Errorf("unexpected filtered packages: %v, %v", filtered[0].Name, filtered[1].Name)
	}
}

func TestFilterPackages_NameAndNVRAGlobs(t *testing.T) {
	pkgs := []*models.Package{
		{Name: "nginx", Epoch: "0", Version: "1.20.1", Release: "1.el9", Arch: "x86_64"},
		{Name: "nginx-debuginfo", Epoch: "0", Version: "1.20.1", Release: "1.el9", Arch: "x86_64"},
		{Name: "openssl", Epoch: "1", Version: "3.0.7", Release: "2.el9", Arch: "x86_64"},
		{Name: "test-debug", Epoch: "0", Version: "0.1", Release: "1", Arch: "x86_64"},
	}

	filtered, err := FilterPackages(pkgs, []string{"*-debuginfo", "openssl-1-*"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(filtered) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(filtered))
	}
	if filtered[0].Name != "nginx" || filtered[1].Name != "test-debug" {
		t.Errorf("unexpected filtered packages: %v, %v", filtered[0].Name, filtered[1].Name)
	}
}
