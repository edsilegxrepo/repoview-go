package logic

import (
	"fmt"
	"path/filepath"

	"github.com/edsilegxrepo/repoview/internal/models"
)

// OBJECTIVES:
// Provide flexible package exclusion mechanisms based on hardware architectures and
// glob-based package name / NVRA patterns, matching legacy repoview options.
//
// CORE COMPONENTS:
//   - FilterPackages: Primary filtering pipeline evaluated before group assignment.
//
// FUNCTIONALITY:
//   - Architecture exclusions (e.g., --exclude-arch src, --exclude-arch i686) filter out
//     packages with matching p.Arch.
//   - Glob exclusions (e.g., --ignore-package "*debuginfo*") match against both the raw
//     package Name and the fully qualified NVRA (name-epoch-version-release) string.
//   - Pre-validates all glob syntax before processing to fail fast on invalid patterns.
//
// DATA FLOW:
//   []*models.Package + []ignoreGlobs + []excludeArches -> FilterPackages -> Filtered []*models.Package

// FilterPackages filters the package list based on exclusion rules.
// It supports:
// 1. Architecture exclusion (e.g., excluding 'src' or 'i686' to reduce noise).
// 2. Package name/EVR globbing (e.g., excluding '*debuginfo*').
//
// The filtering logic matches globs against both the simple package Name AND the
// full NVRA (Name-Version-Release-Arch) string to support complex exclusion patterns.
func FilterPackages(pkgs []*models.Package, ignoreGlobs, excludeArches []string) ([]*models.Package, error) {
	if len(ignoreGlobs) == 0 && len(excludeArches) == 0 {
		return pkgs, nil
	}

	// Pre-validate all glob patterns to fail fast before processing packages
	for _, pattern := range ignoreGlobs {
		if _, err := filepath.Match(pattern, ""); err != nil {
			return nil, fmt.Errorf("invalid ignore-package glob pattern %q: %w", pattern, err)
		}
	}

	excludeArchMap := make(map[string]bool)
	for _, a := range excludeArches {
		excludeArchMap[a] = true
	}

	var filtered []*models.Package

	for _, p := range pkgs {
		// Check architecture exclusion
		if excludeArchMap[p.Arch] {
			continue
		}

		// Check name/EVR glob exclusion
		// Python version does globbing against "name-epoch-version-release" e.g. "foo-0-1.0-1"
		// But usually users just ignore by name e.g. "*debuginfo*"
		// We'll construct the full NVRA string or just name for matching?
		// The help text says: "The globbing will be done against name-epoch-version-release"

		ignored := false
		nvra := p.Name + "-" + p.Epoch + "-" + p.Version + "-" + p.Release
		// Also simple name check is common

		for _, pattern := range ignoreGlobs {
			matched, err := filepath.Match(pattern, nvra)
			if err != nil {
				return nil, err
			}
			if matched {
				ignored = true
				break
			}

			// Also match against just the name as a fallback convenience
			matchedName, err := filepath.Match(pattern, p.Name)
			if err != nil {
				return nil, err
			}
			if matchedName {
				ignored = true
				break
			}

			// Also match against archive filename (e.g. *.deb, *.rpm)
			matchedArchive, err := filepath.Match(pattern, p.ArchiveFilename())
			if err != nil {
				return nil, err
			}
			if matchedArchive {
				ignored = true
				break
			}
		}

		if !ignored {
			filtered = append(filtered, p)
		}
	}

	return filtered, nil
}
