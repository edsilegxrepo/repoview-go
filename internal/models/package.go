// Package models defines domain structures and entity types representing RPM packages,
// repository metadata descriptors, group comps definitions, and search indices.
//
// OBJECTIVES:
// Provide unified, strongly-typed data structures for representing RPM package metadata,
// dependency graphs, binary header inspections, and repository index files throughout
// the generation pipeline.
//
// CORE COMPONENTS:
//   - Package: Core entity representing an RPM package, enriched with changelogs, dependencies,
//     and binary header inspections.
//   - DependencyEntry: A single RPM prerequisite, capability, conflict, or obsoletion.
//   - PackageDependencies: Aggregate collection of all 4 dependency vectors.
//   - RPMDetails: Deep inspection details from the RPM package header (scriptlets, signatures, files).
//   - RPMScriptlets: Pre/post install and uninstall shell scripts.
//   - RPMFile: Individual installed file path with permissions and size.
//   - ChangelogEntry: Historical package changelog note with date and author.
//
// FUNCTIONALITY:
//   - Stores raw relational metadata queried from primary.sqlite and other.sqlite.
//   - Dynamically enriches packages with deep RPM binary header introspection.
//   - Generates sanitized filesystem basenames for static HTML page rendering.
//   - Formats complex dependency relation operators (>=, =, <=) and EVR strings.
//
// DATA FLOW:
//
//	SQLite DBs / RPM Headers / comps.xml -> models.Package -> logic (sorting/grouping) -> render (HTML/RSS)
package models

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/edsilegxrepo/repoview/internal/util"
)

// Package represents a row in the packages table of primary.sqlite, enriched
// with metadata from other repository sources during processing.
type Package struct {
	PkgKey       int64  `db:"pkgKey"`        // Primary key in SQLite primary.sqlite database
	Name         string `db:"name"`          // Package name (e.g. "nginx", "kernel")
	Epoch        string `db:"epoch"`         // RPM epoch number ("0" if unversioned)
	Version      string `db:"version"`       // Upstream source version (e.g. "1.20.1")
	Release      string `db:"release"`       // Distribution release string (e.g. "10.el9")
	Arch         string `db:"arch"`          // Hardware architecture ("x86_64", "noarch", "src")
	Summary      string `db:"summary"`       // Short one-line summary of package function
	Description  string `db:"description"`   // Full multi-line package description
	URL          string `db:"url"`           // Upstream project homepage URL
	TimeBuild    int64  `db:"time_build"`    // Unix build timestamp
	License      string `db:"rpm_license"`   // Software license identifier (e.g. "GPL-2.0-or-later")
	SourceRPM    string `db:"rpm_sourcerpm"` // Source RPM package name from which this was built
	SizePackage  int64  `db:"size_package"`  // Compressed RPM package file size in bytes
	LocationHref string `db:"location_href"` // Relative path to the RPM file in the repository
	Vendor       string `db:"rpm_vendor"`    // Organization or entity packaging this RPM
	RpmGroup     string `db:"rpm_group"`     // Legacy RPM group string from spec file

	BuildHost     string `db:"rpm_buildhost"`  // Hostname of the builder system
	InstalledSize int64  `db:"size_installed"` // Uncompressed disk footprint in bytes

	// Enriched fields - populated during processing
	Changelog    *ChangelogEntry      // Latest changelog entry, fetched from other.sqlite
	AllVersions  []*Package           // List of all versions of this package (for detail page history)
	Group        string               // Resolved functional group name (from Comps or RPM header)
	Details      *RPMDetails          // Deep RPM inspection metadata (scriptlets, signatures, files)
	Dependencies *PackageDependencies // RPM dependencies (requires, provides, conflicts, obsoletes)
}

// DependencyEntry represents a single RPM dependency requirement, capability, conflict, or obsoletion.
type DependencyEntry struct {
	Name    string `db:"name"`    // Capability or package name required/provided
	Flags   string `db:"flags"`   // Comparison flag (EQ, GE, LE, GT, LT)
	Epoch   string `db:"epoch"`   // Target capability epoch
	Version string `db:"version"` // Target capability version
	Release string `db:"release"` // Target capability release
	Pre     bool   `db:"pre"`     // True if required for pre-install scriptlet execution
}

// FormattedRelation formats the comparison operator and EVR string (e.g. ">= 1.2.3-4").
// If no flags or version are specified, returns an empty string.
func (d *DependencyEntry) FormattedRelation() string {
	if d.Flags == "" && d.Version == "" {
		return ""
	}
	symbol := d.Flags
	switch strings.ToUpper(d.Flags) {
	case "EQ":
		symbol = "="
	case "GE":
		symbol = ">="
	case "LE":
		symbol = "<="
	case "GT":
		symbol = ">"
	case "LT":
		symbol = "<"
	}

	evr := d.Version
	if d.Epoch != "" && d.Epoch != "0" {
		evr = d.Epoch + ":" + evr
	}
	if d.Release != "" {
		evr = evr + "-" + d.Release
	}

	if symbol != "" && evr != "" {
		return symbol + " " + evr
	}
	if symbol != "" {
		return symbol
	}
	return evr
}

// PackageDependencies aggregates all dependency vectors for an RPM package.
type PackageDependencies struct {
	Requires  []*DependencyEntry `json:"requires,omitempty"`  // Mandatory prerequisite capabilities
	Provides  []*DependencyEntry `json:"provides,omitempty"`  // Capabilities offered by this package
	Conflicts []*DependencyEntry `json:"conflicts,omitempty"` // Incompatible conflicting packages
	Obsoletes []*DependencyEntry `json:"obsoletes,omitempty"` // Legacy packages replaced by this one
}

// HasAny returns true if at least one dependency relationship exists across all 4 vectors.
func (pd *PackageDependencies) HasAny() bool {
	return pd != nil && (len(pd.Requires) > 0 || len(pd.Provides) > 0 || len(pd.Conflicts) > 0 || len(pd.Obsoletes) > 0)
}

// RPMDetails stores deep inspection information extracted from the RPM package header.
type RPMDetails struct {
	BuildHost     string         `json:"build_host,omitempty"`     // Builder host FQDN
	SourceRPM     string         `json:"source_rpm,omitempty"`     // Source RPM archive name
	InstalledSize int64          `json:"installed_size,omitempty"` // Total uncompressed bytes on disk
	Signature     string         `json:"signature,omitempty"`      // GPG/PGP signature summary string
	KeyID         string         `json:"key_id,omitempty"`         // Hexadecimal GPG signing Key ID
	SigType       string         `json:"sig_type,omitempty"`       // Signature digest type (e.g. RSA/SHA256)
	SigDate       string         `json:"sig_date,omitempty"`       // Human-readable signing date
	Scriptlets    *RPMScriptlets `json:"scriptlets,omitempty"`     // Shell installation hooks
	Files         []RPMFile      `json:"files,omitempty"`          // Installed file manifest
}

// RPMScriptlets stores shell scripts executed during package installation and removal cycles.
type RPMScriptlets struct {
	PreIn      string `json:"prein,omitempty"`       // Pre-install script body
	PreInProg  string `json:"prein_prog,omitempty"`  // Pre-install interpreter (e.g. "/bin/sh")
	PostIn     string `json:"postin,omitempty"`      // Post-install script body
	PostInProg string `json:"postin_prog,omitempty"` // Post-install interpreter
	PreUn      string `json:"preun,omitempty"`       // Pre-uninstall script body
	PreUnProg  string `json:"preun_prog,omitempty"`  // Pre-uninstall interpreter
	PostUn     string `json:"postun,omitempty"`      // Post-uninstall script body
	PostUnProg string `json:"postun_prog,omitempty"` // Post-uninstall interpreter
}

// HasAny returns true if at least one installation or removal scriptlet body exists.
func (s *RPMScriptlets) HasAny() bool {
	return s != nil && (s.PreIn != "" || s.PostIn != "" || s.PreUn != "" || s.PostUn != "")
}

// RPMFile represents a single installed file and its metadata extracted from the RPM cpio header.
type RPMFile struct {
	Mode  string `json:"mode"`  // POSIX permissions (e.g. "-rwxr-xr-x")
	User  string `json:"user"`  // Owning user name (e.g. "root")
	Group string `json:"group"` // Owning group name (e.g. "root")
	Size  int64  `json:"size"`  // Uncompressed file size in bytes
	Name  string `json:"name"`  // Absolute destination filesystem path
}

// ChangelogEntry represents a row from the changelog table in other.sqlite.
type ChangelogEntry struct {
	Author    string `db:"author"`    // Maintainer name and email address
	Date      int64  `db:"date"`      // Unix epoch timestamp of the changelog note
	Changelog string `db:"changelog"` // Changelog entry description text
}

// EVR returns the Epoch-Version-Release tuple string in standard RPM notation.
// If epoch is empty, "0" is defaulted.
func (p *Package) EVR() string {
	e := p.Epoch
	if e == "" {
		e = "0"
	}
	return e + ":" + p.Version + "-" + p.Release
}

// Filename generates a safe, unique filename for the package's HTML page.
// It sanitizes the package name to ensure it is valid for the filesystem and URL friendly.
// Output format: "{sanitized_name}.html" (e.g. "openssl.html", "python-requests.html")
func (p *Package) Filename() string {
	// Logic from repoview.py: _mkid(PKGFILE % pkgname)
	return util.SanitizeFilename(p.Name) + ".html"
}

// RPMFilename returns the base filename of the RPM package, deriving it from
// LocationHref if available, or formatting standard N-V-R.A.rpm.
func (p *Package) RPMFilename() string {
	if p.LocationHref != "" {
		return filepath.Base(p.LocationHref)
	}
	return fmt.Sprintf("%s-%s-%s.%s.rpm", p.Name, p.Version, p.Release, p.Arch)
}
