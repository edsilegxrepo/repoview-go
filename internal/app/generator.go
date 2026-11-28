// Package app coordinates the core generation workflow for repoview-go.
//
// OBJECTIVES:
// Orchestrate the end-to-end transformation of an RPM or Debian/Ubuntu repository into a
// modern, air-gap compliant static website. Manages repository metadata parsing, package
// filtering, deep header/archive introspection, taxonomy construction, incremental state
// persistence, lock-free parallel rendering, and automated stale file cleanup.
//
// CORE COMPONENTS:
//   - Config: Encapsulates all CLI flags, paths, filtering globs, and runtime settings.
//   - Generator: Primary workflow orchestrator managing repository lifecycle and rendering.
//   - ExitCode Constants: Structured UNIX process exit codes for diagnostic automation.
//   - SafetyError & PartialFailureError: Custom domain error types for safety violations and partial runs.
//   - validateOutputDirSafety: Guard function preventing accidental repository data destruction.
//   - detectSiblingRepos / findParentPortal: Filesystem crawlers linking sibling channels and parent portals.
//
// FUNCTIONALITY:
//   - Safety Pre-validation & Incremental Short-Circuit: Halts immediately if output threatens
//     source data, and skips full regeneration when metadata/config SHA-256 fingerprints match.
//   - Repository Ingestion: Dispatches RPM (SQLite/XML + updateinfo) or DEB/DDEB backends via repo.OpenWithOptions.
//   - Bulk Enrichment & On-Demand Memory Management: Batches changelog/dependency lookups and opens each
//     latest archive in a single pass, freeing file lists immediately via defer.
//   - Lock-Free Parallel Rendering: Distributes group and package rendering via util.ForEachWorkers.
//   - Incremental Persistence: Tracks SHA-256 hashes in state.StateStore and prunes orphaned HTML files.
//
// DATA FLOW:
//
//	app.Config (+ repoview.yaml) -> repo.OpenWithOptions -> logic.GroupingService ->
//	render.Renderer (util.ForEachWorkers) -> state.StateStore + repoview.json -> Output dir
package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edsilegxrepo/repoview/internal/logic"
	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/render"
	"github.com/edsilegxrepo/repoview/internal/repo"
	"github.com/edsilegxrepo/repoview/internal/state"
	"github.com/edsilegxrepo/repoview/internal/taxonomy"
	"github.com/edsilegxrepo/repoview/internal/util"
)

// Diagnostic exit codes for automated tooling and scripting
const (
	ExitSuccess           = 0 // View generated successfully without error
	ExitGeneralError      = 1 // Unexpected/unclassified internal error
	ExitUsageError        = 2 // Invalid flags or CLI arguments
	ExitRepoMetadataError = 3 // Missing or corrupt repomd.xml, SQLite DBs, comps.xml
	ExitTemplateError     = 4 // Error loading or parsing HTML/XML templates
	ExitIOError           = 5 // Disk full, permission denied, filesystem error
	ExitPartialFailure    = 6 // Finished with package or child repository errors
	ExitConfigError       = 7 // Invalid or unreadable configuration file or filter glob
	ExitSafetyError       = 8 // Safety guard or security/integrity violation
)

// Domain sentinel error aliases exposed by app for callers and tests.
var (
	ErrUsage        = models.ErrUsage
	ErrRepoMetadata = models.ErrRepoMetadata
	ErrTemplate     = models.ErrTemplate
	ErrIO           = models.ErrIO
	ErrConfig       = models.ErrConfig
	ErrSafety       = models.ErrSafety
	ErrIntegrity    = models.ErrIntegrity
)

// SafetyError indicates a safety violation such as outputDir pointing to repoDir
type SafetyError struct {
	Msg string
}

func (e *SafetyError) Error() string { return e.Msg }
func (e *SafetyError) Unwrap() error { return models.ErrSafety }

// PartialFailureError indicates generation completed but some packages had errors
type PartialFailureError struct {
	ErrorCount int64
}

func (e *PartialFailureError) Error() string {
	return fmt.Sprintf("completed with %d package errors during generation", e.ErrorCount)
}

// Config holds the configuration for the Generator
type Config struct {
	RepoDir               string            // Root directory of the source RPM/DEB repository
	OutputDir             string            // Target directory for generated HTML and assets
	StateDir              string            // Directory for saving incremental state JSON files
	CompsFile             string            // Path to an alternative comps.xml file
	TemplateDir           string            // Path to custom templates (embedded templates used if empty)
	Title                 string            // Display title for the repository view
	Description           string            // Optional repository description
	URL                   string            // Public base URL of the repository (for RSS feed)
	BaseURL               string            // Explicit base URL for client repository configuration (.repo)
	Format                models.RepoFormat // Repository format: auto, rpm, or deb
	Suite                 string            // Optional Debian suite/codename selector
	Arch                  string            // Optional Debian architecture selector
	Distro                string            // Optional explicit distro label
	Channel               string            // Optional explicit channel label
	Badge                 string            // Optional UI badge
	Icon                  string            // Optional distro icon override
	Force                 bool              // If true, forces complete regeneration ignoring state cache
	Quiet                 bool              // If true, suppresses standard informational output
	IgnoreList            []string          // Glob patterns of package names/NVRAs to ignore
	ExcludeArch           []string          // Hardware architectures to exclude from generation
	PortalURL             string            // Explicit or auto-detected URL to parent catalog portal ("auto", "none", or path/URL)
	PackageBaseURL        string            // Base URL or relative path prefix for package file downloads
	GPGKey                string            // Optional GPG public key URL/path for .repo / .sources snippets
	Workers               int               // Optional explicit worker pool limit (clamped 1..16)
	Fsync                 bool              // Flush derived HTML files with per-file fsync (state/index always fsync)
	SkipSiblings          bool              // Optional: disables sibling channel/arch switching chips
	SkipComponentSiblings bool              // Optional: omits Debian component siblings when suite components are aggregated
	HiddenSiblingDirs     map[string]bool   // Optional normalized clean paths of hidden sibling repos
	Version               string            // Repoview tool version string
	Stdout                io.Writer         // Optional output stream (defaults to os.Stdout when nil)
	Stderr                io.Writer         // Optional warning/error stream (defaults to os.Stderr when nil)
	OutputSink            render.OutputSink // Optional custom storage sink for generated files
}

// Generator orchestrates the repository view generation process.
// It manages the flow of reading repository metadata, processing packages,
// and rendering the static HTML output.
type Generator struct {
	config Config
	logMu  sync.Mutex
}

// NewGenerator creates a new Generator instance with the provided configuration.
func NewGenerator(cfg Config) *Generator {
	return &Generator{config: cfg}
}

func (g *Generator) stdout() io.Writer {
	if g.config.Stdout != nil {
		return g.config.Stdout
	}
	return os.Stdout
}

func (g *Generator) stderr() io.Writer {
	if g.config.Stderr != nil {
		return g.config.Stderr
	}
	return os.Stderr
}

// say prints a formatted message to the configured Stdout writer unless quiet mode is active.
func (g *Generator) say(format string, args ...interface{}) {
	if !g.config.Quiet {
		g.logMu.Lock()
		_, _ = fmt.Fprintf(g.stdout(), format, args...)
		g.logMu.Unlock()
	}
}

// warn prints a non-fatal warning message to the configured Stderr writer unless quiet mode is active.
func (g *Generator) warn(format string, args ...interface{}) {
	if !g.config.Quiet {
		g.logMu.Lock()
		_, _ = fmt.Fprintf(g.stderr(), format, args...)
		g.logMu.Unlock()
	}
}

func (g *Generator) applyRepoYAMLIfPresent() (string, error) {
	cfgPath, found := FindRepoConfig(g.config.RepoDir)
	if !found {
		return "", nil
	}
	rc, err := LoadRepoConfig(cfgPath)
	if err != nil {
		return "", err
	}
	if (g.config.Title == "" || g.config.Title == "Repoview") && rc.Title != "" {
		g.config.Title = rc.Title
	}
	if g.config.Description == "" && rc.Description != "" {
		g.config.Description = rc.Description
	}
	if g.config.CompsFile == "" && rc.Comps != "" {
		g.config.CompsFile = rc.Comps
	}
	if g.config.TemplateDir == "" && rc.TemplateDir != "" {
		g.config.TemplateDir = rc.TemplateDir
	}
	if g.config.URL == "" && rc.URL != "" {
		g.config.URL = rc.URL
	}
	if g.config.BaseURL == "" && rc.BaseURL != "" {
		g.config.BaseURL = rc.BaseURL
	}
	if g.config.PackageBaseURL == "" && rc.PackageBaseURL != "" {
		g.config.PackageBaseURL = rc.PackageBaseURL
	}
	if (g.config.PortalURL == "" || g.config.PortalURL == "auto") && rc.PortalURL != "" {
		g.config.PortalURL = rc.PortalURL
	}
	if g.config.GPGKey == "" && rc.GPGKey != "" {
		g.config.GPGKey = rc.GPGKey
	}
	if g.config.Format == "" && rc.Format != "" {
		g.config.Format = models.RepoFormat(strings.ToLower(strings.TrimSpace(rc.Format)))
	}
	if g.config.Suite == "" && rc.Suite != "" {
		g.config.Suite = rc.Suite
	}
	if g.config.Arch == "" && rc.Arch != "" {
		g.config.Arch = rc.Arch
	}
	if g.config.Workers == 0 && rc.Workers > 0 {
		g.config.Workers = rc.Workers
	}
	if !g.config.Fsync && rc.Fsync {
		g.config.Fsync = true
	}
	if g.config.Distro == "" && rc.Distro != "" {
		g.config.Distro = rc.Distro
	}
	if g.config.Channel == "" && rc.Channel != "" {
		g.config.Channel = rc.Channel
	}
	if g.config.Badge == "" && rc.Badge != "" {
		g.config.Badge = rc.Badge
	}
	if g.config.Icon == "" && rc.Icon != "" {
		g.config.Icon = rc.Icon
	}
	if len(g.config.IgnoreList) == 0 && len(rc.IgnorePackage) > 0 {
		g.config.IgnoreList = rc.IgnorePackage
	}
	if len(g.config.ExcludeArch) == 0 && len(rc.ExcludeArch) > 0 {
		g.config.ExcludeArch = rc.ExcludeArch
	}
	return cfgPath, nil
}

// Run executes the full repository view generation pipeline.
func (g *Generator) Run() error {
	// 0. Safety validation: output directory must be safe to write and cannot threaten repository data
	if err := validateOutputDirSafety(g.config.RepoDir, g.config.OutputDir); err != nil {
		return err
	}

	repoCfgPath, err := g.applyRepoYAMLIfPresent()
	if err != nil {
		return err
	}

	// Fast incremental short-circuit check when Force is disabled and using local filesystem output
	repoFP := g.computeRepoFingerprint(repoCfgPath)
	if !g.config.Force && repoFP != "" && g.config.OutputSink == nil {
		if unchanged, err := g.canShortCircuitIncremental(repoFP); err == nil && unchanged {
			g.say("Repository metadata unchanged; skipping regeneration (use --force to rebuild).\n")
			return nil
		}
	}

	// 1. Setup Repo Access
	reader, meta, cleanup, err := g.prepareRepository()
	if err != nil {
		return err
	}
	defer cleanup()

	// 2. Fetch & Filter Packages
	allPkgs, err := g.loadPackages(reader)
	if err != nil {
		return err
	}

	// Enrich all packages with changelogs in bulk when supported by backend (RPM)
	if ce, ok := reader.(repo.ChangelogEnricher); ok {
		g.say("Enriching packages with changelogs...")
		if err := ce.EnrichPackagesWithChangelogs(allPkgs); err != nil {
			g.warn("Warning: failed to enrich packages: %v\n", err)
		}
		g.say("done\n")
	}

	// Attach security/errata advisories when updateinfo.xml* is available
	if ae, ok := reader.(repo.AdvisoryEnricher); ok {
		if err := ae.EnrichPackagesWithAdvisories(allPkgs); err != nil {
			g.warn("Warning: failed to load advisories: %v\n", err)
		}
	}

	// 3. Process Groups
	var locs *repo.RepoLocations
	if meta != nil {
		locs = meta.Locations
	}
	groups, letterGroups, letters, pkgVersions, err := g.processGroups(locs, allPkgs)
	if err != nil {
		return err
	}
	logic.AssignCollisionSafePackageFilenames(pkgVersions)
	allGroups := make([]*logic.GroupData, 0, len(groups)+len(letterGroups))
	allGroups = append(allGroups, groups...)
	allGroups = append(allGroups, letterGroups...)

	latestPkgs := make([]*models.Package, 0, len(pkgVersions))
	for _, versions := range pkgVersions {
		if len(versions) > 0 {
			latestPkgs = append(latestPkgs, versions[0])
		}
	}

	// Bulk-load dependencies in batches for selected latest packages when supported (SQLite RPM).
	if de, ok := reader.(repo.DependencyEnricher); ok {
		if err := de.EnrichPackagesWithDependencies(latestPkgs); err != nil {
			g.warn("Warning: failed to bulk-load dependencies: %v\n", err)
		}
	}

	// Enrich package details upfront only when backend does not support single-pass EnrichForRender.
	if _, isRenderEnricher := reader.(repo.PackageRenderEnricher); !isRenderEnricher {
		g.say("Inspecting package metadata on disk...")
		reader.EnrichPackageDetails(latestPkgs)
		g.say("done\n")
	}

	// 4. Setup Output & Renderer
	renderer, stateStore, assetKeys, err := g.setupRenderer(letters, meta, len(pkgVersions))
	if err != nil {
		return err
	}
	renderer.SetGroups(groups)
	renderer.SetFormat(g.config.Format)

	// Sibling repository detection (architectures and channels)
	if !g.config.SkipSiblings {
		siblings := detectSiblingReposForOutput(g.config.RepoDir, g.config.OutputDir, g.config.PortalURL, g.config.HiddenSiblingDirs, g.config.SkipComponentSiblings)
		renderer.SetSiblings(siblings)
	}

	if meta != nil {
		suiteName := g.config.Suite
		if suiteName == "" {
			suiteName = meta.Suite
		}
		if suiteName != "" {
			renderer.SetSuiteComponents(suiteName, strings.Join(meta.Components, " "))
		}
	}

	repoID := util.SanitizeFilename(g.config.Title)
	if repoID == "" {
		repoID = "repository"
	}
	effectiveBaseURL := g.config.BaseURL
	if effectiveBaseURL == "" && (strings.HasPrefix(g.config.URL, "http://") || strings.HasPrefix(g.config.URL, "https://")) {
		effectiveBaseURL = g.config.URL
	}
	renderer.SetRepoMeta(repoID, effectiveBaseURL)

	portalURL := g.resolvePortalURL()
	if portalURL != "" {
		renderer.SetPortalURL(portalURL)
	}
	if g.config.PackageBaseURL != "" {
		renderer.SetPackageBaseURL(g.config.PackageBaseURL)
	}
	if g.config.GPGKey != "" {
		renderer.SetGPGKey(g.config.GPGKey)
	}

	// Helper data structures
	pkgPrimaryGroup := mapPrimaryGroups(allGroups)

	var generatedFiles []string
	generatedFiles = append(generatedFiles, assetKeys...)

	// 5. Render Groups (renders all groups including letter groups)
	g.say("Generating pages...\n")
	groupFiles, err := g.renderGroups(renderer, stateStore, allGroups)
	if err != nil {
		return err
	}
	generatedFiles = append(generatedFiles, groupFiles...)

	// 6. Render Packages
	pkgFiles, errorCount := g.renderPackages(reader, renderer, stateStore, pkgVersions, pkgPrimaryGroup)
	generatedFiles = append(generatedFiles, pkgFiles...)

	// 7. Render Indices (pass only real groups to index page, letter groups are in letter bar)
	indexFiles, err := g.renderIndices(renderer, stateStore, groups, pkgVersions)
	if err != nil {
		return err
	}
	generatedFiles = append(generatedFiles, indexFiles...)

	// 7b. Render repoview.json descriptor
	descFiles, err := g.renderDescriptor(renderer, stateStore, portalURL, meta, allPkgs)
	if err != nil {
		return fmt.Errorf("failed to write repoview.json: %w", err)
	}
	generatedFiles = append(generatedFiles, descFiles...)

	// Persist repository fingerprint only after clean zero-error run
	if errorCount == 0 && repoFP != "" {
		stateStore.Set(state.MetadataKeyRepoFingerprint, repoFP)
		generatedFiles = append(generatedFiles, state.MetadataKeyRepoFingerprint)
	}

	// 8. Cleanup (only if no package generation errors to prevent deleting valid pages)
	if errorCount > 0 {
		g.say("Warning: %d packages encountered errors during generation, skipping stale cleanup\n", errorCount)
	} else {
		g.cleanupStale(renderer, stateStore, generatedFiles)
	}

	if err := stateStore.Save(); err != nil {
		return fmt.Errorf("%w: failed to save state: %w", models.ErrIO, err)
	}

	g.say("Complete.\n")
	if errorCount > 0 {
		return &PartialFailureError{ErrorCount: errorCount}
	}
	return nil
}

// prepareRepository opens the detected or configured RPM/DEB backend via repo.OpenWithOptions.
func (g *Generator) prepareRepository() (repo.RepoReader, *repo.RepoMetadata, func(), error) {
	g.say("Examining repository...")
	reader, meta, cleanup, err := repo.OpenWithOptions(g.config.RepoDir, repo.OpenOptions{
		Format: g.config.Format,
		Suite:  g.config.Suite,
		Arch:   g.config.Arch,
		Warn:   g.stderr(),
	})
	if err != nil {
		return nil, nil, func() {}, err
	}
	g.config.Format = meta.Format
	if meta.Format == models.FormatDEB {
		g.say("Detected Debian repository format\n")
	} else {
		g.say("Detected RPM repository format\n")
	}
	return reader, meta, cleanup, nil
}

// loadPackages fetches all packages and filters them based on config.
func (g *Generator) loadPackages(reader repo.RepoReader) ([]*models.Package, error) {
	g.say("Reading packages...")
	allPkgs, err := reader.GetAllPackages()
	if err != nil {
		return nil, fmt.Errorf("failed to read packages: %w", err)
	}
	g.say("found %d packages\n", len(allPkgs))

	if len(g.config.IgnoreList) > 0 || len(g.config.ExcludeArch) > 0 {
		allPkgs, err = logic.FilterPackages(allPkgs, g.config.IgnoreList, g.config.ExcludeArch)
		if err != nil {
			return nil, fmt.Errorf("failed to filter packages: %w", err)
		}
		g.say("Filtered down to %d packages\n", len(allPkgs))
	}
	return allPkgs, nil
}

// processGroups loads comps.xml (if available or specified) and organizes packages into groups.
func (g *Generator) processGroups(locs *repo.RepoLocations, allPkgs []*models.Package) ([]*logic.GroupData, []*logic.GroupData, []string, map[string][]*models.Package, error) {
	var comps *models.Comps
	var compsSrc string
	if locs != nil {
		compsSrc = locs.Groups
	}
	if g.config.CompsFile != "" {
		compsSrc = g.config.CompsFile
	}

	if compsSrc != "" {
		g.say("Parsing comps.xml...")
		groupsPath, cleanupG, err := repo.DecompressFile(compsSrc)
		if err == nil {
			defer cleanupG()
			comps, err = repo.ParseComps(groupsPath)
			if err != nil {
				g.warn("Warning: failed to parse comps.xml: %v\n", err)
			}
		} else {
			g.warn("Warning: failed to decompress groups: %v\n", err)
		}
		g.say("done\n")
	}

	g.say("Organizing groups...")
	groupService := logic.NewGroupingService(allPkgs, comps)
	pkgVersions := groupService.PkgVersions()
	groups, err := groupService.GetGroups()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to organize groups: %w", err)
	}

	letterGroups := groupService.GetLetterGroups()
	letters := make([]string, 0, len(letterGroups))
	for _, lg := range letterGroups {
		if lg.Letter != "" {
			letters = append(letters, lg.Letter)
		}
	}

	g.say("done (%d groups, %d letters)\n", len(groups), len(letters))

	return groups, letterGroups, letters, pkgVersions, nil
}

func (g *Generator) resolveStatePath() string {
	stateDir := g.config.OutputDir
	stateFilename := "state.json"

	if g.config.StateDir != "" {
		stateDir = g.config.StateDir
		hash := sha256.Sum256([]byte(g.config.OutputDir))
		stateFilename = fmt.Sprintf("%x.state.json", hash)
	}
	return filepath.Join(stateDir, stateFilename)
}

func (g *Generator) computeRepoFingerprint(repoCfgPath string) string {
	h := sha256.New()
	writeField := func(k, v string) {
		_, _ = fmt.Fprintf(h, "%s=%s\x00", k, v)
	}
	writeField("title", g.config.Title)
	writeField("desc", g.config.Description)
	writeField("url", g.config.URL)
	writeField("baseurl", g.config.BaseURL)
	writeField("pkgbase", g.config.PackageBaseURL)
	writeField("portal", g.config.PortalURL)
	writeField("gpgkey", g.config.GPGKey)
	writeField("tmpl", g.config.TemplateDir)
	writeField("comps", g.config.CompsFile)
	writeField("format", string(g.config.Format))
	writeField("suite", g.config.Suite)
	writeField("arch", g.config.Arch)
	writeField("version", g.config.Version)
	writeField("ignore", strings.Join(g.config.IgnoreList, ","))
	writeField("exarch", strings.Join(g.config.ExcludeArch, ","))

	hashFile := func(label, p string) bool {
		if p == "" {
			return false
		}
		// #nosec G304 -- hashing validated metadata inputs for fingerprint comparison
		f, err := os.Open(filepath.Clean(p))
		if err != nil {
			return false
		}
		defer func() { _ = f.Close() }()
		_, _ = fmt.Fprintf(h, "file:%s\x00", label)
		if _, err := io.Copy(h, io.LimitReader(f, 32*1024*1024)); err != nil {
			return false
		}
		return true
	}

	hasMeta := hashFile("repomd", filepath.Join(g.config.RepoDir, "repodata", "repomd.xml"))
	if g.config.CompsFile != "" {
		_ = hashFile("comps_override", g.config.CompsFile)
	}
	if !hasMeta {
		for _, cand := range []string{"Release", "InRelease", "Packages", "Packages.gz", "Packages.bz2", "Packages.xz", "Packages.zst"} {
			if hashFile(cand, filepath.Join(g.config.RepoDir, cand)) {
				hasMeta = true
			}
		}
		// Multi-suite Debian roots: include Release / InRelease timestamps under dists/*/
		if matches, _ := filepath.Glob(filepath.Join(g.config.RepoDir, "dists", "*", "Release")); len(matches) > 0 {
			sort.Strings(matches)
			for _, m := range matches {
				if hashFile(m, m) {
					hasMeta = true
				}
			}
		}
	}
	if !hasMeta {
		return ""
	}
	if repoCfgPath != "" {
		_ = hashFile("repoview_yaml", repoCfgPath)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (g *Generator) canShortCircuitIncremental(fp string) (bool, error) {
	statePath := g.resolveStatePath()
	if _, err := os.Stat(statePath); err != nil {
		return false, err
	}
	st, err := state.NewStateStore(statePath)
	if err != nil {
		return false, err
	}
	prevFP, ok := st.Get(state.MetadataKeyRepoFingerprint)
	if !ok || prevFP != fp {
		return false, nil
	}
	for _, req := range []string{"index.html", "search.json", "repoview.json", "latest-feed.xml"} {
		if fi, err := os.Stat(filepath.Join(g.config.OutputDir, req)); err != nil || fi.IsDir() {
			return false, nil
		}
	}
	return true, nil
}

// setupRenderer creates output directories and initializes the renderer.
func (g *Generator) setupRenderer(letters []string, meta *repo.RepoMetadata, totalPackages int) (*render.Renderer, *state.StateStore, []string, error) {
	if err := validateOutputDirSafety(g.config.RepoDir, g.config.OutputDir); err != nil {
		return nil, nil, nil, err
	}

	// Parse and validate templates BEFORE touching outputDir or state files so invalid
	// custom templates never leave an existing published site corrupted or wiped.
	renderer, err := render.NewRenderer(g.config.OutputDir, g.config.TemplateDir, g.config.Title, g.config.Version, letters)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to initialize renderer: %w", err)
	}
	if g.config.OutputSink != nil {
		renderer.SetSink(g.config.OutputSink)
	}
	renderer.SetTotalPackages(totalPackages)
	if meta != nil {
		renderer.SetRevision(meta.Revision)
		if g.config.Description != "" {
			renderer.SetDescription(g.config.Description)
		} else if meta.Description != "" {
			renderer.SetDescription(meta.Description)
		}
	} else if g.config.Description != "" {
		renderer.SetDescription(g.config.Description)
	}

	if g.config.OutputSink == nil {
		if err := util.EnsureDir(g.config.OutputDir); err != nil {
			return nil, nil, nil, fmt.Errorf("%w: failed to create output dir: %w", models.ErrIO, err)
		}
	}

	statePath := g.resolveStatePath()
	if err := util.EnsureDir(filepath.Dir(statePath)); err != nil {
		return nil, nil, nil, fmt.Errorf("%w: failed to create state dir: %w", models.ErrIO, err)
	}

	stateStore, err := state.NewStateStore(statePath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: failed to initialize state store: %w", models.ErrIO, err)
	}

	assetKeys, err := renderer.WriteAssetsWithState(stateStore, g.config.Force)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to write assets: %w", err)
	}

	return renderer, stateStore, assetKeys, nil
}

func (g *Generator) effectiveWorkers(itemCount int) int {
	return util.ClampWorkers(g.config.Workers, itemCount)
}

func (g *Generator) writeDerivedFile(renderer *render.Renderer, filename string, content []byte) error {
	if g.config.Fsync {
		return renderer.WriteToFile(filename, content)
	}
	return renderer.WriteToFileFast(filename, content)
}

// renderGroups renders all group pages concurrently using a bounded worker pool.
func (g *Generator) renderGroups(renderer *render.Renderer, stateStore *state.StateStore, allGroups []*logic.GroupData) ([]string, error) {
	if len(allGroups) == 0 {
		return nil, nil
	}

	generatedFiles := make([]string, len(allGroups))
	verboseGroupLog := len(allGroups) <= 100
	var (
		errMu    sync.Mutex
		firstErr error
	)
	util.ForEachWorkers(allGroups, g.effectiveWorkers(len(allGroups)), func(next func() (int, *logic.GroupData, bool)) {
		var pending []state.Entry
		for {
			idx, grp, ok := next()
			if !ok {
				break
			}
			content, err := renderer.RenderGroup(grp)
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("%w: failed to render group %s: %w", models.ErrTemplate, grp.Name, err)
				}
				errMu.Unlock()
				continue
			}

			hash, hasChanged := stateStore.CheckChanged(grp.Filename, content)
			if g.config.Force || hasChanged {
				if verboseGroupLog {
					g.say("Writing group %s\n", grp.Filename)
				}
				if err := g.writeDerivedFile(renderer, grp.Filename, content); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("failed to write group %s: %w", grp.Filename, err)
					}
					errMu.Unlock()
					continue
				}
				pending = append(pending, state.Entry{Filename: grp.Filename, Hash: hash})
			}
			generatedFiles[idx] = grp.Filename
		}
		stateStore.MarkWrittenBatch(pending)
	})
	if firstErr != nil {
		return nil, firstErr
	}
	return generatedFiles, nil
}

// renderPackages renders all package pages in parallel using a bounded worker pool.
func (g *Generator) renderPackages(db repo.RepoReader, renderer *render.Renderer, stateStore *state.StateStore, pkgVersions map[string][]*models.Package, pkgPrimaryGroup map[string]*logic.GroupData) ([]string, int64) {
	uniqueNames := make([]string, 0, len(pkgVersions))
	for name := range pkgVersions {
		uniqueNames = append(uniqueNames, name)
	}
	sort.Strings(uniqueNames)

	rawGenerated := make([]string, len(uniqueNames))
	var errorCount int64
	var writtenCount int64
	verbosePkgLog := len(uniqueNames) <= 200

	util.ForEachWorkers(uniqueNames, g.config.Workers, func(next func() (int, string, bool)) {
		var pending []state.Entry
		for {
			idx, pkgName, ok := next()
			if !ok {
				break
			}
			versions := pkgVersions[pkgName]
			primaryGroup := pkgPrimaryGroup[pkgName]

			filename, entry, wrote, err := g.renderSinglePackageWithBatch(db, renderer, stateStore, versions, primaryGroup, pkgName, verbosePkgLog)
			if err != nil {
				g.warn("Error rendering package %s: %v\n", pkgName, err)
				atomic.AddInt64(&errorCount, 1)
				continue
			}
			rawGenerated[idx] = filename
			if wrote {
				pending = append(pending, entry)
				atomic.AddInt64(&writtenCount, 1)
			}
		}
		stateStore.MarkWrittenBatch(pending)
	})

	if !verbosePkgLog && writtenCount > 0 {
		g.say("Wrote %d package pages (%d total).\n", writtenCount, len(uniqueNames))
	}

	generatedFiles := make([]string, 0, len(uniqueNames))
	for _, f := range rawGenerated {
		if f != "" {
			generatedFiles = append(generatedFiles, f)
		}
	}

	if errorCount > 0 {
		g.say("Finished with %d errors during package generation.\n", errorCount)
	}

	return generatedFiles, errorCount
}

// renderSinglePackage renders an individual package page, immediately recording state on write.
func (g *Generator) renderSinglePackage(db repo.RepoReader, renderer *render.Renderer, stateStore *state.StateStore, versions []*models.Package, primaryGroup *logic.GroupData, pkgName string) (string, error) {
	filename, entry, wrote, err := g.renderSinglePackageWithBatch(db, renderer, stateStore, versions, primaryGroup, pkgName, true)
	if err != nil {
		return "", err
	}
	if wrote {
		stateStore.MarkWritten(entry.Filename, entry.Hash)
	}
	return filename, nil
}

// renderSinglePackageWithBatch renders an individual package page and returns the deferred state entry
// while ensuring heap cleanup of file manifests.
func (g *Generator) renderSinglePackageWithBatch(db repo.RepoReader, renderer *render.Renderer, stateStore *state.StateStore, versions []*models.Package, primaryGroup *logic.GroupData, pkgName string, verboseLog bool) (string, state.Entry, bool, error) {
	if len(versions) == 0 {
		return "", state.Entry{}, false, fmt.Errorf("no versions available for package %s", pkgName)
	}

	latest := versions[0]
	latest.AllVersions = versions

	// Populate package dependencies on-demand if backend did not bulk-load them
	if dr, ok := db.(repo.DependencyReader); ok && latest.Dependencies == nil {
		deps, err := dr.GetPackageDependencies(latest.PkgKey)
		if err != nil {
			return "", state.Entry{}, false, fmt.Errorf("failed to read dependencies for %s: %w", pkgName, err)
		}
		latest.Dependencies = deps
	}

	// Single-pass archive enrichment + on-demand file list loading, releasing Files heap slice via defer
	if latest.LocationHref != "" && db != nil {
		if re, ok := db.(repo.PackageRenderEnricher); ok {
			_ = re.EnrichForRender(g.config.RepoDir, latest)
		} else if latest.Details == nil || len(latest.Details.Files) == 0 {
			files, err := db.ReadPackageFiles(g.config.RepoDir, latest)
			if err == nil && len(files) > 0 {
				if latest.Details == nil {
					latest.Details = &models.PackageDetails{}
				}
				latest.Details.Files = files
			}
		}
	}
	if latest.Details != nil {
		defer func() {
			if latest.Details != nil {
				latest.Details.Files = nil
			}
		}()
	}

	if primaryGroup == nil {
		firstLetter := util.FirstLetter(pkgName)
		primaryGroup = &logic.GroupData{
			Name:     "Letter " + firstLetter,
			Filename: fmt.Sprintf("letter_%s.group.html", strings.ToLower(firstLetter)),
		}
	}

	filename := latest.Filename()
	var writtenEntry state.Entry
	var wrote bool

	renderErr := renderer.RenderPackageTo(latest, primaryGroup, func(content []byte) error {
		hash, hasChanged := stateStore.CheckChanged(filename, content)
		if g.config.Force || hasChanged {
			if verboseLog {
				g.say("Writing package %s\n", filename)
			}
			if err := g.writeDerivedFile(renderer, filename, content); err != nil {
				return err
			}
			writtenEntry = state.Entry{Filename: filename, Hash: hash}
			wrote = true
		}
		return nil
	})
	if renderErr != nil {
		return "", state.Entry{}, false, fmt.Errorf("%w: %w", models.ErrTemplate, renderErr)
	}

	return filename, writtenEntry, wrote, nil
}

// renderIndices generates the index.html, search.json, and RSS feed.
func (g *Generator) renderIndices(renderer *render.Renderer, stateStore *state.StateStore, availableGroups []*logic.GroupData, pkgVersions map[string][]*models.Package) ([]string, error) {
	var generatedFiles []string

	// Get latest packages for index/rss, attaching AllVersions and sorting deterministically
	var latestPkgs []*models.Package
	for _, versions := range pkgVersions {
		if len(versions) > 0 {
			p := versions[0]
			p.AllVersions = versions
			latestPkgs = append(latestPkgs, p)
		}
	}
	sort.Slice(latestPkgs, func(i, j int) bool {
		if latestPkgs[i].TimeBuild != latestPkgs[j].TimeBuild {
			return latestPkgs[i].TimeBuild > latestPkgs[j].TimeBuild
		}
		return latestPkgs[i].Name < latestPkgs[j].Name
	})

	// Generate Search Index (using all latest packages)
	searchContent, err := renderer.RenderSearchIndex(latestPkgs)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to render search index: %w", models.ErrTemplate, err)
	}
	searchHash, searchChanged := stateStore.CheckChanged("search.json", searchContent)
	if g.config.Force || searchChanged {
		g.say("Writing search.json\n")
		if err := renderer.WriteToFile("search.json", searchContent); err != nil {
			return nil, fmt.Errorf("failed to write search.json: %w", err)
		}
		stateStore.MarkWritten("search.json", searchHash)
	}
	generatedFiles = append(generatedFiles, "search.json")

	limit := 30
	if len(latestPkgs) > limit {
		latestPkgs = latestPkgs[:limit]
	}

	effectiveRSSURL := g.config.URL
	if effectiveRSSURL == "" && (strings.HasPrefix(g.config.BaseURL, "http://") || strings.HasPrefix(g.config.BaseURL, "https://")) {
		effectiveRSSURL = g.config.BaseURL
	}

	idxContent, err := renderer.RenderIndex(availableGroups, latestPkgs, effectiveRSSURL)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to render index: %w", models.ErrTemplate, err)
	}
	idxHash, idxHasChanged := stateStore.CheckChanged("index.html", idxContent)
	if g.config.Force || idxHasChanged {
		g.say("Writing index.html\n")
		if err := renderer.WriteToFile("index.html", idxContent); err != nil {
			return nil, fmt.Errorf("failed to write index.html: %w", err)
		}
		stateStore.MarkWritten("index.html", idxHash)
	}
	generatedFiles = append(generatedFiles, "index.html")

	rssContent, err := renderer.RenderRSS(latestPkgs, effectiveRSSURL)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to render RSS: %w", models.ErrTemplate, err)
	}
	rssHash, rssHasChanged := stateStore.CheckChanged("latest-feed.xml", rssContent)
	if g.config.Force || rssHasChanged {
		g.say("Writing latest-feed.xml\n")
		if err := renderer.WriteToFile("latest-feed.xml", rssContent); err != nil {
			return nil, fmt.Errorf("failed to write latest-feed.xml: %w", err)
		}
		stateStore.MarkWritten("latest-feed.xml", rssHash)
	}
	generatedFiles = append(generatedFiles, "latest-feed.xml")

	return generatedFiles, nil
}

// cleanupStale removes files that were not generated in this run.
func (g *Generator) cleanupStale(renderer *render.Renderer, stateStore *state.StateStore, generatedFiles []string) {
	g.say("Cleaning up stale files...\n")
	staleFiles := stateStore.GetStaleFiles(generatedFiles)
	for _, f := range staleFiles {
		if strings.HasPrefix(f, "__") {
			continue
		}
		g.say("Removing stale %s\n", f)
		if renderer != nil && renderer.Sink != nil {
			_ = renderer.Sink.RemoveFile(f)
		} else {
			_ = os.Remove(filepath.Join(g.config.OutputDir, f))
		}
		stateStore.Remove(f)
	}
}

// Helpers

// mapPrimaryGroups creates a fast lookup map pointing from a package name to its primary GroupData.
func mapPrimaryGroups(allGroups []*logic.GroupData) map[string]*logic.GroupData {
	pkgPrimaryGroup := make(map[string]*logic.GroupData)
	for _, grp := range allGroups {
		for _, pkg := range grp.Packages {
			if _, exists := pkgPrimaryGroup[pkg.Name]; !exists {
				pkgPrimaryGroup[pkg.Name] = grp
			}
		}
	}
	return pkgPrimaryGroup
}

// detectSiblingRepos searches nearby directories for sibling repository channels (e.g. base, extras)
// or architectures (e.g. x86_64, aarch64) that contain repodata or Debian binary directories, assuming in-place layout.
func detectSiblingRepos(repoDir string) []*logic.SiblingRepo {
	return detectSiblingReposForOutput(repoDir, "", "auto", nil, false)
}

func isHiddenSibling(candidate string, hiddenDirs map[string]bool) bool {
	if len(hiddenDirs) == 0 {
		return false
	}
	absCand, err := filepath.Abs(candidate)
	if err != nil {
		absCand = candidate
	}
	return hiddenDirs[filepath.Clean(absCand)]
}

func detectSiblingReposForOutput(repoDir, outputDir, portalURL string, hiddenDirs map[string]bool, skipDebComponents bool) []*logic.SiblingRepo {
	inPlace := true
	if outputDir != "" {
		absRepo, errR := filepath.Abs(repoDir)
		absOut, errO := filepath.Abs(outputDir)
		if errR == nil && errO == nil {
			inPlace = util.SamePath(absOut, filepath.Join(absRepo, "repoview"))
		}
	}
	if !inPlace {
		lowerPortal := strings.ToLower(strings.TrimSpace(portalURL))
		if lowerPortal == "" || lowerPortal == "auto" || lowerPortal == "none" || lowerPortal == "off" || lowerPortal == "false" {
			// Standalone detached output directory has no portal tree hosting sibling links.
			return nil
		}
	}

	if debSiblings := detectDebianSiblingsWithOpts(repoDir, inPlace, hiddenDirs, skipDebComponents); len(debSiblings) > 0 {
		return debSiblings
	}

	var siblings []*logic.SiblingRepo

	absRepoDir, err := filepath.Abs(repoDir)
	if err != nil {
		absRepoDir = repoDir
	}

	currentArch := filepath.Base(absRepoDir)
	parentDir := filepath.Dir(absRepoDir)
	currentChannel := filepath.Base(parentDir)
	grandParentDir := filepath.Dir(parentDir)

	// 1. Check sibling architectures in parentDir (e.g. x86_64, aarch64)
	if entries, err := os.ReadDir(parentDir); err == nil {
		var archSiblings []*logic.SiblingRepo
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			candDir := filepath.Join(parentDir, e.Name())
			if isHiddenSibling(candDir, hiddenDirs) {
				continue
			}
			repodataPath := filepath.Join(candDir, "repodata")
			if fi, err := os.Stat(repodataPath); err == nil && fi.IsDir() {
				relURL := fmt.Sprintf("../%s/index.html", e.Name())
				if inPlace {
					relURL = fmt.Sprintf("../../%s/repoview/index.html", e.Name())
				}
				archSiblings = append(archSiblings, &logic.SiblingRepo{
					Name:     e.Name(),
					RelURL:   relURL,
					IsActive: e.Name() == currentArch,
				})
			}
		}
		if len(archSiblings) > 1 {
			siblings = append(siblings, archSiblings...)
		}
	}

	// 2. Check sibling channels in grandParentDir (e.g. base, extras)
	if entries, err := os.ReadDir(grandParentDir); err == nil {
		var channelSiblings []*logic.SiblingRepo
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			candDir := filepath.Join(grandParentDir, e.Name(), currentArch)
			if isHiddenSibling(candDir, hiddenDirs) {
				continue
			}
			repodataPath := filepath.Join(candDir, "repodata")
			if fi, err := os.Stat(repodataPath); err == nil && fi.IsDir() {
				relURL := fmt.Sprintf("../../%s/%s/index.html", e.Name(), currentArch)
				if inPlace {
					relURL = fmt.Sprintf("../../../%s/%s/repoview/index.html", e.Name(), currentArch)
				}
				channelSiblings = append(channelSiblings, &logic.SiblingRepo{
					Name:     e.Name(),
					RelURL:   relURL,
					IsActive: e.Name() == currentChannel,
				})
			}
		}
		if len(channelSiblings) > 1 {
			siblings = append(siblings, channelSiblings...)
		}
	}

	return siblings
}

// detectDebianSiblings inspects adjacent directories in a dists hierarchy using in-place layout.
func detectDebianSiblings(repoDir string) []*logic.SiblingRepo {
	return detectDebianSiblingsWithOpts(repoDir, true, nil, false)
}

// detectDebianSiblingsWithOpts inspects adjacent directories in a dists hierarchy.
func detectDebianSiblingsWithOpts(repoDir string, inPlace bool, hiddenDirs map[string]bool, skipComponents bool) []*logic.SiblingRepo {
	var siblings []*logic.SiblingRepo
	absDir, _ := filepath.Abs(repoDir)

	if strings.HasPrefix(filepath.Base(absDir), "binary-") {
		currentArch := filepath.Base(absDir)
		componentDir := filepath.Dir(absDir)
		suiteDir := filepath.Dir(componentDir)

		// 1. Architecture siblings within the same component
		var archSiblings []*logic.SiblingRepo
		if entries, err := os.ReadDir(componentDir); err == nil {
			for _, e := range entries {
				if e.IsDir() && strings.HasPrefix(e.Name(), "binary-") {
					candDir := filepath.Join(componentDir, e.Name())
					if isHiddenSibling(candDir, hiddenDirs) {
						continue
					}
					relURL := fmt.Sprintf("../%s/index.html", e.Name())
					if inPlace {
						relURL = fmt.Sprintf("../../%s/repoview/index.html", e.Name())
					}
					archSiblings = append(archSiblings, &logic.SiblingRepo{
						Name:     strings.TrimPrefix(e.Name(), "binary-"),
						RelURL:   relURL,
						IsActive: e.Name() == currentArch,
					})
				}
			}
			if len(archSiblings) > 1 {
				siblings = append(siblings, archSiblings...)
			}
		}

		// 2. Component siblings across the same suite (when not aggregated under portal)
		if !skipComponents && len(siblings) <= 1 {
			if entries, err := os.ReadDir(suiteDir); err == nil {
				var compSiblings []*logic.SiblingRepo
				currentComponent := filepath.Base(componentDir)
				for _, e := range entries {
					siblingLeaf := filepath.Join(suiteDir, e.Name(), currentArch)
					if isHiddenSibling(siblingLeaf, hiddenDirs) {
						continue
					}
					if fi, err := os.Stat(siblingLeaf); err == nil && fi.IsDir() {
						relURL := fmt.Sprintf("../../%s/%s/index.html", e.Name(), currentArch)
						if inPlace {
							relURL = fmt.Sprintf("../../../%s/%s/repoview/index.html", e.Name(), currentArch)
						}
						compSiblings = append(compSiblings, &logic.SiblingRepo{
							Name:     e.Name(),
							RelURL:   relURL,
							IsActive: e.Name() == currentComponent,
						})
					}
				}
				if len(compSiblings) > 1 {
					siblings = append(siblings, compSiblings...)
				}
			}
		}
	}
	if len(siblings) <= 1 {
		return nil
	}
	return siblings
}

// validateOutputDirSafety ensures that outputDir is safe to write/delete without risking data loss.
// It verifies that outputDir:
// 1. Is not identical to the repository directory
// 2. Is not a parent of the repository directory
// 3. Is not the filesystem root
// 4. Does not contain a repodata or dists directory
func validateOutputDirSafety(repoDir, outputDir string) error {
	realRepo, err := util.ResolveRealPath(repoDir)
	if err != nil {
		return fmt.Errorf("failed to resolve repoDir %s: %w", repoDir, err)
	}
	realOut, err := util.ResolveRealPath(outputDir)
	if err != nil {
		return fmt.Errorf("failed to resolve outputDir %s: %w", outputDir, err)
	}

	// 1. Output directory cannot be the repository directory (including via symlinks)
	if util.SamePath(realRepo, realOut) {
		return &SafetyError{Msg: fmt.Sprintf("safety violation: output-dir cannot be identical to repository directory (%s)", realOut)}
	}

	// 2. Output directory cannot be a parent of repoDir
	if util.IsPathContained(realOut, realRepo) {
		return &SafetyError{Msg: fmt.Sprintf("safety violation: output-dir (%s) contains repository directory (%s)", realOut, realRepo)}
	}

	// 3. Output directory cannot be a root directory
	cleanOut := filepath.Clean(realOut)
	if cleanOut == "/" || cleanOut == filepath.VolumeName(cleanOut)+"\\" || cleanOut == filepath.VolumeName(cleanOut)+"/" {
		return &SafetyError{Msg: fmt.Sprintf("safety violation: output-dir cannot be the filesystem root (%s)", realOut)}
	}

	// 4. Output directory cannot contain repodata or dists
	repodataCheck := filepath.Join(realOut, "repodata")
	if fi, err := os.Stat(repodataCheck); err == nil && fi.IsDir() {
		return &SafetyError{Msg: fmt.Sprintf("safety violation: output-dir (%s) contains a repodata directory; refusing to delete or overwrite repository contents", realOut)}
	}
	distsCheck := filepath.Join(realOut, "dists")
	if fi, err := os.Stat(distsCheck); err == nil && fi.IsDir() {
		return &SafetyError{Msg: fmt.Sprintf("safety violation: output-dir (%s) contains a dists directory; refusing to delete or overwrite repository contents", realOut)}
	}

	// 5. Output directory cannot be or sit inside protected repository metadata subdirectories (repodata/ or dists/)
	for _, metaSub := range []string{"repodata", "dists"} {
		metaDir := filepath.Join(realRepo, metaSub)
		if util.SamePath(metaDir, realOut) || util.IsPathContained(metaDir, realOut) {
			return &SafetyError{Msg: fmt.Sprintf("safety violation: output-dir (%s) cannot reside inside metadata directory (%s)", realOut, metaDir)}
		}
	}

	return nil
}

// renderDescriptor generates the repoview.json metadata descriptor.
func (g *Generator) renderDescriptor(renderer *render.Renderer, stateStore *state.StateStore, portalURL string, meta *repo.RepoMetadata, allPkgs []*models.Package) ([]string, error) {
	descBytes, err := g.buildDescriptor(portalURL, meta, allPkgs)
	if err != nil {
		return nil, err
	}
	hash, hasChanged := stateStore.CheckChanged("repoview.json", descBytes)
	if g.config.Force || hasChanged {
		g.say("Writing repoview.json\n")
		if err := renderer.WriteToFile("repoview.json", descBytes); err != nil {
			return nil, fmt.Errorf("failed to write repoview.json: %w", err)
		}
		stateStore.MarkWritten("repoview.json", hash)
	}
	return []string{"repoview.json"}, nil
}

// buildDescriptor constructs the models.RepoDescriptor struct and marshals it to JSON.
func (g *Generator) buildDescriptor(portalURL string, meta *repo.RepoMetadata, allPkgs []*models.Package) ([]byte, error) {
	primaryArch := taxonomy.DetectPrimaryArchFromPackages(allPkgs)
	if primaryArch == "all" || primaryArch == "unknown" || primaryArch == "" {
		if pathArch := taxonomy.DetectArchFromPath(g.config.RepoDir); pathArch != "" && pathArch != "unknown" {
			primaryArch = pathArch
		}
	}
	if g.config.Arch != "" && g.config.Arch != "unknown" {
		primaryArch = g.config.Arch
	}

	distro, channel := taxonomy.InferDistroAndChannel(g.config.RepoDir)
	if g.config.Distro != "" {
		distro = g.config.Distro
	}
	if g.config.Channel != "" {
		channel = g.config.Channel
	}

	title := g.config.Title
	if title == "" || title == "Repoview" {
		switch {
		case distro != "" && channel != "":
			title = fmt.Sprintf("%s %s", distro, channel)
		case distro != "":
			title = distro
		default:
			title = "Repository"
		}
	}

	var (
		suite      string
		components []string
		revision   string
		descText   = g.config.Description
	)
	if g.config.Suite != "" {
		suite = g.config.Suite
	}
	if meta != nil {
		revision = meta.Revision
		if suite == "" {
			suite = meta.Suite
		}
		components = meta.Components
		if descText == "" {
			descText = meta.Description
		}
	}

	desc := &models.RepoDescriptor{
		Title:           title,
		Description:     descText,
		Format:          string(g.config.Format),
		Arch:            primaryArch,
		Distro:          distro,
		Channel:         channel,
		Suite:           suite,
		Components:      components,
		Revision:        revision,
		PackageCount:    len(allPkgs),
		LastBuild:       time.Now().UTC().Truncate(time.Second),
		BaseURL:         g.config.BaseURL,
		PortalURL:       portalURL,
		GPGKey:          g.config.GPGKey,
		Badge:           g.config.Badge,
		Icon:            g.config.Icon,
		RepoviewVersion: g.config.Version,
	}

	return json.MarshalIndent(desc, "", "  ")
}

// resolvePortalURL determines the parent portal URL either from explicit config or auto-discovery.
func (g *Generator) resolvePortalURL() string {
	raw := strings.TrimSpace(g.config.PortalURL)
	lower := strings.ToLower(raw)
	if lower == "none" || lower == "off" || lower == "false" {
		return ""
	}
	if raw != "" && lower != "auto" {
		return raw
	}
	return findParentPortal(g.config.OutputDir, g.config.RepoDir)
}

// findParentPortal climbs parent directories looking for portal.yaml or an index.html with RepoView-Portal signature.
func findParentPortal(outputDir, repoDir string) string {
	absOut, err := filepath.Abs(outputDir)
	if err != nil {
		absOut = outputDir
	}

	// 1. Try climbing upwards from outputDir
	if portalDir := climbForPortal(absOut); portalDir != "" {
		rel, err := filepath.Rel(absOut, portalDir)
		if err == nil {
			if rel == "." {
				return "index.html"
			}
			return filepath.ToSlash(filepath.Join(rel, "index.html"))
		}
	}

	// 2. Try climbing upwards from repoDir
	if repoDir != "" {
		absRepo, err := filepath.Abs(repoDir)
		if err == nil {
			if portalDir := climbForPortal(absRepo); portalDir != "" {
				rel, err := filepath.Rel(absOut, portalDir)
				if err == nil {
					if rel == "." {
						return "index.html"
					}
					return filepath.ToSlash(filepath.Join(rel, "index.html"))
				}
			}
		}
	}

	return ""
}

// climbForPortal checks up to 10 parent directories for portal markers.
func climbForPortal(startDir string) string {
	curr := startDir
	for i := 0; i < 10; i++ {
		parent := filepath.Dir(curr)
		if parent == curr || parent == "." || parent == "" {
			break
		}
		curr = parent
		if isPortalDir(curr) {
			return curr
		}
	}
	return ""
}

// isPortalDir checks if a directory contains portal.yaml, portal.yml, or a RepoView-Portal index.html.
func isPortalDir(dir string) bool {
	if fi, err := os.Stat(filepath.Join(dir, "portal.yaml")); err == nil && !fi.IsDir() {
		return true
	}
	if fi, err := os.Stat(filepath.Join(dir, "portal.yml")); err == nil && !fi.IsDir() {
		return true
	}
	indexPath := filepath.Join(dir, "index.html")
	if fi, err := os.Stat(indexPath); err == nil && !fi.IsDir() {
		// #nosec G304 -- inspecting parent directory index.html for RepoView-Portal signature
		f, err := os.Open(filepath.Clean(indexPath))
		if err == nil {
			buf := make([]byte, 2048)
			n, _ := f.Read(buf)
			_ = f.Close()
			if strings.Contains(string(buf[:n]), "RepoView-Portal") {
				return true
			}
		}
	}
	return false
}
