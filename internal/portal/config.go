package portal

// OBJECTIVES:
// Load, validate, merge, and bootstrap declarative portal.yaml configuration files
// governing auto-discovery limits, taxonomy overrides, pinning, hiding, and external repos.
//
// CORE COMPONENTS:
//   - PortalConfig / AutoDiscoveryConfig / RepoOverrideConfig / LocalRepoConfig: YAML schema models.
//   - LoadConfig / DefaultPortalConfig / FindPortalConfigIn: Bounded config discovery & normalization.
//   - DumpConfigWithOptions: Starter YAML emission with non-clobber guard and --force safe merge.
//   - ApplyConfig: Merges overrides, declared local/external repos, icons, and pinned ordering into Catalog.
//
// FUNCTIONALITY:
//   - Normalizes cross-platform override path separators to forward slashes on load and lookup.
//   - Preserves hand-edited portal.yaml sections, comments, and pinned flags when regenerating with --force.
//
// DATA FLOW:
//   portal.yaml -> LoadConfig -> ApplyConfig(Catalog) -> sorted/enriched portal rendering

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/taxonomy"
)

// --- Portal YAML Schema & Override Models ---

const maxPortalConfigBytes = 2 * 1024 * 1024 // 2 MiB ceiling for portal.yaml

// PortalConfig represents the schema of the optional portal.yaml configuration file.
type PortalConfig struct {
	Title         string                        `yaml:"title"`
	Description   string                        `yaml:"description"`
	BaseURL       string                        `yaml:"base_url,omitempty"`
	PortalURL     string                        `yaml:"portal_url,omitempty"`
	RepoURL       string                        `yaml:"repo_url,omitempty"`
	PortalHome    string                        `yaml:"portal_home,omitempty"`
	InPlace       bool                          `yaml:"in_place,omitempty"`
	AutoDiscovery AutoDiscoveryConfig           `yaml:"auto_discovery"`
	Taxonomy      *taxonomy.Rules               `yaml:"taxonomy,omitempty"`
	Overrides     map[string]RepoOverrideConfig `yaml:"overrides,omitempty"`
	Repositories  []LocalRepoConfig             `yaml:"repositories,omitempty"`
	LocalRepos    []LocalRepoConfig             `yaml:"local_repos,omitempty"`
	Pinned        []string                      `yaml:"pinned,omitempty"`
	ExternalRepos []*DiscoveredRepo             `yaml:"external_repos,omitempty"`
}

// AutoDiscoveryConfig controls repository traversal and pruning behavior.
type AutoDiscoveryConfig struct {
	Enabled         bool     `yaml:"enabled"`
	MaxDepth        int      `yaml:"max_depth"`
	RequireRendered bool     `yaml:"require_rendered"`
	Exclude         []string `yaml:"exclude,omitempty"`
}

// RepoOverrideConfig defines custom display and build overrides for specific repository paths.
type RepoOverrideConfig struct {
	Title         string   `yaml:"title,omitempty"`
	Description   string   `yaml:"description,omitempty"`
	Icon          string   `yaml:"icon,omitempty"`
	Badge         string   `yaml:"badge,omitempty"`
	Distro        string   `yaml:"distro,omitempty"`
	Channel       string   `yaml:"channel,omitempty"`
	Suite         string   `yaml:"suite,omitempty"`
	Components    []string `yaml:"components,omitempty"`
	Arch          string   `yaml:"arch,omitempty"`
	Format        string   `yaml:"format,omitempty"`
	BaseURL       string   `yaml:"base_url,omitempty"`
	GPGKey        string   `yaml:"gpgkey,omitempty"`
	GPGCheck      *bool    `yaml:"gpgcheck,omitempty"`
	IgnorePackage []string `yaml:"ignore_package,omitempty"`
	ExcludeArch   []string `yaml:"exclude_arch,omitempty"`
	Comps         string   `yaml:"comps,omitempty"`
	Hidden        bool     `yaml:"hidden,omitempty"`
	Pinned        bool     `yaml:"pinned,omitempty"`
}

// LocalRepoConfig declares an explicit local repository entry when auto-discovery cannot infer layout
// or when custom output/suite/arch mappings are desired.
type LocalRepoConfig struct {
	Path          string   `yaml:"path"`
	RelPath       string   `yaml:"rel_path,omitempty"`
	TargetURL     string   `yaml:"target_url,omitempty"`
	OutputDir     string   `yaml:"output_dir,omitempty"`
	Title         string   `yaml:"title,omitempty"`
	Description   string   `yaml:"description,omitempty"`
	Distro        string   `yaml:"distro,omitempty"`
	Channel       string   `yaml:"channel,omitempty"`
	Suite         string   `yaml:"suite,omitempty"`
	Components    []string `yaml:"components,omitempty"`
	Arch          string   `yaml:"arch,omitempty"`
	Format        string   `yaml:"format,omitempty"`
	BaseURL       string   `yaml:"base_url,omitempty"`
	GPGKey        string   `yaml:"gpgkey,omitempty"`
	Icon          string   `yaml:"icon,omitempty"`
	Badge         string   `yaml:"badge,omitempty"`
	IgnorePackage []string `yaml:"ignore_package,omitempty"`
	ExcludeArch   []string `yaml:"exclude_arch,omitempty"`
	Comps         string   `yaml:"comps,omitempty"`
	Hidden        bool     `yaml:"hidden,omitempty"`
	Pinned        bool     `yaml:"pinned,omitempty"`
}

// DeclaredRepos returns the combined list of Repositories and LocalRepos.
func (c *PortalConfig) DeclaredRepos() []LocalRepoConfig {
	out := make([]LocalRepoConfig, 0, len(c.Repositories)+len(c.LocalRepos))
	out = append(out, c.Repositories...)
	out = append(out, c.LocalRepos...)
	return out
}

// LookupOverride looks up normalized slash override entries by candidate relative or full paths.
func (c *PortalConfig) LookupOverride(keys ...string) (RepoOverrideConfig, bool) {
	if c == nil || len(c.Overrides) == 0 {
		return RepoOverrideConfig{}, false
	}
	for _, k := range keys {
		if k == "" {
			continue
		}
		norm := filepath.ToSlash(k)
		if ov, ok := c.Overrides[norm]; ok {
			return ov, true
		}
		if ov, ok := c.Overrides[k]; ok {
			return ov, true
		}
	}
	return RepoOverrideConfig{}, false
}

// DefaultPortalConfig returns a PortalConfig populated with production defaults.
func DefaultPortalConfig() *PortalConfig {
	return &PortalConfig{
		Title:       "Enterprise Package Repositories",
		Description: "Unified browsing and distribution catalog for RPM and Debian packages",
		AutoDiscovery: AutoDiscoveryConfig{
			Enabled:         true,
			MaxDepth:        7,
			RequireRendered: false,
			Exclude: []string{
				"*debuginfo*",
				"*testing*",
				"*staging*",
				".git",
			},
		},
		Overrides: make(map[string]RepoOverrideConfig),
	}
}

// LoadConfig reads and validates portal.yaml from path.
func LoadConfig(path string) (*PortalConfig, error) {
	// #nosec G304 -- loading validated configuration file path
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read configuration file: %w", models.ErrConfig, err)
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxPortalConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read configuration file: %w", models.ErrConfig, err)
	}
	if int64(len(data)) > maxPortalConfigBytes {
		return nil, fmt.Errorf("%w: configuration file %s exceeds maximum allowed size (%d bytes)", models.ErrConfig, path, maxPortalConfigBytes)
	}

	cfg := DefaultPortalConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("%w: failed to parse YAML configuration: %w", models.ErrConfig, err)
	}

	if cfg.AutoDiscovery.MaxDepth <= 0 {
		cfg.AutoDiscovery.MaxDepth = 7
	}

	if cfg.PortalURL == "" && cfg.BaseURL != "" {
		cfg.PortalURL = cfg.BaseURL
	}
	if cfg.BaseURL == "" && cfg.PortalURL != "" {
		cfg.BaseURL = cfg.PortalURL
	}

	if len(cfg.Overrides) > 0 {
		normalized := make(map[string]RepoOverrideConfig, len(cfg.Overrides))
		for k, v := range cfg.Overrides {
			normalized[strings.ReplaceAll(filepath.ToSlash(k), "\\", "/")] = v
		}
		cfg.Overrides = normalized
	}

	return cfg, nil
}

// DumpConfigOptions customizes portal starter YAML generation, rule preservation, and safe merging.
type DumpConfigOptions struct {
	ExistingConfig *PortalConfig
	RawRepos       []*DiscoveredRepo
	Force          bool
}

// DumpConfig serializes a discovered Catalog into a starter YAML configuration template.
func DumpConfig(outputPath string, catalog *Catalog) error {
	return DumpConfigWithOptions(outputPath, catalog, DumpConfigOptions{})
}

// DumpConfigWithOptions writes a starter or merged portal.yaml while preserving existing rules,
// emitting full inferred metadata, and refusing to clobber existing files without Force.
func DumpConfigWithOptions(outputPath string, catalog *Catalog, opts DumpConfigOptions) error {
	cleanPath := filepath.Clean(outputPath)
	baseCfg := opts.ExistingConfig

	if fi, err := os.Stat(cleanPath); err == nil {
		if fi.IsDir() {
			return fmt.Errorf("%w: configuration path %s is a directory", models.ErrIO, outputPath)
		}
		if !opts.Force {
			return fmt.Errorf("%w: configuration file %s already exists (use --force to merge/overwrite)", models.ErrSafety, outputPath)
		}
		if baseCfg == nil {
			loaded, loadErr := LoadConfig(cleanPath)
			if loadErr != nil {
				return loadErr
			}
			baseCfg = loaded
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("%w: failed to stat configuration file %s: %w", models.ErrIO, outputPath, err)
	}

	starter := clonePortalConfig(baseCfg)
	if catalog != nil {
		defCfg := DefaultPortalConfig()
		if catalog.Title != "" && (starter.Title == "" || starter.Title == defCfg.Title) {
			starter.Title = catalog.Title
		}
		if catalog.Description != "" && (starter.Description == "" || starter.Description == defCfg.Description) {
			starter.Description = catalog.Description
		}
		if catalog.BaseURL != "" && starter.BaseURL == "" {
			starter.BaseURL = catalog.BaseURL
		}

		reposToDump := opts.RawRepos
		if len(reposToDump) == 0 {
			reposToDump = catalog.Repos
		}

		for _, repo := range reposToDump {
			if repo == nil || repo.RelPath == "" || repo.IsExternal {
				continue
			}
			key := strings.ReplaceAll(filepath.ToSlash(repo.RelPath), "\\", "/")
			icon := repo.Icon
			if icon == "" {
				icon = taxonomy.MatchDistroIconWithRules(repo.Distro, starter.Taxonomy)
			}
			inferred := RepoOverrideConfig{
				Title:       repo.Title,
				Description: repo.Description,
				Icon:        icon,
				Badge:       repo.Badge,
				Distro:      repo.Distro,
				Channel:     repo.Channel,
				Suite:       repo.Suite,
				Components:  append([]string(nil), repo.Components...),
				Arch:        repo.Arch,
				Format:      string(repo.Format),
				BaseURL:     repo.BaseURL,
				GPGKey:      repo.GPGKey,
			}
			if existing, ok := starter.LookupOverride(key, repo.Path); ok {
				starter.Overrides[key] = mergeRepoOverride(existing, inferred)
			} else {
				starter.Overrides[key] = inferred
			}
		}
	}

	data, err := yaml.Marshal(starter)
	if err != nil {
		return fmt.Errorf("failed to marshal starter configuration: %w", err)
	}

	header := "# portal.yaml - RepoView Multi-Repository Portal Configuration\n\n"
	footer := "\n# Optional starter examples (uncomment and customize as needed):\n" +
		"# taxonomy:\n" +
		"#   path_patterns:\n" +
		"#     - pattern: \"corp/{distro}/{channel}/{arch}\"\n" +
		"#   distro_aliases:\n" +
		"#     corp-linux: redhat\n" +
		"# repositories:\n" +
		"#   - path: /path/to/custom/repo\n" +
		"#     rel_path: custom/el9\n" +
		"#     format: rpm\n" +
		"# overrides:\n" +
		"#   some/repo/path:\n" +
		"#     hidden: true\n" +
		"#     ignore_package: [\"*-debuginfo\"]\n" +
		"#     exclude_arch: [\"i686\"]\n"

	content := make([]byte, 0, len(header)+len(data)+len(footer))
	content = append(content, header...)
	content = append(content, data...)
	content = append(content, footer...)

	// #nosec G306 -- writing generated configuration file with standard web readability (0644)
	if err := os.WriteFile(cleanPath, content, 0o644); err != nil {
		return fmt.Errorf("%w: failed to write configuration file %s: %w", models.ErrIO, outputPath, err)
	}

	return nil
}

func clonePortalConfig(src *PortalConfig) *PortalConfig {
	if src == nil {
		return DefaultPortalConfig()
	}
	clone := *src
	clone.AutoDiscovery.Exclude = append([]string(nil), src.AutoDiscovery.Exclude...)
	clone.Pinned = append([]string(nil), src.Pinned...)
	clone.Repositories = append([]LocalRepoConfig(nil), src.Repositories...)
	clone.LocalRepos = append([]LocalRepoConfig(nil), src.LocalRepos...)
	clone.ExternalRepos = append([]*DiscoveredRepo(nil), src.ExternalRepos...)
	if src.Taxonomy != nil {
		taxCopy := *src.Taxonomy
		taxCopy.PathPatterns = append([]taxonomy.PathPatternRule(nil), src.Taxonomy.PathPatterns...)
		if src.Taxonomy.DistroAliases != nil {
			taxCopy.DistroAliases = make(map[string]string, len(src.Taxonomy.DistroAliases))
			for k, v := range src.Taxonomy.DistroAliases {
				taxCopy.DistroAliases[k] = v
			}
		}
		clone.Taxonomy = &taxCopy
	}
	clone.Overrides = make(map[string]RepoOverrideConfig, len(src.Overrides))
	for k, v := range src.Overrides {
		normKey := strings.ReplaceAll(filepath.ToSlash(k), "\\", "/")
		v.Components = append([]string(nil), v.Components...)
		v.IgnorePackage = append([]string(nil), v.IgnorePackage...)
		v.ExcludeArch = append([]string(nil), v.ExcludeArch...)
		clone.Overrides[normKey] = v
	}
	return &clone
}

func mergeRepoOverride(existing, inferred RepoOverrideConfig) RepoOverrideConfig {
	merged := existing
	if merged.Title == "" {
		merged.Title = inferred.Title
	}
	if merged.Description == "" {
		merged.Description = inferred.Description
	}
	if merged.Icon == "" {
		merged.Icon = inferred.Icon
	}
	if merged.Badge == "" {
		merged.Badge = inferred.Badge
	}
	if merged.Distro == "" {
		merged.Distro = inferred.Distro
	}
	if merged.Channel == "" {
		merged.Channel = inferred.Channel
	}
	if merged.Suite == "" {
		merged.Suite = inferred.Suite
	}
	if len(merged.Components) == 0 && len(inferred.Components) > 0 {
		merged.Components = append([]string(nil), inferred.Components...)
	}
	if merged.Arch == "" {
		merged.Arch = inferred.Arch
	}
	if merged.Format == "" {
		merged.Format = inferred.Format
	}
	if merged.BaseURL == "" {
		merged.BaseURL = inferred.BaseURL
	}
	if merged.GPGKey == "" {
		merged.GPGKey = inferred.GPGKey
	}
	if existing.Pinned || inferred.Pinned {
		merged.Pinned = true
	}
	return merged
}

// ApplyConfig applies configuration overrides, pins, external repos, and icon assignments to a Catalog.
func ApplyConfig(catalog *Catalog, cfg *PortalConfig) {
	if catalog == nil || cfg == nil {
		return
	}

	if cfg.Title != "" {
		catalog.Title = cfg.Title
	}
	if cfg.Description != "" {
		catalog.Description = cfg.Description
	}
	if cfg.BaseURL != "" {
		catalog.BaseURL = cfg.BaseURL
	}

	// 1. Apply overrides and default icons to discovered repos, filtering out Hidden entries
	visible := make([]*DiscoveredRepo, 0, len(catalog.Repos))
	for _, repo := range catalog.Repos {
		if ov, ok := cfg.LookupOverride(repo.RelPath, repo.Path); ok {
			applyOverride(repo, ov)
		}
		if repo.Hidden {
			continue
		}
		if repo.Icon == "" {
			repo.Icon = taxonomy.MatchDistroIconWithRules(repo.Distro, cfg.Taxonomy)
		}
		visible = append(visible, repo)
	}
	catalog.Repos = visible

	// 1b. Merge explicitly declared local repositories (DISC-02)
	existingRel := make(map[string]bool, len(catalog.Repos))
	existingPath := make(map[string]bool, len(catalog.Repos))
	for _, r := range catalog.Repos {
		if r.RelPath != "" {
			existingRel[filepath.ToSlash(r.RelPath)] = true
		}
		if r.Path != "" {
			existingPath[filepath.ToSlash(r.Path)] = true
		}
	}
	for _, lr := range cfg.DeclaredRepos() {
		if lr.Hidden {
			continue
		}
		rel := filepath.ToSlash(lr.RelPath)
		if rel == "" {
			rel = filepath.ToSlash(lr.Path)
		}
		normPath := filepath.ToSlash(lr.Path)
		if existingRel[rel] || (normPath != "" && existingPath[normPath]) {
			continue
		}
		fmtVal := models.RepoFormat(strings.ToLower(strings.TrimSpace(lr.Format)))
		if fmtVal == "" {
			fmtVal = models.FormatRPM
		}
		distro := lr.Distro
		channel := lr.Channel
		if distro == "" || channel == "" {
			infD, infC := taxonomy.InferDistroAndChannelWithRules(rel, cfg.Taxonomy)
			if distro == "" {
				distro = infD
			}
			if channel == "" {
				channel = infC
			}
		}
		arch := lr.Arch
		if arch == "" {
			arch = taxonomy.DetectArchFromPath(rel)
		}
		title := lr.Title
		if title == "" {
			title = fmt.Sprintf("%s %s", distro, channel)
		}
		targetURL := lr.TargetURL
		if targetURL == "" {
			targetURL = filepath.ToSlash(filepath.Join(rel, "repoview", "index.html"))
		}
		icon := lr.Icon
		if icon == "" {
			icon = taxonomy.MatchDistroIconWithRules(distro, cfg.Taxonomy)
		}
		catalog.Repos = append(catalog.Repos, &DiscoveredRepo{
			Path:          lr.Path,
			RelPath:       rel,
			TargetURL:     targetURL,
			Title:         title,
			Description:   lr.Description,
			Format:        fmtVal,
			Distro:        distro,
			Channel:       channel,
			Suite:         lr.Suite,
			Components:    lr.Components,
			Arch:          arch,
			Badge:         lr.Badge,
			Icon:          icon,
			BaseURL:       lr.BaseURL,
			GPGKey:        lr.GPGKey,
			IgnorePackage: lr.IgnorePackage,
			ExcludeArch:   lr.ExcludeArch,
			Comps:         lr.Comps,
			IsRendered:    true,
		})
	}

	// 2. Append external repositories
	for _, ext := range cfg.ExternalRepos {
		ext.IsExternal = true
		ext.IsRendered = true
		if ext.Icon == "" {
			ext.Icon = MatchDistroIcon(ext.Distro)
		}
		if ext.TargetURL == "" {
			ext.TargetURL = ext.RelPath
		}
		catalog.Repos = append(catalog.Repos, ext)
	}

	// 3. Apply pinned ordering (honoring both top-level pinned list and per-override pinned: true)
	pinnedSet := make(map[string]int)
	for idx, p := range cfg.Pinned {
		pinnedSet[filepath.ToSlash(p)] = idx
	}

	var pinnedRepos []*DiscoveredRepo
	var unpinnedRepos []*DiscoveredRepo

	for _, r := range catalog.Repos {
		if _, isPinned := pinnedSet[r.RelPath]; isPinned {
			r.Pinned = true
			pinnedRepos = append(pinnedRepos, r)
		} else if _, isPinned := pinnedSet[r.Path]; isPinned {
			r.Pinned = true
			pinnedRepos = append(pinnedRepos, r)
		} else if r.Pinned {
			pinnedRepos = append(pinnedRepos, r)
		} else {
			unpinnedRepos = append(unpinnedRepos, r)
		}
	}

	if len(pinnedRepos) > 0 {
		// Sort pinned repositories by their explicit order in cfg.Pinned when listed; override-pinned preserve stable order
		sort.SliceStable(pinnedRepos, func(i, j int) bool {
			orderI := pinnedOrder(pinnedRepos[i], pinnedSet)
			orderJ := pinnedOrder(pinnedRepos[j], pinnedSet)
			return orderI < orderJ
		})

		reordered := make([]*DiscoveredRepo, 0, len(pinnedRepos)+len(unpinnedRepos))
		reordered = append(reordered, pinnedRepos...)
		reordered = append(reordered, unpinnedRepos...)
		catalog.Repos = reordered
	}

	// 4. Recalculate catalog totals and chips
	distroSet := make(map[string]bool)
	archSet := make(map[string]bool)
	formatSet := make(map[string]bool)
	totalPkgs := 0

	for _, r := range catalog.Repos {
		r.ConfigSnippet = r.BuildConfigSnippet(catalog.BaseURL)
		totalPkgs += r.PackageCount
		if r.Distro != "" {
			distroSet[r.Distro] = true
		}
		if r.Arch != "" {
			archSet[r.Arch] = true
		}
		if r.Format != "" {
			formatSet[string(r.Format)] = true
		}
	}

	catalog.TotalRepos = len(catalog.Repos)
	catalog.TotalPkgs = totalPkgs

	var distros []string
	for d := range distroSet {
		distros = append(distros, d)
	}
	sort.Strings(distros)
	catalog.Distros = distros

	var arches []string
	for a := range archSet {
		arches = append(arches, a)
	}
	sort.Strings(arches)
	catalog.Architectures = arches

	var formats []string
	for f := range formatSet {
		formats = append(formats, f)
	}
	sort.Strings(formats)
	catalog.Formats = formats
}

func applyOverride(repo *DiscoveredRepo, ov RepoOverrideConfig) {
	if ov.Title != "" {
		repo.Title = ov.Title
	}
	if ov.Description != "" {
		repo.Description = ov.Description
	}
	if ov.Icon != "" {
		repo.Icon = ov.Icon
	}
	if ov.Badge != "" {
		repo.Badge = ov.Badge
	}
	if ov.Distro != "" {
		repo.Distro = ov.Distro
	}
	if ov.Channel != "" {
		repo.Channel = ov.Channel
	}
	if ov.Suite != "" {
		repo.Suite = ov.Suite
	}
	if len(ov.Components) > 0 {
		repo.Components = append([]string(nil), ov.Components...)
	}
	if ov.Arch != "" {
		repo.Arch = ov.Arch
	}
	if ov.Format != "" {
		repo.Format = models.RepoFormat(strings.ToLower(strings.TrimSpace(ov.Format)))
	}
	if ov.BaseURL != "" {
		repo.BaseURL = ov.BaseURL
	}
	if ov.GPGKey != "" {
		repo.GPGKey = ov.GPGKey
	}
	if len(ov.IgnorePackage) > 0 {
		repo.IgnorePackage = ov.IgnorePackage
	}
	if len(ov.ExcludeArch) > 0 {
		repo.ExcludeArch = ov.ExcludeArch
	}
	if ov.Comps != "" {
		repo.Comps = ov.Comps
	}
	if ov.Hidden {
		repo.Hidden = true
	}
	if ov.Pinned {
		repo.Pinned = true
	}
}

func pinnedOrder(repo *DiscoveredRepo, pinnedSet map[string]int) int {
	if idx, ok := pinnedSet[repo.RelPath]; ok {
		return idx
	}
	if idx, ok := pinnedSet[repo.Path]; ok {
		return idx
	}
	return 999999
}

// FindPortalConfigIn climbs parent directories looking for portal.yaml or portal.yml.
func FindPortalConfigIn(startDir string) (string, error) {
	curr, err := filepath.Abs(startDir)
	if err != nil {
		curr = startDir
	}

	for i := 0; i < 10; i++ {
		for _, name := range []string{"portal.yaml", "portal.yml"} {
			candidate := filepath.Join(curr, name)
			if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
				return candidate, nil
			}
		}
		parent := filepath.Dir(curr)
		if parent == curr || parent == "." || parent == "" {
			break
		}
		curr = parent
	}
	return "", os.ErrNotExist
}
