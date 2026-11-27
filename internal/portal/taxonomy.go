package portal

import "github.com/edsilegxrepo/repoview/internal/taxonomy"

// IsKnownArch returns true if the architecture is in the known architecture dictionary.
func IsKnownArch(arch string) bool {
	return taxonomy.IsKnownArch(arch)
}

// KnownArchitectures returns a sorted list of all supported architecture strings.
func KnownArchitectures() []string {
	return taxonomy.KnownArchitectures()
}

// DetectPrimaryArch selects the most representative binary architecture from a slice,
// prioritizing non-generic binary architectures over 'noarch' or 'all'.
func DetectPrimaryArch(archList []string) string {
	return taxonomy.DetectPrimaryArch(archList)
}

// DetectArchFromPath extracts recognized architecture tokens from path segments or slug suffixes.
func DetectArchFromPath(repoDir string) string {
	return taxonomy.DetectArchFromPath(repoDir)
}

// InferDistroAndChannel extracts distro and channel names from directory layouts.
func InferDistroAndChannel(repoDir string) (string, string) {
	return taxonomy.InferDistroAndChannel(repoDir)
}

// TokenizeSlug splits a directory slug into its distro, channel, and architecture parts.
func TokenizeSlug(dirName string) (distro, channel, arch string) {
	return taxonomy.TokenizeSlug(dirName)
}

// MatchDistroIcon assigns a default SVG icon name based on distro name keywords.
func MatchDistroIcon(distro string) string {
	return taxonomy.MatchDistroIcon(distro)
}
