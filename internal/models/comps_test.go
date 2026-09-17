package models

import (
	"encoding/xml"
	"strings"
	"testing"
)

// TEST STRATEGY:
// Verify XML unmarshaling of comps group data structures, with particular focus on
// multilingual localization resolution and package list extraction.
//
// Test coverage includes:
//   - Unmarshaling a comps snippet with German, English, and Chinese translations.
//   - Confirming that the untagged English name and description are prioritized.
//   - Confirming that the packagelist elements are cleanly parsed into string slices.

func TestCompsLocalization(t *testing.T) {
	xmlData := `
<comps>
  <group>
    <id>development</id>
    <name xml:lang="de">Entwicklungswerkzeuge</name>
    <name>Development Tools</name>
    <name xml:lang="zh">开发工具</name>
    <description xml:lang="de">Deutsche Beschreibung</description>
    <description>These tools include core development packages.</description>
    <description xml:lang="zh">中文描述</description>
    <packagelist>
      <packagereq type="mandatory">gcc</packagereq>
      <packagereq type="default">make</packagereq>
    </packagelist>
  </group>
</comps>`

	var comps Comps
	if err := xml.NewDecoder(strings.NewReader(xmlData)).Decode(&comps); err != nil {
		t.Fatalf("Failed to decode comps XML: %v", err)
	}

	if len(comps.Groups) != 1 {
		t.Fatalf("Expected 1 group, got %d", len(comps.Groups))
	}

	grp := comps.Groups[0]
	if grp.Name != "Development Tools" {
		t.Errorf("Expected Name 'Development Tools', got %q", grp.Name)
	}
	if grp.Description != "These tools include core development packages." {
		t.Errorf("Expected Description 'These tools include core development packages.', got %q", grp.Description)
	}
	if len(grp.Packagelist) != 2 || grp.Packagelist[0] != "gcc" || grp.Packagelist[1] != "make" {
		t.Errorf("Unexpected packagelist: %v", grp.Packagelist)
	}
}
