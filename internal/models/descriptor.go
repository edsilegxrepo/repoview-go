package models

// OBJECTIVES:
// Define the self-describing repository metadata contract written as repoview.json
// alongside generated single-repository sites.
//
// CORE COMPONENTS:
//   - RepoDescriptor: JSON schema carrying identity, format, architecture,
//     distribution/channel taxonomy, package counts, URLs, and branding icons.
//
// FUNCTIONALITY:
//   - Enables the multi-repository portal crawler to read complete repository
//     metadata in sub-millisecond time without re-opening SQLite DBs or DEB indexes.
//
// DATA FLOW:
//   app.buildDescriptor -> repoview.json (disk) -> portal.Scanner.scanSingleRepo

import "time"

// --- Self-Describing Portal Contract ---

// RepoDescriptor represents the repoview.json metadata descriptor
// emitted by single-repository builds for fast portal catalog crawling.
type RepoDescriptor struct {
	Title           string    `json:"title"`                 // Display title for the repository view
	Description     string    `json:"description,omitempty"` // Optional repository subtitle
	Format          string    `json:"format"`                // Package format identifier ("rpm" or "deb")
	Arch            string    `json:"arch"`                  // Primary repository architecture (e.g. "x86_64", "amd64")
	Distro          string    `json:"distro"`                // Classified distribution family (e.g. "el9", "ubu24")
	Channel         string    `json:"channel"`               // Classified release channel (e.g. "base", "custom")
	Suite           string    `json:"suite,omitempty"`       // Target Debian suite codename (e.g. "noble")
	Components      []string  `json:"components,omitempty"`  // Discovered Debian components (e.g. "main", "universe")
	Revision        string    `json:"revision,omitempty"`    // Upstream repository revision string
	PackageCount    int       `json:"package_count"`         // Total unique package count indexed
	LastBuild       time.Time `json:"last_build"`            // UTC timestamp of the last generation pass
	BaseURL         string    `json:"base_url,omitempty"`    // Public mirror URL used in client configs
	PortalURL       string    `json:"portal_url,omitempty"`  // Relative or public link back to parent portal
	GPGKey          string    `json:"gpg_key,omitempty"`     // Optional GPG signing key URL/path
	Badge           string    `json:"badge,omitempty"`       // Optional portal UI badge text
	Icon            string    `json:"icon,omitempty"`        // Matched SVG icon key
	RepoviewVersion string    `json:"repoview_version"`      // Version string of repoview-go that built this output
}
