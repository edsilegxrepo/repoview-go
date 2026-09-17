package deb

import (
	"compress/gzip"
	"io"
	"path"
	"strings"

	"github.com/edsilegxrepo/repoview/internal/models"
	debchangelog "pault.ag/go/debian/changelog"
	pdeb "pault.ag/go/debian/deb"
)

// ParseChangelog parses Debian changelog formatted text from an io.Reader.
func ParseChangelog(r io.Reader) (*models.ChangelogEntry, error) {
	entries, err := debchangelog.Parse(r)
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
				defer func() {
					_ = gzr.Close()
				}()
				reader = gzr
			}
			return ParseChangelog(reader)
		}
	}
	return nil, nil
}
