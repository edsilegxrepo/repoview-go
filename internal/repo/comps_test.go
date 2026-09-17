package repo

import (
	"os"
	"path/filepath"
	"testing"
)

// TEST STRATEGY:
// Validate comps XML parser against valid, missing, and malformed files.

func TestParseComps_Valid(t *testing.T) {
	tempDir := t.TempDir()
	compsFile := filepath.Join(tempDir, "comps.xml")

	xmlData := `<?xml version="1.0" encoding="UTF-8"?>
<comps>
  <group>
    <id>core</id>
    <name>Core</name>
    <description>Smallest possible installation</description>
    <packagelist>
      <packagereq type="mandatory">basesystem</packagereq>
      <packagereq type="mandatory">bash</packagereq>
    </packagelist>
  </group>
</comps>`

	if err := os.WriteFile(compsFile, []byte(xmlData), 0o644); err != nil {
		t.Fatalf("failed to write comps file: %v", err)
	}

	comps, err := ParseComps(compsFile)
	if err != nil {
		t.Fatalf("ParseComps failed: %v", err)
	}

	if len(comps.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(comps.Groups))
	}
	if comps.Groups[0].ID != "core" || comps.Groups[0].Name != "Core" {
		t.Errorf("unexpected group: %+v", comps.Groups[0])
	}
}

func TestParseComps_NotFound(t *testing.T) {
	_, err := ParseComps("/path/that/definitely/does/not/exist/comps.xml")
	if err == nil {
		t.Errorf("expected error for non-existent file, got nil")
	}
}

func TestParseComps_Corrupt(t *testing.T) {
	tempDir := t.TempDir()
	compsFile := filepath.Join(tempDir, "corrupt.xml")
	if err := os.WriteFile(compsFile, []byte("not valid xml"), 0o644); err != nil {
		t.Fatalf("failed to write comps file: %v", err)
	}

	_, err := ParseComps(compsFile)
	if err == nil {
		t.Errorf("expected error for malformed xml, got nil")
	}
}
