package models

// OBJECTIVES:
// Translate Debian control.BinaryIndex records into RepoView's format-agnostic
// domain package representation so downstream grouping, rendering, and search
// pipelines operate uniformly across RPM and DEB repositories.
//
// CORE COMPONENTS:
//   - PackageFromBinaryIndex: Primary adapter mapping Deb822 binary fields, tags,
//     checksums, descriptions, and dependency vectors into models.Package.
//   - mapDebianDependencies: Helper flattening pault.ag/go/debian relations and
//     skipping unresolved substvar placeholders into DependencyEntry slices.
//
// FUNCTIONALITY:
//   - Normalizes KiB Installed-Size values to byte counts, cleans "./" prefixes
//     from archive paths, splits single-line summaries from multi-line bodies,
//     and preserves Debtags as well as weak/reverse dependency relationships.
//
// DATA FLOW:
//   control.BinaryIndex -> PackageFromBinaryIndex -> *models.Package -> logic/render

import (
	"fmt"
	"strings"

	"pault.ag/go/debian/control"
	"pault.ag/go/debian/dependency"
)

// PackageFromBinaryIndex converts a pault.ag/go/debian control.BinaryIndex entry
// directly into a unified models.Package struct with zero manual parsing.
func PackageFromBinaryIndex(idx control.BinaryIndex) *Package {
	epochStr := "0"
	if idx.Version.Epoch > 0 {
		epochStr = fmt.Sprintf("%d", idx.Version.Epoch)
	}

	section := strings.TrimSpace(idx.Section)
	if section == "" {
		section = "unspecified"
	}

	pkg := &Package{
		Format:        FormatDEB,
		Name:          idx.Package,
		Version:       idx.Version.Version,
		Release:       idx.Version.Revision,
		Epoch:         epochStr,
		Arch:          idx.Architecture.String(),
		Maintainer:    idx.Maintainer,
		InstalledSize: int64(idx.InstalledSize) * 1024, // Debian Installed-Size is expressed in KiB
		SizePackage:   int64(idx.Size),
		LocationHref:  strings.TrimPrefix(idx.Filename, "./"),
		Section:       section,
		Group:         section,
		RpmGroup:      section,
		URL:           idx.Homepage,
		SHA256:        idx.SHA256,
		MD5sum:        idx.MD5sum,
		SHA1:          idx.SHA1,
		SourcePackage: idx.SourcePackage(),
		Priority:      strings.TrimSpace(idx.Priority),
		MultiArch:     strings.TrimSpace(idx.MultiArch),
		Essential:     strings.EqualFold(strings.TrimSpace(idx.Values["Essential"]), "yes"),
		Bugs:          strings.TrimSpace(idx.Values["Bugs"]),
		Origin:        strings.TrimSpace(idx.Values["Origin"]),
		BuiltUsing:    strings.TrimSpace(idx.Values["Built-Using"]),
	}
	for _, t := range idx.Tags {
		if trimmed := strings.TrimSpace(t); trimmed != "" {
			pkg.Tags = append(pkg.Tags, trimmed)
		}
	}
	if len(pkg.Tags) == 0 {
		if rawTag := strings.TrimSpace(idx.Values["Tag"]); rawTag != "" {
			for _, t := range strings.Split(rawTag, ",") {
				if trimmed := strings.TrimSpace(t); trimmed != "" {
					pkg.Tags = append(pkg.Tags, trimmed)
				}
			}
		}
	}

	// First line of Description is summary; remaining lines form full description
	desc := strings.TrimSpace(idx.Description)
	if lines := strings.SplitN(desc, "\n", 2); len(lines) > 0 {
		pkg.Summary = strings.TrimSpace(lines[0])
		if len(lines) > 1 {
			pkg.Description = strings.TrimSpace(lines[1])
		}
	}

	// --- Dependency Vector Mapping ---

	pkg.Dependencies = &PackageDependencies{
		Requires:   mapDebianDependencies(idx.GetPreDepends(), true),
		Provides:   mapDebianDependencies(idx.GetProvides(), false),
		Recommends: mapDebianDependencies(idx.GetRecommends(), false),
		Suggests:   mapDebianDependencies(idx.GetSuggests(), false),
		Enhances:   mapDebianDependencies(idx.GetEnhances(), false),
		Conflicts:  mapDebianDependencies(idx.GetConflicts(), false),
		Obsoletes:  mapDebianDependencies(idx.GetReplaces(), false),
	}
	// Append Depends to Requires
	pkg.Dependencies.Requires = append(pkg.Dependencies.Requires, mapDebianDependencies(idx.GetDepends(), false)...)
	// Append Breaks to Conflicts
	pkg.Dependencies.Conflicts = append(pkg.Dependencies.Conflicts, mapDebianDependencies(idx.GetBreaks(), false)...)

	return pkg
}

// --- Dependency Translation Helpers ---

// mapDebianDependencies converts pault.ag/go/debian Dependency objects into DependencyEntry records.
func mapDebianDependencies(dep dependency.Dependency, isPre bool) []*DependencyEntry {
	var entries []*DependencyEntry
	for _, rel := range dep.Relations {
		for _, poss := range rel.Possibilities {
			if poss.Substvar {
				continue
			}
			entry := &DependencyEntry{
				Name: poss.Name,
				Pre:  isPre,
			}
			if poss.Version != nil {
				entry.Flags = poss.Version.Operator
				entry.Version = poss.Version.Number
			}
			entries = append(entries, entry)
		}
	}
	return entries
}
