package util

// OBJECTIVES:
// Enforce strict filesystem path containment and symlink escape checks across RPM/DEB
// ingestion, template/asset writes, and repository safety guards.
//
// CORE COMPONENTS:
//   - ResolveRealPath: Resolves symlinks for existing paths or deepest existing ancestors.
//   - ResolveContainedPath: Verifies relative paths cannot escape baseDir lexically or via symlinks.
//   - IsPathContained / SamePath: OS-aware directory boundary and equality predicates.
//
// FUNCTIONALITY:
//   - Defeats "../../" traversal and out-of-tree symlinks even when destination leaf
//     directories do not yet exist on disk.
//
// DATA FLOW:
//   (baseDir, relPath) -> filepath.IsLocal -> ResolveRealPath -> IsPathContained -> safe path

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ResolveRealPath resolves symbolic links for path.
// If path (or trailing components) does not yet exist on disk, it resolves symbolic links
// on the deepest existing ancestor directory and appends the remaining components.
func ResolveRealPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve absolute path for %s: %w", path, err)
	}

	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}

	curr := abs
	var missing []string
	for {
		parent := filepath.Dir(curr)
		if parent == curr {
			return abs, nil
		}
		missing = append([]string{filepath.Base(curr)}, missing...)
		curr = parent

		resolvedAncestor, ancErr := filepath.EvalSymlinks(curr)
		if ancErr == nil {
			parts := append([]string{resolvedAncestor}, missing...)
			return filepath.Join(parts...), nil
		}
		if !os.IsNotExist(ancErr) {
			return "", ancErr
		}
	}
}

// ResolveContainedPath joins relPath onto baseDir and verifies that neither lexical path
// traversal nor symbolic links can escape baseDir. Returns the clean joined path inside baseDir.
func ResolveContainedPath(baseDir, relPath string) (string, error) {
	if baseDir == "" {
		return "", fmt.Errorf("base directory cannot be empty")
	}
	if relPath == "" {
		return "", fmt.Errorf("relative path cannot be empty")
	}

	cleanRel := filepath.Clean(filepath.FromSlash(relPath))
	if !filepath.IsLocal(cleanRel) {
		return "", fmt.Errorf("security violation: path %q escapes base directory %s", relPath, baseDir)
	}

	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve base directory %s: %w", baseDir, err)
	}
	fullPath := filepath.Join(absBase, cleanRel)

	realBase, err := ResolveRealPath(absBase)
	if err != nil {
		return "", fmt.Errorf("failed to resolve real base directory %s: %w", baseDir, err)
	}
	realTarget, err := ResolveRealPath(fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve real target path %s: %w", relPath, err)
	}

	if !IsPathContained(realBase, realTarget) {
		return "", fmt.Errorf("security violation: symlink or path %q escapes base directory %s", relPath, baseDir)
	}

	// Preserve caller-relative prefix if baseDir itself was relative, while remaining clean
	if !filepath.IsAbs(baseDir) {
		return filepath.Join(filepath.Clean(baseDir), cleanRel), nil
	}
	return fullPath, nil
}

// IsPathContained returns true if target equals base or is strictly inside base.
// Both paths should already be cleaned/resolved.
func IsPathContained(base, target string) bool {
	if SamePath(base, target) {
		return true
	}
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || filepath.IsLocal(rel)
}

// SamePath compares two filesystem paths, accounting for Windows case-insensitivity.
func SamePath(a, b string) bool {
	ca := filepath.Clean(a)
	cb := filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ca, cb)
	}
	return ca == cb
}
