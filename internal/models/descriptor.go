package models

import "time"

// RepoDescriptor represents the repoview.json metadata descriptor
// emitted by single-repository builds for fast portal catalog crawling.
type RepoDescriptor struct {
	Title           string    `json:"title"`
	Description     string    `json:"description,omitempty"`
	Format          string    `json:"format"` // "rpm" or "deb"
	Arch            string    `json:"arch"`
	Distro          string    `json:"distro"`
	Channel         string    `json:"channel"`
	Suite           string    `json:"suite,omitempty"`
	Components      []string  `json:"components,omitempty"`
	Revision        string    `json:"revision,omitempty"`
	PackageCount    int       `json:"package_count"`
	LastBuild       time.Time `json:"last_build"`
	BaseURL         string    `json:"base_url,omitempty"`
	PortalURL       string    `json:"portal_url,omitempty"`
	GPGKey          string    `json:"gpg_key,omitempty"`
	Badge           string    `json:"badge,omitempty"`
	Icon            string    `json:"icon,omitempty"`
	RepoviewVersion string    `json:"repoview_version"`
}
