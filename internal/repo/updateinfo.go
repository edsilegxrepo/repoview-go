package repo

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/edsilegxrepo/repoview/internal/models"
)

// UpdateInfoRecord pairs an errata Advisory with the RPM package NVRAs it affects.
type UpdateInfoRecord struct {
	Advisory models.Advisory
	NVRAs    []string // Format: "<name>|<epoch>|<version>|<release>|<arch>" or "<name>-<version>-<release>.<arch>"
}

type xmlUpdates struct {
	Updates []xmlUpdate `xml:"update"`
}

type xmlUpdate struct {
	Type       string         `xml:"type,attr"`
	ID         string         `xml:"id"`
	Title      string         `xml:"title"`
	Severity   string         `xml:"severity"`
	Issued     xmlIssued      `xml:"issued"`
	References []xmlReference `xml:"references>reference"`
	Packages   []xmlUpdatePkg `xml:"pkglist>collection>package"`
}

type xmlIssued struct {
	Date string `xml:"date,attr"`
}

type xmlReference struct {
	Type string `xml:"type,attr"`
	ID   string `xml:"id,attr"`
	Href string `xml:"href,attr"`
}

type xmlUpdatePkg struct {
	Name    string `xml:"name,attr"`
	Epoch   string `xml:"epoch,attr"`
	Version string `xml:"version,attr"`
	Release string `xml:"release,attr"`
	Arch    string `xml:"arch,attr"`
}

// ParseUpdateInfo decodes an uncompressed updateinfo XML document into UpdateInfoRecords.
func ParseUpdateInfo(updateInfoPath string) ([]*UpdateInfoRecord, error) {
	// #nosec G304 -- updateInfoPath is validated against directory traversal by ParseRepomd
	f, err := os.Open(filepath.Clean(updateInfoPath))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to open updateinfo.xml: %w", models.ErrRepoMetadata, err)
	}
	defer func() { _ = f.Close() }()

	const maxUpdateInfoBytes = 128 * 1024 * 1024
	var parsed xmlUpdates
	dec := xml.NewDecoder(io.LimitReader(f, maxUpdateInfoBytes))
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("%w: failed to decode updateinfo.xml: %w", models.ErrRepoMetadata, err)
	}

	records := make([]*UpdateInfoRecord, 0, len(parsed.Updates))
	for _, u := range parsed.Updates {
		var cves []string
		seenCVE := make(map[string]bool)
		for _, ref := range u.References {
			if strings.EqualFold(ref.Type, "cve") && ref.ID != "" {
				if !seenCVE[ref.ID] {
					seenCVE[ref.ID] = true
					cves = append(cves, ref.ID)
				}
			}
		}

		var nvras []string
		for _, p := range u.Packages {
			key := advisoryPkgKey(p.Name, p.Version, p.Release, p.Arch)
			nvras = append(nvras, key)
		}

		records = append(records, &UpdateInfoRecord{
			Advisory: models.Advisory{
				ID:       strings.TrimSpace(u.ID),
				Type:     strings.TrimSpace(u.Type),
				Severity: strings.TrimSpace(u.Severity),
				Title:    strings.TrimSpace(u.Title),
				Issued:   strings.TrimSpace(u.Issued.Date),
				CVEs:     cves,
			},
			NVRAs: nvras,
		})
	}
	return records, nil
}

// AttachAdvisories links parsed UpdateInfoRecords onto matching packages by NVRA.
func AttachAdvisories(pkgs []*models.Package, records []*UpdateInfoRecord) {
	if len(pkgs) == 0 || len(records) == 0 {
		return
	}

	byNVRA := make(map[string][]*models.Package, len(pkgs))
	for _, p := range pkgs {
		if p == nil {
			continue
		}
		key := advisoryPkgKey(p.Name, p.Version, p.Release, p.Arch)
		byNVRA[key] = append(byNVRA[key], p)
	}

	for _, rec := range records {
		if rec == nil {
			continue
		}
		seenPkg := make(map[*models.Package]bool)
		for _, key := range rec.NVRAs {
			for _, p := range byNVRA[key] {
				if seenPkg[p] {
					continue
				}
				seenPkg[p] = true
				p.Advisories = append(p.Advisories, rec.Advisory)
			}
		}
	}
}

func advisoryPkgKey(name, version, release, arch string) string {
	return name + "\x00" + version + "\x00" + release + "\x00" + arch
}
