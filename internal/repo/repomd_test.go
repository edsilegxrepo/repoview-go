package repo

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edsilegxrepo/repoview/internal/models"
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

func TestParseRepomd_SymlinkEscape(t *testing.T) {
	tempRepo := t.TempDir()
	outsideDir := t.TempDir()
	repodataDir := filepath.Join(tempRepo, "repodata")
	if err := os.MkdirAll(repodataDir, 0o755); err != nil {
		t.Fatalf("failed to create repodata dir: %v", err)
	}
	outsideFile := filepath.Join(outsideDir, "secret.sqlite")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatalf("failed to write outside file: %v", err)
	}

	linkPath := filepath.Join(repodataDir, "linked.sqlite")
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Skipf("skipping symlink escape test (symlinks unsupported): %v", err)
	}

	xmlContent := `<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="primary_db">
    <location href="repodata/linked.sqlite"/>
  </data>
</repomd>`
	if err := os.WriteFile(filepath.Join(repodataDir, "repomd.xml"), []byte(xmlContent), 0o644); err != nil {
		t.Fatalf("failed to write repomd.xml: %v", err)
	}

	if _, err := ParseRepomd(tempRepo); err == nil {
		t.Errorf("expected symlink escape in repomd.xml to be rejected")
	}
}

func TestParseRepomd_ChecksumAndSizeVerification(t *testing.T) {
	tempRepo := t.TempDir()
	repodataDir := filepath.Join(tempRepo, "repodata")
	if err := os.MkdirAll(repodataDir, 0o755); err != nil {
		t.Fatalf("failed to create repodata dir: %v", err)
	}

	pBytes := []byte("primary-sqlite-payload")
	oBytes := []byte("other-sqlite-payload")
	pPath := filepath.Join(repodataDir, "primary.sqlite")
	oPath := filepath.Join(repodataDir, "other.sqlite")
	_ = os.WriteFile(pPath, pBytes, 0o644)
	_ = os.WriteFile(oPath, oBytes, 0o644)

	pSum := sha256.Sum256(pBytes)
	oSum := sha512.Sum512(oBytes)

	validXML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="primary_db">
    <location href="repodata/primary.sqlite"/>
    <checksum type="sha256">%s</checksum>
    <size>%d</size>
  </data>
  <data type="other_db">
    <location href="repodata/other.sqlite"/>
    <checksum type="sha512">%s</checksum>
    <size>%d</size>
  </data>
</repomd>`, hex.EncodeToString(pSum[:]), len(pBytes), hex.EncodeToString(oSum[:]), len(oBytes))
	_ = os.WriteFile(filepath.Join(repodataDir, "repomd.xml"), []byte(validXML), 0o644)

	if _, err := ParseRepomd(tempRepo); err != nil {
		t.Fatalf("expected valid SHA256/SHA512 repomd to pass, got: %v", err)
	}

	// Tampered checksum must fail with models.ErrIntegrity
	badSumXML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="primary_db">
    <location href="repodata/primary.sqlite"/>
    <checksum type="sha256">%s</checksum>
    <size>%d</size>
  </data>
  <data type="other_db">
    <location href="repodata/other.sqlite"/>
  </data>
</repomd>`, strings.Repeat("0", 64), len(pBytes))
	_ = os.WriteFile(filepath.Join(repodataDir, "repomd.xml"), []byte(badSumXML), 0o644)

	_, err := ParseRepomd(tempRepo)
	if !errors.Is(err, models.ErrIntegrity) {
		t.Errorf("expected ErrIntegrity on checksum mismatch, got: %v", err)
	}

	// Size mismatch must also fail with models.ErrIntegrity
	badSizeXML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="primary_db">
    <location href="repodata/primary.sqlite"/>
    <size>%d</size>
  </data>
  <data type="other_db">
    <location href="repodata/other.sqlite"/>
  </data>
</repomd>`, len(pBytes)+10)
	_ = os.WriteFile(filepath.Join(repodataDir, "repomd.xml"), []byte(badSizeXML), 0o644)

	_, err = ParseRepomd(tempRepo)
	if !errors.Is(err, models.ErrIntegrity) {
		t.Errorf("expected ErrIntegrity on size mismatch, got: %v", err)
	}
}
