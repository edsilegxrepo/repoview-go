package deb

// OBJECTIVES:
// Extract and parse Debian binary package changelog entries from .deb/data.tar
// archives into format-agnostic ChangelogEntry records.
//
// CORE COMPONENTS:
//   - ParseChangelog: Decodes size-capped changelog.Debian text via pault.ag/go/debian/changelog.
//   - ExtractChangelogFromDeb: Streams data.tar looking for compressed or plain changelog docs.
//
// FUNCTIONALITY:
//   - Guards against oversized changelog streams via a 2 MiB LimitReader cap and
//     normalizes the newest entry author, Unix timestamp, and description body.
//
// DATA FLOW:
//   .deb path -> data.tar stream -> gzip (optional) -> ParseChangelog -> *models.ChangelogEntry

import (
	"compress/gzip"
	"io"
	"path"
	"strings"

	"github.com/edsilegxrepo/repoview/internal/models"
	debchangelog "pault.ag/go/debian/changelog"
	pdeb "pault.ag/go/debian/deb"
)

// --- Debian Changelog Stream Decoders ---

const maxChangelogBytes = 2 * 1024 * 1024 // 2 MiB ceiling for changelog files

// ParseChangelog parses Debian changelog formatted text from an io.Reader.
func ParseChangelog(r io.Reader) (*models.ChangelogEntry, error) {
	entries, err := debchangelog.Parse(io.LimitReader(r, maxChangelogBytes))
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	first := entries[0]
	return &models.ChangelogEntry{
		Author:    first.ChangedBy,
		Date:      first.When.Unix(),
		Changelog: first.Changelog,
	}, nil
}

// ExtractChangelogFromDeb inspects data.tar for /usr/share/doc/<pkg>/changelog.Debian.gz
// or changelog.gz and parses the latest changelog entry.
func ExtractChangelogFromDeb(debPath string) (*models.ChangelogEntry, error) {
	debFile, closer, err := pdeb.LoadFile(debPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = closer()
	}()

	for {
		header, err := debFile.Data.Next()
		if err != nil {
			break
		}
		base := path.Base(header.Name)
		if strings.HasPrefix(base, "changelog.Debian") || base == "changelog.gz" || base == "changelog" {
			var reader io.Reader = debFile.Data
			if strings.HasSuffix(base, ".gz") {
				gzr, err := gzip.NewReader(debFile.Data)
				if err != nil {
					continue
				}
				entry, parseErr := ParseChangelog(gzr)
				_ = gzr.Close()
				return entry, parseErr
			}
			return ParseChangelog(reader)
		}
	}
	return nil, nil
}
