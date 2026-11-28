// Package logic implements core business rules for repoview-go, including RPM version
// comparison, package filtering, and hierarchical group taxonomy construction.
//
// OBJECTIVES:
// Provide strict RPM Epoch-Version-Release (EVR) comparison semantics matching native rpmvercmp,
// ensuring the newest version of each package is accurately identified across all architectures.
//
// CORE COMPONENTS:
//   - SortPackagesByEVR: Sorts package slices descending by EVR and ascending by architecture.
//   - CompareEVR: Three-stage EVR comparator (Epoch -> Version -> Release).
//   - parseEpoch: Fast, reflection-free string-to-integer conversion for RPM epochs.
//
// FUNCTIONALITY:
//   - Follows RPM specification: Epoch supercedes Version, Version supercedes Release.
//   - Ties are broken by alphabetical architecture ordering (matching legacy repoview ORDER BY arch ASC).
//   - Utilizes strconv.Atoi for sub-microsecond epoch parsing without reflection or fmt overhead.
//
// DATA FLOW:
//
//	[]*models.Package -> SortPackagesByEVR() -> Ordered package slice (newest version at index 0)
package logic

import (
	"sort"
	"strconv"
	"strings"

	"github.com/edsilegxrepo/repoview/internal/models"

	rpmver "github.com/knqyf263/go-rpm-version"
	debversion "pault.ag/go/debian/version"
)

// --- High-Throughput EVR & Version Sorting ---

// SortPackagesByEVR sorts a slice of packages by Epoch, Version, Release (descending)
// and Arch (ascending), supporting both RPM and Debian package formats.
// CRITICAL: This sort order is used to determine which package version is the "latest"
// and orders all versions/architectures on the package detail page.
func SortPackagesByEVR(pkgs []*models.Package) {
	if len(pkgs) <= 1 {
		return
	}
	if len(pkgs) == 2 {
		cmp := CompareVersions(pkgs[0], pkgs[1])
		if cmp < 0 || (cmp == 0 && pkgs[0].Arch > pkgs[1].Arch) {
			pkgs[0], pkgs[1] = pkgs[1], pkgs[0]
		}
		return
	}

	hasDeb := false
	for _, p := range pkgs {
		if p.Format == models.FormatDEB {
			hasDeb = true
			break
		}
	}

	if hasDeb {
		type debEntry struct {
			pkg    *models.Package
			evrStr string
			debVer debversion.Version
			parsed bool
		}
		entries := make([]debEntry, len(pkgs))
		for i, p := range pkgs {
			s := p.EVR()
			v, err := debversion.Parse(s)
			entries[i] = debEntry{pkg: p, evrStr: s, debVer: v, parsed: err == nil}
		}
		sort.Slice(entries, func(i, j int) bool {
			var cmp int
			if entries[i].parsed && entries[j].parsed {
				cmp = debversion.Compare(entries[i].debVer, entries[j].debVer)
			} else {
				cmp = strings.Compare(entries[i].evrStr, entries[j].evrStr)
			}
			if cmp != 0 {
				return cmp > 0
			}
			return entries[i].pkg.Arch < entries[j].pkg.Arch
		})
		for i, e := range entries {
			pkgs[i] = e.pkg
		}
		return
	}

	type rpmEntry struct {
		pkg   *models.Package
		epoch int
		ver   rpmver.Version
		rel   rpmver.Version
	}
	entries := make([]rpmEntry, len(pkgs))
	for i, p := range pkgs {
		entries[i] = rpmEntry{
			pkg:   p,
			epoch: parseEpoch(p.Epoch),
			ver:   rpmver.NewVersion(p.Version),
			rel:   rpmver.NewVersion(p.Release),
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].epoch != entries[j].epoch {
			return entries[i].epoch > entries[j].epoch
		}
		if ret := entries[i].ver.Compare(entries[j].ver); ret != 0 {
			return ret > 0
		}
		if ret := entries[i].rel.Compare(entries[j].rel); ret != 0 {
			return ret > 0
		}
		return entries[i].pkg.Arch < entries[j].pkg.Arch
	})
	for i, e := range entries {
		pkgs[i] = e.pkg
	}
}

// --- Format-Specific Version Comparators ---

// CompareVersions evaluates version precedence according to the package format.
// For Debian packages (FormatDEB), it delegates to pault.ag/go/debian/version.
// For RPM packages, it delegates to CompareEVR.
func CompareVersions(p1, p2 *models.Package) int {
	if p1.Format == models.FormatDEB || p2.Format == models.FormatDEB {
		v1Str := p1.EVR()
		v2Str := p2.EVR()

		v1, err1 := debversion.Parse(v1Str)
		v2, err2 := debversion.Parse(v2Str)
		if err1 == nil && err2 == nil {
			return debversion.Compare(v1, v2)
		}
		return strings.Compare(v1Str, v2Str)
	}

	return CompareEVR(p1, p2)
}

// CompareEVR returns 1 if p1 > p2, -1 if p1 < p2, 0 if equal.
// It compares Epochs first, then Versions, and finally Releases using RPM version comparison rules.
func CompareEVR(p1, p2 *models.Package) int {
	// 1. Epoch
	// (Implementation of epoch comparison needs to handle potential non-int garbage, though rare in repos)
	// We'll skip complex epoch parsing for this snippet and assume mostly standard V-R for now,
	// or rely on the library if we pass "Epoch:Version-Release".

	// go-rpm-version NewVersion parses the version string. It usually expects "Version-Release" or just "Version".
	// It doesn't strictly parse "Epoch:Version".

	// 1. Epoch (fast integer comparison without Sscanf reflection/regex overhead)
	e1 := parseEpoch(p1.Epoch)
	e2 := parseEpoch(p2.Epoch)

	if e1 > e2 {
		return 1
	}
	if e1 < e2 {
		return -1
	}

	// 2. Version
	v1 := rpmver.NewVersion(p1.Version)
	v2 := rpmver.NewVersion(p2.Version)

	if ret := v1.Compare(v2); ret != 0 {
		return ret
	}

	// 3. Release
	// The library usually handles release if included in version, but here they are separate fields.
	// We can treat release as another version string comparison.
	r1 := rpmver.NewVersion(p1.Release)
	r2 := rpmver.NewVersion(p2.Release)

	if ret := r1.Compare(r2); ret != 0 {
		return ret
	}

	// 4. Arch (Optional, but repoview sorts by Arch ascending usually inside the list,
	// but for "Latest" determination we might just care about EVR.
	// Actually repoview logic: sorts by EVR to find "latest".

	return 0
}

// parseEpoch safely converts an RPM epoch string to an integer, returning 0
// if empty or unparseable.
func parseEpoch(epochStr string) int {
	if epochStr == "" {
		return 0
	}
	if v, err := strconv.Atoi(epochStr); err == nil {
		return v
	}
	return 0
}
