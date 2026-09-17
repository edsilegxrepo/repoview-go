package models

import "encoding/xml"

// OBJECTIVES:
// Map the core YUM/DNF repository index document (repomd.xml) into strongly-typed
// Go structures for XML unmarshaling and metadata discovery.
//
// CORE COMPONENTS:
//   - Repomd: Root XML element with revision and data list.
//   - Data: Metadata target descriptor (<data type="...">).
//   - Location: Relative path to the compressed metadata archive.
//   - Checksum: Cryptographic digest of the archive.
//
// FUNCTIONALITY:
//   - Enables discovery of SQLite databases (primary_db, other_db) and XML comps (group).
//   - Extracts relative paths and cryptographic hashes for data integrity verification.
//   - Robustly decodes revision whether specified as an XML element (<revision>...</revision>)
//     or root attribute (<repomd revision="...">).
//
// DATA FLOW:
//   repomd.xml bytes -> xml.Unmarshal -> models.Repomd -> repo.FindRepoMD()

// Repomd represents the root of the repomd.xml metadata file.
// It contains references to all other metadata files in the repository (primary, other, filelists, etc.).
type Repomd struct {
	XMLName  xml.Name `xml:"repomd"`     // XML root element tag name
	Xmlns    string   `xml:"xmlns,attr"` // XML namespace declaration
	Revision string   `xml:"-"`          // Repository revision identifier (element or attribute)
	Data     []Data   `xml:"data"`       // List of metadata file records
}

// UnmarshalXML decodes repomd while supporting revision as both child element and XML attribute.
func (r *Repomd) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type Alias Repomd
	var raw struct {
		Alias
		RevisionElem string `xml:"revision"`
		RevisionAttr string `xml:"revision,attr"`
	}

	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}

	*r = Repomd(raw.Alias)
	if raw.RevisionElem != "" {
		r.Revision = raw.RevisionElem
	} else {
		r.Revision = raw.RevisionAttr
	}

	return nil
}

// Data represents a <data> entry in repomd.xml, describing a specific metadata file.
// Common types include "primary_db", "other_db", and "group".
type Data struct {
	Type            string   `xml:"type,attr"`        // Metadata type ("primary_db", "other_db", "group", etc.)
	Location        Location `xml:"location"`         // Relative path to file within repository
	Checksum        Checksum `xml:"checksum"`         // Integrity checksum of the metadata file
	Timestamp       int64    `xml:"timestamp"`        // Generation timestamp of this record
	Size            int64    `xml:"size"`             // File size in bytes
	DatabaseVersion string   `xml:"database_version"` // Schema version of the SQLite database
}

// Location represents the href attribute pointing to the relative path of the file.
type Location struct {
	Href string `xml:"href,attr"` // Relative URI path (e.g. "repodata/primary.sqlite.gz")
}

// Checksum represents the checksum of the file (typically SHA256 or SHA1).
type Checksum struct {
	Type  string `xml:"type,attr"` // Hash algorithm ("sha256", "sha1")
	Value string `xml:",chardata"` // Hexadecimal digest string
}
