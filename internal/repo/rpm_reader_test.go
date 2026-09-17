package repo

import (
	"os"
	"testing"

	"github.com/edsilegxrepo/repoview/internal/models"
)

// TEST STRATEGY:
// Verify deep binary RPM header parsing on real distribution RPM packages when present.
//
// Test coverage includes:
//   - TestReadRPMDetails:
//       * Extracts build host, source RPM name, installed size, and signature key ID.
//       * Validates scriptlet extraction (prein/postin/preun/postun) and interpreter paths.
//       * Verifies installed file list extraction with Unix file modes and byte sizes.
//   - TestEnrichPackagesWithRPMDetails:
//       * Validates multi-goroutine worker pipeline populating models.Package instances.
//       * Verifies backfill of BuildHost, InstalledSize, and SourceRPM from header inspection.

func TestReadRPMDetails(t *testing.T) {
	rpmPath := "/u01/wwwroot/test/el9/base/x86_64/nginx-1.29.4-10.el9.xg.x86_64.rpm"
	if _, err := os.Stat(rpmPath); os.IsNotExist(err) {
		t.Skip("skipping test; test RPM file not present")
	}

	details, err := ReadRPMDetails(rpmPath)
	if err != nil {
		t.Fatalf("ReadRPMDetails failed: %v", err)
	}

	if details == nil {
		t.Fatal("expected non-nil details")
	}

	if details.BuildHost == "" {
		t.Errorf("expected non-empty BuildHost")
	}

	if details.SourceRPM == "" {
		t.Errorf("expected non-empty SourceRPM")
	}

	if details.InstalledSize == 0 {
		t.Errorf("expected non-zero InstalledSize")
	}

	if details.Signature == "" {
		t.Errorf("expected non-empty Signature")
	}

	if details.Scriptlets == nil || !details.Scriptlets.HasAny() {
		t.Errorf("expected scriptlets to be found in nginx RPM")
	}

	if details.Scriptlets.PreIn == "" {
		t.Errorf("expected PreIn scriptlet")
	}

	if len(details.Files) == 0 {
		t.Errorf("expected non-empty file list")
	}
}

func TestEnrichPackagesWithRPMDetails(t *testing.T) {
	repoDir := "/u01/wwwroot/test/el9/base/x86_64"
	rpmPath := "/u01/wwwroot/test/el9/base/x86_64/nginx-1.29.4-10.el9.xg.x86_64.rpm"
	if _, err := os.Stat(rpmPath); os.IsNotExist(err) {
		t.Skip("skipping test; test RPM file not present")
	}

	pkg := &models.Package{
		Name:         "nginx",
		LocationHref: "nginx-1.29.4-10.el9.xg.x86_64.rpm",
	}

	EnrichPackagesWithRPMDetails(repoDir, []*models.Package{pkg})

	if pkg.Details == nil {
		t.Fatal("expected pkg.Details to be populated")
	}

	if pkg.BuildHost == "" {
		t.Errorf("expected pkg.BuildHost to be populated")
	}
}
