package repo

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"

	"github.com/edsilegxrepo/repoview/internal/models"
)

// OBJECTIVES:
// Read and decode comps XML package group definitions into domain model structures.
//
// CORE COMPONENTS:
//   - ParseComps: Decodes an uncompressed comps XML file into a *models.Comps instance.
//
// FUNCTIONALITY:
//   - Opens the specified comps XML file.
//   - Uses encoding/xml to decode group identifiers, localized names, descriptions,
//     and package requirements.
//
// DATA FLOW:
//   comps.xml path -> ParseComps() -> *models.Comps -> logic.GroupingService

// ParseComps reads and parses the comps.xml file.
// It returns a Comps struct containing all group definitions found in the file.
// This is used to organize packages into user-friendly categories.
func ParseComps(path string) (*models.Comps, error) {
	// #nosec G304 -- path is validated from repo metadata or explicit CLI flag
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("failed to open comps.xml: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	var comps models.Comps
	if err := xml.NewDecoder(f).Decode(&comps); err != nil {
		return nil, fmt.Errorf("failed to parse comps.xml: %w", err)
	}

	return &comps, nil
}
