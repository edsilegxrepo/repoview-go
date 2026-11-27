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
	"crypto/sha1" // #nosec G505 -- legacy repomd.xml files may use SHA1 digests
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/util"
)

// RepoLocations holds the absolute paths to the critical metadata and database files
// extracted from repomd.xml.
type RepoLocations struct {
	Primary    string // Path to primary.sqlite (package metadata)
	Other      string // Path to other.sqlite (changelogs)
	PrimaryXML string // Path to primary.xml* fallback when SQLite DB is absent
	OtherXML   string // Path to other.xml* fallback when SQLite DB is absent
	Groups     string // Path to comps.xml (group definitions)
	UpdateInfo string // Path to updateinfo.xml* (security/bugfix advisories)
	Revision   string // Repository revision string from repomd.xml
	BaseDir    string // Root directory of the repository
}

// ParseRepomd reads the repomd.xml file and resolves the paths to the databases.
// repomd.xml is the entry point for YUM/DNF repositories, listing the locations
// and checksums of the metadata files.
func ParseRepomd(repodir string) (*RepoLocations, error) {
	repomdPath := filepath.Join(repodir, "repodata", "repomd.xml")
	// #nosec G304 -- repomdPath is formed by joining the validated repository directory with static 'repodata/repomd.xml'
	f, err := os.Open(filepath.Clean(repomdPath))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to open repomd.xml: %w", models.ErrRepoMetadata, err)
	}
	defer func() {
		_ = f.Close()
	}()

	var repomd models.Repomd
	if err := xml.NewDecoder(f).Decode(&repomd); err != nil {
		return nil, fmt.Errorf("%w: failed to parse repomd.xml: %w", models.ErrRepoMetadata, err)
	}

	locs := &RepoLocations{
		BaseDir:  repodir,
		Revision: strings.TrimSpace(repomd.Revision),
	}

	for _, data := range repomd.Data {
		if data.Location.Href == "" {
			continue
		}
		// Security: Prevent path traversal and symlink escapes
		fullPath, err := util.ResolveContainedPath(repodir, data.Location.Href)
		if err != nil {
			return nil, fmt.Errorf("%w: potential path traversal detected in repomd.xml (%s): %w", models.ErrIntegrity, data.Location.Href, err)
		}

		switch data.Type {
		case "primary_db":
			if err := verifyRepomdDataIntegrity(fullPath, data); err != nil {
				return nil, err
			}
			locs.Primary = fullPath
			if data.DatabaseVersion != "" {
				var dbVer int
				if _, err := fmt.Sscanf(data.DatabaseVersion, "%d", &dbVer); err == nil && dbVer > 10 {
					return nil, fmt.Errorf("%w: the db_version in the repository is %d, but repoview only supports versions up to 10", models.ErrRepoMetadata, dbVer)
				}
			}
		case "other_db":
			if err := verifyRepomdDataIntegrity(fullPath, data); err != nil {
				return nil, err
			}
			locs.Other = fullPath
		case "primary":
			if err := verifyRepomdDataIntegrity(fullPath, data); err != nil {
				return nil, err
			}
			locs.PrimaryXML = fullPath
		case "other":
			if err := verifyRepomdDataIntegrity(fullPath, data); err != nil {
				return nil, err
			}
			locs.OtherXML = fullPath
		case "group", "group_gz":
			if err := verifyRepomdDataIntegrity(fullPath, data); err != nil {
				return nil, err
			}
			locs.Groups = fullPath
		case "updateinfo":
			if err := verifyRepomdDataIntegrity(fullPath, data); err != nil {
				return nil, err
			}
			locs.UpdateInfo = fullPath
		}
	}

	if locs.Primary == "" && locs.PrimaryXML == "" {
		return nil, fmt.Errorf("%w: primary metadata not found in repository: neither primary_db nor primary exists in repomd.xml", models.ErrRepoMetadata)
	}
	if locs.Other == "" && locs.OtherXML == "" && locs.PrimaryXML == "" {
		return nil, fmt.Errorf("%w: other metadata not found in repository: neither other_db nor other exists in repomd.xml", models.ErrRepoMetadata)
	}

	return locs, nil
}

// verifyRepomdDataIntegrity verifies archive size and cryptographic checksum when declared in repomd.xml.
func verifyRepomdDataIntegrity(fullPath string, data models.Data) error {
	expectedSum := strings.TrimSpace(data.Checksum.Value)
	if expectedSum == "" && data.Size <= 0 {
		return nil
	}

	// #nosec G304 -- fullPath is validated by util.ResolveContainedPath inside ParseRepomd
	f, err := os.Open(filepath.Clean(fullPath))
	if err != nil {
		return fmt.Errorf("%w: failed to open metadata file %s for integrity check: %w", models.ErrRepoMetadata, data.Location.Href, err)
	}
	defer func() { _ = f.Close() }()

	if data.Size > 0 {
		fi, err := f.Stat()
		if err != nil {
			return fmt.Errorf("%w: failed to stat metadata file %s: %w", models.ErrIO, data.Location.Href, err)
		}
		if fi.Size() != data.Size {
			return fmt.Errorf("%w: integrity check failed for %s: expected size %d bytes, got %d bytes", models.ErrIntegrity, data.Location.Href, data.Size, fi.Size())
		}
	}

	if expectedSum == "" {
		return nil
	}

	hashType := strings.ToLower(strings.TrimSpace(data.Checksum.Type))
	var h hash.Hash
	switch hashType {
	case "sha256", "":
		h = sha256.New()
	case "sha512":
		h = sha512.New()
	case "sha1":
		// #nosec G401 -- legacy RPM repositories may declare SHA1 checksums in repomd.xml
		h = sha1.New()
	default:
		return fmt.Errorf("%w: unsupported checksum algorithm %q for %s", models.ErrRepoMetadata, data.Checksum.Type, data.Location.Href)
	}

	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("%w: failed to hash metadata file %s: %w", models.ErrIO, data.Location.Href, err)
	}
	actualSum := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(actualSum, expectedSum) {
		return fmt.Errorf("%w: checksum mismatch for %s (%s): expected %s, got %s", models.ErrIntegrity, data.Location.Href, hashType, expectedSum, actualSum)
	}
	return nil
}
