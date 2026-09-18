package portal

import (
	"testing"
)

func TestTaxonomy_IsKnownArch(t *testing.T) {
	for _, arch := range []string{"x86_64", "amd64", "aarch64", "arm64", "armhf", "i686", "noarch", "all"} {
		if !IsKnownArch(arch) {
			t.Errorf("expected %s to be recognized as known architecture", arch)
		}
	}
	for _, invalid := range []string{"xyz", "win32", "darwin"} {
		if IsKnownArch(invalid) {
			t.Errorf("expected %s to NOT be recognized", invalid)
		}
	}
	arches := KnownArchitectures()
	if len(arches) < 10 {
		t.Errorf("expected KnownArchitectures to return full list, got %d", len(arches))
	}
}

func TestTaxonomy_DetectPrimaryArch(t *testing.T) {
	tests := []struct {
		input    []string
		expected string
	}{
		{[]string{}, "x86_64"},
		{[]string{"noarch", "noarch"}, "noarch"},
		{[]string{"noarch", "x86_64", "noarch"}, "x86_64"},
		{[]string{"aarch64", "aarch64", "noarch"}, "aarch64"},
		{[]string{"all", "amd64"}, "amd64"},
	}

	for _, tt := range tests {
		got := DetectPrimaryArch(tt.input)
		if got != tt.expected {
			t.Errorf("DetectPrimaryArch(%v) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func TestTaxonomy_DetectArchFromPath(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"el9/base/x86_64", "x86_64"},
		{"ubuntu/dists/noble/main/binary-amd64", "amd64"},
		{"fedora/updates/aarch64/repodata", "aarch64"},
		{"el10-base.x86_64", "x86_64"},
		{"debian-bookworm-arm64", "arm64"},
		{"unknown/random/path", "unknown"},
	}

	for _, tt := range tests {
		got := DetectArchFromPath(tt.path)
		if got != tt.expected {
			t.Errorf("DetectArchFromPath(%q) = %q; want %q", tt.path, got, tt.expected)
		}
	}
}

func TestTaxonomy_InferDistroAndChannel(t *testing.T) {
	tests := []struct {
		path        string
		wantDistro  string
		wantChannel string
	}{
		{"el9/base/x86_64", "el9", "base"},
		{"centos8/appstream/x86_64", "centos8", "appstream"},
		{"ubuntu/dists/noble/main/binary-amd64", "ubuntu", "main"},
		{"dists/bookworm/contrib/binary-amd64", "bookworm", "contrib"},
		{"el10-base.x86_64", "el10", "base"},
		{"rocky9-extras.aarch64", "rocky9", "extras"},
		{"fedora-updates.x86_64", "fedora", "updates"},
		{"ubu24/custom", "ubu24", "custom"},
		{"deb12/custom", "deb12", "custom"},
		{"", "generic", "base"},
	}

	for _, tt := range tests {
		distro, channel := InferDistroAndChannel(tt.path)
		if distro != tt.wantDistro || channel != tt.wantChannel {
			t.Errorf("InferDistroAndChannel(%q) = (%q, %q); want (%q, %q)",
				tt.path, distro, channel, tt.wantDistro, tt.wantChannel)
		}
	}
}

func TestTaxonomy_TokenizeSlug(t *testing.T) {
	distro, channel, arch := TokenizeSlug("el10-base.x86_64")
	if distro != "el10" || channel != "base" || arch != "x86_64" {
		t.Errorf("TokenizeSlug() = (%q, %q, %q); want (el10, base, x86_64)", distro, channel, arch)
	}

	distro2, channel2, arch2 := TokenizeSlug("ubuntu-main-amd64")
	if distro2 != "ubuntu" || channel2 != "main" || arch2 != "amd64" {
		t.Errorf("TokenizeSlug() = (%q, %q, %q); want (ubuntu, main, amd64)", distro2, channel2, arch2)
	}
}

func TestTaxonomy_MatchDistroIcon(t *testing.T) {
	tests := []struct {
		distro   string
		expected string
	}{
		{"el9", "redhat"},
		{"rhel8", "redhat"},
		{"ubuntu-24.04", "ubuntu"},
		{"ubu24", "ubuntu"},
		{"debian-12", "debian"},
		{"deb12", "debian"},
		{"fedora-40", "fedora"},
		{"rocky-linux", "rocky"},
		{"almalinux", "almalinux"},
		{"centos-stream", "centos"},
		{"archlinux", "arch"},
		{"opensuse-leap", "opensuse"},
		{"unknown-custom", "generic"},
	}

	for _, tt := range tests {
		got := MatchDistroIcon(tt.distro)
		if got != tt.expected {
			t.Errorf("MatchDistroIcon(%q) = %q; want %q", tt.distro, got, tt.expected)
		}
	}
}
