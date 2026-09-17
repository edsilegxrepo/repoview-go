// Package render handles HTML, XML, JSON, and web asset generation for repoview-go.
//
// OBJECTIVES:
// Transform structured repository domain models into lightweight, accessible,
// air-gap compliant static web pages, RSS 2.0 feeds, and offline search indices.
//
// CORE COMPONENTS:
//   - templateFS: Embedded filesystem bundle containing default HTML, XML, CSS, and JS assets.
//   - RepoContext: Global template state (navigation taxonomy, letters, versions, siblings).
//   - Renderer: Master rendering controller managing template execution and atomic disk persistence.
//
// FUNCTIONALITY:
//   - Template compilation: Compiles HTML templates using html/template (context-aware XSS escaping)
//     and XML templates using text/template (preserving XML declarations and unescaped entities).
//   - Template helper functions: Provides custom template utilities (ymd, humanSize, rssTime,
//     compressionSaved, paragraphs, etc.).
//   - Atomic persistence: Writes output pages via temporary files with fsync and atomic rename,
//     preventing partial or zero-byte file corruption during unexpected termination.
//   - Path traversal defense: Strictly validates that output file paths cannot escape the designated
//     output directory boundary.
//
// DATA FLOW:
//
//	models.Package + logic.GroupData -> Renderer.ExecuteTemplate() -> bytes.Buffer -> Renderer.WriteToFile()
package render

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	htmltmpl "html/template"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	// nosemgrep: go.lang.security.audit.xss.import-text-template.import-text-template -- text/template is strictly used for XML/RSS generation (rss.xml) where HTML contextual escaping would corrupt XML entities
	texttmpl "text/template"
	"time"

	"github.com/edsilegxrepo/repoview/internal/logic"
	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/util"
)

//go:embed templates/*.html templates/*.xml templates/layout/*.css templates/layout/*.js
var templateFS embed.FS

// RepoContext holds global data available to all templates.
// This includes the repository title, navigation letters, tool version, and functional groups.
type RepoContext struct {
	Title     string                 // Repository title displayed in page headers
	Letters   []string               // Alphabetical navigation index letters ("2", "3", "A"..."Z")
	MyVersion string                 // Repoview version identifier
	Groups    []*logic.GroupData     // Flat list of populated functional groups
	GroupTree []*logic.GroupCategory // Hierarchical category tree for sidebar navigation
	Siblings  []*logic.SiblingRepo   // Neighboring repository channels or architectures
	RepoID    string                 // Normalized repository identifier for .repo configuration
	BaseURL   string                 // Explicit or derived repository HTTP/HTTPS base URL
}

// Renderer handles the generation of HTML and XML files using templates.
// It manages the output directory, template context, and helper functions.
type Renderer struct {
	OutDir      string             // Filesystem destination directory for generated static files
	TemplateDir string             // Optional path to custom external templates (empty if using embedded)
	RepoCtx     RepoContext        // Global repository context shared across all template renders
	HTMLTmpls   *htmltmpl.Template // Compiled HTML templates with contextual auto-escaping
	XMLTmpls    *texttmpl.Template // Compiled XML templates for RSS feeds
}

// SetGroups sets the functional package groups in the global repo context.
func (r *Renderer) SetGroups(groups []*logic.GroupData) {
	r.RepoCtx.Groups = groups
	r.RepoCtx.GroupTree = logic.BuildGroupTree(groups)
}

// SetSiblings sets detected sibling repository channels or architectures.
func (r *Renderer) SetSiblings(siblings []*logic.SiblingRepo) {
	r.RepoCtx.Siblings = siblings
}

// SetRepoMeta sets the repository identifier and base URL.
func (r *Renderer) SetRepoMeta(repoID, baseURL string) {
	r.RepoCtx.RepoID = repoID
	r.RepoCtx.BaseURL = baseURL
}

// NewRenderer initializes a new Renderer instance.
// It parses the embedded templates or custom templates from a directory.
func NewRenderer(outDir, templateDir, title, version string, letters []string) (*Renderer, error) {
	funcMap := map[string]interface{}{
		"ymd":       util.YMD,
		"humanSize": util.HumanSize,
		"rssTime":   util.RSSTime,
		"lower":     strings.ToLower,
		"now":       func() int64 { return time.Now().Unix() },
		"compressionSaved": func(pkgSize, installedSize int64) string {
			if installedSize <= 0 || pkgSize <= 0 || installedSize < pkgSize {
				return ""
			}
			saved := (1.0 - float64(pkgSize)/float64(installedSize)) * 100.0
			return fmt.Sprintf("%.1f%%", saved)
		},
		"paragraphs": func(desc string) []string {
			blocks := strings.Split(strings.ReplaceAll(desc, "\r\n", "\n"), "\n\n")
			var res []string
			for _, b := range blocks {
				trimmed := strings.TrimSpace(b)
				if trimmed != "" {
					res = append(res, trimmed)
				}
			}
			return res
		},
	}

	r := &Renderer{
		OutDir:      outDir,
		TemplateDir: templateDir,
		RepoCtx: RepoContext{
			Title:     title,
			Letters:   letters,
			MyVersion: version,
		},
	}

	// 1. Setup HTML templates (context-aware escaping for web pages)
	var htmlTmpl *htmltmpl.Template
	var err error
	if templateDir != "" {
		htmlMatches, _ := filepath.Glob(filepath.Join(templateDir, "*.html"))
		if len(htmlMatches) > 0 {
			htmlTmpl, err = htmltmpl.New("repoview").Funcs(htmltmpl.FuncMap(funcMap)).ParseGlob(filepath.Join(templateDir, "*.html"))
			if err != nil {
				return nil, fmt.Errorf("failed to parse custom html templates: %w", err)
			}
		}
	}
	if htmlTmpl == nil {
		htmlTmpl, err = htmltmpl.New("repoview").Funcs(htmltmpl.FuncMap(funcMap)).ParseFS(templateFS, "templates/*.html")
		if err != nil {
			return nil, fmt.Errorf("failed to parse embedded html templates: %w", err)
		}
	}
	r.HTMLTmpls = htmlTmpl

	// 2. Setup XML templates (using text/template to preserve XML declarations and CDATA without HTML escaping)
	var xmlTmpl *texttmpl.Template
	if templateDir != "" {
		xmlMatches, _ := filepath.Glob(filepath.Join(templateDir, "*.xml"))
		if len(xmlMatches) > 0 {
			xmlTmpl, err = texttmpl.New("repoview-xml").Funcs(texttmpl.FuncMap(funcMap)).ParseGlob(filepath.Join(templateDir, "*.xml"))
			if err != nil {
				return nil, fmt.Errorf("failed to parse custom xml templates: %w", err)
			}
		}
	}
	if xmlTmpl == nil {
		xmlTmpl, err = texttmpl.New("repoview-xml").Funcs(texttmpl.FuncMap(funcMap)).ParseFS(templateFS, "templates/*.xml")
		if err != nil {
			return nil, fmt.Errorf("failed to parse embedded xml templates: %w", err)
		}
	}
	r.XMLTmpls = xmlTmpl

	return r, nil
}

// RenderIndex generates the main 'index.html' file.
// It displays the list of available groups and the most recently updated packages.
func (r *Renderer) RenderIndex(groups []*logic.GroupData, latest []*models.Package, url string) ([]byte, error) {
	data := struct {
		Repo   RepoContext
		Groups []*logic.GroupData
		Latest []*models.Package
		URL    string
	}{
		Repo:   r.RepoCtx,
		Groups: groups,
		Latest: latest,
		URL:    url,
	}

	var buf bytes.Buffer
	if err := r.HTMLTmpls.ExecuteTemplate(&buf, "index.html", data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderGroup generates a group page
func (r *Renderer) RenderGroup(group *logic.GroupData) ([]byte, error) {
	data := struct {
		Repo  RepoContext
		Group *logic.GroupData
	}{
		Repo:  r.RepoCtx,
		Group: group,
	}

	var buf bytes.Buffer
	if err := r.HTMLTmpls.ExecuteTemplate(&buf, "group.html", data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderPackage generates a package detail page.
// It displays comprehensive information about a package, including description,
// license, vendor, and a list of all available versions/architectures with changelogs.
func (r *Renderer) RenderPackage(pkg *models.Package, group *logic.GroupData) ([]byte, error) {
	data := struct {
		Repo  RepoContext
		Group *logic.GroupData
		Pkg   *models.Package
	}{
		Repo:  r.RepoCtx,
		Group: group,
		Pkg:   pkg,
	}

	var buf bytes.Buffer
	if err := r.HTMLTmpls.ExecuteTemplate(&buf, "package.html", data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderRSS generates the RSS feed
func (r *Renderer) RenderRSS(latest []*models.Package, url string) ([]byte, error) {
	data := struct {
		Repo   RepoContext
		Latest []*models.Package
		URL    string
	}{
		Repo:   r.RepoCtx,
		Latest: latest,
		URL:    url,
	}

	var buf bytes.Buffer
	if err := r.XMLTmpls.ExecuteTemplate(&buf, "rss.xml", data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderSearchIndex generates the search.json file content using a compact row-based format.
func (r *Renderer) RenderSearchIndex(pkgs []*models.Package) ([]byte, error) {
	index := models.SearchIndex{
		Schema: []string{"n", "v", "a", "s", "f"},
		Data:   make([][]string, 0, len(pkgs)),
	}

	for _, p := range pkgs {
		row := []string{
			p.Name,
			p.EVR(),
			p.Arch,
			p.Summary,
			p.Filename(),
		}
		index.Data = append(index.Data, row)
	}

	return json.Marshal(index)
}

// WriteToFile atomically writes content to a file in the output directory.
// It verifies that the target path does not escape the output directory (path traversal defense),
// writes to a temporary file, flushes/syncs, and atomically renames to the destination.
func (r *Renderer) WriteToFile(filename string, content []byte) error {
	fullPath := filepath.Join(r.OutDir, filename)

	// Security: Prevent path traversal
	rel, err := filepath.Rel(r.OutDir, fullPath)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("security violation: target path %s escapes output directory %s", filename, r.OutDir)
	}

	// Ensure parent directory exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", fullPath, err)
	}

	// Write atomically via temporary file in the same directory (ensuring same filesystem for atomic rename)
	tmpFile, err := os.CreateTemp(dir, ".repoview-tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file for %s: %w", fullPath, err)
	}
	tmpName := tmpFile.Name()

	if _, err := tmpFile.Write(content); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to write temp file for %s: %w", fullPath, err)
	}

	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to sync temp file for %s: %w", fullPath, err)
	}
	_ = tmpFile.Close()

	if err := os.Rename(tmpName, fullPath); err != nil {
		// Windows replacement fallback
		_ = os.Remove(fullPath)
		if err2 := os.Rename(tmpName, fullPath); err2 != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("failed to atomically rename %s to %s: %w", tmpName, fullPath, err2)
		}
	}

	return nil
}

// WriteAssets copies the layout files to the output directory.
// It supports both embedded assets and custom assets from the filesystem.
func (r *Renderer) WriteAssets() error {
	layoutDir := filepath.Join(r.OutDir, "layout")
	if err := os.MkdirAll(layoutDir, 0o750); err != nil {
		return err
	}

	if r.TemplateDir != "" {
		// Copy recursively from custom layout directory
		srcLayout := filepath.Join(r.TemplateDir, "layout")
		if _, err := os.Stat(srcLayout); os.IsNotExist(err) {
			return nil
		}

		return filepath.WalkDir(srcLayout, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relPath, err := filepath.Rel(srcLayout, path)
			if err != nil {
				return err
			}
			destPath := filepath.Join(layoutDir, relPath)
			if d.IsDir() {
				return os.MkdirAll(destPath, 0o750)
			}
			// #nosec G304, G122 -- path is discovered from walking validated user templateDir
			content, err := os.ReadFile(filepath.Clean(path))
			if err != nil {
				return err
			}
			// #nosec G306, G703 -- static web assets require 0644 permissions for web servers
			return os.WriteFile(destPath, content, 0o644)
		})
	}

	// Walk the embedded FS "templates/layout"
	return fs.WalkDir(templateFS, "templates/layout", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel("templates/layout", path)
		if err != nil {
			return err
		}
		destPath := filepath.Join(layoutDir, relPath)
		if d.IsDir() {
			return os.MkdirAll(destPath, 0o750)
		}

		// Read file content
		content, err := templateFS.ReadFile(path)
		if err != nil {
			return err
		}

		// Write to output
		// #nosec G306 -- static web assets require 0644 permissions for web servers
		return os.WriteFile(destPath, content, 0o644)
	})
}
