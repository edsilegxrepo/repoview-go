package models

import "encoding/xml"

// OBJECTIVES:
// Provide XML unmarshaling representations for comps.xml files, which define
// package groups, category groupings, and group hierarchies in Enterprise Linux repositories.
//
// CORE COMPONENTS:
//   - Comps: Root structure holding all package group definitions.
//   - LocalizedText: Multi-language XML text element supporting xml:lang attributes.
//   - CompsGroup: Representation of an individual comps <group> element with custom unmarshaling.
//
// FUNCTIONALITY:
//   - Decodes comps XML elements while intelligently selecting untagged default English names
//     and descriptions over localized variations.
//   - Extracts package requirement lists (<packagelist><packagereq>...</packagereq></packagelist>).
//
// DATA FLOW:
//   comps.xml bytes -> xml.Unmarshal -> models.Comps -> logic.BuildGroupTree()

// Comps represents the root of the comps.xml file structure.
// Comps (also known as package groups) define logical collections of packages
// (e.g., "Development Tools", "Web Server").
type Comps struct {
	XMLName xml.Name     `xml:"comps"` // Root XML tag
	Groups  []CompsGroup `xml:"group"` // Collection of package group definitions
}

// LocalizedText represents a localized XML element with an optional xml:lang attribute.
type LocalizedText struct {
	Lang string `xml:"http://www.w3.org/XML/1998/namespace lang,attr"` // Language code (e.g. "de", "zh")
	Text string `xml:",chardata"`                                      // Text payload
}

// CompsGroup represents a single <group> entry in comps.xml.
// It contains metadata about the group and the list of packages it includes.
type CompsGroup struct {
	ID          string   `xml:"id"`                     // Unique programmatic identifier (e.g. "development")
	Name        string   `xml:"-"`                      // Resolved default display name
	Description string   `xml:"-"`                      // Resolved default description
	Default     bool     `xml:"default"`                // True if installed by default
	Uservisible bool     `xml:"uservisible"`            // True if displayed in user interfaces
	Packagelist []string `xml:"packagelist>packagereq"` // Simplification: just get names
}

// UnmarshalXML decodes a comps group while prioritizing the untagged default name and description.
// If multiple localized strings exist, it selects the untagged (default English) version, falling
// back to the first available string if no untagged variant is present.
func (g *CompsGroup) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type Alias CompsGroup
	var raw struct {
		Alias
		Names        []LocalizedText `xml:"name"`
		Descriptions []LocalizedText `xml:"description"`
	}

	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}

	*g = CompsGroup(raw.Alias)

	// Pick untagged English/default name
	for _, n := range raw.Names {
		if n.Lang == "" {
			g.Name = n.Text
			break
		}
	}
	if g.Name == "" && len(raw.Names) > 0 {
		g.Name = raw.Names[0].Text
	}

	// Pick untagged English/default description
	for _, desc := range raw.Descriptions {
		if desc.Lang == "" {
			g.Description = desc.Text
			break
		}
	}
	if g.Description == "" && len(raw.Descriptions) > 0 {
		g.Description = raw.Descriptions[0].Text
	}

	return nil
}
