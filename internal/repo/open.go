package repo

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/repo/deb"
)

// OpenOptions customizes backend discovery and initialization in repo.OpenWithOptions.
type OpenOptions struct {
	Format models.RepoFormat // "auto" / "" triggers automatic RPM vs DEB detection
	Suite  string            // Optional Debian suite/codename selector
	Arch   string            // Optional Debian architecture selector
	Warn   io.Writer         // Optional warning output writer for SQLite/metadata diagnostics
}

// RepoMetadata captures repository-level metadata uncovered during repo.Open.
type RepoMetadata struct {
	Format       models.RepoFormat
	Revision     string
	Suite        string
	Components   []string
	Description  string
	Locations    *RepoLocations
	DebLocations *deb.DebRepoLocations
}

// DetectFormat sniffs the repository format ("rpm" or "deb") from directory markers.
func DetectFormat(repoDir string) models.RepoFormat {
	if _, err := os.Stat(filepath.Join(repoDir, "repodata", "repomd.xml")); err == nil {
		return models.FormatRPM
	}
	if _, err := os.Stat(filepath.Join(repoDir, "dists")); err == nil {
		return models.FormatDEB
	}
	if matches, _ := filepath.Glob(filepath.Join(repoDir, "Packages*")); len(matches) > 0 {
		return models.FormatDEB
	}
	if strings.HasPrefix(filepath.Base(repoDir), "binary-") {
		return models.FormatDEB
	}
	return models.FormatRPM
}

// Open discovers and initializes an RPM or DEB RepoReader with default options.
func Open(repoDir string, format models.RepoFormat) (RepoReader, *RepoMetadata, func(), error) {
	return OpenWithOptions(repoDir, OpenOptions{Format: format})
}

// OpenWithOptions discovers and initializes an RPM (SQLite or XML fallback) or DEB RepoReader.
func OpenWithOptions(repoDir string, opts OpenOptions) (RepoReader, *RepoMetadata, func(), error) {
	format := opts.Format
	if format == "" || format == "auto" {
		format = DetectFormat(repoDir)
	}

	if format == models.FormatDEB {
		debLocs, err := deb.DiscoverWithOptions(repoDir, deb.DiscoverOptions{
			Suite: opts.Suite,
			Arch:  opts.Arch,
		})
		if err != nil {
			return nil, nil, func() {}, fmt.Errorf("%w: failed to discover Debian repository: %w", models.ErrRepoMetadata, err)
		}
		reader := deb.NewDebRepository(debLocs, nil)
		reader.SetRepoDir(repoDir)

		desc := ""
		if debLocs.ReleaseInfo != nil {
			desc = strings.TrimSpace(debLocs.ReleaseInfo.Description)
		}
		meta := &RepoMetadata{
			Format:       models.FormatDEB,
			Revision:     debLocs.Suite,
			Suite:        debLocs.Suite,
			Components:   debLocs.Components,
			Description:  desc,
			DebLocations: debLocs,
		}
		if opts.Arch != "" {
			meta.Revision = debLocs.Suite
		}
		return reader, meta, func() { _ = reader.Close() }, nil
	}

	locs, err := ParseRepomd(repoDir)
	if err != nil {
		return nil, nil, func() {}, fmt.Errorf("failed to parse repomd.xml: %w", err)
	}

	meta := &RepoMetadata{
		Format:    models.FormatRPM,
		Revision:  locs.Revision,
		Locations: locs,
	}

	// Prefer SQLite databases when both primary_db and other_db are available; fall back cleanly to XML.
	if locs.Primary != "" && locs.Other != "" {
		var (
			primaryPath, otherPath string
			cleanupP, cleanupO     func()
			errP, errO             error
			wg                     sync.WaitGroup
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			primaryPath, cleanupP, errP = DecompressFile(locs.Primary)
		}()
		go func() {
			defer wg.Done()
			otherPath, cleanupO, errO = DecompressFile(locs.Other)
		}()
		wg.Wait()

		if errP != nil {
			if cleanupO != nil {
				cleanupO()
			}
			return nil, nil, func() {}, fmt.Errorf("failed to decompress primary db: %w", errP)
		}
		if errO != nil {
			if cleanupP != nil {
				cleanupP()
			}
			return nil, nil, func() {}, fmt.Errorf("failed to decompress other db: %w", errO)
		}

		db, err := NewRepositoryAccess(primaryPath, otherPath)
		if err != nil {
			cleanupP()
			cleanupO()
			return nil, nil, func() {}, fmt.Errorf("%w: failed to open databases: %w", models.ErrRepoMetadata, err)
		}
		db.SetRepoDir(repoDir)
		db.SetUpdateInfoPath(locs.UpdateInfo)
		if opts.Warn != nil {
			db.SetWarningWriter(opts.Warn)
		}
		cleanup := func() {
			_ = db.Close()
			cleanupO()
			cleanupP()
		}
		return db, meta, cleanup, nil
	}

	// XML-only fallback when createrepo was run without -d (SQLite DBs omitted)
	var (
		primaryXML, otherXML string
		cleanupP, cleanupO   func()
		errP, errO           error
		wg                   sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		primaryXML, cleanupP, errP = DecompressFile(locs.PrimaryXML)
	}()
	if locs.OtherXML != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			otherXML, cleanupO, errO = DecompressFile(locs.OtherXML)
		}()
	} else {
		cleanupO = func() {}
	}
	wg.Wait()

	if errP != nil {
		cleanupO()
		return nil, nil, func() {}, fmt.Errorf("failed to decompress primary.xml: %w", errP)
	}
	if errO != nil {
		cleanupP()
		return nil, nil, func() {}, fmt.Errorf("failed to decompress other.xml: %w", errO)
	}

	cleanup := func() {
		cleanupO()
		cleanupP()
	}
	xmlReader := NewXMLRepository(repoDir, primaryXML, otherXML, locs.UpdateInfo, cleanup)
	return xmlReader, meta, func() { _ = xmlReader.Close() }, nil
}

var _ RepoReader = (*deb.DebRepository)(nil)
