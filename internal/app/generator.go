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
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

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

	sem := make(chan struct{}, runtime.NumCPU()*2)
	var wg sync.WaitGroup

	for _, name := range uniqueNames {
		wg.Add(1)
		sem <- struct{}{}
		go func(pkgName string) {
			defer wg.Done()
			defer func() { <-sem }()

			versions := pkgVersions[pkgName]
			latest := versions[0]

			latest.AllVersions = versions

			// Populate package dependencies from reader (using cached prepared statements or index)
			if db != nil && latest.Dependencies == nil {
				deps, err := db.GetPackageDependencies(latest.PkgKey)
				if err == nil {
					latest.Dependencies = deps
				}
			}

			// On-demand file list loading: only load files when rendering this specific package page,
			// and immediately release file list from heap memory upon render completion to keep resident RAM minimal.
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

			primaryGroup, ok := pkgPrimaryGroup[pkgName]
			if !ok {
				firstLetter := util.FirstLetter(pkgName)
				primaryGroup = &logic.GroupData{
					Name:     "Letter " + firstLetter,
					Filename: fmt.Sprintf("letter_%s.group.html", strings.ToLower(firstLetter)),
				}
			}

			filename := latest.Filename()

			content, err := renderer.RenderPackage(latest, primaryGroup)
			if err != nil {
				log.Printf("Error rendering package %s: %v", pkgName, err)
				atomic.AddInt64(&errorCount, 1)
				return
			}

			hasChanged := stateStore.HasChanged(filename, content)
			if g.config.Force || hasChanged {
				if !g.config.Quiet {
					fmt.Printf("Writing package %s\n", filename)
				}
				if err := renderer.WriteToFile(filename, content); err != nil {
					log.Printf("Error writing %s: %v", filename, err)
					atomic.AddInt64(&errorCount, 1)
				}
			}

			mu.Lock()
			generatedFiles = append(generatedFiles, filename)
			mu.Unlock()
		}(name)
	}
	wg.Wait()

	if errorCount > 0 {
		g.say("Finished with %d errors during package generation.\n", errorCount)
	}

	return generatedFiles, errorCount, nil
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
