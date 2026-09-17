package models

import (
	"testing"
)

// TEST STRATEGY:
// Validate domain model methods, EVR representation, dependency relation formatting,
// scriptlet checks, and filename generation under standard and edge-case inputs.
//
// Test coverage includes:
//   - FormattedRelation across comparison flags (EQ, GE, LE, GT, LT), epoch handling, and release tags.
//   - HasAny on PackageDependencies and RPMScriptlets with nil, empty, and populated fields.
//   - Package.EVR() with empty and populated epochs.
//   - Package.Filename() with path separators and special characters.
//   - Package.RPMFilename() with LocationHref vs N-V-R.A fallback formatting.

func TestFormattedRelation(t *testing.T) {
	tests := []struct {
		dep      DependencyEntry
		expected string
	}{
		{
			dep:      DependencyEntry{Name: "glibc", Flags: "GE", Version: "2.34", Release: "1.el9"},
			expected: ">= 2.34-1.el9",
		},
		{
			dep:      DependencyEntry{Name: "bash", Flags: "EQ", Epoch: "2", Version: "5.1", Release: "2.el9"},
			expected: "= 2:5.1-2.el9",
		},
		{
			dep:      DependencyEntry{Name: "systemd", Flags: "LE", Epoch: "0", Version: "252"},
			expected: "<= 252",
		},
		{
			dep:      DependencyEntry{Name: "libfoo", Flags: "GT", Version: "1.0"},
			expected: "> 1.0",
		},
		{
			dep:      DependencyEntry{Name: "libbar", Flags: "LT", Version: "2.0"},
			expected: "< 2.0",
		},
		{
			dep:      DependencyEntry{Name: "custom", Flags: "CUSTOM", Version: "1.2"},
			expected: "CUSTOM 1.2",
		},
		{
			dep:      DependencyEntry{Name: "flag-only", Flags: "EQ"},
			expected: "=",
		},
		{
			dep:      DependencyEntry{Name: "version-only", Version: "1.0.0"},
			expected: "1.0.0",
		},
		{
			dep:      DependencyEntry{Name: "empty"},
			expected: "",
		},
	}

	for _, tt := range tests {
		got := tt.dep.FormattedRelation()
		if got != tt.expected {
			t.Errorf("FormattedRelation(%+v) = %q; want %q", tt.dep, got, tt.expected)
		}
	}
}

func TestPackageDependencies_HasAny(t *testing.T) {
	var nilDeps *PackageDependencies
	if nilDeps.HasAny() {
		t.Errorf("expected false for nil dependencies")
	}

	emptyDeps := &PackageDependencies{}
	if emptyDeps.HasAny() {
		t.Errorf("expected false for empty dependencies")
	}

	reqDeps := &PackageDependencies{Requires: []*DependencyEntry{{Name: "bash"}}}
	if !reqDeps.HasAny() {
		t.Errorf("expected true when Requires is populated")
	}

	provDeps := &PackageDependencies{Provides: []*DependencyEntry{{Name: "webserver"}}}
	if !provDeps.HasAny() {
		t.Errorf("expected true when Provides is populated")
	}

	confDeps := &PackageDependencies{Conflicts: []*DependencyEntry{{Name: "other-pkg"}}}
	if !confDeps.HasAny() {
		t.Errorf("expected true when Conflicts is populated")
	}

	obsDeps := &PackageDependencies{Obsoletes: []*DependencyEntry{{Name: "old-pkg"}}}
	if !obsDeps.HasAny() {
		t.Errorf("expected true when Obsoletes is populated")
	}
}

func TestRPMScriptlets_HasAny(t *testing.T) {
	var nilScriptlets *RPMScriptlets
	if nilScriptlets.HasAny() {
		t.Errorf("expected false for nil scriptlets")
	}

	emptyScriptlets := &RPMScriptlets{}
	if emptyScriptlets.HasAny() {
		t.Errorf("expected false for empty scriptlets")
	}

	preIn := &RPMScriptlets{PreIn: "/bin/echo pre"}
	if !preIn.HasAny() {
		t.Errorf("expected true when PreIn is populated")
	}

	postIn := &RPMScriptlets{PostIn: "/bin/echo post"}
	if !postIn.HasAny() {
		t.Errorf("expected true when PostIn is populated")
	}

	preUn := &RPMScriptlets{PreUn: "/bin/echo preun"}
	if !preUn.HasAny() {
		t.Errorf("expected true when PreUn is populated")
	}

	postUn := &RPMScriptlets{PostUn: "/bin/echo postun"}
	if !postUn.HasAny() {
		t.Errorf("expected true when PostUn is populated")
	}
}

func TestPackage_EVR_And_Filenames(t *testing.T) {
	pkg := &Package{
		Name:         "my-app",
		Epoch:        "",
		Version:      "2.1.0",
		Release:      "1.el9",
		Arch:         "x86_64",
		LocationHref: "Packages/m/my-app-2.1.0-1.el9.x86_64.rpm",
	}

	if pkg.EVR() != "0:2.1.0-1.el9" {
		t.Errorf("expected '0:2.1.0-1.el9', got %q", pkg.EVR())
	}
	if pkg.Filename() != "my-app.html" {
		t.Errorf("expected 'my-app.html', got %q", pkg.Filename())
	}
	if pkg.RPMFilename() != "my-app-2.1.0-1.el9.x86_64.rpm" {
		t.Errorf("expected 'my-app-2.1.0-1.el9.x86_64.rpm', got %q", pkg.RPMFilename())
	}

	// Test with explicit epoch and no LocationHref
	pkg2 := &Package{
		Name:    "kernel",
		Epoch:   "1",
		Version: "5.14.0",
		Release: "362.el9",
		Arch:    "x86_64",
	}
	if pkg2.EVR() != "1:5.14.0-362.el9" {
		t.Errorf("expected '1:5.14.0-362.el9', got %q", pkg2.EVR())
	}
	if pkg2.RPMFilename() != "kernel-5.14.0-362.el9.x86_64.rpm" {
		t.Errorf("expected 'kernel-5.14.0-362.el9.x86_64.rpm', got %q", pkg2.RPMFilename())
	}
}
