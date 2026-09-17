package repo

import (
	"os"
	"path/filepath"
	"testing"
)

// TEST STRATEGY:
// Validate repomd.xml discovery, database resolution, path traversal defense,
// and database schema version guards.

func TestParseRepomd_Valid(t *testing.T) {
	tempRepo := t.TempDir()
	repodataDir := filepath.Join(tempRepo, "repodata")
	if err := os.MkdirAll(repodataDir, 0o755); err != nil {
		t.Fatalf("failed to create repodata dir: %v", err)
	}

	xmlContent := `<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="primary_db">
    <location href="repodata/primary.sqlite.gz"/>
    <database_version>10</database_version>
  </data>
  <data type="other_db">
    <location href="repodata/other.sqlite.gz"/>
  </data>
  <data type="group">
    <location href="repodata/comps.xml.gz"/>
  </data>
</repomd>`

	if err := os.WriteFile(filepath.Join(repodataDir, "repomd.xml"), []byte(xmlContent), 0o644); err != nil {
		t.Fatalf("failed to write repomd.xml: %v", err)
	}

	locs, err := ParseRepomd(tempRepo)
	if err != nil {
		t.Fatalf("ParseRepomd failed: %v", err)
	}

	expectedPrimary := filepath.Join(tempRepo, "repodata", "primary.sqlite.gz")
	if locs.Primary != expectedPrimary {
		t.Errorf("expected Primary %s, got %s", expectedPrimary, locs.Primary)
	}
	expectedOther := filepath.Join(tempRepo, "repodata", "other.sqlite.gz")
	if locs.Other != expectedOther {
		t.Errorf("expected Other %s, got %s", expectedOther, locs.Other)
	}
	expectedGroups := filepath.Join(tempRepo, "repodata", "comps.xml.gz")
	if locs.Groups != expectedGroups {
		t.Errorf("expected Groups %s, got %s", expectedGroups, locs.Groups)
	}
}

func TestParseRepomd_PathTraversal(t *testing.T) {
	tempRepo := t.TempDir()
	repodataDir := filepath.Join(tempRepo, "repodata")
	if err := os.MkdirAll(repodataDir, 0o755); err != nil {
		t.Fatalf("failed to create repodata dir: %v", err)
	}

	xmlContent := `<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="primary_db">
    <location href="../outside/primary.sqlite.gz"/>
  </data>
</repomd>`

	if err := os.WriteFile(filepath.Join(repodataDir, "repomd.xml"), []byte(xmlContent), 0o644); err != nil {
		t.Fatalf("failed to write repomd.xml: %v", err)
	}

	_, err := ParseRepomd(tempRepo)
	if err == nil {
		t.Errorf("expected path traversal error, got nil")
	}
}

func TestParseRepomd_UnsupportedVersion(t *testing.T) {
	tempRepo := t.TempDir()
	repodataDir := filepath.Join(tempRepo, "repodata")
	if err := os.MkdirAll(repodataDir, 0o755); err != nil {
		t.Fatalf("failed to create repodata dir: %v", err)
	}

	xmlContent := `<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="primary_db">
    <location href="repodata/primary.sqlite"/>
    <database_version>11</database_version>
  </data>
  <data type="other_db">
    <location href="repodata/other.sqlite"/>
  </data>
</repomd>`

	if err := os.WriteFile(filepath.Join(repodataDir, "repomd.xml"), []byte(xmlContent), 0o644); err != nil {
		t.Fatalf("failed to write repomd.xml: %v", err)
	}

	_, err := ParseRepomd(tempRepo)
	if err == nil {
		t.Errorf("expected error for db_version > 10, got nil")
	}
}

func TestParseRepomd_MissingPrimaryOrOther(t *testing.T) {
	tempRepo := t.TempDir()
	repodataDir := filepath.Join(tempRepo, "repodata")
	_ = os.MkdirAll(repodataDir, 0o755)

	// Missing primary
	xmlContent := `<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="other_db">
    <location href="repodata/other.sqlite"/>
  </data>
</repomd>`
	_ = os.WriteFile(filepath.Join(repodataDir, "repomd.xml"), []byte(xmlContent), 0o644)

	_, err := ParseRepomd(tempRepo)
	if err == nil {
		t.Errorf("expected error when primary_db is missing, got nil")
	}
}
