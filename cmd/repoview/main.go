// Package main is the entry point for the repoview-go utility.
//
// OBJECTIVES:
// Provides the CLI frontend for repoview-go, transforming RPM repository
// metadata into high-performance, air-gap compliant static HTML browsing sites.
//
// CORE COMPONENTS:
//   - stringList: Custom flag.Value implementation for accumulating repeatable CLI options.
//   - run(): Core CLI runner accepting input arguments and output streams, returning exit codes.
//   - main(): CLI entry point delegating to run() and exiting with the resulting status code.
//
// FUNCTIONALITY:
//   - Parses long-only command-line flags (--output-dir, --baseurl, --force, etc.).
//   - Validates existence and readability of the target repository directory early.
//   - Assembles application configuration (app.Config) with safety validations.
//   - Executes the core generation engine (app.Generator).
//   - Translates domain errors into granular exit codes (ExitSuccess, ExitUsageError, ExitRepoMetadataError, etc.).
//
// DATA FLOW:
//
//	os.Args -> run() -> flag.Parse() -> app.Config -> app.NewGenerator() -> generator.Run() -> exit code
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/edsilegxrepo/repoview/internal/app"
	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/portal"
	"github.com/edsilegxrepo/repoview/internal/util"
)

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
	// Enforce standard umask (0022) so directories (0755) and files (0644)
	// are created with web-accessible permissions even if the parent environment (systemd/cron) has a restrictive umask.
	util.SetUmask(util.DefaultUmask)

	// Check for portal subcommand
	if len(args) > 0 && args[0] == "portal" {
		return runPortal(args[1:], stdout, stderr)
	}

	var (
		repoDir     string
		outputDir   string
		stateDir    string
		compsFile   string
		templateDir string
		title       string
		url         string
		baseURL     string
		format      string
		force       bool
		quiet       bool
		showVer     bool
		portalURL   string
		ignoreList  stringList
		excludeArch stringList
	)

	fs := flag.NewFlagSet("repoview", flag.ContinueOnError)
	fs.SetOutput(stderr)

	fs.StringVar(&outputDir, "output-dir", "repoview", "Output directory")
	fs.StringVar(&stateDir, "state-dir", "", "State directory (default: output directory)")
	fs.StringVar(&title, "title", "Repoview", "Repository title")
	fs.StringVar(&url, "url", "", "Repository URL (for RSS feed)")
	fs.StringVar(&baseURL, "baseurl", "", "Repository base URL for client configuration")
	fs.StringVar(&format, "format", "auto", "Repository format: auto, rpm, or deb")
	fs.StringVar(&templateDir, "template-dir", "", "Template directory")
	fs.BoolVar(&force, "force", false, "Force regeneration")
	fs.BoolVar(&quiet, "quiet", false, "Quiet mode")
	fs.StringVar(&compsFile, "comps", "", "Alternative comps.xml file")
	fs.Var(&ignoreList, "ignore-package", "Ignore package glob (can be repeated)")
	fs.Var(&excludeArch, "exclude-arch", "Exclude architecture (can be repeated)")
	fs.StringVar(&portalURL, "portal-url", "auto", "Parent portal URL ('auto', 'none', or custom URL/path)")
	fs.BoolVar(&showVer, "version", false, "Display version")

	// Custom Usage output to display clean long-only options to the user.
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage:\n  repoview [options] <repodir>\n  repoview portal [options] [repodir]\n\nOptions:\n")

		printOption := func(long, desc string, def interface{}) {
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

			_, _ = fmt.Fprintf(stderr, "  --%-20s\t%s%s\n", long, desc, defStr)
		}

		printOption("output-dir <dir>", "Output directory", "repoview")
		printOption("state-dir <dir>", "State directory", "output directory")
		printOption("title <text>", "Repository title", "Repoview")
		printOption("url <url>", "Repository URL (for RSS feed)", "")
		printOption("baseurl <url>", "Repository base URL for client configuration", "")
		printOption("format <type>", "Repository format (auto, rpm, deb)", "auto")
		printOption("template-dir <dir>", "Template directory", "embedded")
		printOption("comps <file>", "Alternative comps.xml file", "")
		printOption("ignore-package <glob>", "Ignore package glob (can be repeated)", "")
		printOption("exclude-arch <arch>", "Exclude architecture (can be repeated)", "")
		printOption("portal-url <url>", "Parent portal URL ('auto', 'none', or path)", "auto")
		printOption("force", "Force regeneration", false)
		printOption("quiet", "Quiet mode", false)
		printOption("version", "Display version information", false)
	}

	if err := fs.Parse(args); err != nil {
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

	if !quiet {
		_, _ = fmt.Fprintf(stdout, "Repoview %s\n", version)
	}

	// If baseURL is omitted but URL is a full HTTP(S) address, reuse it as the default base URL
	if baseURL == "" && (strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")) {
		baseURL = url
	}

	config := app.Config{
		RepoDir:     repoDir,
		OutputDir:   outputDir,
		StateDir:    stateDir,
		CompsFile:   compsFile,
		TemplateDir: templateDir,
		Title:       title,
		URL:         url,
		BaseURL:     baseURL,
		Format:      repoFormat,
		Force:       force,
		Quiet:       quiet,
		IgnoreList:  ignoreList,
		ExcludeArch: excludeArch,
		PortalURL:   portalURL,
		Version:     version,
	}

	// Instantiate generator and execute generation workflow
	generator := app.NewGenerator(config)
	if err := generator.Run(); err != nil {
		var partialErr *app.PartialFailureError
		var safetyErr *app.SafetyError

		// Translate internal errors into structured diagnostic exit codes
		switch {
		case errors.As(err, &safetyErr):
			_, _ = fmt.Fprintf(stderr, "Safety Violation: %v\n", safetyErr)
			return app.ExitUsageError
		case errors.As(err, &partialErr):
			_, _ = fmt.Fprintf(stderr, "Warning: %v\n", partialErr)
			return app.ExitPartialFailure
		default:
			errMsg := strings.ToLower(err.Error())
			switch {
			case strings.Contains(errMsg, "safety violation"):
				_, _ = fmt.Fprintf(stderr, "Configuration Error: %v\n", err)
				return app.ExitUsageError
			case strings.Contains(errMsg, "repomd") || strings.Contains(errMsg, "primary") || strings.Contains(errMsg, "sqlite") || strings.Contains(errMsg, "decompress") || strings.Contains(errMsg, "packages") || strings.Contains(errMsg, "debian"):
				_, _ = fmt.Fprintf(stderr, "Repository Metadata Error: %v\n", err)
				return app.ExitRepoMetadataError
			case strings.Contains(errMsg, "template"):
				_, _ = fmt.Fprintf(stderr, "Template Error: %v\n", err)
				return app.ExitTemplateError
			case strings.Contains(errMsg, "output dir") || strings.Contains(errMsg, "no space") || strings.Contains(errMsg, "permission"):
				_, _ = fmt.Fprintf(stderr, "I/O Error: %v\n", err)
				return app.ExitIOError
			default:
				_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
				return 1
			}
		}
	}

	return app.ExitSuccess
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

	defaultWorkers := runtime.NumCPU() * 2
	if defaultWorkers < 1 {
		defaultWorkers = 1
	} else if defaultWorkers > 16 {
		defaultWorkers = 16
	}

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
	fs.IntVar(&maxDepth, "max-depth", 5, "Maximum directory scan depth")
	fs.BoolVar(&requireRendered, "require-rendered", false, "Only include repositories with generated repoview pages")
	fs.BoolVar(&renderMissing, "render-missing", false, "Automatically generate repoview pages for raw repositories in parallel")
	fs.BoolVar(&renderAll, "render-all", false, "Force generate repoview pages for all discovered repositories in parallel")
	fs.IntVar(&workers, "workers", defaultWorkers, "Parallel worker concurrency for batch rendering (clamped 1-16)")
	fs.BoolVar(&force, "force", false, "Force overwrite existing index.html even if not created by RepoView")
	fs.BoolVar(&quiet, "quiet", false, "Quiet mode")
	fs.BoolVar(&showVer, "version", false, "Display version")

	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: repoview portal [options] [repodir]\n\nOptions:\n")

		printOption := func(long, desc string, def interface{}) {
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
			_, _ = fmt.Fprintf(stderr, "  --%-20s\t%s%s\n", long, desc, defStr)
		}

		printOption("portal-home <dir>", "Detached portal output directory (outside repo)", "")
		printOption("in-place", "Generate repoview files inside source repository mirrors (legacy)", false)
		printOption("portal-url <url>", "Portal public base URL for UI browsing and portal RSS feed", "")
		printOption("repo-url <url>", "Repository package download and client configuration base URL", "")
		printOption("title <text>", "Portal page title", "Enterprise Package Repositories")
		printOption("description <text>", "Portal page description", "")
		printOption("config <file>", "Path to optional portal.yaml configuration", "")
		printOption("dump-config <file>", "Dump auto-discovered catalog to starter YAML and exit", "")
		printOption("max-depth <n>", "Maximum directory scan depth", 5)
		printOption("require-rendered", "Only include repositories with generated repoview pages", false)
		printOption("render-missing", "Automatically generate repoview pages for raw repositories in parallel", false)
		printOption("render-all", "Force generate repoview pages for all discovered repositories in parallel", false)
		printOption("workers <n>", "Parallel worker concurrency for batch rendering (1-16)", defaultWorkers)
		printOption("force", "Force overwrite existing index.html even if not created by RepoView", false)
		printOption("quiet", "Quiet mode", false)
		printOption("version", "Display version information", false)
	}

	if err := fs.Parse(args); err != nil {
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
		_, _ = fmt.Fprintf(stderr, "Error: failed to resolve directory: %v\n", err)
		return app.ExitUsageError
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
			return app.ExitUsageError
		}
		portalCfg = loadedCfg
	} else {
		foundConfig, err := portal.FindPortalConfigIn(absTargetDir)
		if err == nil {
			loadedCfg, err := portal.LoadConfig(foundConfig)
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "Configuration Error: %v\n", err)
				return app.ExitUsageError
			}
			portalCfg = loadedCfg
		} else {
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
				_, _ = fmt.Fprintf(stderr, "Error: failed to resolve portal home directory: %v\n", err)
				return app.ExitUsageError
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
	}
	scanner := portal.NewScanner(scanCfg)
	repos, err := scanner.Scan()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Scan Error: %v\n", err)
		return app.ExitRepoMetadataError
	}

	// Parallel Batch Rendering for unrendered or all repositories
	if renderAll || renderMissing {
		var targets []*portal.DiscoveredRepo
		for _, r := range repos {
			if r.IsExternal {
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

			numWorkers := workers
			if numWorkers > len(targets) {
				numWorkers = len(targets)
			}

			jobs := make(chan *portal.DiscoveredRepo, len(targets))
			for _, repo := range targets {
				jobs <- repo
			}
			close(jobs)

			var wg sync.WaitGroup
			var errMu sync.Mutex
			var renderErrors []error

			for w := 0; w < numWorkers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for r := range jobs {
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

						// Compute pkgBaseURL for download links & client configs (.repo/.sources)
						var pkgBaseURL string
						if effectiveRepoURL != "" {
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
						} else if effectivePortalHome != "" {
							// Detached with local files: compute relative path from repoOut to raw package files
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

						// Resolve child repository title from overrides or inferred title
						repoTitle := r.Title
						if ov, ok := portalCfg.Overrides[r.RelPath]; ok && ov.Title != "" {
							repoTitle = ov.Title
						} else if ov, ok := portalCfg.Overrides[r.Path]; ok && ov.Title != "" {
							repoTitle = ov.Title
						}

						repoCfg := app.Config{
							RepoDir:        r.Path,
							OutputDir:      repoOut,
							Format:         r.Format,
							Title:          repoTitle,
							Quiet:          true,
							Force:          force,
							PortalURL:      portalLink,
							PackageBaseURL: pkgBaseURL,
							BaseURL:        pkgBaseURL,
							Version:        version,
						}
						gen := app.NewGenerator(repoCfg)
						if genErr := gen.Run(); genErr != nil {
							errMu.Lock()
							renderErrors = append(renderErrors, fmt.Errorf("failed to render %s: %w", r.Path, genErr))
							errMu.Unlock()
						}
					}
				}()
			}
			wg.Wait()

			if len(renderErrors) > 0 {
				for _, rErr := range renderErrors {
					_, _ = fmt.Fprintf(stderr, "Warning: %v\n", rErr)
				}
			}

			// Re-scan directory with the final RequireRendered setting
			scanCfg.RequireRendered = portalCfg.AutoDiscovery.RequireRendered
			repos, err = portal.NewScanner(scanCfg).Scan()
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "Re-scan Error: %v\n", err)
				return app.ExitRepoMetadataError
			}
		}
	}

	// Build catalog and apply configuration overrides / pinned repos / external repos
	catalog := portal.BuildCatalog(portalCfg.Title, portalCfg.Description, effectivePortalURL, repos)
	portal.ApplyConfig(catalog, portalCfg)
	if effectiveRepoURL != "" {
		for _, r := range catalog.Repos {
			r.ConfigSnippet = r.BuildConfigSnippet(effectiveRepoURL)
		}
	}

	// Dump starter configuration if requested
	if dumpConfigPath != "" {
		if err := portal.DumpConfig(dumpConfigPath, catalog); err != nil {
			_, _ = fmt.Fprintf(stderr, "Error dumping config: %v\n", err)
			return app.ExitIOError
		}
		if !quiet {
			_, _ = fmt.Fprintf(stdout, "Wrote starter configuration to %s\n", dumpConfigPath)
		}
		return app.ExitSuccess
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
		var safetyErr *portal.ErrSafetyOverwrite
		if errors.As(err, &safetyErr) {
			_, _ = fmt.Fprintf(stderr, "Safety Violation: %v\n", safetyErr)
			return app.ExitUsageError
		}
		_, _ = fmt.Fprintf(stderr, "I/O Error: %v\n", err)
		return app.ExitIOError
	}

	if !quiet {
		_, _ = fmt.Fprintf(stdout, "Generated repository portal in %s (%d repositories, %d packages)\n", finalOutputDir, catalog.TotalRepos, catalog.TotalPkgs)
	}

	return app.ExitSuccess
}

// main is the primary execution entry point.
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
