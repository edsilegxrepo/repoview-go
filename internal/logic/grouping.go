package logic

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/util"
)

// OBJECTIVES:
// Organize repository packages into logical taxonomies, hierarchical category trees,
// and alphabetical index buckets. Solve modern Enterprise Linux distribution packaging
// gaps by automatically inferring canonical groups for packages with empty or "unspecified" groups.
//
// CORE COMPONENTS:
//   - GroupData: Aggregated package collection representing an individual group HTML page.
//   - GroupCategory: Parent category node (e.g. "applications") containing subgroups or acting as a leaf.
//   - GroupSubItem: Child subgroup leaf node (e.g. "internet", "archiving") with package counts.
//   - GroupingService: Orchestrates comps-based grouping, RPM header grouping, and letter indexing.
//   - InferGroupForPackage: Heuristic engine classifying unspecified packages by name, summary, and description.
//   - BuildGroupTree: Transforms flat group slices into structured hierarchical navigation trees.
//
// FUNCTIONALITY:
//   - Priority grouping: Uses comps.xml group and category definitions when available.
//   - Fallback grouping: Uses RPM header Group tags with automatic group inference.
//   - Letter grouping: Buckets packages alphabetically by initial Unicode character.
//   - Deduplication: Associates the latest version of each package to the group views.
//
// DATA FLOW:
//   []*models.Package (+ comps.xml) -> GroupingService -> []*GroupData -> BuildGroupTree -> []*GroupCategory -> Template Rendering

// GroupData holds the aggregated data for a specific package group.
// This structure maps directly to the data context passed to 'group.html'.
type GroupData struct {
	ID          string            // Unique identifier (e.g., "group_development")
	Name        string            // Display name (e.g., "Development Tools")
	Letter      string            // Single-character letter token when representing an alphabetical bucket
	Description string            // Optional description
	Filename    string            // Output filename (e.g., "group_development.html")
	Packages    []*models.Package // List of packages belonging to this group
}

// GroupSubItem represents a child/subgroup item in a hierarchical category.
type GroupSubItem struct {
	Name     string            // Subgroup leaf name (e.g., "internet", "archiving")
	FullName string            // Full original group name (e.g., "applications/internet")
	Filename string            // Target HTML filename (e.g., "applications.internet.group.html")
	Packages []*models.Package // Packages in this subgroup
	Count    int               // len(Packages)
}

// GroupCategory represents a parent category in the group tree.
type GroupCategory struct {
	Name          string            // Category name (e.g., "applications", "development")
	TotalPackages int               // Sum of packages across all subgroups in this category
	Subgroups     []*GroupSubItem   // Ordered list of subgroups
	IsLeaf        bool              // True if standalone group without slash
	Filename      string            // Target HTML filename if IsLeaf is true
	Packages      []*models.Package // Packages if IsLeaf is true
}

// HasActive checks if any subgroup (or leaf) matches the given active filename.
func (c *GroupCategory) HasActive(activeFilename string) bool {
	if c.IsLeaf && c.Filename == activeFilename {
		return true
	}
	for _, sub := range c.Subgroups {
		if sub.Filename == activeFilename {
			return true
		}
	}
	return false
}

// SiblingRepo represents a nearby repository channel or architecture detected on disk.
type SiblingRepo struct {
	Name     string // Channel identifier (e.g. "base", "extras", "x86_64")
	RelURL   string // Relative URL to sibling repoview index
	IsActive bool   // True if this is the currently generated repository channel
}

// BuildGroupTree decomposes a flat slice of GroupData into a hierarchical category tree.
// Category prefixes before a forward slash ('/') become parent categories, and the
// remaining text becomes the subgroup. Standalone groups without slashes become leaf categories.
func BuildGroupTree(groups []*GroupData) []*GroupCategory {
	type catCollector struct {
		name      string
		subgroups []*GroupSubItem
		totalPkgs int
		isLeaf    bool
		filename  string
		packages  []*models.Package
	}

	var catOrder []string
	catMap := make(map[string]*catCollector)

	for _, g := range groups {
		parts := strings.SplitN(g.Name, "/", 2)
		if len(parts) == 2 {
			catName := strings.TrimSpace(parts[0])
			subName := strings.TrimSpace(parts[1])

			subItem := &GroupSubItem{
				Name:     subName,
				FullName: g.Name,
				Filename: g.Filename,
				Packages: g.Packages,
				Count:    len(g.Packages),
			}

			col, exists := catMap[catName]
			if !exists {
				col = &catCollector{name: catName}
				catMap[catName] = col
				catOrder = append(catOrder, catName)
			}
			col.subgroups = append(col.subgroups, subItem)
			col.totalPkgs += len(g.Packages)
		} else {
			// Standalone group without slash (e.g. "databases", "documentation")
			catName := strings.TrimSpace(g.Name)
			col, exists := catMap[catName]
			if !exists {
				col = &catCollector{
					name:      catName,
					isLeaf:    true,
					filename:  g.Filename,
					packages:  g.Packages,
					totalPkgs: len(g.Packages),
				}
				catMap[catName] = col
				catOrder = append(catOrder, catName)
			} else {
				if col.isLeaf && col.filename != g.Filename {
					col.subgroups = append(col.subgroups, &GroupSubItem{
						Name:     col.name,
						FullName: col.name,
						Filename: col.filename,
						Packages: col.packages,
						Count:    len(col.packages),
					})
					col.isLeaf = false
					col.filename = ""
					col.packages = nil
				}
				if !col.isLeaf {
					col.subgroups = append(col.subgroups, &GroupSubItem{
						Name:     catName,
						FullName: g.Name,
						Filename: g.Filename,
						Packages: g.Packages,
						Count:    len(g.Packages),
					})
				} else {
					col.packages = append(col.packages, g.Packages...)
				}
				col.totalPkgs += len(g.Packages)
			}
		}
	}

	result := make([]*GroupCategory, 0, len(catOrder))
	for _, catName := range catOrder {
		col := catMap[catName]

		// Sort subgroups by name
		sort.Slice(col.subgroups, func(i, j int) bool {
			return col.subgroups[i].Name < col.subgroups[j].Name
		})

		result = append(result, &GroupCategory{
			Name:          col.name,
			TotalPackages: col.totalPkgs,
			Subgroups:     col.subgroups,
			IsLeaf:        col.isLeaf,
			Filename:      col.filename,
			Packages:      col.packages,
		})
	}

	return result
}

// GroupingService handles the organization of packages into logical groups.
// It supports grouping by Comps.xml definitions, RPM Header Groups, and First Letter.
type GroupingService struct {
	Packages    []*models.Package            // All available packages in the repository
	Comps       *models.Comps                // Optional comps metadata
	pkgVersions map[string][]*models.Package // Cached Name -> EVR-descending sorted package slices
}

// NewGroupingService creates a new grouping service instance.
func NewGroupingService(pkgs []*models.Package, comps *models.Comps) *GroupingService {
	return &GroupingService{
		Packages: pkgs,
		Comps:    comps,
	}
}

// PkgVersions builds (once) and returns the map of package name to EVR-descending sorted versions.
func (s *GroupingService) PkgVersions() map[string][]*models.Package {
	if s.pkgVersions != nil {
		return s.pkgVersions
	}
	pkgVersions := make(map[string][]*models.Package)
	for _, p := range s.Packages {
		pkgVersions[p.Name] = append(pkgVersions[p.Name], p)
	}
	for _, versions := range pkgVersions {
		SortPackagesByEVR(versions)
	}
	s.pkgVersions = pkgVersions
	return s.pkgVersions
}

// GetGroups returns the list of groups populated with their packages.
// It prioritizes 'comps.xml' definitions (standard YUM grouping) if available.
// If comps data is missing, it automatically falls back to RPM 'Group' header tags
// to ensure the view is still structured and navigable.
// Groups containing no packages are filtered out to keep the UI clean.
func (s *GroupingService) GetGroups() ([]*GroupData, error) {
	var groups []*GroupData

	if s.Comps != nil {
		groups = s.getCompsGroups()
	} else {
		groups = s.getRpmGroups()
	}

	// Filter empty groups
	var activeGroups []*GroupData
	for _, g := range groups {
		if len(g.Packages) > 0 {
			// Sort packages by name within the group
			sort.Slice(g.Packages, func(i, j int) bool {
				return g.Packages[i].Name < g.Packages[j].Name
			})
			activeGroups = append(activeGroups, g)
		}
	}

	disambiguateGroupFilenames(activeGroups)
	return activeGroups, nil
}

// getCompsGroups organizes packages based on the 'comps.xml' definition file.
// This is the preferred grouping method as it provides curated, user-friendly categories
// (e.g., "Web Servers", "Development Tools") defined by the repository maintainers.
// It ensures that for each package name listed in a group, the *latest* version
// of that package present in the repository is linked.
func (s *GroupingService) getCompsGroups() []*GroupData {
	var groups []*GroupData
	pkgVersions := s.PkgVersions()

	for _, g := range s.Comps.Groups {
		groupData := &GroupData{
			ID:          g.ID,
			Name:        g.Name,
			Description: g.Description,
			Filename:    fmt.Sprintf("%s.group.html", cleanID(g.ID)),
		}

		// Add packages belonging to this group
		seenInGroup := make(map[string]bool)
		for _, pkgName := range g.Packagelist {
			if versions, exists := pkgVersions[pkgName]; exists && len(versions) > 0 {
				if !seenInGroup[pkgName] {
					groupData.Packages = append(groupData.Packages, versions[0])
					seenInGroup[pkgName] = true
				}
			}
		}
		groups = append(groups, groupData)
	}
	return groups
}

// MapDebianSectionToGroup maps standard Debian/Ubuntu package sections
// to canonical Repoview taxonomy categories.
func MapDebianSectionToGroup(section string) string {
	s := strings.ToLower(strings.TrimSpace(section))
	// Strip component prefix if present (e.g. "main/devel" -> "devel", "universe/web" -> "web")
	if idx := strings.LastIndex(s, "/"); idx != -1 {
		s = s[idx+1:]
	}

	switch s {
	case "admin":
		return "applications/system"
	case "cli-mono", "gnu-r", "haskell", "interpreters", "java", "javascript", "lisp", "ocaml", "perl", "php", "python", "ruby", "rust":
		return "development/languages"
	case "comm", "hamradio":
		return "applications/communications"
	case "database":
		return "applications/databases"
	case "debug":
		return "development/debug"
	case "devel", "vcs":
		return "development/tools"
	case "doc":
		return "documentation"
	case "editors":
		return "applications/editors"
	case "education":
		return "applications/education"
	case "electronics", "embedded", "math", "science":
		return "applications/engineering"
	case "fonts", "x11":
		return "user interface/x"
	case "games":
		return "amusements/games"
	case "gnome", "gnustep", "kde", "xfce":
		return "user interface/desktops"
	case "graphics", "sound", "video":
		return "applications/multimedia"
	case "httpd", "web", "net", "mail", "news", "zope":
		return "applications/internet"
	case "introspection", "libdevel", "libs", "oldlibs":
		return "development/libraries"
	case "kernel":
		return "system environment/kernel"
	case "shells":
		return "system/shells"
	case "tex":
		return "applications/publishing"
	case "text":
		return "applications/text"
	case "utils":
		return "applications/system"
	case "default", "unspecified", "unknown", "misc", "":
		return ""
	default:
		return ""
	}
}

// getRpmGroups organizes packages based on their embedded RPM Group header or Debian Section.
// This is used as a fallback strategy when 'comps.xml' is not present.
func (s *GroupingService) getRpmGroups() []*GroupData {
	groupsMap := make(map[string]*GroupData)
	pkgVersions := s.PkgVersions()

	// Distinct names
	names := make([]string, 0, len(pkgVersions))
	for n := range pkgVersions {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		versions := pkgVersions[name]
		if len(versions) == 0 {
			continue
		}
		latest := versions[0]

		// Determine the group from latest version's group or section.
		grpName := strings.TrimSpace(latest.RpmGroup)

		// Check Debian section mapping if available
		if latest.Section != "" {
			if mapped := MapDebianSectionToGroup(latest.Section); mapped != "" {
				grpName = mapped
			}
		}

		// If unspecified, missing, or generic (e.g. "default", "misc", "unknown"),
		// infer from package name, summary, and description.
		if grpName == "" || strings.EqualFold(grpName, "unspecified") || strings.EqualFold(grpName, "unknown") || strings.EqualFold(grpName, "default") || strings.EqualFold(grpName, "misc") {
			inferred := InferGroupForPackage(latest)
			if inferred != "" {
				grpName = inferred
			} else {
				grpName = "unspecified"
			}
		} else {
			// For broad categories like "development/tools" or "applications/system",
			// check if specific package rules can provide higher fidelity (e.g. clamav -> security, proxysql -> databases).
			if grpName == "development/tools" || grpName == "applications/system" {
				if specific := InferGroupForPackage(latest); specific != "" && specific != "unspecified" && specific != "development/tools" && specific != "applications/system" {
					grpName = specific
				}
			}
			// Parity: normalize to lowercase
			grpName = strings.ToLower(grpName)
		}

		// Update RpmGroup and Group on all versions of this package so that
		// package detail views, primary group links, and breadcrumbs are consistent.
		for _, v := range versions {
			v.RpmGroup = grpName
			v.Group = grpName
		}

		if _, exists := groupsMap[grpName]; !exists {
			groupsMap[grpName] = &GroupData{
				ID:       grpName,
				Name:     grpName,
				Filename: fmt.Sprintf("%s.group.html", cleanID(grpName)),
			}
		}
		groupsMap[grpName].Packages = append(groupsMap[grpName].Packages, latest)
	}

	// Convert map to slice
	groups := make([]*GroupData, 0, len(groupsMap))
	for _, g := range groupsMap {
		groups = append(groups, g)
	}

	// Sort groups by name
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Name < groups[j].Name
	})

	return groups
}

// GetLetterGroups organizes packages alphabetically by their first letter.
// This provides an alternative navigation method (e.g., "Letter A", "Letter B").
func (s *GroupingService) GetLetterGroups() []*GroupData {
	groupsMap := make(map[rune]*GroupData)
	pkgVersions := s.PkgVersions()

	names := make([]string, 0, len(pkgVersions))
	for n := range pkgVersions {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		if len(name) == 0 {
			continue
		}
		versions := pkgVersions[name]
		if len(versions) == 0 {
			continue
		}
		firstRune, _ := utf8.DecodeRuneInString(name)
		if firstRune == utf8.RuneError {
			continue
		}
		firstLetter := unicode.ToUpper(firstRune)

		if _, exists := groupsMap[firstLetter]; !exists {
			groupsMap[firstLetter] = &GroupData{
				ID:          fmt.Sprintf("letter_%c", firstLetter),
				Name:        fmt.Sprintf("Letter %c", firstLetter),
				Letter:      string(firstLetter),
				Description: fmt.Sprintf("Packages beginning with letter \"%c\".", firstLetter),
				Filename:    fmt.Sprintf("letter_%c.group.html", unicode.ToLower(firstLetter)),
			}
		}

		groupsMap[firstLetter].Packages = append(groupsMap[firstLetter].Packages, versions[0])
	}

	groups := make([]*GroupData, 0, len(groupsMap))
	for _, g := range groupsMap {
		groups = append(groups, g)
	}

	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Name < groups[j].Name
	})

	disambiguateGroupFilenames(groups)
	return groups
}

// disambiguateGroupFilenames appends a deterministic short hash suffix when two groups
// map to identical filenames under case-insensitive comparison (Windows/macOS safety).
func disambiguateGroupFilenames(groups []*GroupData) {
	seen := make(map[string]bool, len(groups))
	for _, g := range groups {
		lower := strings.ToLower(g.Filename)
		if !seen[lower] {
			seen[lower] = true
			continue
		}
		sum := sha256.Sum256([]byte(g.ID + "|" + g.Name))
		suffix := hex.EncodeToString(sum[:3])
		stem := strings.TrimSuffix(g.Filename, ".group.html")
		candidate := fmt.Sprintf("%s_%s.group.html", stem, suffix)
		for seen[strings.ToLower(candidate)] {
			sum = sha256.Sum256([]byte(candidate))
			candidate = fmt.Sprintf("%s_%s.group.html", stem, hex.EncodeToString(sum[:3]))
		}
		g.Filename = candidate
		seen[strings.ToLower(candidate)] = true
	}
}

// AssignCollisionSafePackageFilenames resolves case-insensitive HTML filename collisions
// across distinct package names by setting PageFilename with a deterministic short hash suffix.
func AssignCollisionSafePackageFilenames(pkgVersions map[string][]*models.Package) {
	names := make([]string, 0, len(pkgVersions))
	for n := range pkgVersions {
		names = append(names, n)
	}
	sort.Strings(names)

	seen := make(map[string]bool, len(names))
	for _, name := range names {
		versions := pkgVersions[name]
		if len(versions) == 0 {
			continue
		}
		baseName := util.SanitizeFilename(name) + ".html"
		lower := strings.ToLower(baseName)
		if !seen[lower] {
			seen[lower] = true
			continue
		}
		sum := sha256.Sum256([]byte(name))
		suffix := hex.EncodeToString(sum[:3])
		stem := strings.TrimSuffix(baseName, ".html")
		candidate := fmt.Sprintf("%s_%s.html", stem, suffix)
		for seen[strings.ToLower(candidate)] {
			sum = sha256.Sum256([]byte(candidate))
			candidate = fmt.Sprintf("%s_%s.html", stem, hex.EncodeToString(sum[:3]))
		}
		seen[strings.ToLower(candidate)] = true
		for _, p := range versions {
			p.PageFilename = candidate
		}
	}
}

// Helpers

// cleanID sanitizes a group identifier into a safe filename.
func cleanID(text string) string {
	return util.SanitizeFilename(text)
}

// InferGroupForPackage inspects package name, summary, and description to determine
// an appropriate canonical group if the package lacks an RPM group tag or is marked "Unspecified".
// It maps packages into standard RPM/comps categories (e.g. applications/databases,
// development/tools, system environment/daemons, productivity/networking/ssh).
func InferGroupForPackage(p *models.Package) string {
	if p == nil {
		return "unspecified"
	}

	name := strings.ToLower(p.Name)
	summary := strings.ToLower(p.Summary)
	desc := strings.ToLower(p.Description)

	// 1. Documentation
	if strings.HasSuffix(name, "-doc") || strings.HasSuffix(name, "-docs") || strings.Contains(name, "-doc-") ||
		strings.HasPrefix(summary, "documentation") || strings.HasPrefix(summary, "manual pages") {
		return "documentation"
	}

	// 2. Specific package prefixes and families
	if strings.HasPrefix(name, "libreoffice") || strings.HasPrefix(name, "libobasis") || strings.HasPrefix(name, "openoffice") {
		return "applications/productivity"
	}
	if name == "rclone" {
		return "applications/internet"
	}
	if name == "miller" || name == "mlr" {
		return "applications/text"
	}
	if name == "gcsfuse" || strings.Contains(name, "fuse") {
		if strings.HasSuffix(name, "-libs") || strings.HasSuffix(name, "-devel") {
			return "system environment/libraries"
		}
		return "system environment/base"
	}
	if strings.HasPrefix(name, "proxysql") {
		return "applications/databases"
	}
	if strings.HasPrefix(name, "openssh") || strings.HasPrefix(name, "pam_ssh") || strings.Contains(name, "ssh-agent") {
		return "productivity/networking/ssh"
	}
	if strings.HasPrefix(name, "pg_") || strings.HasPrefix(name, "pg-") || strings.HasPrefix(name, "postgresql") || strings.HasPrefix(name, "postgres") ||
		name == "pgaudit" || name == "pgbouncer" || name == "pgcenter" || name == "pgcli" {
		return "applications/databases"
	}
	if strings.HasPrefix(name, "git") || strings.HasPrefix(name, "perl-git") {
		if name == "git-daemon" {
			return "system environment/daemons"
		}
		return "development/tools"
	}
	if strings.HasPrefix(name, "php") {
		if name == "php" || name == "php-cli" || name == "php-common" || name == "php-fpm" {
			return "development/languages"
		}
		if strings.Contains(name, "mysql") || strings.Contains(name, "pgsql") || strings.Contains(name, "pdo") ||
			strings.Contains(name, "odbc") || strings.Contains(name, "dba") || strings.Contains(name, "redis") {
			return "applications/databases"
		}
		return "development/libraries"
	}
	if strings.HasPrefix(name, "python") {
		if strings.Contains(name, "freethreading") || strings.Contains(name, "tkinter") || strings.Contains(name, "idle") ||
			strings.Contains(name, "test") || strings.HasSuffix(name, "310") || strings.HasSuffix(name, "311") ||
			strings.HasSuffix(name, "312") || strings.HasSuffix(name, "313") || strings.HasSuffix(name, "314") {
			return "development/languages"
		}
		return "development/libraries"
	}
	if strings.HasPrefix(name, "ruby") || strings.HasPrefix(name, "rubygem") {
		if name == "ruby" || name == "ruby-devel" {
			return "development/languages"
		}
		if name == "rubygem-rake" || name == "rubygem-bundler" {
			return "development/tools"
		}
		if name == "rubygem-rdoc" {
			return "documentation"
		}
		return "development/libraries"
	}
	if strings.HasPrefix(name, "glusterfs") || strings.HasPrefix(name, "libgf") {
		if strings.Contains(name, "server") || strings.Contains(name, "gnfs") {
			return "system environment/daemons"
		}
		if strings.HasPrefix(name, "libgf") {
			return "system environment/libraries"
		}
		return "system environment/base"
	}
	if strings.HasPrefix(name, "nfs-ganesha") {
		return "system environment/daemons"
	}
	if strings.HasPrefix(name, "ostree") {
		if strings.HasSuffix(name, "-libs") {
			return "system environment/libraries"
		}
		return "system environment/base"
	}
	if strings.HasPrefix(name, "knot") {
		if strings.Contains(name, "resolver") || name == "knot" {
			return "system environment/daemons"
		}
		if strings.HasSuffix(name, "-libs") {
			return "system environment/libraries"
		}
		return "system environment/tools"
	}
	if strings.HasPrefix(name, "clam") {
		if name == "clamd" || strings.Contains(name, "milter") {
			return "system environment/daemons"
		}
		if strings.HasSuffix(name, "-lib") || strings.HasSuffix(name, "-libs") {
			return "system environment/libraries"
		}
		return "system environment/security"
	}
	if strings.HasPrefix(name, "clang") || strings.HasPrefix(name, "llvm") {
		if strings.HasSuffix(name, "-libs") {
			return "development/libraries"
		}
		return "development/tools"
	}
	if strings.HasPrefix(name, "dpkg") || name == "dselect" {
		if name == "dpkg-dev" {
			return "development/tools"
		}
		return "applications/system"
	}
	if strings.HasPrefix(name, "e2fs") || name == "e2scrub" {
		if strings.HasSuffix(name, "-libs") {
			return "system environment/libraries"
		}
		return "system environment/base"
	}
	if strings.HasPrefix(name, "exiv2") || strings.HasPrefix(name, "lensfun") {
		if strings.HasSuffix(name, "-libs") || name == "exiv2" || name == "lensfun" {
			return "development/libraries"
		}
		return "applications/multimedia"
	}
	if strings.HasPrefix(name, "nss") || name == "nspr" || strings.HasPrefix(name, "tcp_wrappers") {
		return "system environment/security"
	}
	if name == "asciidoc" || name == "unpaper" || name == "ocrmypdf" || name == "mupdf" || name == "diffutils" {
		return "applications/text"
	}
	if name == "dockerize" || name == "partclone" || name == "nwipe" {
		return "system environment/base"
	}
	if name == "haveged" || name == "supervisor" || name == "rabbitmq-server" || name == "ocserv" || name == "vouch-proxy" {
		return "system environment/daemons"
	}
	if name == "tmux" || name == "screen" {
		return "system/shells"
	}
	if name == "netcat" || name == "socat" {
		return "applications/internet"
	}
	if name == "cfitsio" || name == "nanomsg" || name == "infinipath-psm" || name == "libseccomp" || name == "libcom_err" || name == "libss" {
		return "system environment/libraries"
	}
	if strings.HasPrefix(name, "createrepo_c") {
		if strings.HasSuffix(name, "-libs") {
			return "development/libraries"
		}
		return "development/tools"
	}
	if name == "fpack" {
		return "applications/archiving"
	}
	if strings.HasPrefix(name, "ufraw") {
		return "applications/multimedia"
	}

	// 3. Keyword / semantic heuristics on summary & description
	text := name + " " + summary + " " + desc

	// Daemons & background services
	if containsAny(summary, "daemon", "server daemon", "background service", "fastcgi process manager") ||
		(containsAny(summary, " server", "server ") && !containsAny(summary, "client", "driver", "library")) {
		return "system environment/daemons"
	}

	// Databases
	if containsAny(text, "database", "postgresql", "mysql", "mariadb", "sqlite", "redis key-value", "mongodb") {
		return "applications/databases"
	}

	// Libraries
	if strings.HasSuffix(name, "-libs") || strings.HasSuffix(name, "-lib") || strings.HasPrefix(name, "lib") ||
		containsAny(summary, "shared library", "c library", "runtime library", "development library", "shared libraries") {
		if containsAny(text, "kernel", "filesystem", "system", "seccomp", "posix") {
			return "system environment/libraries"
		}
		return "development/libraries"
	}

	// Development tools / compilers
	if containsAny(summary, "compiler", "debugger", "profiler", "build tool", "assembler", "version control", "development environment", "make utility") {
		return "development/tools"
	}

	// Programming languages & runtimes
	if containsAny(summary, "programming language", "interpreter", "runtime environment") {
		return "development/languages"
	}

	// Security & cryptography
	if containsAny(summary, "security", "antivirus", "cryptographic", "authentication", "firewall", "vulnerability", "ssl", "tls", "certificate") {
		return "system environment/security"
	}

	// Web / Internet / Networking
	if containsAny(summary, "web", "http", "proxy", "dns", "network", "socket", "tcp", "udp", "ftp", "browser") {
		return "applications/internet"
	}

	// Multimedia / Audio / Video / Graphics
	if containsAny(summary, "image", "video", "audio", "multimedia", "graphics", "photo", "camera", "codec") {
		return "applications/multimedia"
	}

	// Text & Publishing
	if containsAny(summary, "text", "editor", "document", "pdf", "diff", "epub", "typography", "typesetting") {
		return "applications/text"
	}

	// Archiving & Compression
	if containsAny(summary, "archive", "archiving", "compression", "compress", "zip", "tar", "gzip", "bzip2", "xz") {
		return "applications/archiving"
	}

	// System Base / Filesystem / Kernel
	if containsAny(summary, "filesystem", "partition", "kernel", "boot", "hardware", "firmware", "driver") {
		return "system environment/base"
	}

	// System Management
	if containsAny(summary, "management", "monitoring", "admin", "process state") {
		return "applications/system"
	}

	return "unspecified"
}

// containsAny returns true if s contains any of the provided substrings.
func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
