package app

// OBJECTIVES:
// Support declarative per-repository defaults (repoview.yaml) alongside interactive
// --dump-config bootstrapping so single-repository runs are repeatable without long CLI invocations.
//
// CORE COMPONENTS:
//   - RepoConfig: YAML schema mirroring single-repo CLI options and metadata overrides.
//   - FindRepoConfig / LoadRepoConfig: Discovery and size-bounded YAML decoding in repoDir.
//   - BuildStarterRepoConfig / WriteStarterRepoConfig: Metadata introspection + non-clobber/merge YAML writer.
//
// FUNCTIONALITY:
//   - Inspects RPM/DEB metadata and path taxonomy to infer starter titles, formats, arches,
//     suites, comps paths, and distro icons.
//   - Refuses overwriting existing YAML configs unless --force is given, in which case
//     inferred defaults fill only unset fields without clobbering manual customizations.
//
// DATA FLOW:
//   repoDir (+ Config) -> repo.OpenWithOptions + taxonomy -> Starter RepoConfig -> repoview.yaml

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/repo"
	"github.com/edsilegxrepo/repoview/internal/taxonomy"
)

// --- Repository YAML Discovery & Loading ---

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
	Fsync          bool     `yaml:"fsync,omitempty"`
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

// --- Starter Config Generation & Safe Merge ---

// BuildStarterRepoConfig inspects cfg.RepoDir metadata and constructs a starter RepoConfig
// suitable for review and customization before generating HTML output.
func BuildStarterRepoConfig(cfg Config) (*RepoConfig, error) {
	reader, meta, cleanup, err := repo.OpenWithOptions(cfg.RepoDir, repo.OpenOptions{
		Format: cfg.Format,
		Suite:  cfg.Suite,
		Arch:   cfg.Arch,
		Warn:   cfg.Stderr,
	})
	if err != nil {
		return nil, err
	}
	defer cleanup()

	allPkgs, err := reader.GetAllPackages()
	if err != nil {
		return nil, err
	}

	absRepo, absErr := filepath.Abs(cfg.RepoDir)
	if absErr != nil {
		absRepo = cfg.RepoDir
	}

	formatVal := strings.TrimSpace(string(cfg.Format))
	if meta != nil && meta.Format != "" {
		formatVal = string(meta.Format)
	}
	if formatVal == "" {
		formatVal = string(repo.DetectFormat(absRepo))
	}

	distro, channel := taxonomy.InferDistroAndChannel(absRepo)
	if cfg.Distro != "" {
		distro = cfg.Distro
	}
	if cfg.Channel != "" {
		channel = cfg.Channel
	}

	archVal := strings.TrimSpace(cfg.Arch)
	if archVal == "" && meta != nil && meta.DebLocations != nil && meta.DebLocations.Arch != "" {
		archVal = meta.DebLocations.Arch
	}
	if archVal == "" {
		archVal = taxonomy.DetectPrimaryArchFromPackages(allPkgs)
	}
	if archVal == "" || archVal == "unknown" || archVal == "all" {
		if pathArch := taxonomy.DetectArchFromPath(absRepo); pathArch != "" && pathArch != "unknown" {
			archVal = pathArch
		}
	}

	suiteVal := strings.TrimSpace(cfg.Suite)
	if suiteVal == "" && meta != nil {
		suiteVal = meta.Suite
	}

	descVal := strings.TrimSpace(cfg.Description)
	if descVal == "" && meta != nil {
		descVal = strings.TrimSpace(meta.Description)
	}

	titleVal := strings.TrimSpace(cfg.Title)
	if titleVal == "" || titleVal == "Repoview" {
		switch {
		case distro != "" && channel != "" && archVal != "" && archVal != "unknown":
			titleVal = fmt.Sprintf("%s %s (%s)", distro, channel, archVal)
		case distro != "" && channel != "":
			titleVal = fmt.Sprintf("%s %s", distro, channel)
		case distro != "":
			titleVal = distro
		default:
			titleVal = "Repoview"
		}
	}

	compsVal := strings.TrimSpace(cfg.CompsFile)
	if compsVal == "" && meta != nil && meta.Locations != nil && meta.Locations.Groups != "" {
		if rel, relErr := filepath.Rel(absRepo, meta.Locations.Groups); relErr == nil && !strings.HasPrefix(rel, "..") {
			compsVal = filepath.ToSlash(rel)
		} else {
			compsVal = filepath.ToSlash(meta.Locations.Groups)
		}
	}

	iconVal := strings.TrimSpace(cfg.Icon)
	if iconVal == "" && distro != "" {
		iconVal = taxonomy.MatchDistroIcon(distro)
	}

	portalURLVal := strings.TrimSpace(cfg.PortalURL)
	if strings.EqualFold(portalURLVal, "auto") {
		portalURLVal = ""
	}

	starter := &RepoConfig{
		Title:          titleVal,
		Description:    descVal,
		Comps:          compsVal,
		TemplateDir:    cfg.TemplateDir,
		URL:            cfg.URL,
		BaseURL:        cfg.BaseURL,
		PackageBaseURL: cfg.PackageBaseURL,
		PortalURL:      portalURLVal,
		GPGKey:         cfg.GPGKey,
		Format:         formatVal,
		Suite:          suiteVal,
		Arch:           archVal,
		Workers:        cfg.Workers,
		Distro:         distro,
		Channel:        channel,
		Badge:          cfg.Badge,
		Icon:           iconVal,
		IgnorePackage:  append([]string(nil), cfg.IgnoreList...),
		ExcludeArch:    append([]string(nil), cfg.ExcludeArch...),
	}
	return starter, nil
}

// WriteStarterRepoConfig serializes a starter repoview.yaml to outputPath, merging safely
// when force is true and refusing to overwrite existing files when force is false.
func WriteStarterRepoConfig(outputPath string, rc *RepoConfig, force bool) error {
	cleanPath := filepath.Clean(outputPath)
	outCfg := rc
	if outCfg == nil {
		outCfg = &RepoConfig{}
	}

	if fi, err := os.Stat(cleanPath); err == nil {
		if fi.IsDir() {
			return fmt.Errorf("%w: configuration path %s is a directory", models.ErrIO, outputPath)
		}
		if !force {
			return fmt.Errorf("%w: configuration file %s already exists (use --force to merge/overwrite)", models.ErrSafety, outputPath)
		}
		existing, loadErr := LoadRepoConfig(cleanPath)
		if loadErr != nil {
			return loadErr
		}
		merged := mergeRepoConfig(existing, outCfg)
		outCfg = merged
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("%w: failed to stat configuration file %s: %w", models.ErrIO, outputPath, err)
	}

	data, err := yaml.Marshal(outCfg)
	if err != nil {
		return fmt.Errorf("failed to marshal repository configuration: %w", err)
	}

	header := "# repoview.yaml - RepoView Single-Repository Configuration\n\n"
	footer := "\n# Optional filtering and tuning examples (uncomment and customize as needed):\n" +
		"# ignore_package:\n" +
		"#   - \"*-debuginfo\"\n" +
		"# exclude_arch:\n" +
		"#   - \"i686\"\n" +
		"# gpgkey: https://mirrors.example.com/RPM-GPG-KEY-example\n" +
		"# workers: 8\n"

	content := make([]byte, 0, len(header)+len(data)+len(footer))
	content = append(content, header...)
	content = append(content, data...)
	content = append(content, footer...)

	// #nosec G306 -- writing repository configuration file with standard readability (0644)
	if err := os.WriteFile(cleanPath, content, 0o644); err != nil {
		return fmt.Errorf("%w: failed to write repository configuration %s: %w", models.ErrIO, outputPath, err)
	}
	return nil
}

func mergeRepoConfig(existing, inferred *RepoConfig) *RepoConfig {
	merged := *existing
	if merged.Title == "" {
		merged.Title = inferred.Title
	}
	if merged.Description == "" {
		merged.Description = inferred.Description
	}
	if merged.OutputDir == "" {
		merged.OutputDir = inferred.OutputDir
	}
	if merged.StateDir == "" {
		merged.StateDir = inferred.StateDir
	}
	if merged.Comps == "" {
		merged.Comps = inferred.Comps
	}
	if merged.TemplateDir == "" {
		merged.TemplateDir = inferred.TemplateDir
	}
	if merged.URL == "" {
		merged.URL = inferred.URL
	}
	if merged.BaseURL == "" {
		merged.BaseURL = inferred.BaseURL
	}
	if merged.PackageBaseURL == "" {
		merged.PackageBaseURL = inferred.PackageBaseURL
	}
	if merged.PortalURL == "" {
		merged.PortalURL = inferred.PortalURL
	}
	if merged.GPGKey == "" {
		merged.GPGKey = inferred.GPGKey
	}
	if merged.Format == "" {
		merged.Format = inferred.Format
	}
	if merged.Suite == "" {
		merged.Suite = inferred.Suite
	}
	if merged.Arch == "" {
		merged.Arch = inferred.Arch
	}
	if merged.Workers == 0 {
		merged.Workers = inferred.Workers
	}
	if merged.Distro == "" {
		merged.Distro = inferred.Distro
	}
	if merged.Channel == "" {
		merged.Channel = inferred.Channel
	}
	if merged.Badge == "" {
		merged.Badge = inferred.Badge
	}
	if merged.Icon == "" {
		merged.Icon = inferred.Icon
	}
	if len(merged.IgnorePackage) == 0 && len(inferred.IgnorePackage) > 0 {
		merged.IgnorePackage = append([]string(nil), inferred.IgnorePackage...)
	}
	if len(merged.ExcludeArch) == 0 && len(inferred.ExcludeArch) > 0 {
		merged.ExcludeArch = append([]string(nil), inferred.ExcludeArch...)
	}
	return &merged
}
