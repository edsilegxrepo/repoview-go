// Package taxonomy provides unified architecture, distribution, channel, and
// icon classification helpers shared between single-repository builds and the multi-repo portal.
//
// OBJECTIVES:
// Centralize repository classification rules so repoview builds and portal scans
// agree consistently on architectures, OS families, release channels, and SVG icons.
//
// CORE COMPONENTS:
//   - PathPatternRule / Rules: Declarative custom path-matching & distro alias config.
//   - DetectPrimaryArch / DetectArchFromPath: Concrete hardware architecture resolution.
//   - InferDistroAndChannelWithRules / MatchPathRules: Hierarchy and slug parsers.
//   - MatchDistroIconWithRules: Embedded distribution badge/icon resolver.
//
// FUNCTIONALITY:
//   - Evaluates custom path_patterns first, then falls back cleanly across APT dists,
//     3-level RPM hierarchies, 2-level layouts, and hyphen/dot directory slugs.
//
// DATA FLOW:
//
//	repo path / []*models.Package + Rules -> InferDistroAndChannelWithRules -> distro, channel, arch, icon
package taxonomy

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/edsilegxrepo/repoview/internal/models"
)

// --- Declarative Configuration Types ---

// PathPatternRule allows users to declare custom enterprise repository path layouts
// in portal.yaml (for example "repos/{distro}/{channel}/{arch}").
type PathPatternRule struct {
	Pattern string `yaml:"pattern"`
	Distro  string `yaml:"distro,omitempty"`
	Channel string `yaml:"channel,omitempty"`
	Arch    string `yaml:"arch,omitempty"`
	Suite   string `yaml:"suite,omitempty"`
}

// Rules holds optional declarative taxonomy overrides from configuration files.
type Rules struct {
	PathPatterns  []PathPatternRule `yaml:"path_patterns,omitempty"`
	DistroAliases map[string]string `yaml:"distro_aliases,omitempty"`
}

// --- Architecture Recognition & Primary Selection ---

var knownArchitectures = map[string]bool{
	"x86_64":      true,
	"amd64":       true,
	"aarch64":     true,
	"arm64":       true,
	"armhf":       true,
	"armel":       true,
	"armv7hl":     true,
	"i686":        true,
	"i386":        true,
	"s390x":       true,
	"ppc64le":     true,
	"ppc64el":     true,
	"riscv64":     true,
	"loong64":     true,
	"loongarch64": true,
	"sparc64":     true,
	"mips64el":    true,
	"noarch":      true,
	"all":         true,
}

// IsKnownArch returns true if arch is a recognized package architecture identifier.
func IsKnownArch(arch string) bool {
	return knownArchitectures[strings.ToLower(strings.TrimSpace(arch))]
}

// IsArchToken reports whether a directory segment (e.g. "x86_64" or "binary-amd64") is an arch token.
func IsArchToken(token string) bool {
	return IsKnownArch(strings.TrimPrefix(token, "binary-"))
}

// KnownArchitectures returns a sorted slice of all recognized architecture strings.
func KnownArchitectures() []string {
	list := make([]string, 0, len(knownArchitectures))
	for a := range knownArchitectures {
		list = append(list, a)
	}
	sort.Strings(list)
	return list
}

// DetectPrimaryArch selects the most representative binary architecture from a slice,
// prioritizing non-generic binary architectures over "noarch" or "all".
func DetectPrimaryArch(archList []string) string {
	if len(archList) == 0 {
		return "x86_64"
	}
	counts := make(map[string]int)
	for _, a := range archList {
		norm := strings.ToLower(strings.TrimSpace(a))
		if norm != "" {
			counts[norm]++
		}
	}

	bestArch := ""
	bestCount := -1
	for a, count := range counts {
		if a != "noarch" && a != "all" && a != "src" && a != "" {
			if count > bestCount {
				bestArch = a
				bestCount = count
			}
		}
	}
	if bestArch != "" {
		return bestArch
	}
	for a := range counts {
		if a != "src" && a != "" {
			return a
		}
	}
	return "noarch"
}

// DetectPrimaryArchFromPackages selects the dominant architecture from a slice of packages.
func DetectPrimaryArchFromPackages(pkgs []*models.Package) string {
	if len(pkgs) == 0 {
		return "all"
	}
	arches := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		if p != nil {
			arches = append(arches, p.Arch)
		}
	}
	arch := DetectPrimaryArch(arches)
	if len(arches) == 0 {
		return "all"
	}
	return arch
}

// DetectArchFromPath extracts recognized architecture tokens from path segments or slug suffixes.
// Returns "unknown" when no architecture segment can be inferred.
func DetectArchFromPath(repoDir string) string {
	parts := strings.Split(filepath.ToSlash(repoDir), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		part := strings.TrimSpace(parts[i])
		if part == "" {
			continue
		}
		part = strings.TrimPrefix(part, "binary-")
		if knownArchitectures[part] {
			return part
		}
		if dotIdx := strings.LastIndex(part, "."); dotIdx != -1 {
			candidate := part[dotIdx+1:]
			if knownArchitectures[candidate] {
				return candidate
			}
		}
		if dashIdx := strings.LastIndex(part, "-"); dashIdx != -1 {
			candidate := part[dashIdx+1:]
			if knownArchitectures[candidate] {
				return candidate
			}
		}
	}
	return "unknown"
}

// --- Path Rules, Distro & Channel Inference ---

// InferDistroAndChannel extracts distro and channel names from directory layouts.
func InferDistroAndChannel(repoDir string) (string, string) {
	return InferDistroAndChannelWithRules(repoDir, nil)
}

// MatchPathRules matches a repository path against declarative PathPatternRules and returns
// extracted distro, channel, arch, and suite values on the first matching rule.
func MatchPathRules(repoPath string, rules *Rules) (distro, channel, arch, suite string, matched bool) {
	if rules == nil || len(rules.PathPatterns) == 0 {
		return "", "", "", "", false
	}

	segments := splitNonEmpty(filepath.ToSlash(repoPath))
	for _, rule := range rules.PathPatterns {
		patSegs := splitNonEmpty(filepath.ToSlash(rule.Pattern))
		if len(patSegs) == 0 || len(patSegs) != len(segments) {
			continue
		}
		captures := make(map[string]string)
		ok := true
		for i, pat := range patSegs {
			seg := segments[i]
			if strings.HasPrefix(pat, "{") && strings.HasSuffix(pat, "}") {
				key := strings.TrimSuffix(strings.TrimPrefix(pat, "{"), "}")
				captures[key] = seg
				continue
			}
			if m, _ := filepath.Match(pat, seg); !m {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		d := captures["distro"]
		if rule.Distro != "" {
			d = rule.Distro
		}
		c := captures["channel"]
		if rule.Channel != "" {
			c = rule.Channel
		}
		a := captures["arch"]
		if rule.Arch != "" {
			a = rule.Arch
		}
		s := captures["suite"]
		if rule.Suite != "" {
			s = rule.Suite
		}
		return d, c, a, s, true
	}
	return "", "", "", "", false
}

// InferDistroAndChannelWithRules extracts distro and channel using optional custom PathPatterns first,
// then falling back to standard Debian dists, hierarchical RPM trees, and directory slugs.
func InferDistroAndChannelWithRules(repoDir string, rules *Rules) (string, string) {
	if d, c, _, _, ok := MatchPathRules(repoDir, rules); ok {
		if d == "" {
			d = "generic"
		}
		if c == "" {
			c = "base"
		}
		return d, c
	}

	parts := splitNonEmpty(filepath.ToSlash(repoDir))
	if len(parts) == 0 {
		return "generic", "base"
	}

	// 1. Debian layout check: .../dists/<suite>/<component>/...
	for i, part := range parts {
		if part == "dists" {
			distro := ""
			if i > 0 {
				distro = parts[i-1]
			}
			suite := ""
			if i+1 < len(parts) {
				suite = parts[i+1]
			}
			component := "main"
			if i+2 < len(parts) && !strings.HasPrefix(parts[i+2], "binary-") {
				component = parts[i+2]
			}
			if distro == "" {
				if suite != "" {
					distro = suite
				} else {
					distro = "debian"
				}
			}
			return distro, component
		}
	}

	// 2. Hierarchical RPM tree check: .../<distro>/<channel>/<arch>
	lastPart := strings.TrimPrefix(parts[len(parts)-1], "binary-")
	if knownArchitectures[lastPart] && len(parts) >= 3 {
		channel := parts[len(parts)-2]
		distro := parts[len(parts)-3]
		return distro, channel
	}

	// 3. Hierarchical two-level tree check: .../<distro>/<channel> (e.g. ubu24/custom, deb12/custom)
	if len(parts) >= 2 {
		parent := parts[len(parts)-2]
		if MatchDistroIconWithRules(parent, rules) != "generic" {
			return parent, parts[len(parts)-1]
		}
	}

	// 4. Slug check: e.g. el10-base.x86_64, el-9-x86_64, ubuntu-24.04-x86_64
	base := filepath.Base(repoDir)
	if dotIdx := strings.LastIndex(base, "."); dotIdx != -1 {
		if knownArchitectures[base[dotIdx+1:]] {
			base = base[:dotIdx]
		}
	} else if dashIdx := strings.LastIndex(base, "-"); dashIdx != -1 {
		if knownArchitectures[base[dashIdx+1:]] {
			base = base[:dashIdx]
		}
	}

	commonChannels := []string{"base", "baseos", "extras", "updates", "appstream", "powertools", "crb", "main", "universe", "multiverse", "restricted"}
	for _, ch := range commonChannels {
		if strings.HasSuffix(base, "-"+ch) {
			return strings.TrimSuffix(base, "-"+ch), ch
		}
		if strings.HasSuffix(base, "."+ch) {
			return strings.TrimSuffix(base, "."+ch), ch
		}
	}

	return base, "base"
}

// TokenizeSlug splits a directory slug into its distro, channel, and architecture parts.
func TokenizeSlug(dirName string) (distro, channel, arch string) {
	arch = DetectArchFromPath(dirName)
	clean := dirName
	if arch != "unknown" {
		clean = strings.TrimSuffix(clean, "."+arch)
		clean = strings.TrimSuffix(clean, "-"+arch)
	}
	distro, channel = InferDistroAndChannel(clean)
	return distro, channel, arch
}

// --- Branding Icon Matching & Helpers ---

// MatchDistroIcon assigns a default SVG icon name based on distro name keywords.
func MatchDistroIcon(distro string) string {
	return MatchDistroIconWithRules(distro, nil)
}

// MatchDistroIconWithRules assigns an SVG icon name, honoring custom distro_aliases before defaults.
func MatchDistroIconWithRules(distro string, rules *Rules) string {
	d := strings.ToLower(strings.TrimSpace(distro))
	if rules != nil && len(rules.DistroAliases) > 0 {
		for alias, icon := range rules.DistroAliases {
			if strings.EqualFold(alias, d) || strings.Contains(d, strings.ToLower(alias)) {
				return icon
			}
		}
	}

	switch {
	case strings.Contains(d, "el8") || strings.Contains(d, "el9") || strings.Contains(d, "el10") ||
		strings.Contains(d, "rhel") || strings.Contains(d, "redhat") || strings.Contains(d, "enterprise"):
		return "redhat"
	case strings.Contains(d, "fedora"):
		return "fedora"
	case strings.Contains(d, "rocky"):
		return "rocky"
	case strings.Contains(d, "alma"):
		return "almalinux"
	case strings.Contains(d, "centos"):
		return "centos"
	case strings.Contains(d, "ubuntu") || strings.HasPrefix(d, "ubu"):
		return "ubuntu"
	case strings.Contains(d, "debian") || strings.HasPrefix(d, "deb"):
		return "debian"
	case strings.Contains(d, "arch") || strings.Contains(d, "manjaro"):
		return "arch"
	case strings.Contains(d, "suse") || strings.Contains(d, "opensuse"):
		return "opensuse"
	default:
		return "generic"
	}
}

func splitNonEmpty(slashPath string) []string {
	parts := strings.Split(slashPath, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
