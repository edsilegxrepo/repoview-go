// Package repo provides filesystem and database access layers for parsing RPM repository
// metadata formats including repomd.xml, SQLite databases, comps.xml, and RPM package binaries.
//
// OBJECTIVES:
// Discover, decompress, and index all repository metadata files, ensuring strict security
// validations against path traversal and decompression bomb attacks.
//
// CORE COMPONENTS:
//   - RepoLocations: Resolved absolute filesystem paths to primary.sqlite, other.sqlite, and comps.xml.
//   - ParseRepomd: Parser for repodata/repomd.xml that resolves database locations and validates integrity.
//
// FUNCTIONALITY:
//   - Reads and decodes repodata/repomd.xml.
//   - Detects primary_db, other_db, and group metadata records.
//   - Prevents directory traversal attacks by validating that metadata hrefs cannot escape the repository root.
//   - Verifies that SQLite database versions do not exceed supported limits.
//
// DATA FLOW:
//
//	repodir/repodata/repomd.xml -> ParseRepomd() -> RepoLocations -> SQLite & Comps loaders
package repo

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edsilegxrepo/repoview/internal/models"
)

// RepoLocations holds the absolute paths to the critical database files
// extracted from repomd.xml. These files (Primary, Other, Groups) are
// essential for generating the repository view.
type RepoLocations struct {
	Primary string // Path to primary.sqlite (package metadata)
	Other   string // Path to other.sqlite (changelogs)
	Groups  string // Path to comps.xml (group definitions)
	BaseDir string // Root directory of the repository
}

// ParseRepomd reads the repomd.xml file and resolves the paths to the databases.
// repomd.xml is the entry point for YUM/DNF repositories, listing the locations
// and checksums of the metadata files.
func ParseRepomd(repodir string) (*RepoLocations, error) {
	repomdPath := filepath.Join(repodir, "repodata", "repomd.xml")
	// #nosec G304 -- repomdPath is formed by joining the validated repository directory with static 'repodata/repomd.xml'
	f, err := os.Open(filepath.Clean(repomdPath))
	if err != nil {
		return nil, fmt.Errorf("failed to open repomd.xml: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	var repomd models.Repomd
	if err := xml.NewDecoder(f).Decode(&repomd); err != nil {
		return nil, fmt.Errorf("failed to parse repomd.xml: %w", err)
	}

	locs := &RepoLocations{BaseDir: repodir}

	for _, data := range repomd.Data {
		if data.Location.Href == "" {
			continue
		}
		// The href in repomd.xml is usually relative to the repo root, e.g. "repodata/primary.sqlite.bz2"
		fullPath := filepath.Join(repodir, data.Location.Href)

		// Security: Prevent path traversal
		rel, err := filepath.Rel(repodir, fullPath)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve path %s: %w", data.Location.Href, err)
		}
		if strings.HasPrefix(rel, "..") || strings.HasPrefix(rel, "/") {
			return nil, fmt.Errorf("potential path traversal detected in repomd.xml: %s", data.Location.Href)
		}

		switch data.Type {
		case "primary_db":
			locs.Primary = fullPath
			if data.DatabaseVersion != "" {
				var dbVer int
				if _, err := fmt.Sscanf(data.DatabaseVersion, "%d", &dbVer); err == nil && dbVer > 10 {
					return nil, fmt.Errorf("the db_version in the repository is %d, but repoview only supports versions up to 10", dbVer)
				}
			}
		case "other_db":
			locs.Other = fullPath
		case "group", "group_gz":
			locs.Groups = fullPath
		}
	}

	if locs.Primary == "" {
		return nil, fmt.Errorf("sqlite files not found in repository: primary_db missing in repomd.xml (rerun createrepo with -d)")
	}
	if locs.Other == "" {
		return nil, fmt.Errorf("sqlite files not found in repository: other_db missing in repomd.xml (rerun createrepo with -d)")
	}

	return locs, nil
}
