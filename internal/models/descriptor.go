package models

import "time"

// RepoDescriptor represents the 1KB repoview.json metadata descriptor
// emitted by single-repository builds for fast portal catalog crawling.
type RepoDescriptor struct {
	Title           string    `json:"title"`
	Format          string    `json:"format"` // "rpm" or "deb"
	Arch            string    `json:"arch"`
	Distro          string    `json:"distro"`
	Channel         string    `json:"channel"`
	PackageCount    int       `json:"package_count"`
	LastBuild       time.Time `json:"last_build"`
	BaseURL         string    `json:"base_url,omitempty"`
	PortalURL       string    `json:"portal_url,omitempty"`
	RepoviewVersion string    `json:"repoview_version"`
}
