package models

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
		SourcePackage: idx.SourcePackage(),
	}

	// First line of Description is summary; remaining lines form full description
	desc := strings.TrimSpace(idx.Description)
	if lines := strings.SplitN(desc, "\n", 2); len(lines) > 0 {
		pkg.Summary = strings.TrimSpace(lines[0])
		if len(lines) > 1 {
			pkg.Description = strings.TrimSpace(lines[1])
		}
	}

	// Map dependencies directly from parsed dependency.Dependency fields
	recommendsDep := parseOptionalDependency(idx.Values["Recommends"])
	providesDep := parseOptionalDependency(idx.Values["Provides"])

	pkg.Dependencies = &PackageDependencies{
		Requires:   mapDebianDependencies(idx.GetPreDepends(), true),
		Provides:   mapDebianDependencies(providesDep, false),
		Recommends: mapDebianDependencies(recommendsDep, false),
		Suggests:   mapDebianDependencies(idx.GetSuggests(), false),
		Conflicts:  mapDebianDependencies(idx.GetConflicts(), false),
		Obsoletes:  mapDebianDependencies(idx.GetReplaces(), false),
	}
	// Append Depends to Requires
	pkg.Dependencies.Requires = append(pkg.Dependencies.Requires, mapDebianDependencies(idx.GetDepends(), false)...)
	// Append Breaks to Conflicts
	pkg.Dependencies.Conflicts = append(pkg.Dependencies.Conflicts, mapDebianDependencies(idx.GetBreaks(), false)...)

	return pkg
}

// parseOptionalDependency safely parses a raw dependency line if present.
func parseOptionalDependency(val string) dependency.Dependency {
	val = strings.TrimSpace(val)
	if val == "" {
		return dependency.Dependency{}
	}
	dep, err := dependency.Parse(val)
	if err != nil {
		return dependency.Dependency{}
	}
	return *dep
}

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
