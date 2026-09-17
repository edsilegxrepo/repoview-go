package logic

import (
	"testing"

	"github.com/edsilegxrepo/repoview/internal/models"
)

// TEST STRATEGY:
// Verify RPM EVR (Epoch-Version-Release) sorting order and architecture tie-breaking.
//
// Test coverage includes:
//   - Epoch supremacy (Epoch 1:0.9 beats Epoch 0:2.0).
//   - Version comparison (Version 2.0 beats Version 1.0).
//   - Release comparison (Release 2 beats Release 1).
//   - Architecture tie-breaker (aarch64 precedes x86_64 when EVR is identical).

func TestSortPackagesByEVR(t *testing.T) {
	pkgs := []*models.Package{
		{Name: "foo", Epoch: "0", Version: "1.0", Release: "1", Arch: "x86_64"},
		{Name: "foo", Epoch: "0", Version: "2.0", Release: "1", Arch: "x86_64"},
		{Name: "foo", Epoch: "1", Version: "0.9", Release: "1", Arch: "x86_64"},
		{Name: "foo", Epoch: "0", Version: "2.0", Release: "1", Arch: "aarch64"},
		{Name: "foo", Epoch: "0", Version: "2.0", Release: "2", Arch: "x86_64"},
	}

	SortPackagesByEVR(pkgs)

	// Expected order:
	// 1. Epoch 1: 0.9-1.x86_64
	// 2. Epoch 0: 2.0-2.x86_64
	// 3. Epoch 0: 2.0-1.aarch64 (arch tie-breaker ascending)
	// 4. Epoch 0: 2.0-1.x86_64
	// 5. Epoch 0: 1.0-1.x86_64

	expected := []struct {
		epoch, version, release, arch string
	}{
		{"1", "0.9", "1", "x86_64"},
		{"0", "2.0", "2", "x86_64"},
		{"0", "2.0", "1", "aarch64"},
		{"0", "2.0", "1", "x86_64"},
		{"0", "1.0", "1", "x86_64"},
	}

	for i, exp := range expected {
		p := pkgs[i]
		if p.Epoch != exp.epoch || p.Version != exp.version || p.Release != exp.release || p.Arch != exp.arch {
			t.Errorf("pkg[%d] = %s:%s-%s.%s; want %s:%s-%s.%s",
				i, p.Epoch, p.Version, p.Release, p.Arch, exp.epoch, exp.version, exp.release, exp.arch)
		}
	}
}

func TestCompareEVR_EdgeCases(t *testing.T) {
	// Equal EVR
	p1 := &models.Package{Epoch: "0", Version: "1.0", Release: "1"}
	p2 := &models.Package{Epoch: "0", Version: "1.0", Release: "1"}
	if cmp := CompareEVR(p1, p2); cmp != 0 {
		t.Errorf("expected 0 for identical EVR, got %d", cmp)
	}

	// Greater Epoch
	p1 = &models.Package{Epoch: "2", Version: "1.0", Release: "1"}
	p2 = &models.Package{Epoch: "1", Version: "2.0", Release: "1"}
	if cmp := CompareEVR(p1, p2); cmp <= 0 {
		t.Errorf("expected p1 > p2 due to epoch, got %d", cmp)
	}

	// Lesser Epoch
	p1 = &models.Package{Epoch: "1", Version: "2.0", Release: "1"}
	p2 = &models.Package{Epoch: "2", Version: "1.0", Release: "1"}
	if cmp := CompareEVR(p1, p2); cmp >= 0 {
		t.Errorf("expected p1 < p2 due to epoch, got %d", cmp)
	}

	// Version difference
	p1 = &models.Package{Epoch: "0", Version: "2.1", Release: "1"}
	p2 = &models.Package{Epoch: "0", Version: "2.0", Release: "1"}
	if cmp := CompareEVR(p1, p2); cmp <= 0 {
		t.Errorf("expected p1 > p2 due to version, got %d", cmp)
	}

	// Release difference
	p1 = &models.Package{Epoch: "0", Version: "2.0", Release: "2"}
	p2 = &models.Package{Epoch: "0", Version: "2.0", Release: "1"}
	if cmp := CompareEVR(p1, p2); cmp <= 0 {
		t.Errorf("expected p1 > p2 due to release, got %d", cmp)
	}
}

func TestParseEpoch(t *testing.T) {
	if parseEpoch("") != 0 {
		t.Errorf("expected 0 for empty epoch")
	}
	if parseEpoch("not-a-number") != 0 {
		t.Errorf("expected 0 for invalid epoch")
	}
	if parseEpoch("5") != 5 {
		t.Errorf("expected 5 for epoch '5'")
	}
}

func TestCompareVersions_Debian(t *testing.T) {
	// Tilde sorting: 1.0~beta1 < 1.0
	p1 := &models.Package{Format: models.FormatDEB, Version: "1.0~beta1", Release: "1"}
	p2 := &models.Package{Format: models.FormatDEB, Version: "1.0", Release: "1"}
	if cmp := CompareVersions(p1, p2); cmp >= 0 {
		t.Errorf("expected 1.0~beta1 < 1.0 in Debian, got %d", cmp)
	}
	if cmp := CompareVersions(p2, p1); cmp <= 0 {
		t.Errorf("expected 1.0 > 1.0~beta1 in Debian, got %d", cmp)
	}

	// Epoch: 1:1.0 > 2.0
	pEpoch := &models.Package{Format: models.FormatDEB, Epoch: "1", Version: "1.0", Release: "1"}
	pNoEpoch := &models.Package{Format: models.FormatDEB, Epoch: "0", Version: "2.0", Release: "1"}
	if cmp := CompareVersions(pEpoch, pNoEpoch); cmp <= 0 {
		t.Errorf("expected 1:1.0 > 2.0 in Debian, got %d", cmp)
	}

	// Revision: 2.0-1 < 2.0-2
	pRev1 := &models.Package{Format: models.FormatDEB, Version: "2.0", Release: "1"}
	pRev2 := &models.Package{Format: models.FormatDEB, Version: "2.0", Release: "2"}
	if cmp := CompareVersions(pRev1, pRev2); cmp >= 0 {
		t.Errorf("expected 2.0-1 < 2.0-2 in Debian, got %d", cmp)
	}

	// Identical
	pIdent1 := &models.Package{Format: models.FormatDEB, Epoch: "1", Version: "2.0", Release: "3"}
	pIdent2 := &models.Package{Format: models.FormatDEB, Epoch: "1", Version: "2.0", Release: "3"}
	if cmp := CompareVersions(pIdent1, pIdent2); cmp != 0 {
		t.Errorf("expected 0 for identical Debian packages, got %d", cmp)
	}
}

func TestSortPackagesByEVR_Debian(t *testing.T) {
	pkgs := []*models.Package{
		{Format: models.FormatDEB, Name: "pkg", Version: "1.0", Release: "1", Arch: "amd64"},
		{Format: models.FormatDEB, Name: "pkg", Version: "1.0~rc1", Release: "1", Arch: "amd64"},
		{Format: models.FormatDEB, Name: "pkg", Epoch: "1", Version: "0.1", Release: "1", Arch: "amd64"},
		{Format: models.FormatDEB, Name: "pkg", Version: "1.0", Release: "2", Arch: "amd64"},
		{Format: models.FormatDEB, Name: "pkg", Version: "1.0", Release: "2", Arch: "arm64"},
	}

	SortPackagesByEVR(pkgs)

	// Expected order (descending EVR, ascending Arch for ties):
	// 1. 1:0.1-1.amd64 (epoch 1)
	// 2. 1.0-2.amd64 (arch tie-breaker)
	// 3. 1.0-2.arm64
	// 4. 1.0-1.amd64
	// 5. 1.0~rc1-1.amd64 (tilde sorts before release 1.0)

	expected := []struct {
		epoch, version, release, arch string
	}{
		{"1", "0.1", "1", "amd64"},
		{"", "1.0", "2", "amd64"},
		{"", "1.0", "2", "arm64"},
		{"", "1.0", "1", "amd64"},
		{"", "1.0~rc1", "1", "amd64"},
	}

	for i, exp := range expected {
		p := pkgs[i]
		if p.Epoch != exp.epoch || p.Version != exp.version || p.Release != exp.release || p.Arch != exp.arch {
			t.Errorf("pkg[%d] = %s:%s-%s.%s; want %s:%s-%s.%s",
				i, p.Epoch, p.Version, p.Release, p.Arch, exp.epoch, exp.version, exp.release, exp.arch)
		}
	}
}
