package taxonomy

import (
	"testing"

	"github.com/edsilegxrepo/repoview/internal/models"
)

func TestIsKnownArchAndTokens(t *testing.T) {
	for _, arch := range []string{"x86_64", "amd64", "aarch64", "arm64", "riscv64", "loong64", "loongarch64", "noarch", "all"} {
		if !IsKnownArch(arch) {
			t.Errorf("expected IsKnownArch(%q) to be true", arch)
		}
	}
	if IsKnownArch("unknown_arch") {
		t.Errorf("expected IsKnownArch(\"unknown_arch\") to be false")
	}
	if !IsArchToken("binary-riscv64") {
		t.Errorf("expected IsArchToken(\"binary-riscv64\") to be true")
	}
	if len(KnownArchitectures()) < 15 {
		t.Errorf("expected KnownArchitectures to return full list, got %d", len(KnownArchitectures()))
	}
}

func TestDetectPrimaryArchAndFromPackages(t *testing.T) {
	if got := DetectPrimaryArch(nil); got != "x86_64" {
		t.Errorf("expected DetectPrimaryArch(nil) = x86_64, got %q", got)
	}
	if got := DetectPrimaryArch([]string{"noarch", "riscv64", "riscv64", "x86_64"}); got != "riscv64" {
		t.Errorf("expected DetectPrimaryArch dominant = riscv64, got %q", got)
	}
	if got := DetectPrimaryArchFromPackages([]*models.Package{{Arch: "all"}}); got != "all" {
		t.Errorf("expected DetectPrimaryArchFromPackages = all, got %q", got)
	}
}

func TestMatchPathRulesAndDistroAliases(t *testing.T) {
	rules := &Rules{
		PathPatterns: []PathPatternRule{
			{Pattern: "enterprise/{distro}/{channel}/{arch}"},
			{Pattern: "custom-deb/{suite}/{arch}", Distro: "corp-debian", Channel: "internal"},
		},
		DistroAliases: map[string]string{
			"corp-debian": "debian",
			"alma-custom": "almalinux",
		},
	}

	d, c, a, s, ok := MatchPathRules("enterprise/alma-custom/appstream/aarch64", rules)
	if !ok || d != "alma-custom" || c != "appstream" || a != "aarch64" || s != "" {
		t.Fatalf("MatchPathRules enterprise mismatch: ok=%v d=%q c=%q a=%q s=%q", ok, d, c, a, s)
	}

	d2, c2 := InferDistroAndChannelWithRules("custom-deb/bookworm/amd64", rules)
	if d2 != "corp-debian" || c2 != "internal" {
		t.Fatalf("InferDistroAndChannelWithRules override mismatch: got (%q, %q)", d2, c2)
	}

	if icon := MatchDistroIconWithRules("corp-debian", rules); icon != "debian" {
		t.Errorf("expected DistroAliases icon 'debian', got %q", icon)
	}
	if icon := MatchDistroIconWithRules("alma-custom", rules); icon != "almalinux" {
		t.Errorf("expected DistroAliases icon 'almalinux', got %q", icon)
	}
	if td, tc, ta := TokenizeSlug("fedora-41-updates.riscv64"); td != "fedora-41" || tc != "updates" || ta != "riscv64" {
		t.Errorf("TokenizeSlug mismatch: got (%q, %q, %q)", td, tc, ta)
	}
}

func TestTaxonomy_ComprehensiveDistroAndPathVariants(t *testing.T) {
	iconCases := map[string]string{
		"el9":            "redhat",
		"rhel-9":         "redhat",
		"fedora-41":      "fedora",
		"rocky-9":        "rocky",
		"almalinux-9":    "almalinux",
		"centos-stream":  "centos",
		"ubuntu-24.04":   "ubuntu",
		"debian-12":      "debian",
		"archlinux":      "arch",
		"opensuse-leap":  "opensuse",
		"mystery-distro": "generic",
	}
	for d, want := range iconCases {
		if got := MatchDistroIcon(d); got != want {
			t.Errorf("MatchDistroIcon(%q) = %q; want %q", d, got, want)
		}
	}

	archPaths := map[string]string{
		"/repos/el9-base.aarch64":          "aarch64",
		"/repos/fedora-40-riscv64":         "riscv64",
		"/repos/deb/binary-armhf":          "armhf",
		"/repos/no-arch-indicator/packages": "unknown",
	}
	for p, want := range archPaths {
		if got := DetectArchFromPath(p); got != want {
			t.Errorf("DetectArchFromPath(%q) = %q; want %q", p, got, want)
		}
	}

	if d, c := InferDistroAndChannel("/repos/ubu24/custom"); d != "ubu24" || c != "custom" {
		t.Errorf("expected two-level ubuntu inference (ubu24, custom), got (%q, %q)", d, c)
	}
	if d, c := InferDistroAndChannel("/repos/centos-9-extras.x86_64"); d != "centos-9" || c != "extras" {
		t.Errorf("expected slug inference (centos-9, extras), got (%q, %q)", d, c)
	}
	if d, c := InferDistroAndChannel("/dists/bookworm"); d != "bookworm" || c != "main" {
		t.Errorf("expected dists fallback (bookworm, main), got (%q, %q)", d, c)
	}
	if _, _, _, _, ok := MatchPathRules("a/b", &Rules{PathPatterns: []PathPatternRule{{Pattern: "a/b/c"}}}); ok {
		t.Errorf("expected unmatched length-mismatch path pattern")
	}
	if d, c := InferDistroAndChannelWithRules("", nil); d != "generic" || c != "base" {
		t.Errorf("expected empty path fallback (generic, base), got (%q, %q)", d, c)
	}
}
