// Package main is the entry point for the repoview-go utility.
//
// OBJECTIVES:
// Provide the CLI frontend for both single-repository builds (repoview [opts] <repo>)
// and multi-repository catalog portals (repoview portal [opts] [root]), converting
// RPM and Debian/Ubuntu mirrors into air-gap compliant static websites.
//
// CORE COMPONENTS:
//   - stringList / syncWriter: Thread-safe CLI logging and repeatable flag accumulation.
//   - run(): Single-repo flag parser, repoview.yaml loader, --dump-config handler, and runner.
//   - runPortal(): Portal scanner coordinator, parallel --render-missing/--render-all worker launcher,
//     --dump-config merger, and catalog writer.
//   - handleCLIError() / main(): Startup umask configuration and deterministic OS exit code mapping.
//
// FUNCTIONALITY:
//   - Parses long-only flags, merges optional YAML defaults (CLI > YAML > defaults), enforces
//     safety barriers (--portal-home vs --in-place, no output overwrite), and maps domain sentinels
//     onto exit codes 0-8.
//
// DATA FLOW:
//
//	os.Args -> run() / runPortal() -> (app.Generator | portal.Scanner+Renderer) -> Exit Code
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/edsilegxrepo/repoview/internal/app"
	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/portal"
	"github.com/edsilegxrepo/repoview/internal/util"
)

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func printOption(w io.Writer, long, desc string, def interface{}) {
	defStr := ""
	if def != nil {
		defStr = fmt.Sprintf(" (default %v)", def)
	}
	if s, ok := def.(string); ok && s == "" {
		defStr = ""
	}
	if b, ok := def.(bool); ok && !b {
		defStr = ""
	}
	_, _ = fmt.Fprintf(w, "  --%-22s\t%s%s\n", long, desc, defStr)
}

// Version string for the repoview binary.
var version = "dev"

// stringList implements flag.Value to support repeatable command-line options
// such as --ignore-package and --exclude-arch.
type stringList []string

// String returns a comma-separated representation of the stringList values.
func (s *stringList) String() string {
	return strings.Join(*s, ", ")
}

// Set appends a new string value to the stringList slice.
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// run parses arguments, executes the generation pipeline, and returns the appropriate exit code.
// It accepts stdout and stderr writers to allow in-process unit testing without global state leaks.
func run(args []string, stdout, stderr io.Writer) int {
	// Check for portal subcommand
	if len(args) > 0 && args[0] == "portal" {
		return runPortal(args[1:], stdout, stderr)
	}

	var (
		repoDir        string
		outputDir      string
		stateDir       string
		compsFile      string
		templateDir    string
		title          string
		url            string
		baseURL        string
		format         string
		suite          string
		arch           string
		force          bool
		quiet          bool
		showVer        bool
		portalURL      string
		gpgKey         string
		dumpConfigPath string
		workers        int
		fsyncDerived   bool
		ignoreList     stringList
		excludeArch    stringList
	)

	fs := flag.NewFlagSet("repoview", flag.ContinueOnError)
	fs.SetOutput(stderr)

	fs.StringVar(&outputDir, "output-dir", "repoview", "Output directory")
	fs.StringVar(&stateDir, "state-dir", "", "State directory (default: output directory)")
	fs.StringVar(&title, "title", "Repoview", "Repository title")
	fs.StringVar(&url, "url", "", "Repository URL (for RSS feed)")
	fs.StringVar(&baseURL, "baseurl", "", "Repository base URL for client configuration")
	fs.StringVar(&format, "format", "auto", "Repository format: auto, rpm, or deb")
	fs.StringVar(&suite, "suite", "", "Debian suite/codename selector for multi-suite roots")
	fs.StringVar(&arch, "arch", "", "Target architecture selector for multi-arch Debian roots")
	fs.StringVar(&templateDir, "template-dir", "", "Template directory")
	fs.BoolVar(&force, "force", false, "Force regeneration")
	fs.BoolVar(&quiet, "quiet", false, "Quiet mode")
	fs.StringVar(&compsFile, "comps", "", "Alternative comps.xml file")
	fs.Var(&ignoreList, "ignore-package", "Ignore package glob (can be repeated)")
	fs.Var(&excludeArch, "exclude-arch", "Exclude architecture (can be repeated)")
	fs.StringVar(&portalURL, "portal-url", "auto", "Parent portal URL ('auto', 'none', or custom URL/path)")
	fs.StringVar(&gpgKey, "gpgkey", "", "GPG public key URL or path for client snippets (enables gpgcheck=1)")
	fs.StringVar(&dumpConfigPath, "dump-config", "", "Inspect repository and write starter repoview.yaml, then exit")
	fs.IntVar(&workers, "workers", 0, "Parallel worker count (1-16, default auto)")
	fs.BoolVar(&fsyncDerived, "fsync", false, "Sync every derived group/package HTML page individually to disk")
	fs.BoolVar(&showVer, "version", false, "Display version")

	// Custom Usage output to display clean long-only options to the user.
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage:\n  repoview [options] <repodir>\n  repoview portal [options] [repodir]\n\nOptions:\n")

		printOption(stderr, "output-dir <dir>", "Output directory", "repoview")
		printOption(stderr, "state-dir <dir>", "State directory", "output directory")
		printOption(stderr, "title <text>", "Repository title", "Repoview")
		printOption(stderr, "url <url>", "Repository URL (for RSS feed)", "")
		printOption(stderr, "baseurl <url>", "Repository base URL for client configuration", "")
		printOption(stderr, "format <type>", "Repository format (auto, rpm, deb)", "auto")
		printOption(stderr, "suite <name>", "Debian suite selector for multi-suite roots", "")
		printOption(stderr, "arch <arch>", "Debian arch selector for multi-arch roots", "")
		printOption(stderr, "template-dir <dir>", "Template directory", "embedded")
		printOption(stderr, "comps <file>", "Alternative comps.xml file", "")
		printOption(stderr, "ignore-package <glob>", "Ignore package glob (can be repeated)", "")
		printOption(stderr, "exclude-arch <arch>", "Exclude architecture (can be repeated)", "")
		printOption(stderr, "portal-url <url>", "Parent portal URL ('auto', 'none', or path)", "auto")
		printOption(stderr, "gpgkey <url-or-path>", "GPG public key URL or path (enables gpgcheck=1)", "")
		printOption(stderr, "dump-config <file>", "Inspect repository and write starter repoview.yaml, then exit", "")
		printOption(stderr, "workers <n>", "Parallel worker count (1-16, default auto)", "")
		printOption(stderr, "fsync", "Sync every derived group/package HTML page to disk", false)
		printOption(stderr, "force", "Force regeneration", false)
		printOption(stderr, "quiet", "Quiet mode", false)
		printOption(stderr, "version", "Display version information", false)
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return app.ExitSuccess
		}
		return app.ExitUsageError
	}

	// Handle version query
	if showVer {
		_, _ = fmt.Fprintf(stdout, "repoview version %s\n", version)
		return app.ExitSuccess
	}

	// Positional repodir argument is required
	if fs.NArg() < 1 {
		fs.Usage()
		return app.ExitUsageError
	}
	repoDir = fs.Arg(0)

	// Validate repository directory existence
	if fi, err := os.Stat(repoDir); err != nil || !fi.IsDir() {
		_, _ = fmt.Fprintf(stderr, "Error: repository directory does not exist or is not a directory: %s\n", repoDir)
		return app.ExitRepoMetadataError
	}

	// Load optional repoview.yaml defaults before resolving CLI flag overrides
	cliFlags := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		cliFlags[f.Name] = true
	})

	var descText, distroName, channelName, badgeText, iconName, pkgBaseURL string
	if rcPath, ok := app.FindRepoConfig(repoDir); ok {
		rc, err := app.LoadRepoConfig(rcPath)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Configuration Error: %v\n", err)
			return app.ExitConfigError
		}
		if !cliFlags["output-dir"] && rc.OutputDir != "" {
			outputDir = rc.OutputDir
		}
		if !cliFlags["state-dir"] && rc.StateDir != "" {
			stateDir = rc.StateDir
		}
		if !cliFlags["title"] && rc.Title != "" {
			title = rc.Title
		}
		if !cliFlags["url"] && rc.URL != "" {
			url = rc.URL
		}
		if !cliFlags["baseurl"] && rc.BaseURL != "" {
			baseURL = rc.BaseURL
		}
		if !cliFlags["format"] && rc.Format != "" {
			format = rc.Format
		}
		if !cliFlags["suite"] && rc.Suite != "" {
			suite = rc.Suite
		}
		if !cliFlags["arch"] && rc.Arch != "" {
			arch = rc.Arch
		}
		if !cliFlags["template-dir"] && rc.TemplateDir != "" {
			templateDir = rc.TemplateDir
		}
		if !cliFlags["comps"] && rc.Comps != "" {
			compsFile = rc.Comps
		}
		if !cliFlags["portal-url"] && rc.PortalURL != "" {
			portalURL = rc.PortalURL
		}
		if !cliFlags["gpgkey"] && rc.GPGKey != "" {
			gpgKey = rc.GPGKey
		}
		if !cliFlags["workers"] && rc.Workers > 0 {
			workers = rc.Workers
		}
		if !cliFlags["fsync"] && rc.Fsync {
			fsyncDerived = true
		}
		if !cliFlags["ignore-package"] && len(rc.IgnorePackage) > 0 {
			ignoreList = rc.IgnorePackage
		}
		if !cliFlags["exclude-arch"] && len(rc.ExcludeArch) > 0 {
			excludeArch = rc.ExcludeArch
		}
		descText = rc.Description
		distroName = rc.Distro
		channelName = rc.Channel
		badgeText = rc.Badge
		iconName = rc.Icon
		pkgBaseURL = rc.PackageBaseURL
	}

	// Validate format option
	var repoFormat models.RepoFormat
	switch strings.ToLower(format) {
	case "auto", "":
		repoFormat = "" // Auto-detection
	case "rpm":
		repoFormat = models.FormatRPM
	case "deb":
		repoFormat = models.FormatDEB
	default:
		_, _ = fmt.Fprintf(stderr, "Error: invalid repository format '%s' (must be 'auto', 'rpm', or 'deb')\n", format)
		return app.ExitUsageError
	}

	// Resolve relative outputDir against repoDir, matching Python repoview behavior
	if !filepath.IsAbs(outputDir) {
		outputDir = filepath.Join(repoDir, outputDir)
	}

	// If baseURL is omitted but URL is a full HTTP(S) address, reuse it as the default base URL
	if baseURL == "" && (strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")) {
		baseURL = url
	}

	config := app.Config{
		RepoDir:        repoDir,
		OutputDir:      outputDir,
		StateDir:       stateDir,
		CompsFile:      compsFile,
		TemplateDir:    templateDir,
		Title:          title,
		Description:    descText,
		URL:            url,
		BaseURL:        baseURL,
		PackageBaseURL: pkgBaseURL,
		Format:         repoFormat,
		Suite:          suite,
		Arch:           arch,
		Distro:         distroName,
		Channel:        channelName,
		Badge:          badgeText,
		Icon:           iconName,
		Force:          force,
		Quiet:          quiet,
		IgnoreList:     ignoreList,
		ExcludeArch:    excludeArch,
		PortalURL:      portalURL,
		GPGKey:         gpgKey,
		Workers:        workers,
		Fsync:          fsyncDerived,
		Version:        version,
		Stdout:         stdout,
		Stderr:         stderr,
	}

	// Dump single-repository starter YAML configuration if requested (no HTML output written)
	if dumpConfigPath != "" {
		starterCfg, err := app.BuildStarterRepoConfig(config)
		if err == nil {
			err = app.WriteStarterRepoConfig(dumpConfigPath, starterCfg, force)
		}
		if err != nil {
			return handleCLIError(err, stderr)
		}
		if !quiet {
			_, _ = fmt.Fprintf(stdout, "Wrote starter configuration to %s\n", dumpConfigPath)
		}
		return app.ExitSuccess
	}

	if !quiet {
		_, _ = fmt.Fprintf(stdout, "Repoview %s\n", version)
	}

	// Instantiate generator and execute generation workflow
	generator := app.NewGenerator(config)
	if err := generator.Run(); err != nil {
		return handleCLIError(err, stderr)
	}

	return app.ExitSuccess
}

// handleCLIError maps domain errors deterministically via errors.Is / errors.As to exit codes.
func handleCLIError(err error, stderr io.Writer) int {
	var partialErr *app.PartialFailureError

	switch {
	case errors.Is(err, app.ErrSafety) || errors.Is(err, app.ErrIntegrity):
		_, _ = fmt.Fprintf(stderr, "Safety Violation: %v\n", err)
		return app.ExitSafetyError
	case errors.As(err, &partialErr):
		_, _ = fmt.Fprintf(stderr, "Warning: %v\n", partialErr)
		return app.ExitPartialFailure
	case errors.Is(err, app.ErrConfig):
		_, _ = fmt.Fprintf(stderr, "Configuration Error: %v\n", err)
		return app.ExitConfigError
	case errors.Is(err, app.ErrRepoMetadata):
		_, _ = fmt.Fprintf(stderr, "Repository Metadata Error: %v\n", err)
		return app.ExitRepoMetadataError
	case errors.Is(err, app.ErrTemplate):
		_, _ = fmt.Fprintf(stderr, "Template Error: %v\n", err)
		return app.ExitTemplateError
	case errors.Is(err, app.ErrIO):
		_, _ = fmt.Fprintf(stderr, "I/O Error: %v\n", err)
		return app.ExitIOError
	default:
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return app.ExitGeneralError
	}
}

// runPortal handles multi-repository discovery, catalog aggregation, and portal site generation.
func runPortal(args []string, stdout, stderr io.Writer) int {
	var (
		portalHome      string
		inPlace         bool
		portalURL       string
		repoURL         string
		title           string
		description     string
		configPath      string
		dumpConfigPath  string
		maxDepth        int
		requireRendered bool
		renderMissing   bool
		renderAll       bool
		workers         int
		force           bool
		quiet           bool
		showVer         bool
	)

	defaultWorkers := util.ClampWorkers(0, util.MaxDefaultWorkers)

	fs := flag.NewFlagSet("repoview portal", flag.ContinueOnError)
	fs.SetOutput(stderr)

	fs.StringVar(&portalHome, "portal-home", "", "Detached portal output directory (outside repo)")
	fs.BoolVar(&inPlace, "in-place", false, "Generate repoview files inside source repository mirrors (legacy)")
	fs.StringVar(&portalURL, "portal-url", "", "Portal public base URL for UI browsing and portal RSS feed")
	fs.StringVar(&repoURL, "repo-url", "", "Repository package download and client configuration base URL")
	fs.StringVar(&title, "title", "", "Portal page title")
	fs.StringVar(&description, "description", "", "Portal page description")
	fs.StringVar(&configPath, "config", "", "Path to optional portal.yaml configuration")
	fs.StringVar(&dumpConfigPath, "dump-config", "", "Dump auto-discovered catalog to starter YAML and exit")
	fs.IntVar(&maxDepth, "max-depth", 7, "Maximum directory scan depth")
	fs.BoolVar(&requireRendered, "require-rendered", false, "Only include repositories with generated repoview pages")
	fs.BoolVar(&renderMissing, "render-missing", false, "Automatically generate repoview pages for raw repositories in parallel")
	fs.BoolVar(&renderAll, "render-all", false, "Force generate repoview pages for all discovered repositories in parallel")
	fs.IntVar(&workers, "workers", defaultWorkers, "Parallel worker concurrency for batch rendering (clamped 1-16)")
	fs.BoolVar(&force, "force", false, "Force overwrite existing index.html even if not created by RepoView")
	fs.BoolVar(&quiet, "quiet", false, "Quiet mode")
	fs.BoolVar(&showVer, "version", false, "Display version")

	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: repoview portal [options] [repodir]\n\nOptions:\n")

		printOption(stderr, "portal-home <dir>", "Detached portal output directory (outside repo)", "")
		printOption(stderr, "in-place", "Generate repoview files inside source repository mirrors (legacy)", false)
		printOption(stderr, "portal-url <url>", "Portal public base URL for UI browsing and portal RSS feed", "")
		printOption(stderr, "repo-url <url>", "Repository package download and client configuration base URL", "")
		printOption(stderr, "title <text>", "Portal page title", "Enterprise Package Repositories")
		printOption(stderr, "description <text>", "Portal page description", "")
		printOption(stderr, "config <file>", "Path to optional portal.yaml configuration", "")
		printOption(stderr, "dump-config <file>", "Dump auto-discovered catalog to starter YAML and exit", "")
		printOption(stderr, "max-depth <n>", "Maximum directory scan depth", 7)
		printOption(stderr, "require-rendered", "Only include repositories with generated repoview pages", false)
		printOption(stderr, "render-missing", "Automatically generate repoview pages for raw repositories in parallel", false)
		printOption(stderr, "render-all", "Force generate repoview pages for all discovered repositories in parallel", false)
		printOption(stderr, "workers <n>", "Parallel worker concurrency for batch rendering (1-16)", defaultWorkers)
		printOption(stderr, "force", "Force overwrite existing index.html even if not created by RepoView", false)
		printOption(stderr, "quiet", "Quiet mode", false)
		printOption(stderr, "version", "Display version information", false)
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return app.ExitSuccess
		}
		return app.ExitUsageError
	}

	if showVer {
		_, _ = fmt.Fprintf(stdout, "repoview version %s\n", version)
		return app.ExitSuccess
	}

	targetDir := "."
	if fs.NArg() >= 1 {
		targetDir = fs.Arg(0)
	}

	absTargetDir, err := filepath.Abs(targetDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "I/O Error: failed to resolve directory: %v\n", err)
		return app.ExitIOError
	}

	fi, err := os.Stat(absTargetDir)
	if err != nil || !fi.IsDir() {
		_, _ = fmt.Fprintf(stderr, "Error: repository directory does not exist or is not a directory: %s\n", targetDir)
		return app.ExitRepoMetadataError
	}

	// Load or initialize portal configuration
	var portalCfg *portal.PortalConfig
	if configPath != "" {
		loadedCfg, err := portal.LoadConfig(configPath)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Configuration Error: %v\n", err)
			return app.ExitConfigError
		}
		portalCfg = loadedCfg
	} else {
		foundConfig, err := portal.FindPortalConfigIn(absTargetDir)
		switch {
		case err == nil:
			loadedCfg, err := portal.LoadConfig(foundConfig)
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "Configuration Error: %v\n", err)
				return app.ExitConfigError
			}
			portalCfg = loadedCfg
		case dumpConfigPath != "" && force:
			if fi, stErr := os.Stat(filepath.Clean(dumpConfigPath)); stErr == nil && !fi.IsDir() {
				loadedCfg, loadErr := portal.LoadConfig(dumpConfigPath)
				if loadErr != nil {
					_, _ = fmt.Fprintf(stderr, "Configuration Error: %v\n", loadErr)
					return app.ExitConfigError
				}
				portalCfg = loadedCfg
			} else {
				portalCfg = portal.DefaultPortalConfig()
			}
		default:
			portalCfg = portal.DefaultPortalConfig()
		}
	}

	// Apply explicit CLI flag overrides
	cliFlags := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		cliFlags[f.Name] = true
	})

	if cliFlags["title"] {
		portalCfg.Title = title
	}
	if cliFlags["description"] {
		portalCfg.Description = description
	}
	if cliFlags["portal-home"] {
		portalCfg.PortalHome = portalHome
	}
	if cliFlags["in-place"] {
		portalCfg.InPlace = inPlace
	}
	if cliFlags["portal-url"] {
		portalCfg.PortalURL = portalURL
	}
	if cliFlags["repo-url"] {
		portalCfg.RepoURL = repoURL
	}
	if cliFlags["max-depth"] {
		portalCfg.AutoDiscovery.MaxDepth = maxDepth
	}
	if cliFlags["require-rendered"] {
		portalCfg.AutoDiscovery.RequireRendered = requireRendered
	}

	// Resolve effective settings
	effectivePortalHome := portalHome
	if effectivePortalHome == "" {
		effectivePortalHome = portalCfg.PortalHome
	}

	effectiveInPlace := inPlace || portalCfg.InPlace

	effectivePortalURL := portalURL
	if effectivePortalURL == "" {
		effectivePortalURL = portalCfg.PortalURL
	}
	if effectivePortalURL == "" {
		effectivePortalURL = portalCfg.BaseURL
	}

	effectiveRepoURL := repoURL
	if effectiveRepoURL == "" {
		effectiveRepoURL = portalCfg.RepoURL
	}

	// Validate mandatory requirement: detached portal-home vs explicit in-place
	if dumpConfigPath == "" {
		if !effectiveInPlace && effectivePortalHome == "" {
			_, _ = fmt.Fprintln(stderr, "Error: repoview portal requires either --portal-home <path> (detached output mode) or --in-place (in-tree generation).")
			_, _ = fmt.Fprintln(stderr, "Detached mode is recommended to avoid modifying source repository mirrors.")
			return app.ExitUsageError
		}
		if effectiveInPlace && effectivePortalHome != "" {
			_, _ = fmt.Fprintln(stderr, "Error: cannot specify both --portal-home and --in-place.")
			return app.ExitUsageError
		}
	}

	// Determine final output directory for portal
	var finalOutputDir string
	if effectivePortalHome != "" {
		if filepath.IsAbs(effectivePortalHome) {
			finalOutputDir = filepath.Clean(effectivePortalHome)
		} else {
			absHome, err := filepath.Abs(effectivePortalHome)
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "I/O Error: failed to resolve portal home directory: %v\n", err)
				return app.ExitIOError
			}
			finalOutputDir = absHome
		}
	} else {
		finalOutputDir = absTargetDir
	}

	// Clamp workers between 1 and 16
	if workers < 1 {
		workers = 1
	} else if workers > 16 {
		workers = 16
	}

	// Scan repository tree
	initialRequireRendered := portalCfg.AutoDiscovery.RequireRendered
	if renderMissing || renderAll {
		initialRequireRendered = false
	}

	scanCfg := portal.ScanConfig{
		RootDir:         absTargetDir,
		PortalHome:      effectivePortalHome,
		MaxDepth:        portalCfg.AutoDiscovery.MaxDepth,
		RequireRendered: initialRequireRendered,
		ExcludePatterns: portalCfg.AutoDiscovery.Exclude,
		TaxonomyRules:   portalCfg.Taxonomy,
	}
	scanner := portal.NewScanner(scanCfg)
	repos, err := scanner.Scan()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Scan Error: %v\n", err)
		return app.ExitRepoMetadataError
	}
	if !quiet {
		for _, wMsg := range scanner.Warnings() {
			_, _ = fmt.Fprintln(stderr, wMsg)
		}
	}

	// Parallel Batch Rendering for unrendered or all repositories (updating catalog entries in memory without re-scanning)
	renderFailureCount := 0
	if renderAll || renderMissing {
		hiddenDirs := make(map[string]bool)
		var targets []*portal.DiscoveredRepo
		for _, r := range repos {
			if r.IsExternal {
				continue
			}
			if ov, ok := portalCfg.LookupOverride(r.RelPath, r.Path); ok && ov.Hidden {
				if absH, absErr := filepath.Abs(r.Path); absErr == nil {
					hiddenDirs[filepath.Clean(absH)] = true
				}
				continue
			}
			if renderAll || !r.IsRendered {
				targets = append(targets, r)
			}
		}

		if len(targets) > 0 {
			actionDesc := "unrendered"
			if renderAll {
				actionDesc = "discovered"
			}
			if !quiet {
				_, _ = fmt.Fprintf(stdout, "Discovered %d %s repositories. Rendering in parallel with %d workers...\n", len(targets), actionDesc, workers)
			}

			var errMu sync.Mutex
			var renderErrors []error
			safeStdout := &syncWriter{w: stdout}
			safeStderr := &syncWriter{w: stderr}

			util.ForEachConcurrent(targets, workers, func(_ int, r *portal.DiscoveredRepo) {
				var repoOut string
				if effectivePortalHome != "" {
					if r.RelPath == "." || r.RelPath == "" {
						repoOut = filepath.Join(finalOutputDir, "repoview")
					} else {
						repoOut = filepath.Join(finalOutputDir, r.RelPath)
					}
				} else {
					repoOut = filepath.Join(r.Path, "repoview")
				}

				portalLink := "auto"
				if effectivePortalURL != "" {
					portalLink = effectivePortalURL
				} else if rel, err := filepath.Rel(repoOut, filepath.Join(finalOutputDir, "index.html")); err == nil {
					portalLink = filepath.ToSlash(rel)
				}

				ov, hasOv := portalCfg.LookupOverride(r.RelPath, r.Path)

				// Compute pkgBaseURL for download links & client configs (.repo/.sources)
				var pkgBaseURL string
				switch {
				case hasOv && ov.BaseURL != "":
					pkgBaseURL = strings.TrimRight(ov.BaseURL, "/")
				case effectiveRepoURL != "":
					baseClean := strings.TrimRight(effectiveRepoURL, "/")
					if r.Format == models.FormatDEB {
						archRoot := strings.Trim(r.DebArchiveRoot(), "/")
						if archRoot == "" || archRoot == "." {
							pkgBaseURL = baseClean
						} else {
							pkgBaseURL = baseClean + "/" + archRoot
						}
					} else {
						rel := strings.Trim(filepath.ToSlash(r.RelPath), "/")
						if rel == "" || rel == "." {
							pkgBaseURL = baseClean
						} else {
							pkgBaseURL = baseClean + "/" + rel
						}
					}
				case effectivePortalHome != "":
					targetRawDir := r.Path
					if r.Format == models.FormatDEB {
						debRoot := r.DebArchiveRoot()
						if debRoot == "" || debRoot == "." {
							targetRawDir = absTargetDir
						} else {
							targetRawDir = filepath.Join(absTargetDir, debRoot)
						}
					}
					if rel, err := filepath.Rel(repoOut, targetRawDir); err == nil {
						pkgBaseURL = filepath.ToSlash(rel)
					}
				}

				repoTitle := r.Title
				repoFormat := r.Format
				repoSuite := r.Suite
				repoArch := r.Arch
				if strings.EqualFold(repoArch, "unknown") {
					repoArch = ""
				}
				if hasOv {
					if ov.Title != "" {
						repoTitle = ov.Title
					}
					if ov.Format != "" {
						repoFormat = models.RepoFormat(strings.ToLower(strings.TrimSpace(ov.Format)))
					}
					if ov.Suite != "" {
						repoSuite = ov.Suite
					}
					if ov.Arch != "" {
						repoArch = ov.Arch
					}
				}

				repoCfg := app.Config{
					RepoDir:               r.Path,
					OutputDir:             repoOut,
					Format:                repoFormat,
					Suite:                 repoSuite,
					Arch:                  repoArch,
					Title:                 repoTitle,
					Description:           ov.Description,
					Distro:                ov.Distro,
					Channel:               ov.Channel,
					Badge:                 ov.Badge,
					Icon:                  ov.Icon,
					GPGKey:                ov.GPGKey,
					CompsFile:             ov.Comps,
					IgnoreList:            ov.IgnorePackage,
					ExcludeArch:           ov.ExcludeArch,
					Quiet:                 true,
					Force:                 force,
					PortalURL:             portalLink,
					PackageBaseURL:        pkgBaseURL,
					BaseURL:               pkgBaseURL,
					SkipComponentSiblings: true,
					HiddenSiblingDirs:     hiddenDirs,
					Version:               version,
					Stdout:                safeStdout,
					Stderr:                safeStderr,
				}
				gen := app.NewGenerator(repoCfg)
				if genErr := gen.Run(); genErr != nil {
					errMu.Lock()
					renderErrors = append(renderErrors, fmt.Errorf("failed to render %s: %w", r.Path, genErr))
					errMu.Unlock()
					return
				}

				// Refresh catalog node directly in memory from freshly emitted repoview.json
				descPath := filepath.Join(repoOut, "repoview.json")
				// #nosec G304 -- reading newly generated descriptor from validated child output dir
				if descData, readErr := os.ReadFile(filepath.Clean(descPath)); readErr == nil {
					var desc models.RepoDescriptor
					if json.Unmarshal(descData, &desc) == nil {
						r.Descriptor = &desc
						if desc.Title != "" {
							r.Title = desc.Title
						}
						if desc.PackageCount > 0 || r.PackageCount == 0 {
							r.PackageCount = desc.PackageCount
						}
						if !desc.LastBuild.IsZero() {
							r.LastBuild = desc.LastBuild
						}
						if desc.Arch != "" {
							r.Arch = desc.Arch
						}
						if desc.Distro != "" {
							r.Distro = desc.Distro
						}
						if desc.Channel != "" {
							r.Channel = desc.Channel
						}
					}
				}
				r.IsRendered = true
				if relOut, relErr := filepath.Rel(finalOutputDir, filepath.Join(repoOut, "index.html")); relErr == nil && !strings.HasPrefix(relOut, "..") {
					r.TargetURL = filepath.ToSlash(relOut)
				} else if r.RelPath == "." || r.RelPath == "" {
					r.TargetURL = "repoview/index.html"
				} else {
					r.TargetURL = filepath.ToSlash(filepath.Join(r.RelPath, "repoview", "index.html"))
				}
			})

			if len(renderErrors) > 0 {
				renderFailureCount = len(renderErrors)
				for _, rErr := range renderErrors {
					_, _ = fmt.Fprintf(stderr, "Warning: %v\n", rErr)
				}
			}

			if portalCfg.AutoDiscovery.RequireRendered {
				filtered := make([]*portal.DiscoveredRepo, 0, len(repos))
				for _, r := range repos {
					if r.IsRendered {
						filtered = append(filtered, r)
					}
				}
				repos = filtered
			}
		}
	}

	// Dump starter configuration if requested before ApplyConfig strips hidden entries
	if dumpConfigPath != "" {
		rawCatalog := portal.BuildCatalog(portalCfg.Title, portalCfg.Description, effectivePortalURL, repos)
		dumpOpts := portal.DumpConfigOptions{
			ExistingConfig: portalCfg,
			RawRepos:       repos,
			Force:          force,
		}
		if err := portal.DumpConfigWithOptions(dumpConfigPath, rawCatalog, dumpOpts); err != nil {
			return handleCLIError(err, stderr)
		}
		if !quiet {
			_, _ = fmt.Fprintf(stdout, "Wrote starter configuration to %s\n", dumpConfigPath)
		}
		return app.ExitSuccess
	}

	// Build catalog and apply configuration overrides / pinned repos / external repos
	catalog := portal.BuildCatalog(portalCfg.Title, portalCfg.Description, effectivePortalURL, repos)
	portal.ApplyConfig(catalog, portalCfg)
	if effectiveRepoURL != "" {
		for _, r := range catalog.Repos {
			r.ConfigSnippet = r.BuildConfigSnippet(effectiveRepoURL)
		}
	}

	if !quiet {
		_, _ = fmt.Fprintf(stdout, "Repoview %s - Portal Generator\n", version)
	}

	// Render and write portal HTML and RSS feed
	renderer, err := portal.NewRenderer("")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Template Error: %v\n", err)
		return app.ExitTemplateError
	}

	if err := renderer.WritePortal(finalOutputDir, absTargetDir, catalog, force); err != nil {
		switch {
		case errors.Is(err, app.ErrSafety) || errors.Is(err, app.ErrIntegrity):
			_, _ = fmt.Fprintf(stderr, "Safety Violation: %v\n", err)
			return app.ExitSafetyError
		case errors.Is(err, app.ErrTemplate):
			_, _ = fmt.Fprintf(stderr, "Template Error: %v\n", err)
			return app.ExitTemplateError
		default:
			_, _ = fmt.Fprintf(stderr, "I/O Error: %v\n", err)
			return app.ExitIOError
		}
	}

	if !quiet {
		_, _ = fmt.Fprintf(stdout, "Generated repository portal in %s (%d repositories, %d packages)\n", finalOutputDir, catalog.TotalRepos, catalog.TotalPkgs)
	}

	if renderFailureCount > 0 {
		return app.ExitPartialFailure
	}

	return app.ExitSuccess
}

// main is the primary execution entry point.
func main() {
	// Enforce standard umask (0022) once at process startup so directories (0755)
	// and public web files (0644) remain web-accessible under restrictive cron/systemd umasks.
	util.SetUmask(util.DefaultUmask)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
