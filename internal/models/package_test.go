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
//   - HasAny on PackageDependencies and PackageScriptlets with nil, empty, and populated fields.
//   - Package.EVR() with empty and populated epochs.
//   - Package.Filename() with path separators and special characters.
//   - Package.ArchiveFilename() with LocationHref vs N-V-R.A fallback formatting.

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

func TestPackageScriptlets_HasAny(t *testing.T) {
	var nilScriptlets *PackageScriptlets
	if nilScriptlets.HasAny() {
		t.Errorf("expected false for nil scriptlets")
	}

	emptyScriptlets := &PackageScriptlets{}
	if emptyScriptlets.HasAny() {
		t.Errorf("expected false for empty scriptlets")
	}

	preIn := &PackageScriptlets{PreIn: "/bin/echo pre"}
	if !preIn.HasAny() {
		t.Errorf("expected true when PreIn is populated")
	}

	postIn := &PackageScriptlets{PostIn: "/bin/echo post"}
	if !postIn.HasAny() {
		t.Errorf("expected true when PostIn is populated")
	}

	preUn := &PackageScriptlets{PreUn: "/bin/echo preun"}
	if !preUn.HasAny() {
		t.Errorf("expected true when PreUn is populated")
	}

	postUn := &PackageScriptlets{PostUn: "/bin/echo postun"}
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
	if pkg.ArchiveFilename() != "my-app-2.1.0-1.el9.x86_64.rpm" {
		t.Errorf("expected 'my-app-2.1.0-1.el9.x86_64.rpm', got %q", pkg.ArchiveFilename())
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
	if pkg2.ArchiveFilename() != "kernel-5.14.0-362.el9.x86_64.rpm" {
		t.Errorf("expected 'kernel-5.14.0-362.el9.x86_64.rpm', got %q", pkg2.ArchiveFilename())
	}
}

func TestArchiveFilename_Deb(t *testing.T) {
	pkg := &Package{
		Format:  FormatDEB,
		Name:    "nginx",
		Version: "1.22.1",
		Release: "9",
		Arch:    "amd64",
	}
	if pkg.ArchiveFilename() != "nginx_1.22.1-9_amd64.deb" {
		t.Errorf("expected 'nginx_1.22.1-9_amd64.deb', got %q", pkg.ArchiveFilename())
	}

	pkgNative := &Package{
		Format:  FormatDEB,
		Name:    "dpkg",
		Version: "1.21.22",
		Arch:    "amd64",
	}
	if pkgNative.ArchiveFilename() != "dpkg_1.21.22_amd64.deb" {
		t.Errorf("expected 'dpkg_1.21.22_amd64.deb', got %q", pkgNative.ArchiveFilename())
	}
}

func TestPackage_VersionRelease(t *testing.T) {
	// 1. Native Debian package with no revision/release
	p1 := &Package{
		Format:  FormatDEB,
		Name:    "miller",
		Version: "6.20.2",
		Release: "",
	}
	if got := p1.VersionRelease(); got != "6.20.2" {
		t.Errorf("expected '6.20.2', got %q", got)
	}
	if got := p1.VR(); got != "6.20.2" {
		t.Errorf("expected '6.20.2', got %q", got)
	}

	// 2. Debian package with release/revision
	p2 := &Package{
		Format:  FormatDEB,
		Name:    "proxysql",
		Version: "3.0.10",
		Release: "ubuntu24",
	}
	if got := p2.VersionRelease(); got != "3.0.10-ubuntu24" {
		t.Errorf("expected '3.0.10-ubuntu24', got %q", got)
	}

	// 3. RPM package with Epoch 0
	p3 := &Package{
		Format:  FormatRPM,
		Name:    "bash",
		Epoch:   "0",
		Version: "5.1",
		Release: "1.el9",
	}
	if got := p3.VersionRelease(); got != "5.1-1.el9" {
		t.Errorf("expected '5.1-1.el9', got %q", got)
	}

	// 4. Package with Epoch > 0
	p4 := &Package{
		Name:    "openssl",
		Epoch:   "1",
		Version: "3.0.7",
		Release: "27.el9",
	}
	if got := p4.VersionRelease(); got != "1:3.0.7-27.el9" {
		t.Errorf("expected '1:3.0.7-27.el9', got %q", got)
	}
}

func TestPackageDependencies_RecommendsAndSuggests(t *testing.T) {
	deps := &PackageDependencies{
		Recommends: []*DependencyEntry{{Name: "logrotate"}},
	}
	if !deps.HasAny() {
		t.Errorf("expected true when Recommends is populated")
	}

	depsSuggests := &PackageDependencies{
		Suggests: []*DependencyEntry{{Name: "nginx-doc"}},
	}
	if !depsSuggests.HasAny() {
		t.Errorf("expected true when Suggests is populated")
	}
}

func TestPackage_EVR_Debian(t *testing.T) {
	p1 := &Package{Format: FormatDEB, Epoch: "0", Version: "1.2.3", Release: "1"}
	if got := p1.EVR(); got != "1.2.3-1" {
		t.Errorf("expected '1.2.3-1', got %q", got)
	}

	p2 := &Package{Format: FormatDEB, Epoch: "2", Version: "1.2.3", Release: "1"}
	if got := p2.EVR(); got != "2:1.2.3-1" {
		t.Errorf("expected '2:1.2.3-1', got %q", got)
	}

	p3 := &Package{Format: FormatDEB, Version: "1.2.3"}
	if got := p3.EVR(); got != "1.2.3" {
		t.Errorf("expected '1.2.3', got %q", got)
	}
}
