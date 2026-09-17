package deb

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DebRepoLocations encapsulates discovered paths to Debian repository metadata.
type DebRepoLocations struct {
	BaseDir       string   // Root repository directory
	PackagesFile  string   // Primary absolute path to Packages (.gz, .xz, .zst, or uncompressed)
	PackagesFiles []string // All discovered Packages files for multi-component aggregation
	ReleaseFile   string   // Absolute path to Release or InRelease (optional in flat repos)
	Suite         string   // Suite name (e.g., "noble", "stable")
	Component     string   // Primary component name (e.g., "main", "universe")
	Components    []string // All discovered components (e.g., ["main", "universe"])
	Arch          string   // Target architecture (e.g., "amd64", "arm64")
	IsFlat        bool     // True if flat directory layout without dists/
}

// Discover resolves the active Debian repository metadata files from a repository path.
// It handles:
//   - Multi-suite/multi-component pool layouts (from root or dists/ hierarchy) with aggregation
//   - Direct pointers to suite or binary-<arch> directories
//   - Flat single-directory repositories
func Discover(repoDir string) (*DebRepoLocations, error) {
	absDir, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to determine absolute path for %s: %w", repoDir, err)
	}

	// 1. Check if repoDir directly contains a Packages file (flat repo or binary-<arch> leaf)
	if pkgFile := findPackagesFile(absDir); pkgFile != "" {
		relFile := findReleaseFile(absDir)

		base := filepath.Base(absDir)
		if strings.HasPrefix(base, "binary-") {
			arch := strings.TrimPrefix(base, "binary-")
			compDir := filepath.Dir(absDir)
			component := filepath.Base(compDir)
			suiteDir := filepath.Dir(compDir)
			suite := filepath.Base(suiteDir)

			if relFile == "" {
				relFile = findReleaseFile(suiteDir)
			}

			// Find repository base dir (parent of dists or root)
			distsDir := filepath.Dir(suiteDir)
			baseDir := absDir
			if filepath.Base(distsDir) == "dists" {
				baseDir = filepath.Dir(distsDir)
			}

			return &DebRepoLocations{
				BaseDir:       baseDir,
				PackagesFile:  pkgFile,
				PackagesFiles: []string{pkgFile},
				ReleaseFile:   relFile,
				Suite:         suite,
				Component:     component,
				Components:    []string{component},
				Arch:          arch,
				IsFlat:        false,
			}, nil
		}

		// Flat repository
		return &DebRepoLocations{
			BaseDir:       absDir,
			PackagesFile:  pkgFile,
			PackagesFiles: []string{pkgFile},
			ReleaseFile:   relFile,
			IsFlat:        true,
		}, nil
	}

	// 2. Check if repoDir contains a dists directory
	distsDir := filepath.Join(absDir, "dists")
	if fi, err := os.Stat(distsDir); err == nil && fi.IsDir() {
		suites, err := os.ReadDir(distsDir)
		if err != nil {
			return nil, fmt.Errorf("failed to read dists directory: %w", err)
		}
		for _, s := range suites {
			if !s.IsDir() {
				continue
			}
			suiteName := s.Name()
			suitePath := filepath.Join(distsDir, suiteName)
			releaseFile := findReleaseFile(suitePath)

			components, err := os.ReadDir(suitePath)
			if err != nil {
				continue
			}

			targetArch := findTargetArch(suitePath, components)
			if targetArch == "" {
				continue
			}

			sortComponents(components)

			var (
				pkgFiles       []string
				compNames      []string
				seenPkgFiles   = make(map[string]bool)
				seenComponents = make(map[string]bool)
			)

			for _, c := range components {
				if !c.IsDir() {
					continue
				}
				compName := c.Name()
				compPath := filepath.Join(suitePath, compName)

				archPath := filepath.Join(compPath, "binary-"+targetArch)
				if pkgFile := findPackagesFile(archPath); pkgFile != "" {
					if !seenPkgFiles[pkgFile] {
						seenPkgFiles[pkgFile] = true
						pkgFiles = append(pkgFiles, pkgFile)
					}
					if !seenComponents[compName] {
						seenComponents[compName] = true
						compNames = append(compNames, compName)
					}
				}

				if targetArch != "all" {
					allPath := filepath.Join(compPath, "binary-all")
					if pkgFile := findPackagesFile(allPath); pkgFile != "" {
						if !seenPkgFiles[pkgFile] {
							seenPkgFiles[pkgFile] = true
							pkgFiles = append(pkgFiles, pkgFile)
						}
					}
				}
			}

			if len(pkgFiles) > 0 {
				return &DebRepoLocations{
					BaseDir:       absDir,
					PackagesFile:  pkgFiles[0],
					PackagesFiles: pkgFiles,
					ReleaseFile:   releaseFile,
					Suite:         suiteName,
					Component:     compNames[0],
					Components:    compNames,
					Arch:          targetArch,
					IsFlat:        false,
				}, nil
			}
		}
	}

	// 3. Check if repoDir is a suite directory containing components
	if entries, err := os.ReadDir(absDir); err == nil {
		targetArch := findTargetArch(absDir, entries)
		if targetArch != "" {
			releaseFile := findReleaseFile(absDir)
			suite := filepath.Base(absDir)
			baseDir := absDir
			parent := filepath.Dir(absDir)
			if filepath.Base(parent) == "dists" {
				baseDir = filepath.Dir(parent)
			}

			sortComponents(entries)

			var (
				pkgFiles       []string
				compNames      []string
				seenPkgFiles   = make(map[string]bool)
				seenComponents = make(map[string]bool)
			)

			for _, c := range entries {
				if !c.IsDir() {
					continue
				}
				compName := c.Name()
				compPath := filepath.Join(absDir, compName)

				archPath := filepath.Join(compPath, "binary-"+targetArch)
				if pkgFile := findPackagesFile(archPath); pkgFile != "" {
					if !seenPkgFiles[pkgFile] {
						seenPkgFiles[pkgFile] = true
						pkgFiles = append(pkgFiles, pkgFile)
					}
					if !seenComponents[compName] {
						seenComponents[compName] = true
						compNames = append(compNames, compName)
					}
				}

				if targetArch != "all" {
					allPath := filepath.Join(compPath, "binary-all")
					if pkgFile := findPackagesFile(allPath); pkgFile != "" {
						if !seenPkgFiles[pkgFile] {
							seenPkgFiles[pkgFile] = true
							pkgFiles = append(pkgFiles, pkgFile)
						}
					}
				}
			}

			if len(pkgFiles) > 0 {
				return &DebRepoLocations{
					BaseDir:       baseDir,
					PackagesFile:  pkgFiles[0],
					PackagesFiles: pkgFiles,
					ReleaseFile:   releaseFile,
					Suite:         suite,
					Component:     compNames[0],
					Components:    compNames,
					Arch:          targetArch,
					IsFlat:        false,
				}, nil
			}
		}

		// 4. Check if repoDir is a component directory containing binary-* dirs
		var archDirs []string
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), "binary-") {
				archDirs = append(archDirs, strings.TrimPrefix(e.Name(), "binary-"))
			}
		}
		if len(archDirs) > 0 {
			sort.Slice(archDirs, func(i, j int) bool {
				if archDirs[i] == "all" && archDirs[j] != "all" {
					return false
				}
				if archDirs[j] == "all" && archDirs[i] != "all" {
					return true
				}
				return archDirs[i] < archDirs[j]
			})
			targetArch := archDirs[0]
			archPath := filepath.Join(absDir, "binary-"+targetArch)
			if pkgFile := findPackagesFile(archPath); pkgFile != "" {
				compName := filepath.Base(absDir)
				suiteDir := filepath.Dir(absDir)
				suite := filepath.Base(suiteDir)
				releaseFile := findReleaseFile(suiteDir)
				baseDir := absDir
				distsDir := filepath.Dir(suiteDir)
				if filepath.Base(distsDir) == "dists" {
					baseDir = filepath.Dir(distsDir)
				}
				pkgFiles := []string{pkgFile}
				if targetArch != "all" {
					allPath := filepath.Join(absDir, "binary-all")
					if allPkg := findPackagesFile(allPath); allPkg != "" {
						pkgFiles = append(pkgFiles, allPkg)
					}
				}
				return &DebRepoLocations{
					BaseDir:       baseDir,
					PackagesFile:  pkgFile,
					PackagesFiles: pkgFiles,
					ReleaseFile:   releaseFile,
					Suite:         suite,
					Component:     compName,
					Components:    []string{compName},
					Arch:          targetArch,
					IsFlat:        false,
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("no Debian repository metadata (Packages or dists/ hierarchy) found in %s", repoDir)
}

func findTargetArch(suitePath string, components []os.DirEntry) string {
	var archs []string
	seen := make(map[string]bool)

	for _, c := range components {
		if !c.IsDir() {
			continue
		}
		compPath := filepath.Join(suitePath, c.Name())
		entries, err := os.ReadDir(compPath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), "binary-") {
				arch := strings.TrimPrefix(e.Name(), "binary-")
				if !seen[arch] {
					seen[arch] = true
					archs = append(archs, arch)
				}
			}
		}
	}

	if len(archs) == 0 {
		return ""
	}

	sort.Slice(archs, func(i, j int) bool {
		if archs[i] == "all" && archs[j] != "all" {
			return false
		}
		if archs[j] == "all" && archs[i] != "all" {
			return true
		}
		return archs[i] < archs[j]
	})

	return archs[0]
}

func sortComponents(components []os.DirEntry) {
	priority := func(name string) int {
		switch name {
		case "main":
			return 0
		case "universe":
			return 1
		case "restricted":
			return 2
		case "multiverse":
			return 3
		case "contrib":
			return 4
		case "non-free":
			return 5
		default:
			return 10
		}
	}
	sort.Slice(components, func(i, j int) bool {
		pI, pJ := priority(components[i].Name()), priority(components[j].Name())
		if pI != pJ {
			return pI < pJ
		}
		return components[i].Name() < components[j].Name()
	})
}

func findPackagesFile(dir string) string {
	candidates := []string{
		"Packages",
		"Packages.gz",
		"Packages.xz",
		"Packages.zst",
	}
	for _, c := range candidates {
		p := filepath.Join(dir, c)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

func findReleaseFile(dir string) string {
	candidates := []string{
		"Release",
		"InRelease",
	}
	for _, c := range candidates {
		p := filepath.Join(dir, c)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}
