// Package portal provides auto-discovery scanning, metadata aggregation,
// and unified catalog generation for multi-repository hosting.
//
// OBJECTIVES:
// Traverse nested, flat, or dedicated repository trees quickly, prune noisy payload
// subdirectories (pool/, SRPMS/, unrelated debug/), guard against symlink cycles using
// platform file IDs, parse repoview.json with search/path fallbacks, and aggregate
// Debian multi-component suites into unified catalog cards.
//
// CORE COMPONENTS:
//   - ScanConfig / Scanner: Recursive crawler tracking canonical paths and (dev/vol, ino/idx) IDs.
//   - inspectDirectory: Evaluates detached portal home, in-tree descriptors, HTML signatures, and raw mirrors.
//   - AggregateDebianSuites: Consolidates suite components while isolating debug (.ddeb) subtrees.
//
// DATA FLOW:
//
//	RootDir (+ PortalHome) -> Scanner.Scan -> AggregateDebianSuites -> sorted []*DiscoveredRepo
package portal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/taxonomy"
)

// --- Scanner Configuration & Directory Crawler ---

// ScanConfig defines traversal and filtering options for repository auto-discovery.
type ScanConfig struct {
	RootDir         string          // Root directory to crawl (HOME PATH)
	PortalHome      string          // Optional detached portal home directory (containing rendered views)
	MaxDepth        int             // Maximum directory recursion depth (default: 7)
	RequireRendered bool            // If true, filters out unbuilt/raw repositories
	ExcludePatterns []string        // Directory names or glob patterns to skip
	TaxonomyRules   *taxonomy.Rules // Optional custom path patterns and distro icon aliases
}

// Scanner crawls directory trees to locate and classify repositories.
type Scanner struct {
	config        ScanConfig
	visitedPaths  map[string]*DiscoveredRepo // canonical path -> discovered repo
	visitedInodes map[fileID]bool            // dev+ino -> visited
	warnings      []string                   // non-fatal directory read warnings
}

// Warnings returns non-fatal directory traversal warnings recorded during the last Scan.
func (s *Scanner) Warnings() []string {
	return s.warnings
}

// NewScanner creates a new Scanner instance.
func NewScanner(cfg ScanConfig) *Scanner {
	if cfg.MaxDepth <= 0 {
		cfg.MaxDepth = 7
	}
	return &Scanner{
		config:        cfg,
		visitedPaths:  make(map[string]*DiscoveredRepo),
		visitedInodes: make(map[fileID]bool),
	}
}

// Default directory names to immediately prune during traversal.
var defaultExcludedDirs = map[string]bool{
	"pool":      true,
	"SRPMS":     true,
	"debug":     true,
	"source":    true,
	".git":      true,
	".cache":    true,
	".snapshot": true,
	"tmp":       true,
	".tmp":      true,
}

// Scan crawls RootDir and returns all discovered repositories.
func (s *Scanner) Scan() ([]*DiscoveredRepo, error) {
	absRoot, err := filepath.Abs(s.config.RootDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve root directory: %w", err)
	}

	fi, err := os.Stat(absRoot)
	if err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("root directory does not exist or is not a directory: %s", absRoot)
	}

	s.visitedPaths = make(map[string]*DiscoveredRepo)
	s.visitedInodes = make(map[fileID]bool)
	s.warnings = nil

	var discovered []*DiscoveredRepo

	if rootID, ok := getFileID(absRoot, fi); ok {
		s.visitedInodes[rootID] = true
	}
	s.visitedPaths[absRoot] = nil

	if rootRepo, isRepo := s.inspectDirectory(absRoot, "."); isRepo {
		s.visitedPaths[absRoot] = rootRepo
		if !s.config.RequireRendered || rootRepo.IsRendered {
			discovered = append(discovered, rootRepo)
		}
	} else if err := s.crawl(absRoot, ".", 0, absRoot, &discovered); err != nil {
		return nil, fmt.Errorf("error during traversal: %w", err)
	}

	// Option A: Aggregate Debian components by Suite + Arch
	aggregated := AggregateDebianSuites(discovered)

	// Sort deterministically: Distro -> Suite/Channel -> Arch -> RelPath
	sortRepos(aggregated)

	return aggregated, nil
}

// crawl recursively inspects directories and follows directory symlink trees with cycle protection.
func (s *Scanner) crawl(dirPath, virtualRel string, depth int, absRoot string, discovered *[]*DiscoveredRepo) error {
	if depth > s.config.MaxDepth {
		return nil
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		s.warnings = append(s.warnings, fmt.Sprintf("warning: cannot read directory %s: %v", dirPath, err))
		return nil // Inaccessible directory, record warning and continue crawl without halting
	}

	for _, entry := range entries {
		name := entry.Name()

		childPath := filepath.Join(dirPath, name)

		// Never prune root, but prune subdirectories matching default or custom exclusions
		if s.isDirExcluded(name, childPath) {
			continue
		}
		childRel := name
		if virtualRel != "." {
			childRel = filepath.Join(virtualRel, name)
		}

		childDepth := depth + 1
		if childDepth > s.config.MaxDepth {
			continue
		}

		// Determine if directory or symlink to directory
		isDir := entry.IsDir()
		isSymlink := entry.Type()&os.ModeSymlink != 0

		if !isDir && isSymlink {
			if targetFi, err := os.Stat(childPath); err == nil && targetFi.IsDir() {
				isDir = true
			}
		}

		if !isDir {
			continue
		}

		// Resolve canonical path
		canonicalPath, err := filepath.EvalSymlinks(childPath)
		if err != nil {
			canonicalPath = childPath
		}

		// Device+Inode cycle guard (reuse DirEntry info for non-symlinks to avoid extra stat syscalls)
		var fi os.FileInfo
		if isSymlink {
			fi, err = os.Stat(childPath)
		} else {
			fi, err = entry.Info()
		}
		if err == nil && fi != nil {
			if id, ok := getFileID(canonicalPath, fi); ok {
				if s.visitedInodes[id] {
					if existing := s.visitedPaths[canonicalPath]; existing != nil {
						aliasRel := filepath.ToSlash(childRel)
						if aliasRel != existing.RelPath && !containsString(existing.Aliases, aliasRel) {
							existing.Aliases = append(existing.Aliases, aliasRel)
						}
					}
					continue
				}
				s.visitedInodes[id] = true
			}
		}

		// Canonical path cycle and alias guard
		if existing, visited := s.visitedPaths[canonicalPath]; visited {
			if existing != nil {
				aliasRel := filepath.ToSlash(childRel)
				if aliasRel != existing.RelPath && !containsString(existing.Aliases, aliasRel) {
					existing.Aliases = append(existing.Aliases, aliasRel)
				}
			}
			continue
		}

		// Compute relative path for repository inspection
		canRel, err := filepath.Rel(absRoot, canonicalPath)
		if err != nil || strings.HasPrefix(canRel, "..") {
			canRel = childRel
		}

		// Inspect directory for repository signatures
		repo, isRepo := s.inspectDirectory(canonicalPath, canRel)
		if isRepo {
			// If accessed via a symlink, record symlink as alias (avoiding self-alias)
			if isSymlink {
				aliasRel := filepath.ToSlash(childRel)
				if aliasRel != repo.RelPath && !containsString(repo.Aliases, aliasRel) {
					repo.Aliases = append(repo.Aliases, aliasRel)
				}
			}

			s.visitedPaths[canonicalPath] = repo
			if !s.config.RequireRendered || repo.IsRendered {
				*discovered = append(*discovered, repo)
			}

			// Prune directory: once classified as a repository node, never descend into package contents
			continue
		}

		// Mark non-repo visited to prevent loops
		s.visitedPaths[canonicalPath] = nil

		// Recurse into child directory (or symlinked directory tree)
		if err := s.crawl(childPath, childRel, childDepth, absRoot, discovered); err != nil {
			return err
		}
	}

	return nil
}

// inspectDirectory checks whether a directory node matches repository criteria.
func (s *Scanner) inspectDirectory(path, relPath string) (*DiscoveredRepo, bool) {
	// 0. Detached Portal Home check: inspect detached portal home for rendered view/metadata
	if s.config.PortalHome != "" {
		detachedSub := relPath
		if detachedSub == "." || detachedSub == "" {
			detachedSub = "repoview"
		}
		detachedRepoDir := filepath.Join(s.config.PortalHome, detachedSub)

		// Check detached repoview.json
		descCandidates := []string{
			filepath.Join(detachedRepoDir, "repoview.json"),
			filepath.Join(detachedRepoDir, "repoview", "repoview.json"),
		}
		for _, descFile := range descCandidates {
			if fi, err := os.Stat(descFile); err == nil && !fi.IsDir() {
				if desc, err := loadDescriptor(descFile); err == nil {
					targetURL := filepath.ToSlash(filepath.Join(relPath, "index.html"))
					if strings.Contains(descFile, filepath.Join("repoview", "repoview.json")) || relPath == "." || relPath == "" {
						targetURL = filepath.ToSlash(filepath.Join(relPath, "repoview", "index.html"))
						if relPath == "." || relPath == "" {
							targetURL = "repoview/index.html"
						}
					}
					repo := &DiscoveredRepo{
						Path:         path,
						RelPath:      filepath.ToSlash(relPath),
						TargetURL:    targetURL,
						Format:       models.RepoFormat(desc.Format),
						Arch:         desc.Arch,
						Distro:       desc.Distro,
						Channel:      desc.Channel,
						Suite:        desc.Suite,
						Components:   desc.Components,
						Title:        desc.Title,
						Description:  desc.Description,
						BaseURL:      desc.BaseURL,
						GPGKey:       desc.GPGKey,
						Badge:        desc.Badge,
						Icon:         desc.Icon,
						PackageCount: desc.PackageCount,
						LastBuild:    desc.LastBuild,
						IsRendered:   true,
						Descriptor:   desc,
					}
					s.enrichDebianSuite(repo, path)
					return repo, true
				}
			}
		}

		// Check detached index.html without repoview.json
		indexCandidates := []string{
			filepath.Join(detachedRepoDir, "index.html"),
			filepath.Join(detachedRepoDir, "repoview", "index.html"),
		}
		for _, idxFile := range indexCandidates {
			if fi, err := os.Stat(idxFile); err == nil && !fi.IsDir() {
				if isRepoViewRendered(idxFile) {
					format := sniffFormat(path)
					arch := taxonomy.DetectArchFromPath(path)
					if arch == "unknown" || arch == "" {
						if a := detectArchFromDebianMeta(path); a != "" && a != "unknown" {
							arch = a
						} else if a := detectArchFromPackageFiles(path); a != "" && a != "unknown" {
							arch = a
						}
					}
					distro, channel := s.inferDistroAndChannel(path)
					targetURL := filepath.ToSlash(filepath.Join(relPath, "index.html"))
					if strings.Contains(idxFile, filepath.Join("repoview", "index.html")) || relPath == "." || relPath == "" {
						targetURL = filepath.ToSlash(filepath.Join(relPath, "repoview", "index.html"))
						if relPath == "." || relPath == "" {
							targetURL = "repoview/index.html"
						}
					}
					title := extractHTMLTitle(idxFile)
					title = strings.TrimPrefix(title, "RepoView: ")
					title = strings.TrimSpace(title)
					if title == "" || strings.EqualFold(title, "Repoview") {
						title = fmt.Sprintf("%s %s", distro, channel)
					}
					pkgCount, searchArch := parseSearchJSONFirst(filepath.Join(filepath.Dir(idxFile), "search.json"))
					if (arch == "unknown" || arch == "") && searchArch != "" && searchArch != "unknown" {
						arch = searchArch
					}
					repo := &DiscoveredRepo{
						Path:         path,
						RelPath:      filepath.ToSlash(relPath),
						TargetURL:    targetURL,
						Format:       format,
						Arch:         arch,
						Distro:       distro,
						Channel:      channel,
						Title:        title,
						PackageCount: pkgCount,
						LastBuild:    fi.ModTime().UTC(),
						IsRendered:   true,
					}
					s.enrichDebianSuite(repo, path)
					return repo, true
				}
			}
		}
	}

	defaultRawTargetURL := filepath.ToSlash(filepath.Join(relPath, "repoview", "index.html"))
	if s.config.PortalHome != "" {
		if relPath == "." || relPath == "" {
			defaultRawTargetURL = "repoview/index.html"
		} else {
			defaultRawTargetURL = filepath.ToSlash(filepath.Join(relPath, "index.html"))
		}
	}

	// 1. Pillar 1: Check for self-describing repoview.json
	descFile := filepath.Join(path, "repoview", "repoview.json")
	inSubDir := true
	if fi, err := os.Stat(descFile); err != nil || fi.IsDir() {
		descFile = filepath.Join(path, "repoview.json")
		inSubDir = false
	}

	if fi, err := os.Stat(descFile); err == nil && !fi.IsDir() {
		desc, err := loadDescriptor(descFile)
		if err == nil {
			targetURL := filepath.ToSlash(filepath.Join(relPath, "repoview", "index.html"))
			if !inSubDir {
				targetURL = filepath.ToSlash(filepath.Join(relPath, "index.html"))
			}

			repo := &DiscoveredRepo{
				Path:         path,
				RelPath:      filepath.ToSlash(relPath),
				TargetURL:    targetURL,
				Format:       models.RepoFormat(desc.Format),
				Arch:         desc.Arch,
				Distro:       desc.Distro,
				Channel:      desc.Channel,
				Suite:        desc.Suite,
				Components:   desc.Components,
				Title:        desc.Title,
				Description:  desc.Description,
				BaseURL:      desc.BaseURL,
				GPGKey:       desc.GPGKey,
				Badge:        desc.Badge,
				Icon:         desc.Icon,
				PackageCount: desc.PackageCount,
				LastBuild:    desc.LastBuild,
				IsRendered:   true,
				Descriptor:   desc,
			}
			s.enrichDebianSuite(repo, path)
			return repo, true
		}
	}

	// 2. Check for pre-existing rendered view without repoview.json (legacy or un-annotated)
	renderedIndex := filepath.Join(path, "repoview", "index.html")
	renderedInSubDir := true
	if fi, err := os.Stat(renderedIndex); err != nil || fi.IsDir() {
		renderedIndex = filepath.Join(path, "index.html")
		renderedInSubDir = false
	}

	if fi, err := os.Stat(renderedIndex); err == nil && !fi.IsDir() {
		if isRepoViewRendered(renderedIndex) {
			format := sniffFormat(path)
			pkgCount, searchArch := parseSearchJSONFirst(
				filepath.Join(path, "repoview", "search.json"),
				filepath.Join(path, "search.json"),
			)

			arch := taxonomy.DetectArchFromPath(path)
			if arch == "unknown" || arch == "" {
				if searchArch != "" && searchArch != "unknown" {
					arch = searchArch
				} else if a := detectArchFromDebianMeta(path); a != "" && a != "unknown" {
					arch = a
				} else if a := detectArchFromPackageFiles(path); a != "" && a != "unknown" {
					arch = a
				}
			}
			distro, channel := s.inferDistroAndChannel(path)
			targetURL := filepath.ToSlash(filepath.Join(relPath, "repoview", "index.html"))
			if !renderedInSubDir {
				targetURL = filepath.ToSlash(filepath.Join(relPath, "index.html"))
			}

			title := extractHTMLTitle(renderedIndex)
			title = strings.TrimPrefix(title, "RepoView: ")
			title = strings.TrimSpace(title)
			if title == "" || strings.EqualFold(title, "Repoview") {
				title = fmt.Sprintf("%s %s", distro, channel)
			}

			repo := &DiscoveredRepo{
				Path:         path,
				RelPath:      filepath.ToSlash(relPath),
				TargetURL:    targetURL,
				Format:       format,
				Arch:         arch,
				Distro:       distro,
				Channel:      channel,
				Title:        title,
				PackageCount: pkgCount,
				LastBuild:    fi.ModTime().UTC(),
				IsRendered:   true,
			}
			s.enrichDebianSuite(repo, path)
			return repo, true
		}
	}

	// 3. Raw/unrendered RPM repository (repodata directory present)
	repodataPath := filepath.Join(path, "repodata")
	if fi, err := os.Stat(repodataPath); err == nil && fi.IsDir() {
		arch := taxonomy.DetectArchFromPath(path)
		if arch == "unknown" || arch == "" {
			if a := detectArchFromPackageFiles(path); a != "" && a != "unknown" {
				arch = a
			}
		}
		distro, channel := s.inferDistroAndChannel(path)
		targetURL := defaultRawTargetURL

		repo := &DiscoveredRepo{
			Path:       path,
			RelPath:    filepath.ToSlash(relPath),
			TargetURL:  targetURL,
			Format:     models.FormatRPM,
			Arch:       arch,
			Distro:     distro,
			Channel:    channel,
			Title:      fmt.Sprintf("%s %s", distro, channel),
			IsRendered: false,
		}
		return repo, true
	}

	// 4. Raw/unrendered Debian repository leaf (binary-<arch> or Packages files)
	base := filepath.Base(path)
	if strings.HasPrefix(base, "binary-") {
		arch := strings.TrimPrefix(base, "binary-")
		distro, channel := s.inferDistroAndChannel(path)
		targetURL := defaultRawTargetURL

		repo := &DiscoveredRepo{
			Path:       path,
			RelPath:    filepath.ToSlash(relPath),
			TargetURL:  targetURL,
			Format:     models.FormatDEB,
			Arch:       arch,
			Distro:     distro,
			Channel:    channel,
			Title:      fmt.Sprintf("%s %s", distro, channel),
			IsRendered: false,
		}
		s.enrichDebianSuite(repo, path)
		return repo, true
	}

	// Flat Debian package index (Packages, Packages.gz, Packages.bz2, Packages.xz, Packages.zst)
	if hasDebianIndex(path) {
		arch := taxonomy.DetectArchFromPath(path)
		if arch == "unknown" || arch == "" {
			if a := detectArchFromDebianMeta(path); a != "" && a != "unknown" {
				arch = a
			} else if a := detectArchFromPackageFiles(path); a != "" && a != "unknown" {
				arch = a
			}
		}
		distro, channel := s.inferDistroAndChannel(path)
		targetURL := defaultRawTargetURL

		repo := &DiscoveredRepo{
			Path:       path,
			RelPath:    filepath.ToSlash(relPath),
			TargetURL:  targetURL,
			Format:     models.FormatDEB,
			Arch:       arch,
			Distro:     distro,
			Channel:    channel,
			Title:      fmt.Sprintf("%s %s", distro, channel),
			IsRendered: false,
		}
		s.enrichDebianSuite(repo, path)
		return repo, true
	}

	return nil, false
}

// enrichDebianSuite inspects path segments for Debian suite and component classification.
func (s *Scanner) enrichDebianSuite(repo *DiscoveredRepo, path string) {
	if repo.Format != models.FormatDEB {
		return
	}
	slashPath := filepath.ToSlash(path)
	parts := strings.Split(slashPath, "/")

	for i, p := range parts {
		if p == "dists" {
			if i+1 < len(parts) {
				repo.Suite = parts[i+1]
			}
			if i+2 < len(parts) && !strings.HasPrefix(parts[i+2], "binary-") {
				comp := parts[i+2]
				if i+3 < len(parts) && parts[i+3] == "debug" && !strings.HasPrefix(parts[i+3], "binary-") {
					comp += "/debug"
				}
				repo.Components = []string{comp}
			}
			break
		}
	}
}

// AggregateDebianSuites groups Debian repositories sharing the same Suite and Architecture
// into a single unified Suite card (Pillar 5, Option A).
func AggregateDebianSuites(repos []*DiscoveredRepo) []*DiscoveredRepo {
	suiteMap := make(map[string]*DiscoveredRepo)
	var nonDebRepos []*DiscoveredRepo

	for _, repo := range repos {
		if repo.Format != models.FormatDEB || repo.Suite == "" {
			nonDebRepos = append(nonDebRepos, repo)
			continue
		}

		isDebug := isDebugDebianRepo(repo)
		key := fmt.Sprintf("%s|%s|%s|%t", repo.Distro, repo.Suite, repo.Arch, isDebug)
		if existing, exists := suiteMap[key]; exists {
			// Prioritize "main" component for primary Path, RelPath, and TargetURL
			hasMainBefore := containsString(existing.Components, "main")
			incomingHasMain := containsString(repo.Components, "main")
			if incomingHasMain && !hasMainBefore {
				existing.Path = repo.Path
				existing.RelPath = repo.RelPath
				existing.TargetURL = repo.TargetURL
			}

			// Merge component names
			for _, comp := range repo.Components {
				if !containsString(existing.Components, comp) {
					existing.Components = append(existing.Components, comp)
				}
			}
			// Aggregate package count
			existing.PackageCount += repo.PackageCount

			// Use latest build date
			if repo.LastBuild.After(existing.LastBuild) {
				existing.LastBuild = repo.LastBuild
			}

			// Rendered status: true if rendered
			if repo.IsRendered {
				existing.IsRendered = true
			}

			// Merge aliases
			for _, alias := range repo.Aliases {
				if !containsString(existing.Aliases, alias) {
					existing.Aliases = append(existing.Aliases, alias)
				}
			}
		} else {
			// Clone to prevent mutating original
			copyRepo := *repo
			if len(copyRepo.Components) == 0 && copyRepo.Channel != "" {
				copyRepo.Components = []string{copyRepo.Channel}
			}
			suiteMap[key] = &copyRepo
		}
	}

	var result []*DiscoveredRepo
	result = append(result, nonDebRepos...)

	for _, unified := range suiteMap {
		sort.Strings(unified.Components)
		unified.Channel = strings.Join(unified.Components, ", ")
		if unified.Title == "" || strings.HasPrefix(unified.Title, "Repoview") {
			unified.Title = fmt.Sprintf("%s (%s) [%s]", strings.ToUpper(unified.Distro), unified.Suite, unified.Arch)
		}
		result = append(result, unified)
	}

	return result
}

func isDebugDebianRepo(repo *DiscoveredRepo) bool {
	if repo == nil {
		return false
	}
	for _, c := range repo.Components {
		if c == "debug" || strings.HasSuffix(c, "/debug") {
			return true
		}
	}
	slash := filepath.ToSlash(repo.RelPath)
	return strings.Contains(slash, "/debug/binary-")
}

// isDirExcluded checks exclusions with path awareness so Debian APT debug component trees
// (dists/<suite>/<component>/debug/binary-<arch>) are spared while unrelated debug dirs remain pruned.
func (s *Scanner) isDirExcluded(name, childPath string) bool {
	for _, pattern := range s.config.ExcludePatterns {
		if pattern == "" {
			continue
		}
		if matched, _ := filepath.Match(pattern, name); matched {
			return true
		}
	}
	if name == "debug" && hasPathSegment(childPath, "dists") && hasDebugDebianEntries(childPath) {
		return false
	}
	return defaultExcludedDirs[name]
}

func hasPathSegment(p, segment string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == segment {
			return true
		}
	}
	return false
}

func hasDebugDebianEntries(dirPath string) bool {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return false
	}
	for _, e := range entries {
		ename := e.Name()
		if e.IsDir() && strings.HasPrefix(ename, "binary-") {
			return true
		}
		if !e.IsDir() && (ename == "Packages" || strings.HasPrefix(ename, "Packages.")) {
			return true
		}
	}
	return false
}

// loadDescriptor unmarshals repoview.json safely.
func loadDescriptor(path string) (*models.RepoDescriptor, error) {
	// #nosec G304 -- loading discovered repoview.json from validated path
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var desc models.RepoDescriptor
	if err := json.Unmarshal(data, &desc); err != nil {
		return nil, err
	}
	return &desc, nil
}

// isRepoViewRendered checks if index.html contains RepoView signature without being a portal.
func isRepoViewRendered(indexPath string) bool {
	dir := filepath.Dir(indexPath)
	if filepath.Base(dir) == "repoview" {
		if fi, err := os.Stat(filepath.Join(dir, "search.json")); err == nil && !fi.IsDir() {
			return true
		}
		if fi, err := os.Stat(filepath.Join(dir, "layout", "repostyle.css")); err == nil && !fi.IsDir() {
			return true
		}
	}

	// #nosec G304 -- inspecting generated index.html signature
	f, err := os.Open(filepath.Clean(indexPath))
	if err != nil {
		return false
	}
	buf := make([]byte, 2048)
	n, _ := f.Read(buf)
	_ = f.Close()

	content := string(buf[:n])
	if strings.Contains(content, "RepoView-Portal") {
		return false
	}

	return strings.Contains(content, `name="generator" content="RepoView"`) ||
		strings.Contains(content, "repostyle.css") ||
		strings.Contains(content, "repoview-theme") ||
		strings.Contains(content, "RepoView:")
}

// extractHTMLTitle reads <title> tag from index.html.
func extractHTMLTitle(indexPath string) string {
	// #nosec G304 -- reading <title> tag from index.html
	f, err := os.Open(filepath.Clean(indexPath))
	if err != nil {
		return ""
	}
	buf := make([]byte, 2048)
	n, _ := f.Read(buf)
	_ = f.Close()

	content := string(buf[:n])
	start := strings.Index(content, "<title>")
	if start == -1 {
		return ""
	}
	start += 7
	end := strings.Index(content[start:], "</title>")
	if end == -1 {
		return ""
	}
	return strings.TrimSpace(content[start : start+end])
}

// hasDebianIndex checks for existence of Packages, Packages.gz, Packages.bz2, Packages.xz, or Packages.zst.
func hasDebianIndex(dir string) bool {
	for _, name := range []string{"Packages", "Packages.gz", "Packages.bz2", "Packages.xz", "Packages.zst"} {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

// sniffFormat sniffs repository format in a directory.
func sniffFormat(dir string) models.RepoFormat {
	if fi, err := os.Stat(filepath.Join(dir, "repodata")); err == nil && fi.IsDir() {
		return models.FormatRPM
	}
	if hasDebianIndex(dir) || strings.Contains(dir, "dists") || strings.HasPrefix(filepath.Base(dir), "binary-") {
		return models.FormatDEB
	}
	return models.FormatRPM
}

func (s *Scanner) inferDistroAndChannel(repoDir string) (string, string) {
	return taxonomy.InferDistroAndChannelWithRules(repoDir, s.config.TaxonomyRules)
}

func containsString(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

func sortRepos(repos []*DiscoveredRepo) {
	sort.Slice(repos, func(i, j int) bool {
		if repos[i].Distro != repos[j].Distro {
			return repos[i].Distro < repos[j].Distro
		}
		if repos[i].Suite != repos[j].Suite {
			return repos[i].Suite < repos[j].Suite
		}
		if repos[i].Channel != repos[j].Channel {
			return repos[i].Channel < repos[j].Channel
		}
		if repos[i].Arch != repos[j].Arch {
			return repos[i].Arch < repos[j].Arch
		}
		return repos[i].RelPath < repos[j].RelPath
	})
}

// parseSearchJSONFirst tries candidate search.json paths in order and extracts both package count
// and primary architecture in a single read + JSON unmarshal pass.
func parseSearchJSONFirst(paths ...string) (int, string) {
	for _, p := range paths {
		count, arch, ok := parseSearchJSON(p)
		if ok {
			return count, arch
		}
	}
	return 0, ""
}

// parseSearchJSON parses search.json once to return total indexed package count and primary architecture.
func parseSearchJSON(searchPath string) (int, string, bool) {
	// #nosec G304 -- inspecting discovered search.json for package count and arch
	data, err := os.ReadFile(filepath.Clean(searchPath))
	if err != nil {
		return 0, "", false
	}
	var index struct {
		Schema []string        `json:"schema"`
		Data   [][]interface{} `json:"data"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return 0, "", false
	}

	archIdx := -1
	for i, s := range index.Schema {
		if s == "a" {
			archIdx = i
			break
		}
	}
	var primaryArch string
	if archIdx != -1 {
		var archList []string
		for _, row := range index.Data {
			if archIdx < len(row) {
				if a, ok := row[archIdx].(string); ok && a != "" {
					archList = append(archList, a)
				}
			}
		}
		if len(archList) > 0 {
			primaryArch = DetectPrimaryArch(archList)
		}
	}
	return len(index.Data), primaryArch, true
}

// detectArchFromDebianMeta inspects Release or InRelease files in the directory for Architecture(s).
func detectArchFromDebianMeta(repoDir string) string {
	for _, name := range []string{"Release", "InRelease"} {
		path := filepath.Join(repoDir, name)
		// #nosec G304 -- inspecting Release metadata for arch
		f, err := os.Open(filepath.Clean(path))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "Architecture:") {
				val := strings.TrimSpace(strings.TrimPrefix(line, "Architecture:"))
				_ = f.Close()
				if val != "" && val != "all" {
					return val
				}
			}
			if strings.HasPrefix(line, "Architectures:") {
				val := strings.TrimSpace(strings.TrimPrefix(line, "Architectures:"))
				_ = f.Close()
				fields := strings.Fields(val)
				if len(fields) > 0 {
					return DetectPrimaryArch(fields)
				}
			}
		}
		_ = f.Close()
	}
	return ""
}

// detectArchFromPackageFiles inspects package filenames in repoDir to infer architecture.
func detectArchFromPackageFiles(repoDir string) string {
	entries, err := os.ReadDir(filepath.Clean(repoDir))
	if err != nil {
		return ""
	}
	var archList []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".deb") || strings.HasSuffix(name, ".ddeb") {
			ext := ".deb"
			if strings.HasSuffix(name, ".ddeb") {
				ext = ".ddeb"
			}
			base := strings.TrimSuffix(name, ext)
			if idx := strings.LastIndex(base, "_"); idx != -1 {
				arch := base[idx+1:]
				if IsKnownArch(arch) {
					archList = append(archList, arch)
				}
			}
		} else if strings.HasSuffix(name, ".rpm") {
			base := strings.TrimSuffix(name, ".rpm")
			if idx := strings.LastIndex(base, "."); idx != -1 {
				arch := base[idx+1:]
				if IsKnownArch(arch) {
					archList = append(archList, arch)
				}
			}
		}
		if len(archList) >= 50 {
			break
		}
	}
	if len(archList) > 0 {
		return DetectPrimaryArch(archList)
	}
	return ""
}
