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
