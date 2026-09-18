package portal

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/edsilegxrepo/repoview/internal/models"
)

// RepoRunner defines the callback signature used by --render-missing to build raw repos.
type RepoRunner func(ctx context.Context, repoPath, outDir string) error

// DiscoveredRepo represents an indexed RPM, DEB, or aggregated Debian suite repository.
type DiscoveredRepo struct {
	Path          string                 `json:"path"`                     // Absolute filesystem path on disk
	RelPath       string                 `json:"rel_path"`                 // Relative directory from portal root
	TargetURL     string                 `json:"target_url"`               // Strictly relative link from portal index.html
	Title         string                 `json:"title"`                    // Human display title
	Format        models.RepoFormat      `json:"format"`                   // "rpm" or "deb"
	Distro        string                 `json:"distro"`                   // Normalized distro slug (e.g. "el9", "ubuntu")
	Channel       string                 `json:"channel"`                  // Channel / Component (e.g. "base", "main")
	Suite         string                 `json:"suite,omitempty"`          // Debian suite name (e.g. "noble", "bookworm")
	Components    []string               `json:"components,omitempty"`     // Aggregated Debian components
	Arch          string                 `json:"arch"`                     // Architecture (e.g. "x86_64", "amd64")
	PackageCount  int                    `json:"package_count"`            // Number of packages
	LastBuild     time.Time              `json:"last_build"`               // Last generation or repodata timestamp
	IsRendered    bool                   `json:"is_rendered"`              // True if repoview/index.html exists
	Badge         string                 `json:"badge,omitempty"`          // UI badge (e.g. "Production", "LTS")
	Icon          string                 `json:"icon,omitempty"`           // SVG icon identifier (e.g. "redhat", "ubuntu", "debian")
	IsExternal    bool                   `json:"is_external,omitempty"`    // True if remote URL from portal.yaml
	Descriptor    *models.RepoDescriptor `json:"descriptor,omitempty"`     // Parsed repoview.json if present
	Aliases       []string               `json:"aliases,omitempty"`        // Symlink aliases pointing here
	ConfigSnippet string                 `json:"config_snippet,omitempty"` // Ready-to-copy package manager setup snippet
}

// Catalog holds the aggregated portal state ready for rendering.
type Catalog struct {
	Title         string            `json:"title"`
	Description   string            `json:"description"`
	BaseURL       string            `json:"base_url,omitempty"`
	GeneratedAt   time.Time         `json:"generated_at"`
	TotalRepos    int               `json:"total_repos"`
	TotalPkgs     int               `json:"total_pkgs"`
	Repos         []*DiscoveredRepo `json:"repos"`
	Distros       []string          `json:"distros"`       // Deduplicated unique list for filter chips
	Architectures []string          `json:"architectures"` // Deduplicated unique list for filter chips
	Formats       []string          `json:"formats"`       // ["rpm", "deb"]
}

// BuildCatalog constructs a Catalog instance from discovered repositories and portal settings.
func BuildCatalog(title, description, baseURL string, repos []*DiscoveredRepo) *Catalog {
	if title == "" {
		title = "Repository Portal"
	}
	if description == "" {
		description = "Package repositories and catalogs"
	}

	distroSet := make(map[string]bool)
	archSet := make(map[string]bool)
	formatSet := make(map[string]bool)
	totalPkgs := 0

	for _, r := range repos {
		if r.ConfigSnippet == "" {
			r.ConfigSnippet = r.BuildConfigSnippet(baseURL)
		}
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

	var distros []string
	for d := range distroSet {
		distros = append(distros, d)
	}
	sort.Strings(distros)

	var arches []string
	for a := range archSet {
		arches = append(arches, a)
	}
	sort.Strings(arches)

	var formats []string
	for f := range formatSet {
		formats = append(formats, f)
	}
	sort.Strings(formats)

	return &Catalog{
		Title:         title,
		Description:   description,
		BaseURL:       baseURL,
		GeneratedAt:   time.Now().UTC(),
		TotalRepos:    len(repos),
		TotalPkgs:     totalPkgs,
		Repos:         repos,
		Distros:       distros,
		Architectures: arches,
		Formats:       formats,
	}
}

// DebArchiveRoot computes the root directory containing dists/ for Debian repositories.
func (r *DiscoveredRepo) DebArchiveRoot() string {
	slashRel := filepath.ToSlash(r.RelPath)
	parts := strings.Split(slashRel, "/")
	for i, p := range parts {
		if p == "dists" {
			if i == 0 {
				return ""
			}
			return strings.Join(parts[:i], "/")
		}
	}
	return slashRel
}

// RepoDirURL returns the clean URL/path pointing directly to the repository root directory.
func (r *DiscoveredRepo) RepoDirURL(baseURL string) string {
	if r.IsExternal && (strings.HasPrefix(r.TargetURL, "http://") || strings.HasPrefix(r.TargetURL, "https://")) {
		return r.TargetURL
	}
	rel := strings.Trim(filepath.ToSlash(r.RelPath), "/")
	if baseURL != "" {
		base := strings.TrimSuffix(baseURL, "/")
		if rel == "" || rel == "." {
			return base + "/"
		}
		return base + "/" + rel + "/"
	}
	if rel == "" || rel == "." {
		return "./"
	}
	return rel + "/"
}

// DebArchiveURL returns the URL/path pointing to the Debian archive root (parent directory of dists/).
func (r *DiscoveredRepo) DebArchiveURL(baseURL string) string {
	if r.IsExternal && (strings.HasPrefix(r.TargetURL, "http://") || strings.HasPrefix(r.TargetURL, "https://")) {
		return r.TargetURL
	}
	archiveRoot := strings.Trim(r.DebArchiveRoot(), "/")
	if baseURL != "" {
		base := strings.TrimSuffix(baseURL, "/")
		if archiveRoot == "" || archiveRoot == "." {
			return base + "/"
		}
		return base + "/" + archiveRoot + "/"
	}
	if archiveRoot == "" || archiveRoot == "." {
		return "./"
	}
	return archiveRoot + "/"
}

// BuildConfigSnippet generates a valid, copyable package manager configuration snippet.
func (r *DiscoveredRepo) BuildConfigSnippet(baseURL string) string {
	if r.Format == models.FormatDEB {
		archiveURL := r.DebArchiveURL(baseURL)
		if r.Suite != "" {
			comps := strings.Join(r.Components, " ")
			if comps == "" {
				comps = r.Channel
			}
			if comps == "" {
				comps = "main"
			}
			return fmt.Sprintf("Types: deb\nURIs: %s\nSuites: %s\nComponents: %s", archiveURL, r.Suite, comps)
		}
		return fmt.Sprintf("Types: deb\nURIs: %s\nSuites: ./", archiveURL)
	}

	// RPM (.repo format)
	repoURL := r.RepoDirURL(baseURL)
	ch := r.Channel
	if ch == "" {
		ch = "repo"
	}
	return fmt.Sprintf("[%s]\nname=%s\nbaseurl=%s\nenabled=1\ngpgcheck=0", ch, r.Title, repoURL)
}
