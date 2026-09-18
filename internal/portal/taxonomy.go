package portal

import (
	"path/filepath"
	"sort"
	"strings"
)

// Known architectures supported for taxonomy detection and filtering.
var knownArchitectures = map[string]bool{
	"x86_64":  true,
	"amd64":   true,
	"aarch64": true,
	"arm64":   true,
	"armhf":   true,
	"armv7hl": true,
	"i686":    true,
	"i386":    true,
	"s390x":   true,
	"ppc64le": true,
	"riscv64": true,
	"noarch":  true,
	"all":     true,
}

// IsKnownArch returns true if the architecture is in the known architecture dictionary.
func IsKnownArch(arch string) bool {
	return knownArchitectures[strings.ToLower(strings.TrimSpace(arch))]
}

// KnownArchitectures returns a sorted list of all supported architecture strings.
func KnownArchitectures() []string {
	var list []string
	for a := range knownArchitectures {
		list = append(list, a)
	}
	sort.Strings(list)
	return list
}

// DetectPrimaryArch selects the most representative binary architecture from a slice,
// prioritizing non-generic binary architectures over 'noarch' or 'all'.
func DetectPrimaryArch(archList []string) string {
	if len(archList) == 0 {
		return "x86_64"
	}
	counts := make(map[string]int)
	for _, a := range archList {
		counts[a]++
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

// DetectArchFromPath extracts recognized architecture tokens from path segments or slug suffixes.
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

// InferDistroAndChannel extracts distro and channel names from directory layouts:
//   - Debian dists tree: .../dists/<suite>/<component>/...
//   - Hierarchical RPM tree: .../<distro>/<channel>/<arch>
//   - Slug naming: <distro>-<channel>.<arch>
func InferDistroAndChannel(repoDir string) (string, string) {
	slashPath := filepath.ToSlash(repoDir)
	parts := strings.Split(slashPath, "/")
	var nonClean []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			nonClean = append(nonClean, p)
		}
	}
	parts = nonClean
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
	lastPart := parts[len(parts)-1]
	lastPart = strings.TrimPrefix(lastPart, "binary-")
	if knownArchitectures[lastPart] && len(parts) >= 3 {
		channel := parts[len(parts)-2]
		distro := parts[len(parts)-3]
		return distro, channel
	}

	// 3. Hierarchical two-level tree check: .../<distro>/<channel> (e.g. ubu24/custom, deb12/custom)
	if len(parts) >= 2 {
		parent := parts[len(parts)-2]
		if MatchDistroIcon(parent) != "generic" {
			return parent, parts[len(parts)-1]
		}
	}

	// 4. Slug check: e.g. el10-base.x86_64, el-9-x86_64, ubuntu-24.04-x86_64
	base := filepath.Base(repoDir)
	if dotIdx := strings.LastIndex(base, "."); dotIdx != -1 {
		suffix := base[dotIdx+1:]
		if knownArchitectures[suffix] {
			base = base[:dotIdx]
		}
	} else if dashIdx := strings.LastIndex(base, "-"); dashIdx != -1 {
		suffix := base[dashIdx+1:]
		if knownArchitectures[suffix] {
			base = base[:dashIdx]
		}
	}

	commonChannels := []string{"base", "baseos", "extras", "updates", "appstream", "powertools", "crb", "main", "universe", "multiverse", "restricted"}
	for _, ch := range commonChannels {
		if strings.HasSuffix(base, "-"+ch) {
			distro := strings.TrimSuffix(base, "-"+ch)
			return distro, ch
		}
		if strings.HasSuffix(base, "."+ch) {
			distro := strings.TrimSuffix(base, "."+ch)
			return distro, ch
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

// MatchDistroIcon assigns a default SVG icon name based on distro name keywords.
func MatchDistroIcon(distro string) string {
	d := strings.ToLower(distro)
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
