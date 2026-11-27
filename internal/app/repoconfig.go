package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"github.com/edsilegxrepo/repoview/internal/models"
)

const maxRepoConfigBytes = 2 * 1024 * 1024 // 2 MiB ceiling for repoview.yaml

// RepoConfig represents an optional single-repository declarative configuration file
// (repoview.yaml, .repoview.yaml, or repoview.yml) stored inside the repository root.
type RepoConfig struct {
	Title          string   `yaml:"title,omitempty"`
	Description    string   `yaml:"description,omitempty"`
	OutputDir      string   `yaml:"output_dir,omitempty"`
	StateDir       string   `yaml:"state_dir,omitempty"`
	Comps          string   `yaml:"comps,omitempty"`
	TemplateDir    string   `yaml:"template_dir,omitempty"`
	URL            string   `yaml:"url,omitempty"`
	BaseURL        string   `yaml:"base_url,omitempty"`
	PackageBaseURL string   `yaml:"package_base_url,omitempty"`
	PortalURL      string   `yaml:"portal_url,omitempty"`
	GPGKey         string   `yaml:"gpgkey,omitempty"`
	Format         string   `yaml:"format,omitempty"`
	Suite          string   `yaml:"suite,omitempty"`
	Arch           string   `yaml:"arch,omitempty"`
	Workers        int      `yaml:"workers,omitempty"`
	Distro         string   `yaml:"distro,omitempty"`
	Channel        string   `yaml:"channel,omitempty"`
	Badge          string   `yaml:"badge,omitempty"`
	Icon           string   `yaml:"icon,omitempty"`
	IgnorePackage  []string `yaml:"ignore_package,omitempty"`
	ExcludeArch    []string `yaml:"exclude_arch,omitempty"`
}

// FindRepoConfig locates repoview.yaml, .repoview.yaml, or repoview.yml inside repoDir.
func FindRepoConfig(repoDir string) (string, bool) {
	for _, name := range []string{"repoview.yaml", ".repoview.yaml", "repoview.yml"} {
		candidate := filepath.Join(repoDir, name)
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

// LoadRepoConfig reads and unmarshals a declarative repoview.yaml configuration file.
func LoadRepoConfig(path string) (*RepoConfig, error) {
	// #nosec G304 -- reading validated per-repository configuration file
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to open repo config %s: %w", models.ErrConfig, path, err)
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxRepoConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read repo config %s: %w", models.ErrConfig, path, err)
	}
	if int64(len(data)) > maxRepoConfigBytes {
		return nil, fmt.Errorf("%w: repo config %s exceeds %d bytes", models.ErrConfig, path, maxRepoConfigBytes)
	}

	var cfg RepoConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%w: failed to parse %s: %w", models.ErrConfig, path, err)
	}
	return &cfg, nil
}
