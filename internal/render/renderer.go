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
	"encoding/xml"
	"fmt"
	"html"
	htmltmpl "html/template"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	// nosemgrep: go.lang.security.audit.xss.import-text-template.import-text-template -- text/template is strictly used for XML/RSS generation (rss.xml) with explicit xmlEscape/cdataHTML/safeURL escaping
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
	Title          string                 // Repository title displayed in page headers
	Description    string                 // Optional repository description (Release / repoview.yaml)
	Revision       string                 // Optional repomd revision or Debian suite identifier
	TotalPackages  int                    // Total unique package count across the repository
	Letters        []string               // Alphabetical navigation index letters ("2", "3", "A"..."Z")
	MyVersion      string                 // Repoview version identifier
	Groups         []*logic.GroupData     // Flat list of populated functional groups
	GroupTree      []*logic.GroupCategory // Hierarchical category tree for sidebar navigation
	Siblings       []*logic.SiblingRepo   // Neighboring repository channels or architectures
	RepoID         string                 // Normalized repository identifier for .repo configuration
	BaseURL        string                 // Explicit or derived repository HTTP/HTTPS base URL
	Format         models.RepoFormat      // Underlying repository format ("rpm" or "deb")
	PortalURL      string                 // Optional or auto-detected relative/absolute link to parent catalog portal
	PackageBaseURL string                 // Optional base URL or relative path for raw package downloads
	GPGKey         string                 // Optional GPG public key URL/path for client configuration snippets
}

// PageData unifies the template execution context passed to index, group, and package pages
// and their shared layout partials (_head.html, _sidebar.html, _topbar.html).
type PageData struct {
	PageKind  string
	PageTitle string
	Robots    string
	Repo      RepoContext
	Groups    []*logic.GroupData
	Group     *logic.GroupData
	Latest    []*models.Package
	Pkg       *models.Package
	URL       string
}

// Renderer handles the generation of HTML and XML files using templates.
// It manages the output directory, OutputSink, template context, and helper functions.
type Renderer struct {
	OutDir      string             // Filesystem destination directory for generated static files
	TemplateDir string             // Optional path to custom external templates (empty if using embedded)
	Sink        OutputSink         // Output sink (defaults to AtomicLocalSink on OutDir)
	RepoCtx     RepoContext        // Global repository context shared across all template renders
	HTMLTmpls   *htmltmpl.Template // Compiled HTML templates with contextual auto-escaping
	XMLTmpls    *texttmpl.Template // Compiled XML templates for RSS feeds
}

// SetSink injects a custom OutputSink implementation.
func (r *Renderer) SetSink(s OutputSink) {
	if s != nil {
		r.Sink = s
	}
}

// SetGroups sets the functional package groups in the global repo context.
func (r *Renderer) SetGroups(groups []*logic.GroupData) {
	r.RepoCtx.Groups = groups
	r.RepoCtx.GroupTree = logic.BuildGroupTree(groups)
}

// SetTotalPackages stores the repository-wide unique package count for index summary chips.
func (r *Renderer) SetTotalPackages(total int) {
	r.RepoCtx.TotalPackages = total
}

// SetRevision stores the repository revision or suite identifier.
func (r *Renderer) SetRevision(rev string) {
	r.RepoCtx.Revision = strings.TrimSpace(rev)
}

// SetDescription stores an optional repository description.
func (r *Renderer) SetDescription(desc string) {
	r.RepoCtx.Description = strings.TrimSpace(desc)
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

// SetPackageBaseURL sets the base URL or relative path prefix for package file downloads.
func (r *Renderer) SetPackageBaseURL(pkgBaseURL string) {
	r.RepoCtx.PackageBaseURL = strings.TrimRight(pkgBaseURL, "/")
}

// SetFormat sets the repository package format in the global repo context.
func (r *Renderer) SetFormat(format models.RepoFormat) {
	r.RepoCtx.Format = format
}

// SetPortalURL sets the parent catalog portal URL.
func (r *Renderer) SetPortalURL(portalURL string) {
	r.RepoCtx.PortalURL = portalURL
}

// SetGPGKey sets the optional GPG public key URL or local path for client snippets.
func (r *Renderer) SetGPGKey(gpgKey string) {
	r.RepoCtx.GPGKey = strings.TrimSpace(gpgKey)
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
		"xmlEscape":  xmlEscape,
		"cdataHTML":  cdataHTML,
		"safeURL":    safeURL,
		"shellQuote": util.ShellQuote,
		"singleLine": util.SanitizeSingleLine,
		"joinCSV":    func(items []string) string { return strings.Join(items, ", ") },
	}

	r := &Renderer{
		OutDir:      outDir,
		TemplateDir: templateDir,
		Sink:        &AtomicLocalSink{RootDir: outDir},
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
				return nil, fmt.Errorf("%w: failed to parse custom html templates: %w", models.ErrTemplate, err)
			}
		}
	}
	if htmlTmpl == nil {
		htmlTmpl, err = htmltmpl.New("repoview").Funcs(htmltmpl.FuncMap(funcMap)).ParseFS(templateFS, "templates/*.html")
		if err != nil {
			return nil, fmt.Errorf("%w: failed to parse embedded html templates: %w", models.ErrTemplate, err)
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
				return nil, fmt.Errorf("%w: failed to parse custom xml templates: %w", models.ErrTemplate, err)
			}
		}
	}
	if xmlTmpl == nil {
		xmlTmpl, err = texttmpl.New("repoview-xml").Funcs(texttmpl.FuncMap(funcMap)).ParseFS(templateFS, "templates/*.xml")
		if err != nil {
			return nil, fmt.Errorf("%w: failed to parse embedded xml templates: %w", models.ErrTemplate, err)
		}
	}
	r.XMLTmpls = xmlTmpl

	return r, nil
}

// RenderIndex generates the main 'index.html' file.
// It displays the list of available groups and the most recently updated packages.
func (r *Renderer) RenderIndex(groups []*logic.GroupData, latest []*models.Package, url string) ([]byte, error) {
	data := PageData{
		PageKind:  "index",
		PageTitle: "RepoView: " + r.RepoCtx.Title,
		Robots:    "index,follow",
		Repo:      r.RepoCtx,
		Groups:    groups,
		Latest:    latest,
		URL:       url,
	}

	var buf bytes.Buffer
	if err := r.HTMLTmpls.ExecuteTemplate(&buf, "index.html", data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderGroup generates a group page.
func (r *Renderer) RenderGroup(group *logic.GroupData) ([]byte, error) {
	groupName := ""
	if group != nil {
		groupName = group.Name
	}
	data := PageData{
		PageKind:  "group",
		PageTitle: fmt.Sprintf("RepoView: %s - %s", groupName, r.RepoCtx.Title),
		Robots:    "noindex,follow",
		Repo:      r.RepoCtx,
		Group:     group,
	}

	var buf bytes.Buffer
	if err := r.HTMLTmpls.ExecuteTemplate(&buf, "group.html", data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderPackage generates a package detail page.
func (r *Renderer) RenderPackage(pkg *models.Package, group *logic.GroupData) ([]byte, error) {
	pkgName := ""
	if pkg != nil {
		pkgName = pkg.Name
	}
	data := PageData{
		PageKind:  "package",
		PageTitle: fmt.Sprintf("RepoView: %s - %s", pkgName, r.RepoCtx.Title),
		Robots:    "noindex,follow",
		Repo:      r.RepoCtx,
		Group:     group,
		Pkg:       pkg,
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

// RenderSearchIndex generates search.json using extended Structure-of-Arrays columns:
// ["n", "v", "a", "s", "f", "g", "l", "p", "t"] (name, evr, arch, summary, href, group, license, provides/source, tags/cves).
func (r *Renderer) RenderSearchIndex(pkgs []*models.Package) ([]byte, error) {
	index := models.SearchIndex{
		Schema: []string{"n", "v", "a", "s", "f", "g", "l", "p", "t"},
		Data:   make([][]string, 0, len(pkgs)),
	}

	for _, p := range pkgs {
		groupVal := p.Group
		if groupVal == "" {
			groupVal = p.Section
		}
		if groupVal == "" {
			groupVal = p.RpmGroup
		}
		row := []string{
			p.Name,
			p.EVR(),
			p.Arch,
			p.Summary,
			p.Filename(),
			groupVal,
			p.License,
			compactProvidesString(p),
			compactTagsAndCVEs(p),
		}
		index.Data = append(index.Data, row)
	}

	return json.Marshal(index)
}

func compactProvidesString(p *models.Package) string {
	var tokens []string
	seen := make(map[string]bool)
	addTok := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		tokens = append(tokens, s)
	}
	addTok(p.SourcePackage)
	if p.Dependencies != nil {
		for _, prov := range p.Dependencies.Provides {
			if prov == nil || prov.Name == p.Name {
				continue
			}
			addTok(prov.Name)
			if len(tokens) >= 24 {
				break
			}
		}
	}
	return strings.Join(tokens, " ")
}

func compactTagsAndCVEs(p *models.Package) string {
	var tokens []string
	seen := make(map[string]bool)
	addTok := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		tokens = append(tokens, s)
	}
	for _, t := range p.Tags {
		addTok(t)
	}
	for _, adv := range p.Advisories {
		addTok(adv.ID)
		for _, cve := range adv.CVEs {
			addTok(cve)
		}
	}
	return strings.Join(tokens, " ")
}

// WriteToFile delegates atomic contained writing to Renderer.Sink.
func (r *Renderer) WriteToFile(filename string, content []byte) error {
	if r.Sink == nil {
		r.Sink = &AtomicLocalSink{RootDir: r.OutDir}
	}
	return r.Sink.WriteFile(filename, content)
}

// AssetStateTracker enables skipping unchanged static asset writes across incremental runs.
type AssetStateTracker interface {
	CheckChanged(filename string, content []byte) (string, bool)
	MarkWritten(filename, hash string)
}

// WriteAssets copies the layout files to the output directory.
// It supports both embedded assets and custom assets from the filesystem.
func (r *Renderer) WriteAssets() error {
	_, err := r.WriteAssetsWithState(nil, false)
	return err
}

// WriteAssetsWithState writes layout CSS/JS assets, skipping files whose content hashes match state.
// It returns the tracked relative asset keys (e.g. "layout/repostyle.css") for stale-file exclusion.
func (r *Renderer) WriteAssetsWithState(state AssetStateTracker, force bool) ([]string, error) {
	if r.Sink == nil {
		r.Sink = &AtomicLocalSink{RootDir: r.OutDir}
	}
	layoutDir := filepath.Join(r.OutDir, "layout")
	var assetKeys []string

	writeTrackedAsset := func(relPath, _ string, content []byte) error {
		assetKey := filepath.ToSlash(filepath.Join("layout", relPath))
		assetKeys = append(assetKeys, assetKey)

		if state != nil {
			hash, changed := state.CheckChanged(assetKey, content)
			if !force && !changed && r.Sink.Exists(assetKey) {
				return nil
			}
			if err := r.Sink.WriteFile(assetKey, content); err != nil {
				return err
			}
			state.MarkWritten(assetKey, hash)
			return nil
		}

		return r.Sink.WriteFile(assetKey, content)
	}

	if r.TemplateDir != "" {
		// Copy recursively from custom layout directory
		srcLayout := filepath.Join(r.TemplateDir, "layout")
		if _, err := os.Stat(srcLayout); os.IsNotExist(err) {
			return nil, nil
		}

		err := filepath.WalkDir(srcLayout, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return fmt.Errorf("%w: %w", models.ErrIO, err)
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("%w: symlink not permitted in custom layout assets: %s", models.ErrIntegrity, path)
			}
			relPath, err := filepath.Rel(srcLayout, path)
			if err != nil {
				return err
			}
			if relPath == "." {
				return nil
			}
			if _, err := util.ResolveContainedPath(srcLayout, relPath); err != nil {
				return fmt.Errorf("%w: %w", models.ErrIntegrity, err)
			}
			destPath, err := util.ResolveContainedPath(layoutDir, relPath)
			if err != nil {
				return fmt.Errorf("%w: %w", models.ErrIntegrity, err)
			}
			if d.IsDir() {
				return nil
			}
			// #nosec G304 G122 -- path is verified contained inside validated user templateDir without symlinks
			content, err := os.ReadFile(filepath.Clean(path))
			if err != nil {
				return fmt.Errorf("%w: %w", models.ErrIO, err)
			}
			return writeTrackedAsset(relPath, destPath, content)
		})
		return assetKeys, err
	}

	// Walk the embedded FS "templates/layout"
	err := fs.WalkDir(templateFS, "templates/layout", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel("templates/layout", path)
		if err != nil {
			return err
		}
		if relPath == "." || d.IsDir() {
			return nil
		}
		destPath := filepath.Join(layoutDir, relPath)

		// Read file content
		content, err := templateFS.ReadFile(path)
		if err != nil {
			return err
		}

		return writeTrackedAsset(relPath, destPath, content)
	})
	return assetKeys, err
}

// xmlEscape escapes arbitrary text for safe inclusion inside XML elements and attributes.
func xmlEscape(v interface{}) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(fmt.Sprint(v)))
	return buf.String()
}

// cdataHTML HTML-escapes text and neutralizes any CDATA end sequence (]]>) for safe CDATA embedding.
func cdataHTML(v interface{}) string {
	escaped := html.EscapeString(fmt.Sprint(v))
	return strings.ReplaceAll(escaped, "]]>", "]]]]><![CDATA[>")
}

// safeURL permits only relative URLs (excluding protocol-relative "//") or http/https/ftp schemes.
// Any potentially dangerous scheme (such as javascript: or data:) is replaced with an empty string.
func safeURL(v interface{}) string {
	raw := strings.TrimSpace(fmt.Sprint(v))
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "ftp://") {
		return raw
	}
	if strings.HasPrefix(raw, "//") {
		return ""
	}
	if idx := strings.IndexByte(raw, ':'); idx != -1 {
		// Reject scheme-prefixed URIs (e.g. javascript:, data:, vbscript:) unless matched above
		if slashIdx := strings.IndexByte(raw, '/'); slashIdx == -1 || idx < slashIdx {
			return ""
		}
	}
	return raw
}
