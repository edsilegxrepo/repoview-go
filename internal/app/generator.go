// Package app coordinates the core generation workflow for repoview-go.
//
// OBJECTIVES:
// Orchestrate the end-to-end transformation of an RPM repository into a modern,
// air-gap compliant static website. Manages repository metadata parsing, package filtering,
// deep RPM header introspection, taxonomy construction, incremental state persistence,
// multi-threaded HTML rendering, and automated stale file cleanup.
//
// CORE COMPONENTS:
//   - Config: Encapsulates all CLI flags, paths, filtering globs, and runtime settings.
//   - Generator: Primary workflow orchestrator managing repository lifecycle and rendering.
//   - ExitCode Constants: Structured UNIX process exit codes for diagnostic automation.
//   - SafetyError & PartialFailureError: Custom domain error types for safety violations and partial runs.
//   - validateOutputDirSafety: Guard function preventing accidental repository data destruction.
//   - detectSiblingRepos: Filesystem crawler discovering neighboring channels and architectures.
//
// FUNCTIONALITY:
//   - Safety Pre-validation: Halts immediately if the output directory threatens repository files.
//   - Repository Ingestion: Parses repomd.xml, decompresses databases, and establishes read-only SQLite connections.
//   - Bulk Changelog & RPM Enrichment: Uses batched queries and worker pools to inspect packages efficiently.
//   - On-Demand Memory Management: Loads package file manifests only during HTML page write and frees them immediately via defer.
//   - Parallel Rendering: Employs bounded goroutine worker pools (NumCPU * 2) for parallel package generation.
//   - Incremental Persistence: Skips unchanged pages using SHA-256 state tracking and prunes orphaned files.
//
// DATA FLOW:
//
//	app.Config -> repo.ParseRepomd() -> repo.RepositoryAccess -> logic.GroupingService ->
//	render.Renderer (parallel workers) -> state.StateStore -> Output directory
package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edsilegxrepo/repoview/internal/logic"
	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/render"
	"github.com/edsilegxrepo/repoview/internal/repo"
	"github.com/edsilegxrepo/repoview/internal/repo/deb"
	"github.com/edsilegxrepo/repoview/internal/state"
	"github.com/edsilegxrepo/repoview/internal/util"
)

// Diagnostic exit codes for automated tooling and scripting
const (
	ExitSuccess           = 0 // View generated successfully without error
	ExitUsageError        = 2 // Invalid flags, missing arguments, or safety violations
	ExitRepoMetadataError = 3 // Missing or corrupt repomd.xml, primary.sqlite, comps.xml
	ExitTemplateError     = 4 // Error loading or parsing HTML/XML templates
	ExitIOError           = 5 // Disk full, permission denied, filesystem error
	ExitPartialFailure    = 6 // Finished with package generation errors
)

// SafetyError indicates a safety violation such as outputDir pointing to repoDir
type SafetyError struct {
	Msg string
}

func (e *SafetyError) Error() string { return e.Msg }

// PartialFailureError indicates generation completed but some packages had errors
type PartialFailureError struct {
	ErrorCount int64
}

func (e *PartialFailureError) Error() string {
	return fmt.Sprintf("completed with %d package errors during generation", e.ErrorCount)
}

// Config holds the configuration for the Generator
type Config struct {
	RepoDir     string            // Root directory of the source RPM repository
	OutputDir   string            // Target directory for generated HTML and assets
	StateDir    string            // Directory for saving incremental state JSON files
	CompsFile   string            // Path to an alternative comps.xml file
	TemplateDir string            // Path to custom templates (embedded templates used if empty)
	Title       string            // Display title for the repository view
	URL         string            // Public base URL of the repository (for RSS feed)
	BaseURL     string            // Explicit base URL for client repository configuration (.repo)
	Format      models.RepoFormat // Repository format: auto, rpm, or deb
	Force       bool              // If true, forces complete regeneration ignoring state cache
	Quiet       bool              // If true, suppresses standard informational output
	IgnoreList  []string          // Glob patterns of package names/NVRAs to ignore
	ExcludeArch []string          // Hardware architectures to exclude from generation
	PortalURL   string            // Explicit or auto-detected URL to parent catalog portal ("auto", "none", or path/URL)
	Version     string            // Repoview tool version string
}

// Generator orchestrates the repository view generation process.
// It manages the flow of reading repository metadata, processing packages,
// and rendering the static HTML output.
type Generator struct {
	config Config
}

// NewGenerator creates a new Generator instance with the provided configuration.
func NewGenerator(cfg Config) *Generator {
	return &Generator{config: cfg}
}

// say prints a formatted message to standard output unless quiet mode is active.
func (g *Generator) say(format string, args ...interface{}) {
	if !g.config.Quiet {
		fmt.Printf(format, args...)
	}
}

// Run executes the full repository view generation pipeline.
func (g *Generator) Run() error {
	// 0. Safety validation: output directory must be safe to write and cannot threaten repository data
	if err := validateOutputDirSafety(g.config.RepoDir, g.config.OutputDir); err != nil {
		return err
	}

	// 1. Setup Repo Access
	reader, locs, cleanup, err := g.prepareRepository()
	if err != nil {
		return err
	}
	defer cleanup()

	// 2. Fetch & Filter Packages
	allPkgs, err := g.loadPackages(reader)
	if err != nil {
		return err
	}

	// Enrich all packages with changelogs in bulk (Performance Optimization)
	g.say("Enriching packages with changelogs...")
	if err := reader.EnrichPackagesWithChangelogs(allPkgs); err != nil {
		log.Printf("Warning: failed to enrich packages: %v", err)
	}
	g.say("done\n")

	// Enrich packages with package details (Scriptlets, Signatures, File Lists)
	g.say("Inspecting package metadata on disk...")
	reader.EnrichPackageDetails(g.config.RepoDir, allPkgs)
	g.say("done\n")

	// 3. Process Groups
	groups, letterGroups, letters, err := g.processGroups(locs, allPkgs)
	if err != nil {
		return err
	}
	allGroups := append(groups, letterGroups...)

	// 4. Setup Output & Renderer
	renderer, stateStore, err := g.setupRenderer(letters)
	if err != nil {
		return err
	}
	renderer.SetGroups(groups)
	renderer.SetFormat(g.config.Format)

	// Sibling repository detection (architectures and channels)
	siblings := detectSiblingRepos(g.config.RepoDir)
	renderer.SetSiblings(siblings)

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

	// Helper data structures
	pkgVersions := organizePackages(allPkgs)
	pkgPrimaryGroup := mapPrimaryGroups(allGroups)

	var generatedFiles []string

	// 5. Render Groups (renders all groups including letter groups)
	g.say("Generating pages...\n")
	groupFiles, err := g.renderGroups(renderer, stateStore, allGroups)
	if err != nil {
		return err
	}
	generatedFiles = append(generatedFiles, groupFiles...)

	// 6. Render Packages
	pkgFiles, errorCount, err := g.renderPackages(reader, renderer, stateStore, pkgVersions, pkgPrimaryGroup)
	if err != nil {
		return err
	}
	generatedFiles = append(generatedFiles, pkgFiles...)

	// 7. Render Indices (pass only real groups to index page, letter groups are in letter bar)
	indexFiles, err := g.renderIndices(renderer, stateStore, groups, pkgVersions)
	if err != nil {
		return err
	}
	generatedFiles = append(generatedFiles, indexFiles...)

	// 7b. Render repoview.json descriptor
	descFiles, err := g.renderDescriptor(renderer, stateStore, portalURL, allPkgs)
	if err != nil {
		log.Printf("Failed to render repoview.json: %v", err)
	} else {
		generatedFiles = append(generatedFiles, descFiles...)
	}

	// 8. Cleanup (only if no package generation errors to prevent deleting valid pages)
	if errorCount > 0 {
		g.say("Warning: %d packages encountered errors during generation, skipping stale cleanup\n", errorCount)
	} else {
		g.cleanupStale(stateStore, generatedFiles)
	}

	if err := stateStore.Save(); err != nil {
		log.Printf("Error saving state: %v", err)
	}

	g.say("Complete.\n")
	if errorCount > 0 {
		return &PartialFailureError{ErrorCount: errorCount}
	}
	return nil
}

// prepareRepository parses repomd.xml or Debian metadata, decompresses databases, and opens reader.
func (g *Generator) prepareRepository() (repo.RepoReader, *repo.RepoLocations, func(), error) {
	g.say("Examining repository...")
	format := g.config.Format
	if format == "" || format == "auto" {
		format = detectFormat(g.config.RepoDir)
		g.config.Format = format
	}

	if format == models.FormatDEB {
		g.say("Detected Debian repository format\n")
		debLocs, err := deb.Discover(g.config.RepoDir)
		if err != nil {
			return nil, nil, func() {}, fmt.Errorf("failed to discover Debian repository: %w", err)
		}
		reader := deb.NewDebRepository(debLocs, nil)
		return reader, nil, func() { _ = reader.Close() }, nil
	}

	g.say("Detected RPM repository format\n")
	locs, err := repo.ParseRepomd(g.config.RepoDir)
	if err != nil {
		return nil, nil, func() {}, fmt.Errorf("failed to parse repomd.xml: %w", err)
	}

	// Decompress DBs
	primaryPath, cleanupP, err := repo.DecompressFile(locs.Primary)
	if err != nil {
		return nil, nil, func() {}, fmt.Errorf("failed to decompress primary db: %w", err)
	}

	otherPath, cleanupO, err := repo.DecompressFile(locs.Other)
	if err != nil {
		cleanupP()
		return nil, nil, func() {}, fmt.Errorf("failed to decompress other db: %w", err)
	}
	g.say("done\n")

	g.say("Opening databases...")
	db, err := repo.NewRepositoryAccess(primaryPath, otherPath)
	if err != nil {
		cleanupP()
		cleanupO()
		return nil, nil, func() {}, fmt.Errorf("failed to open databases: %w", err)
	}
	g.say("done\n")

	cleanup := func() {
		_ = db.Close()
		cleanupO()
		cleanupP()
	}

	return db, locs, cleanup, nil
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
func (g *Generator) processGroups(locs *repo.RepoLocations, allPkgs []*models.Package) ([]*logic.GroupData, []*logic.GroupData, []string, error) {
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
				log.Printf("Warning: failed to parse comps.xml: %v", err)
			}
		} else {
			log.Printf("Warning: failed to decompress groups: %v", err)
		}
		g.say("done\n")
	}

	g.say(" organizing groups...")
	groupService := logic.NewGroupingService(allPkgs, comps)
	groups, err := groupService.GetGroups()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to organize groups: %w", err)
	}

	letterGroups := groupService.GetLetterGroups()
	var letters []string
	for _, lg := range letterGroups {
		parts := strings.Split(lg.Name, " ")
		if len(parts) > 1 {
			letters = append(letters, parts[1])
		}
	}

	g.say("done (%d groups, %d letters)\n", len(groups), len(letters))

	return groups, letterGroups, letters, nil
}

// setupRenderer creates output directories and initializes the renderer.
func (g *Generator) setupRenderer(letters []string) (*render.Renderer, *state.StateStore, error) {
	if err := validateOutputDirSafety(g.config.RepoDir, g.config.OutputDir); err != nil {
		return nil, nil, err
	}

	if g.config.Force && g.config.OutputDir != "" {
		if _, err := os.Stat(g.config.OutputDir); err == nil {
			_ = os.RemoveAll(g.config.OutputDir)
		}
	}

	if err := util.EnsureDir(g.config.OutputDir); err != nil {
		return nil, nil, fmt.Errorf("failed to create output dir: %w", err)
	}

	stateDir := g.config.OutputDir
	stateFilename := "state.json"

	if g.config.StateDir != "" {
		stateDir = g.config.StateDir
		if err := util.EnsureDir(stateDir); err != nil {
			log.Printf("Warning: failed to create state dir: %v", err)
		}
		hash := sha256.Sum256([]byte(g.config.OutputDir))
		stateFilename = fmt.Sprintf("%x.state.json", hash)
	}

	statePath := filepath.Join(stateDir, stateFilename)
	if g.config.Force {
		_ = os.Remove(statePath)
	}

	stateStore, err := state.NewStateStore(statePath)
	if err != nil {
		log.Printf("Warning: failed to load state: %v", err)
	}

	renderer, err := render.NewRenderer(g.config.OutputDir, g.config.TemplateDir, g.config.Title, g.config.Version, letters)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize renderer: %w", err)
	}

	if err := renderer.WriteAssets(); err != nil {
		return nil, nil, fmt.Errorf("failed to write assets: %w", err)
	}

	return renderer, stateStore, nil
}

// renderGroups renders all group pages.
func (g *Generator) renderGroups(renderer *render.Renderer, stateStore *state.StateStore, allGroups []*logic.GroupData) ([]string, error) {
	var generatedFiles []string

	for _, grp := range allGroups {
		content, err := renderer.RenderGroup(grp)
		if err != nil {
			return nil, fmt.Errorf("failed to render group %s: %w", grp.Name, err)
		}

		hasChanged := stateStore.HasChanged(grp.Filename, content)
		if g.config.Force || hasChanged {
			g.say("Writing group %s\n", grp.Filename)
			if err := renderer.WriteToFile(grp.Filename, content); err != nil {
				log.Printf("Error writing %s: %v", grp.Filename, err)
			}
		}
		generatedFiles = append(generatedFiles, grp.Filename)
	}
	return generatedFiles, nil
}

// renderPackages renders all package pages in parallel using a bounded worker pool.
func (g *Generator) renderPackages(db repo.RepoReader, renderer *render.Renderer, stateStore *state.StateStore, pkgVersions map[string][]*models.Package, pkgPrimaryGroup map[string]*logic.GroupData) ([]string, int64, error) {
	var generatedFiles []string
	var mu sync.Mutex
	var errorCount int64

	// Unique names
	var uniqueNames []string
	for name := range pkgVersions {
		uniqueNames = append(uniqueNames, name)
	}

	numWorkers := runtime.NumCPU() * 2
	if numWorkers < 1 {
		numWorkers = 1
	}
	if numWorkers > len(uniqueNames) {
		numWorkers = len(uniqueNames)
	}

	jobs := make(chan string, len(uniqueNames))
	for _, name := range uniqueNames {
		jobs <- name
	}
	close(jobs)

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var localGenerated []string
			for pkgName := range jobs {
				versions := pkgVersions[pkgName]
				primaryGroup := pkgPrimaryGroup[pkgName]

				filename, err := g.renderSinglePackage(db, renderer, stateStore, versions, primaryGroup, pkgName)
				if err != nil {
					log.Printf("Error rendering package %s: %v", pkgName, err)
					atomic.AddInt64(&errorCount, 1)
					continue
				}

				localGenerated = append(localGenerated, filename)
			}

			if len(localGenerated) > 0 {
				mu.Lock()
				generatedFiles = append(generatedFiles, localGenerated...)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if errorCount > 0 {
		g.say("Finished with %d errors during package generation.\n", errorCount)
	}

	return generatedFiles, errorCount, nil
}

// renderSinglePackage renders an individual package page and ensures heap cleanup of file manifests.
func (g *Generator) renderSinglePackage(db repo.RepoReader, renderer *render.Renderer, stateStore *state.StateStore, versions []*models.Package, primaryGroup *logic.GroupData, pkgName string) (string, error) {
	if len(versions) == 0 {
		return "", fmt.Errorf("no versions available for package %s", pkgName)
	}

	latest := versions[0]
	latest.AllVersions = versions

	// Populate package dependencies from reader
	if db != nil && latest.Dependencies == nil {
		deps, err := db.GetPackageDependencies(latest.PkgKey)
		if err == nil {
			latest.Dependencies = deps
		}
	}

	// On-demand file list loading: clean up heap memory upon render completion
	if latest.LocationHref != "" && db != nil {
		if latest.Details == nil || len(latest.Details.Files) == 0 {
			files, err := db.ReadPackageFiles(g.config.RepoDir, latest)
			if err == nil && len(files) > 0 {
				if latest.Details == nil {
					latest.Details = &models.PackageDetails{}
				}
				latest.Details.Files = files
				defer func() {
					if latest.Details != nil {
						latest.Details.Files = nil
					}
				}()
			}
		}
	}

	if primaryGroup == nil {
		firstLetter := util.FirstLetter(pkgName)
		primaryGroup = &logic.GroupData{
			Name:     "Letter " + firstLetter,
			Filename: fmt.Sprintf("letter_%s.group.html", strings.ToLower(firstLetter)),
		}
	}

	filename := latest.Filename()

	content, err := renderer.RenderPackage(latest, primaryGroup)
	if err != nil {
		return "", err
	}

	hasChanged := stateStore.HasChanged(filename, content)
	if g.config.Force || hasChanged {
		if !g.config.Quiet {
			fmt.Printf("Writing package %s\n", filename)
		}
		if err := renderer.WriteToFile(filename, content); err != nil {
			return "", err
		}
	}

	return filename, nil
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
		log.Printf("Failed to render search index: %v", err)
	} else {
		hasChanged := stateStore.HasChanged("search.json", searchContent)
		if g.config.Force || hasChanged {
			g.say("Writing search.json\n")
			if err := renderer.WriteToFile("search.json", searchContent); err != nil {
				log.Printf("Failed to write search index: %v", err)
			}
		}
		generatedFiles = append(generatedFiles, "search.json")
	}

	limit := 30
	if len(latestPkgs) > limit {
		latestPkgs = latestPkgs[:limit]
	}

	effectiveRSSURL := g.config.URL
	if effectiveRSSURL == "" && g.config.BaseURL != "" {
		effectiveRSSURL = g.config.BaseURL
	}

	idxContent, err := renderer.RenderIndex(availableGroups, latestPkgs, effectiveRSSURL)
	if err != nil {
		return nil, fmt.Errorf("failed to render index: %w", err)
	}
	idxHasChanged := stateStore.HasChanged("index.html", idxContent)
	if g.config.Force || idxHasChanged {
		g.say("Writing index.html\n")
		if err := renderer.WriteToFile("index.html", idxContent); err != nil {
			return nil, fmt.Errorf("failed to write index.html: %w", err)
		}
	}
	generatedFiles = append(generatedFiles, "index.html")

	rssContent, err := renderer.RenderRSS(latestPkgs, effectiveRSSURL)
	if err != nil {
		log.Printf("Failed to render RSS: %v", err)
	} else {
		rssHasChanged := stateStore.HasChanged("latest-feed.xml", rssContent)
		if g.config.Force || rssHasChanged {
			g.say("Writing latest-feed.xml\n")
			if err := renderer.WriteToFile("latest-feed.xml", rssContent); err != nil {
				log.Printf("Failed to write RSS: %v", err)
			}
		}
		generatedFiles = append(generatedFiles, "latest-feed.xml")
	}

	return generatedFiles, nil
}

// cleanupStale removes files that were not generated in this run.
func (g *Generator) cleanupStale(stateStore *state.StateStore, generatedFiles []string) {
	g.say("Cleaning up stale files...\n")
	staleFiles := stateStore.GetStaleFiles(generatedFiles)
	for _, f := range staleFiles {
		g.say("Removing stale %s\n", f)
		_ = os.Remove(filepath.Join(g.config.OutputDir, f))
		stateStore.Remove(f)
	}
}

// Helpers

// organizePackages groups package records by their base name and sorts each version list by EVR descending.
func organizePackages(allPkgs []*models.Package) map[string][]*models.Package {
	pkgVersions := make(map[string][]*models.Package)
	for _, p := range allPkgs {
		pkgVersions[p.Name] = append(pkgVersions[p.Name], p)
	}
	for _, list := range pkgVersions {
		logic.SortPackagesByEVR(list)
	}
	return pkgVersions
}

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
// or architectures (e.g. x86_64, aarch64) that contain repodata or Debian binary directories.
func detectSiblingRepos(repoDir string) []*logic.SiblingRepo {
	if debSiblings := detectDebianSiblings(repoDir); len(debSiblings) > 0 {
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
			repodataPath := filepath.Join(parentDir, e.Name(), "repodata")
			if fi, err := os.Stat(repodataPath); err == nil && fi.IsDir() {
				archSiblings = append(archSiblings, &logic.SiblingRepo{
					Name:     e.Name(),
					RelURL:   fmt.Sprintf("../../%s/repoview/index.html", e.Name()),
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
			repodataPath := filepath.Join(grandParentDir, e.Name(), currentArch, "repodata")
			if fi, err := os.Stat(repodataPath); err == nil && fi.IsDir() {
				channelSiblings = append(channelSiblings, &logic.SiblingRepo{
					Name:     e.Name(),
					RelURL:   fmt.Sprintf("../../../%s/%s/repoview/index.html", e.Name(), currentArch),
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

// detectDebianSiblings inspects adjacent directories in a dists hierarchy.
func detectDebianSiblings(repoDir string) []*logic.SiblingRepo {
	var siblings []*logic.SiblingRepo
	absDir, _ := filepath.Abs(repoDir)

	// Case 1: Currently inside binary-<arch> leaf (e.g. dists/noble/main/binary-amd64)
	if strings.HasPrefix(filepath.Base(absDir), "binary-") {
		currentArch := filepath.Base(absDir)
		componentDir := filepath.Dir(absDir)
		suiteDir := filepath.Dir(componentDir)

		// 1. Architecture siblings within the same component
		var archSiblings []*logic.SiblingRepo
		if entries, err := os.ReadDir(componentDir); err == nil {
			for _, e := range entries {
				if e.IsDir() && strings.HasPrefix(e.Name(), "binary-") {
					archSiblings = append(archSiblings, &logic.SiblingRepo{
						Name:     strings.TrimPrefix(e.Name(), "binary-"),
						RelURL:   fmt.Sprintf("../%s/repoview/index.html", e.Name()),
						IsActive: e.Name() == currentArch,
					})
				}
			}
			if len(archSiblings) > 1 {
				siblings = append(siblings, archSiblings...)
			}
		}

		// 2. Component siblings across the same suite (e.g. main vs universe)
		if entries, err := os.ReadDir(suiteDir); err == nil && len(siblings) <= 1 {
			var compSiblings []*logic.SiblingRepo
			currentComponent := filepath.Base(componentDir)
			for _, e := range entries {
				siblingLeaf := filepath.Join(suiteDir, e.Name(), currentArch)
				if fi, err := os.Stat(siblingLeaf); err == nil && fi.IsDir() {
					compSiblings = append(compSiblings, &logic.SiblingRepo{
						Name:     e.Name(),
						RelURL:   fmt.Sprintf("../../%s/%s/repoview/index.html", e.Name(), currentArch),
						IsActive: e.Name() == currentComponent,
					})
				}
			}
			if len(compSiblings) > 1 {
				siblings = append(siblings, compSiblings...)
			}
		}
	}
	return siblings
}

// detectFormat sniffs signatures in the root repository folder.
func detectFormat(repoDir string) models.RepoFormat {
	if _, err := os.Stat(filepath.Join(repoDir, "repodata", "repomd.xml")); err == nil {
		return models.FormatRPM
	}
	if _, err := os.Stat(filepath.Join(repoDir, "dists")); err == nil {
		return models.FormatDEB
	}
	if matches, _ := filepath.Glob(filepath.Join(repoDir, "Packages*")); len(matches) > 0 {
		return models.FormatDEB
	}
	base := filepath.Base(repoDir)
	if strings.HasPrefix(base, "binary-") {
		return models.FormatDEB
	}
	return models.FormatRPM // Default fallback
}

// validateOutputDirSafety ensures that outputDir is safe to write/delete without risking data loss.
// It verifies that outputDir:
// 1. Is not identical to the repository directory
// 2. Is not a parent of the repository directory
// 3. Is not the filesystem root
// 4. Does not contain a repodata or dists directory
func validateOutputDirSafety(repoDir, outputDir string) error {
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return fmt.Errorf("failed to resolve repoDir %s: %w", repoDir, err)
	}
	absOut, err := filepath.Abs(outputDir)
	if err != nil {
		return fmt.Errorf("failed to resolve outputDir %s: %w", outputDir, err)
	}

	// 1. Output directory cannot be the repository directory
	if absRepo == absOut {
		return &SafetyError{Msg: fmt.Sprintf("safety violation: output-dir cannot be identical to repository directory (%s)", absOut)}
	}

	// 2. Output directory cannot be a parent of repoDir
	rel, err := filepath.Rel(absOut, absRepo)
	if err == nil && !strings.HasPrefix(rel, "..") {
		return &SafetyError{Msg: fmt.Sprintf("safety violation: output-dir (%s) contains repository directory (%s)", absOut, absRepo)}
	}

	// 3. Output directory cannot be a root directory
	cleanOut := filepath.Clean(absOut)
	if cleanOut == "/" || cleanOut == filepath.VolumeName(cleanOut)+"\\" || cleanOut == filepath.VolumeName(cleanOut)+"/" {
		return &SafetyError{Msg: fmt.Sprintf("safety violation: output-dir cannot be the filesystem root (%s)", absOut)}
	}

	// 4. Output directory cannot contain repodata or dists
	repodataCheck := filepath.Join(absOut, "repodata")
	if fi, err := os.Stat(repodataCheck); err == nil && fi.IsDir() {
		return &SafetyError{Msg: fmt.Sprintf("safety violation: output-dir (%s) contains a repodata directory; refusing to delete or overwrite repository contents", absOut)}
	}
	distsCheck := filepath.Join(absOut, "dists")
	if fi, err := os.Stat(distsCheck); err == nil && fi.IsDir() {
		return &SafetyError{Msg: fmt.Sprintf("safety violation: output-dir (%s) contains a dists directory; refusing to delete or overwrite repository contents", absOut)}
	}

	return nil
}

// renderDescriptor generates the repoview.json metadata descriptor.
func (g *Generator) renderDescriptor(renderer *render.Renderer, stateStore *state.StateStore, portalURL string, allPkgs []*models.Package) ([]string, error) {
	descBytes, err := g.buildDescriptor(portalURL, allPkgs)
	if err != nil {
		return nil, err
	}
	hasChanged := stateStore.HasChanged("repoview.json", descBytes)
	if g.config.Force || hasChanged {
		g.say("Writing repoview.json\n")
		if err := renderer.WriteToFile("repoview.json", descBytes); err != nil {
			return nil, fmt.Errorf("failed to write repoview.json: %w", err)
		}
	}
	return []string{"repoview.json"}, nil
}

// buildDescriptor constructs the models.RepoDescriptor struct and marshals it to JSON.
func (g *Generator) buildDescriptor(portalURL string, allPkgs []*models.Package) ([]byte, error) {
	primaryArch := detectPrimaryArch(allPkgs)
	if primaryArch == "all" || primaryArch == "unknown" || primaryArch == "" {
		if pathArch := detectArchFromPath(g.config.RepoDir); pathArch != "" {
			primaryArch = pathArch
		}
	}
	distro, channel := inferDistroAndChannel(g.config.RepoDir)
	title := g.config.Title
	if title == "" || title == "Repoview" {
		if distro != "" && channel != "" {
			title = fmt.Sprintf("%s %s", distro, channel)
		} else if distro != "" {
			title = distro
		} else {
			title = "Repository"
		}
	}

	desc := &models.RepoDescriptor{
		Title:           title,
		Format:          string(g.config.Format),
		Arch:            primaryArch,
		Distro:          distro,
		Channel:         channel,
		PackageCount:    len(allPkgs),
		LastBuild:       time.Now().UTC().Truncate(time.Second),
		BaseURL:         g.config.BaseURL,
		PortalURL:       portalURL,
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

// detectPrimaryArch determines the primary binary architecture from package records.
func detectPrimaryArch(allPkgs []*models.Package) string {
	archCounts := make(map[string]int)
	for _, p := range allPkgs {
		arch := strings.ToLower(strings.TrimSpace(p.Arch))
		if arch != "" {
			archCounts[arch]++
		}
	}
	if len(archCounts) == 0 {
		return "all"
	}

	var bestArch string
	var maxCount int
	for arch, count := range archCounts {
		if arch == "noarch" || arch == "all" {
			continue
		}
		if count > maxCount {
			maxCount = count
			bestArch = arch
		}
	}
	if bestArch != "" {
		return bestArch
	}
	if archCounts["noarch"] > 0 {
		return "noarch"
	}
	if archCounts["all"] > 0 {
		return "all"
	}
	return "unknown"
}

var knownArchitectures = map[string]bool{
	"x86_64":  true,
	"amd64":   true,
	"aarch64": true,
	"arm64":   true,
	"armhf":   true,
	"armv7hl": true,
	"i686":    true,
	"i386":    true,
	"s390x":   true,
	"ppc64le": true,
	"riscv64": true,
	"noarch":  true,
	"all":     true,
}

func isArchToken(token string) bool {
	token = strings.TrimPrefix(token, "binary-")
	return knownArchitectures[token]
}

// detectArchFromPath extracts a recognized architecture keyword from directory paths.
func detectArchFromPath(repoDir string) string {
	abs, err := filepath.Abs(repoDir)
	if err != nil {
		abs = repoDir
	}
	parts := strings.Split(filepath.ToSlash(abs), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		part := strings.TrimSpace(parts[i])
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "binary-") {
			candidate := strings.TrimPrefix(part, "binary-")
			if knownArchitectures[candidate] {
				return candidate
			}
		}
		if knownArchitectures[part] {
			return part
		}
		if dotIdx := strings.LastIndex(part, "."); dotIdx != -1 {
			candidate := part[dotIdx+1:]
			if knownArchitectures[candidate] {
				return candidate
			}
		}
		if dashIdx := strings.LastIndex(part, "-"); dashIdx != -1 {
			candidate := part[dashIdx+1:]
			if knownArchitectures[candidate] {
				return candidate
			}
		}
	}
	return ""
}

// inferDistroAndChannel deduces distribution and channel identifiers from directory layout.
func inferDistroAndChannel(repoDir string) (string, string) {
	abs, err := filepath.Abs(repoDir)
	if err != nil {
		abs = repoDir
	}
	slashPath := filepath.ToSlash(abs)
	parts := strings.Split(slashPath, "/")
	var nonClean []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			nonClean = append(nonClean, p)
		}
	}
	parts = nonClean
	if len(parts) == 0 {
		return "generic", "base"
	}

	// 1. Debian layout check: .../dists/<suite>/<component>/...
	for i, part := range parts {
		if part == "dists" {
			distro := ""
			if i > 0 {
				distro = parts[i-1]
			}
			suite := ""
			if i+1 < len(parts) {
				suite = parts[i+1]
			}
			component := "main"
			if i+2 < len(parts) && !strings.HasPrefix(parts[i+2], "binary-") {
				component = parts[i+2]
			}
			if distro == "" {
				if suite != "" {
					distro = suite
				} else {
					distro = "debian"
				}
			}
			return distro, component
		}
	}

	// 2. Hierarchical RPM tree check: .../<distro>/<channel>/<arch>
	lastPart := parts[len(parts)-1]
	if isArchToken(lastPart) && len(parts) >= 3 {
		channel := parts[len(parts)-2]
		distro := parts[len(parts)-3]
		return distro, channel
	}

	// 3. Hierarchical two-level tree check: .../<distro>/<channel> (e.g. ubu24/custom, deb12/custom)
	if len(parts) >= 2 {
		parent := parts[len(parts)-2]
		if isLikelyDistro(parent) {
			return parent, parts[len(parts)-1]
		}
	}

	// 4. Slug check: e.g. el10-base.x86_64, el-9-x86_64, ubuntu-24.04-x86_64
	base := filepath.Base(abs)
	if dotIdx := strings.LastIndex(base, "."); dotIdx != -1 {
		suffix := base[dotIdx+1:]
		if knownArchitectures[suffix] {
			base = base[:dotIdx]
		}
	} else if dashIdx := strings.LastIndex(base, "-"); dashIdx != -1 {
		suffix := base[dashIdx+1:]
		if knownArchitectures[suffix] {
			base = base[:dashIdx]
		}
	}

	commonChannels := []string{"base", "baseos", "extras", "updates", "appstream", "powertools", "crb", "main", "universe", "multiverse", "restricted"}
	for _, ch := range commonChannels {
		if strings.HasSuffix(base, "-"+ch) {
			distro := strings.TrimSuffix(base, "-"+ch)
			return distro, ch
		}
		if strings.HasSuffix(base, "."+ch) {
			distro := strings.TrimSuffix(base, "."+ch)
			return distro, ch
		}
	}

	return base, "base"
}

// isLikelyDistro checks if a directory name looks like a Linux distribution identifier.
func isLikelyDistro(name string) bool {
	d := strings.ToLower(name)
	return strings.HasPrefix(d, "el") || strings.HasPrefix(d, "ubu") || strings.HasPrefix(d, "deb") ||
		strings.Contains(d, "rhel") || strings.Contains(d, "centos") || strings.Contains(d, "fedora") ||
		strings.Contains(d, "rocky") || strings.Contains(d, "alma") || strings.Contains(d, "suse") ||
		strings.Contains(d, "arch") || strings.Contains(d, "alpine")
}
