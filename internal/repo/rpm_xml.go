package repo

// OBJECTIVES:
// Support pure-XML RPM repositories generated without SQLite databases
// (`createrepo --no-database`) via streaming decoding of primary.xml and other.xml.
//
// CORE COMPONENTS:
//   - XMLRepository: RepoReader implementation holding paths to decompressed XML scratch files
//     and an in-memory dependency lookup map indexed by synthetic PkgKey.
//   - GetAllPackages / EnrichPackagesWithChangelogs / EnrichPackagesWithAdvisories: Streaming parsers.
//   - EnrichForRender / ReadPackageFiles: Single-pass or on-demand RPM header inspection.
//
// FUNCTIONALITY:
//   - Enforces MaxDecompressedSize via io.LimitReader during XML decoding.
//   - Caps loaded changelogs at the 10 newest entries per package ordered newest-first.
//
// DATA FLOW:
//   primary.xml + other.xml + updateinfo.xml* -> XMLRepository -> []*models.Package -> render

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/util"
)

// XMLRepository provides a fallback RepoReader backed by repodata/primary.xml* and other.xml*
// when a repository was created without SQLite databases (`createrepo --no-database`).
type XMLRepository struct {
	repoDir    string
	primaryXML string
	otherXML   string
	updateInfo string
	cleanup    func()
	depsByKey  map[int64]*models.PackageDependencies
}

// NewXMLRepository initializes a fallback XML RPM reader.
func NewXMLRepository(repoDir, primaryXMLPath, otherXMLPath, updateInfoPath string, cleanup func()) *XMLRepository {
	return &XMLRepository{
		repoDir:    repoDir,
		primaryXML: primaryXMLPath,
		otherXML:   otherXMLPath,
		updateInfo: updateInfoPath,
		cleanup:    cleanup,
		depsByKey:  make(map[int64]*models.PackageDependencies),
	}
}

type xmlPrimaryMetadata struct {
	Packages []xmlPrimaryPkg `xml:"package"`
}

type xmlPrimaryPkg struct {
	Type        string           `xml:"type,attr"`
	Name        string           `xml:"name"`
	Arch        string           `xml:"arch"`
	Version     xmlPrimaryVer    `xml:"version"`
	Checksum    xmlPrimaryCheck  `xml:"checksum"`
	Summary     string           `xml:"summary"`
	Description string           `xml:"description"`
	Packager    string           `xml:"packager"`
	URL         string           `xml:"url"`
	Time        xmlPrimaryTime   `xml:"time"`
	Size        xmlPrimarySize   `xml:"size"`
	Location    xmlPrimaryLoc    `xml:"location"`
	Format      xmlPrimaryFormat `xml:"format"`
}

type xmlPrimaryVer struct {
	Epoch string `xml:"epoch,attr"`
	Ver   string `xml:"ver,attr"`
	Rel   string `xml:"rel,attr"`
}

type xmlPrimaryCheck struct {
	Type  string `xml:"type,attr"`
	PkgID string `xml:"pkgid,attr"`
	Value string `xml:",chardata"`
}

type xmlPrimaryTime struct {
	File  int64 `xml:"file,attr"`
	Build int64 `xml:"build,attr"`
}

type xmlPrimarySize struct {
	Package   int64 `xml:"package,attr"`
	Installed int64 `xml:"installed,attr"`
	Archive   int64 `xml:"archive,attr"`
}

type xmlPrimaryLoc struct {
	Href string `xml:"href,attr"`
}

type xmlPrimaryFormat struct {
	License     string            `xml:"license"`
	Vendor      string            `xml:"vendor"`
	Group       string            `xml:"group"`
	BuildHost   string            `xml:"buildhost"`
	SourceRPM   string            `xml:"sourcerpm"`
	Requires    xmlPrimaryEntries `xml:"requires"`
	Provides    xmlPrimaryEntries `xml:"provides"`
	Conflicts   xmlPrimaryEntries `xml:"conflicts"`
	Obsoletes   xmlPrimaryEntries `xml:"obsoletes"`
	Recommends  xmlPrimaryEntries `xml:"recommends"`
	Suggests    xmlPrimaryEntries `xml:"suggests"`
	Enhances    xmlPrimaryEntries `xml:"enhances"`
	Supplements xmlPrimaryEntries `xml:"supplements"`
}

type xmlPrimaryEntries struct {
	Entries []xmlPrimaryDep `xml:"entry"`
}

type xmlPrimaryDep struct {
	Name  string `xml:"name,attr"`
	Flags string `xml:"flags,attr"`
	Epoch string `xml:"epoch,attr"`
	Ver   string `xml:"ver,attr"`
	Rel   string `xml:"rel,attr"`
	Pre   string `xml:"pre,attr"`
}

// GetAllPackages parses primary.xml into models.Package records and stores dependencies by PkgKey.
func (r *XMLRepository) GetAllPackages() ([]*models.Package, error) {
	if r.primaryXML == "" {
		return nil, fmt.Errorf("%w: no primary.xml metadata available", models.ErrRepoMetadata)
	}
	// #nosec G304 -- primaryXML path validated against directory traversal by ParseRepomd
	f, err := os.Open(filepath.Clean(r.primaryXML))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to open primary.xml: %w", models.ErrRepoMetadata, err)
	}
	defer func() { _ = f.Close() }()

	var meta xmlPrimaryMetadata
	dec := xml.NewDecoder(io.LimitReader(f, MaxDecompressedSize))
	if err := dec.Decode(&meta); err != nil {
		return nil, fmt.Errorf("%w: failed to decode primary.xml: %w", models.ErrRepoMetadata, err)
	}

	pkgs := make([]*models.Package, 0, len(meta.Packages))
	for i, xp := range meta.Packages {
		pkgKey := int64(i + 1)
		checksumVal := strings.TrimSpace(xp.Checksum.Value)
		checksumType := strings.ToLower(strings.TrimSpace(xp.Checksum.Type))
		sha256Val := ""
		if checksumType == "sha256" || len(checksumVal) == 64 {
			sha256Val = checksumVal
		}
		epoch := xp.Version.Epoch
		if epoch == "" {
			epoch = "0"
		}

		deps := &models.PackageDependencies{
			Requires:    mapXMLDeps(xp.Format.Requires.Entries),
			Provides:    mapXMLDeps(xp.Format.Provides.Entries),
			Conflicts:   mapXMLDeps(xp.Format.Conflicts.Entries),
			Obsoletes:   mapXMLDeps(xp.Format.Obsoletes.Entries),
			Recommends:  mapXMLDeps(xp.Format.Recommends.Entries),
			Suggests:    mapXMLDeps(xp.Format.Suggests.Entries),
			Enhances:    mapXMLDeps(xp.Format.Enhances.Entries),
			Supplements: mapXMLDeps(xp.Format.Supplements.Entries),
		}
		r.depsByKey[pkgKey] = deps

		p := &models.Package{
			PkgKey:        pkgKey,
			Name:          strings.TrimSpace(xp.Name),
			Epoch:         epoch,
			Version:       strings.TrimSpace(xp.Version.Ver),
			Release:       strings.TrimSpace(xp.Version.Rel),
			Arch:          strings.TrimSpace(xp.Arch),
			Summary:       strings.TrimSpace(xp.Summary),
			Description:   strings.TrimSpace(xp.Description),
			URL:           strings.TrimSpace(xp.URL),
			TimeBuild:     xp.Time.Build,
			TimeFile:      xp.Time.File,
			License:       strings.TrimSpace(xp.Format.License),
			SourceRPM:     strings.TrimSpace(xp.Format.SourceRPM),
			SourcePackage: strings.TrimSpace(xp.Format.SourceRPM),
			SizePackage:   xp.Size.Package,
			InstalledSize: xp.Size.Installed,
			SizeArchive:   xp.Size.Archive,
			LocationHref:  strings.TrimSpace(xp.Location.Href),
			Vendor:        strings.TrimSpace(xp.Format.Vendor),
			RpmGroup:      strings.TrimSpace(xp.Format.Group),
			BuildHost:     strings.TrimSpace(xp.Format.BuildHost),
			Packager:      strings.TrimSpace(xp.Packager),
			SHA256:        sha256Val,
			ChecksumType:  checksumType,
			Format:        models.FormatRPM,
			Dependencies:  deps,
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

func mapXMLDeps(entries []xmlPrimaryDep) []*models.DependencyEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]*models.DependencyEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, &models.DependencyEntry{
			Name:    e.Name,
			Flags:   e.Flags,
			Epoch:   e.Epoch,
			Version: e.Ver,
			Release: e.Rel,
			Pre:     e.Pre == "1" || strings.EqualFold(e.Pre, "true"),
		})
	}
	return out
}

type xmlOtherData struct {
	Packages []xmlOtherPkg `xml:"package"`
}

type xmlOtherPkg struct {
	PkgID      string              `xml:"pkgid,attr"`
	Name       string              `xml:"name,attr"`
	Arch       string              `xml:"arch,attr"`
	Version    xmlPrimaryVer       `xml:"version"`
	Changelogs []xmlOtherChangelog `xml:"changelog"`
}

type xmlOtherChangelog struct {
	Author string `xml:"author,attr"`
	Date   int64  `xml:"date,attr"`
	Text   string `xml:",chardata"`
}

// EnrichPackagesWithChangelogs loads changelogs from other.xml when available.
func (r *XMLRepository) EnrichPackagesWithChangelogs(pkgs []*models.Package) error {
	if r.otherXML == "" || len(pkgs) == 0 {
		return nil
	}
	// #nosec G304 -- otherXML path validated against directory traversal by ParseRepomd
	f, err := os.Open(filepath.Clean(r.otherXML))
	if err != nil {
		return fmt.Errorf("%w: failed to open other.xml: %w", models.ErrRepoMetadata, err)
	}
	defer func() { _ = f.Close() }()

	var meta xmlOtherData
	if err := xml.NewDecoder(io.LimitReader(f, MaxDecompressedSize)).Decode(&meta); err != nil {
		return fmt.Errorf("%w: failed to decode other.xml: %w", models.ErrRepoMetadata, err)
	}

	byNVRA := make(map[string][]*models.Package, len(pkgs))
	for _, p := range pkgs {
		k := advisoryPkgKey(p.Name, p.Version, p.Release, p.Arch)
		byNVRA[k] = append(byNVRA[k], p)
	}

	for _, op := range meta.Packages {
		if len(op.Changelogs) == 0 {
			continue
		}
		k := advisoryPkgKey(op.Name, op.Version.Ver, op.Version.Rel, op.Arch)
		targets := byNVRA[k]
		if len(targets) == 0 {
			continue
		}

		entries := make([]*models.ChangelogEntry, 0, len(op.Changelogs))
		for _, c := range op.Changelogs {
			entries = append(entries, &models.ChangelogEntry{
				Author:    cleanAuthor(c.Author),
				Date:      c.Date,
				Changelog: strings.TrimSpace(c.Text),
			})
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Date > entries[j].Date
		})
		if len(entries) > 10 {
			entries = entries[:10]
		}
		for _, p := range targets {
			p.Changelogs = entries
			p.Changelog = entries[0]
		}
	}
	return nil
}

// EnrichPackagesWithAdvisories populates errata advisories from updateinfo.xml when present.
func (r *XMLRepository) EnrichPackagesWithAdvisories(pkgs []*models.Package) error {
	if r.updateInfo == "" || len(pkgs) == 0 {
		return nil
	}
	path, cleanup, err := DecompressFile(r.updateInfo)
	if err != nil {
		return err
	}
	defer cleanup()
	recs, err := ParseUpdateInfo(path)
	if err != nil {
		return err
	}
	AttachAdvisories(pkgs, recs)
	return nil
}

// GetPackageDependencies returns dependencies parsed from primary.xml for pkgKey.
func (r *XMLRepository) GetPackageDependencies(pkgKey int64) (*models.PackageDependencies, error) {
	return r.depsByKey[pkgKey], nil
}

// EnrichPackageDetails inspects local .rpm binaries for scriptlets and digital signatures.
func (r *XMLRepository) EnrichPackageDetails(pkgs []*models.Package) {
	EnrichPackagesWithRPMDetails(r.repoDir, pkgs)
}

// ReadPackageFiles extracts on-demand file manifests from the .rpm binary header.
func (r *XMLRepository) ReadPackageFiles(repoDir string, pkg *models.Package) ([]models.PackageFile, error) {
	if pkg.LocationHref == "" {
		return nil, nil
	}
	rpmPath, err := util.ResolveContainedPath(repoDir, pkg.LocationHref)
	if err != nil {
		return nil, fmt.Errorf("invalid package location %s: %w", pkg.LocationHref, err)
	}
	return ReadRPMFiles(rpmPath)
}

// EnrichForRender loads RPM header details and file manifests in a single file open pass.
func (r *XMLRepository) EnrichForRender(repoDir string, pkg *models.Package) error {
	return EnrichRPMForRender(repoDir, pkg)
}

// Close releases temporary decompressed XML files.
func (r *XMLRepository) Close() error {
	if r.cleanup != nil {
		r.cleanup()
	}
	return nil
}

var (
	_ RepoReader            = (*XMLRepository)(nil)
	_ ChangelogEnricher     = (*XMLRepository)(nil)
	_ DependencyReader      = (*XMLRepository)(nil)
	_ AdvisoryEnricher      = (*XMLRepository)(nil)
	_ PackageRenderEnricher = (*XMLRepository)(nil)
)
