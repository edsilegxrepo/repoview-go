package logic

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

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
				col.totalPkgs += len(g.Packages)
				col.packages = append(col.packages, g.Packages...)
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
	Packages []*models.Package // All available packages in the repository
	Comps    *models.Comps     // Optional comps metadata
}

// NewGroupingService creates a new grouping service instance.
func NewGroupingService(pkgs []*models.Package, comps *models.Comps) *GroupingService {
	return &GroupingService{
		Packages: pkgs,
		Comps:    comps,
	}
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

	return activeGroups, nil
}

// getCompsGroups organizes packages based on the 'comps.xml' definition file.
// This is the preferred grouping method as it provides curated, user-friendly categories
// (e.g., "Web Servers", "Development Tools") defined by the repository maintainers.
// It ensures that for each package name listed in a group, the *latest* version
// of that package present in the repository is linked.
func (s *GroupingService) getCompsGroups() []*GroupData {
	var groups []*GroupData

	// Create a lookup map for packages by name
	// Note: We might have multiple versions of the same package in s.Packages.
	// We typically want to associate all versions, or just the latest?
	// Repoview usually lists the package name once, and the link goes to the package detail page
	// which lists all versions. So we just need to match by name.

	// However, s.Packages contains all versions. We should distinct them first if we are just checking existence?
	// Actually, we assign the package object to the group.
	// But we need to handle duplicates in the packages list passed to this service?
	// Assuming s.Packages is the raw list from DB (all rows).

	// Let's optimize: Map Name -> List of Packages (versions)
	pkgVersions := make(map[string][]*models.Package)
	for _, p := range s.Packages {
		pkgVersions[p.Name] = append(pkgVersions[p.Name], p)
	}

	for _, g := range s.Comps.Groups {
		// Parity: Python 3 repoview comments out the uservisible check
		// if !g.Uservisible {
		// 	continue
		// }

		groupData := &GroupData{
			ID:          g.ID,
			Name:        g.Name,
			Description: g.Description,
			Filename:    fmt.Sprintf("%s.group.html", cleanID(g.ID)),
		}

		// Add packages belonging to this group
		seenInGroup := make(map[string]bool)
		for _, pkgName := range g.Packagelist {
			if _, exists := pkgVersions[pkgName]; exists {
				// We found the package in the repo
				// We add one entry per package name to the group list,
				// The view will likely just link to the package detail page.
				// But we need a representative "Package" struct to get Summary/Description.
				// Ideally the "latest" version.

				if !seenInGroup[pkgName] {
					latest := getLatestVersion(pkgVersions[pkgName])
					groupData.Packages = append(groupData.Packages, latest)
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
	pkgVersions := make(map[string][]*models.Package)

	for _, p := range s.Packages {
		pkgVersions[p.Name] = append(pkgVersions[p.Name], p)
	}

	// Distinct names
	var names []string
	for n := range pkgVersions {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		versions := pkgVersions[name]
		latest := getLatestVersion(versions)

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
	var groups []*GroupData
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
	pkgVersions := make(map[string][]*models.Package)

	for _, p := range s.Packages {
		pkgVersions[p.Name] = append(pkgVersions[p.Name], p)
	}

	var names []string
	for n := range pkgVersions {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		if len(name) == 0 {
			continue
		}
		firstLetter := unicode.ToUpper(rune(name[0]))

		if _, exists := groupsMap[firstLetter]; !exists {
			groupsMap[firstLetter] = &GroupData{
				ID:          fmt.Sprintf("letter_%c", firstLetter),
				Name:        fmt.Sprintf("Letter %c", firstLetter),
				Description: fmt.Sprintf("Packages beginning with letter \"%c\".", firstLetter),
				Filename:    fmt.Sprintf("letter_%c.group.html", unicode.ToLower(firstLetter)),
			}
		}

		latest := getLatestVersion(pkgVersions[name])
		groupsMap[firstLetter].Packages = append(groupsMap[firstLetter].Packages, latest)
	}

	var groups []*GroupData
	for _, g := range groupsMap {
		groups = append(groups, g)
	}

	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Name < groups[j].Name
	})

	return groups
}

// Helpers

// getLatestVersion returns the package with the highest EVR from a slice of versions.
func getLatestVersion(pkgs []*models.Package) *models.Package {
	if len(pkgs) == 0 {
		return nil
	}
	// Sort descending
	SortPackagesByEVR(pkgs)
	// Return first (newest)
	return pkgs[0]
}

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
