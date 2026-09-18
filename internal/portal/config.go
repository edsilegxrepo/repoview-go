package portal

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// PortalConfig represents the schema of the optional portal.yaml configuration file.
type PortalConfig struct {
	Title         string                        `yaml:"title"`
	Description   string                        `yaml:"description"`
	BaseURL       string                        `yaml:"base_url,omitempty"`
	AutoDiscovery AutoDiscoveryConfig           `yaml:"auto_discovery"`
	Overrides     map[string]RepoOverrideConfig `yaml:"overrides,omitempty"`
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

// RepoOverrideConfig defines custom display overrides for specific repository paths.
type RepoOverrideConfig struct {
	Title string `yaml:"title,omitempty"`
	Icon  string `yaml:"icon,omitempty"`
	Badge string `yaml:"badge,omitempty"`
}

// DefaultPortalConfig returns a PortalConfig populated with production defaults.
func DefaultPortalConfig() *PortalConfig {
	return &PortalConfig{
		Title:       "Enterprise Package Repositories",
		Description: "Unified browsing and distribution catalog for RPM and Debian packages",
		AutoDiscovery: AutoDiscoveryConfig{
			Enabled:         true,
			MaxDepth:        5,
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
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("failed to read configuration file: %w", err)
	}

	cfg := DefaultPortalConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse YAML configuration: %w", err)
	}

	if cfg.AutoDiscovery.MaxDepth <= 0 {
		cfg.AutoDiscovery.MaxDepth = 5
	}

	return cfg, nil
}

// DumpConfig serializes a discovered Catalog into a starter YAML configuration template.
func DumpConfig(outputPath string, catalog *Catalog) error {
	starter := DefaultPortalConfig()
	if catalog != nil {
		if catalog.Title != "" {
			starter.Title = catalog.Title
		}
		if catalog.Description != "" {
			starter.Description = catalog.Description
		}
		if catalog.BaseURL != "" {
			starter.BaseURL = catalog.BaseURL
		}

		for i, repo := range catalog.Repos {
			if i < 2 && repo.RelPath != "" {
				starter.Pinned = append(starter.Pinned, repo.RelPath)
			}
			if repo.RelPath != "" {
				starter.Overrides[repo.RelPath] = RepoOverrideConfig{
					Title: repo.Title,
					Icon:  MatchDistroIcon(repo.Distro),
					Badge: "Production",
				}
			}
		}
	}

	data, err := yaml.Marshal(starter)
	if err != nil {
		return fmt.Errorf("failed to marshal starter configuration: %w", err)
	}

	header := []byte("# portal.yaml - RepoView Multi-Repository Portal Configuration\n\n")
	content := append(header, data...)

	cleanPath := filepath.Clean(outputPath)
	// #nosec G306 -- writing generated configuration file with standard web readability (0644)
	if err := os.WriteFile(cleanPath, content, 0o644); err != nil {
		return fmt.Errorf("failed to write configuration file %s: %w", outputPath, err)
	}

	return nil
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

	// 1. Apply overrides and default icons to discovered repos
	for _, repo := range catalog.Repos {
		if ov, ok := cfg.Overrides[repo.RelPath]; ok {
			applyOverride(repo, ov)
		} else if ov, ok := cfg.Overrides[repo.Path]; ok {
			applyOverride(repo, ov)
		}

		if repo.Icon == "" {
			repo.Icon = MatchDistroIcon(repo.Distro)
		}
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

	// 3. Apply pinned ordering
	if len(cfg.Pinned) > 0 {
		pinnedSet := make(map[string]int)
		for idx, p := range cfg.Pinned {
			pinnedSet[filepath.ToSlash(p)] = idx
		}

		var pinnedRepos []*DiscoveredRepo
		var unpinnedRepos []*DiscoveredRepo

		for _, r := range catalog.Repos {
			if _, isPinned := pinnedSet[r.RelPath]; isPinned {
				pinnedRepos = append(pinnedRepos, r)
			} else if _, isPinned := pinnedSet[r.Path]; isPinned {
				pinnedRepos = append(pinnedRepos, r)
			} else {
				unpinnedRepos = append(unpinnedRepos, r)
			}
		}

		// Sort pinned repositories by their order in cfg.Pinned
		sort.SliceStable(pinnedRepos, func(i, j int) bool {
			orderI := pinnedOrder(pinnedRepos[i], pinnedSet)
			orderJ := pinnedOrder(pinnedRepos[j], pinnedSet)
			return orderI < orderJ
		})

		catalog.Repos = append(pinnedRepos, unpinnedRepos...)
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
	if ov.Icon != "" {
		repo.Icon = ov.Icon
	}
	if ov.Badge != "" {
		repo.Badge = ov.Badge
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
