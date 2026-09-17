package deb

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscover_DistsLayout(t *testing.T) {
	tmpDir := t.TempDir()

	binDir := filepath.Join(tmpDir, "dists", "noble", "main", "binary-amd64")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("failed to create binDir: %v", err)
	}

	packagesFile := filepath.Join(binDir, "Packages")
	if err := os.WriteFile(packagesFile, []byte("Package: test\nVersion: 1.0\n"), 0o644); err != nil {
		t.Fatalf("failed to write Packages: %v", err)
	}

	releaseFile := filepath.Join(tmpDir, "dists", "noble", "Release")
	if err := os.WriteFile(releaseFile, []byte("Suite: noble\nCodename: noble\n"), 0o644); err != nil {
		t.Fatalf("failed to write Release: %v", err)
	}

	locs, err := Discover(tmpDir)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	if locs.Suite != "noble" {
		t.Errorf("expected suite 'noble', got '%s'", locs.Suite)
	}
	if locs.Component != "main" {
		t.Errorf("expected component 'main', got '%s'", locs.Component)
	}
	if locs.Arch != "amd64" {
		t.Errorf("expected arch 'amd64', got '%s'", locs.Arch)
	}
	if locs.IsFlat {
		t.Errorf("expected IsFlat=false")
	}
	if locs.PackagesFile != packagesFile {
		t.Errorf("expected PackagesFile '%s', got '%s'", packagesFile, locs.PackagesFile)
	}
	if locs.ReleaseFile != releaseFile {
		t.Errorf("expected ReleaseFile '%s', got '%s'", releaseFile, locs.ReleaseFile)
	}
}

func TestDiscover_FlatLayoutGz(t *testing.T) {
	tmpDir := t.TempDir()

	pkgGzPath := filepath.Join(tmpDir, "Packages.gz")
	f, err := os.Create(pkgGzPath)
	if err != nil {
		t.Fatalf("failed to create Packages.gz: %v", err)
	}
	gw := gzip.NewWriter(f)
	if _, err := gw.Write([]byte("Package: test\nVersion: 1.0\n")); err != nil {
		t.Fatalf("failed to write to gzip: %v", err)
	}
	_ = gw.Close()
	_ = f.Close()

	locs, err := Discover(tmpDir)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	if !locs.IsFlat {
		t.Errorf("expected IsFlat=true for flat repository")
	}
	if locs.PackagesFile != pkgGzPath {
		t.Errorf("expected PackagesFile '%s', got '%s'", pkgGzPath, locs.PackagesFile)
	}
}

func TestDiscover_DirectBinaryLeaf(t *testing.T) {
	tmpDir := t.TempDir()

	binDir := filepath.Join(tmpDir, "dists", "bookworm", "main", "binary-amd64")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("failed to create binDir: %v", err)
	}

	packagesFile := filepath.Join(binDir, "Packages")
	if err := os.WriteFile(packagesFile, []byte("Package: test\nVersion: 1.0\n"), 0o644); err != nil {
		t.Fatalf("failed to write Packages: %v", err)
	}

	releaseFile := filepath.Join(tmpDir, "dists", "bookworm", "Release")
	if err := os.WriteFile(releaseFile, []byte("Suite: bookworm\n"), 0o644); err != nil {
		t.Fatalf("failed to write Release: %v", err)
	}

	// 1. Discover pointing directly to binary-amd64
	locs, err := Discover(binDir)
	if err != nil {
		t.Fatalf("Discover directly on leaf failed: %v", err)
	}
	if locs.Suite != "bookworm" {
		t.Errorf("expected suite 'bookworm', got %q", locs.Suite)
	}
	if locs.Component != "main" {
		t.Errorf("expected component 'main', got %q", locs.Component)
	}
	if locs.Arch != "amd64" {
		t.Errorf("expected arch 'amd64', got %q", locs.Arch)
	}
	if locs.BaseDir != tmpDir {
		t.Errorf("expected BaseDir %q, got %q", tmpDir, locs.BaseDir)
	}
	if locs.ReleaseFile != releaseFile {
		t.Errorf("expected ReleaseFile %q, got %q", releaseFile, locs.ReleaseFile)
	}

	// 2. Discover pointing directly to suite dir (dists/bookworm)
	suiteDir := filepath.Join(tmpDir, "dists", "bookworm")
	suiteLocs, err := Discover(suiteDir)
	if err != nil {
		t.Fatalf("Discover on suite dir failed: %v", err)
	}
	if suiteLocs.Suite != "bookworm" || suiteLocs.Arch != "amd64" {
		t.Errorf("expected bookworm amd64, got suite=%q arch=%q", suiteLocs.Suite, suiteLocs.Arch)
	}
	if suiteLocs.BaseDir != tmpDir {
		t.Errorf("expected BaseDir %q, got %q", tmpDir, suiteLocs.BaseDir)
	}
}

func TestDiscover_ArchPriority_PicksConcreteOverAll(t *testing.T) {
	tmpDir := t.TempDir()

	compDir := filepath.Join(tmpDir, "dists", "trixie", "contrib")
	allDir := filepath.Join(compDir, "binary-all")
	armDir := filepath.Join(compDir, "binary-arm64")

	if err := os.MkdirAll(allDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(armDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_ = os.WriteFile(filepath.Join(allDir, "Packages"), []byte("Package: allpkg\nVersion: 1.0\n"), 0o644)
	_ = os.WriteFile(filepath.Join(armDir, "Packages"), []byte("Package: armpkg\nVersion: 1.0\n"), 0o644)

	locs, err := Discover(tmpDir)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if locs.Arch != "arm64" {
		t.Errorf("expected concrete architecture 'arm64' over 'all', got %q", locs.Arch)
	}
}

func TestDiscover_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := Discover(tmpDir)
	if err == nil {
		t.Errorf("expected error discovering empty dir, got nil")
	}
}

func TestDiscover_MultiComponentAggregation(t *testing.T) {
	tmpDir := t.TempDir()

	// Create dists/noble with main, universe, and multiverse components
	suiteDir := filepath.Join(tmpDir, "dists", "noble")
	mainBin := filepath.Join(suiteDir, "main", "binary-amd64")
	univBin := filepath.Join(suiteDir, "universe", "binary-amd64")
	multiBin := filepath.Join(suiteDir, "multiverse", "binary-amd64")

	for _, d := range []string{mainBin, univBin, multiBin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		pkgFile := filepath.Join(d, "Packages")
		if err := os.WriteFile(pkgFile, []byte("Package: pkg-"+filepath.Base(filepath.Dir(d))+"\nVersion: 1.0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	releaseFile := filepath.Join(suiteDir, "Release")
	_ = os.WriteFile(releaseFile, []byte("Suite: noble\n"), 0o644)

	// 1. Discover from repository root
	locsRoot, err := Discover(tmpDir)
	if err != nil {
		t.Fatalf("Discover from root failed: %v", err)
	}
	if locsRoot.Suite != "noble" {
		t.Errorf("expected suite noble, got %q", locsRoot.Suite)
	}
	if locsRoot.Component != "main" {
		t.Errorf("expected primary component 'main', got %q", locsRoot.Component)
	}
	if len(locsRoot.Components) != 3 {
		t.Fatalf("expected 3 aggregated components, got %d: %v", len(locsRoot.Components), locsRoot.Components)
	}
	if len(locsRoot.PackagesFiles) != 3 {
		t.Fatalf("expected 3 aggregated PackagesFiles, got %d: %v", len(locsRoot.PackagesFiles), locsRoot.PackagesFiles)
	}

	// 2. Discover from suite directory
	locsSuite, err := Discover(suiteDir)
	if err != nil {
		t.Fatalf("Discover from suite failed: %v", err)
	}
	if len(locsSuite.Components) != 3 {
		t.Fatalf("expected 3 aggregated components from suite dir, got %d", len(locsSuite.Components))
	}
	if len(locsSuite.PackagesFiles) != 3 {
		t.Fatalf("expected 3 aggregated PackagesFiles from suite dir, got %d", len(locsSuite.PackagesFiles))
	}

	// 3. Discover from single component directory
	locsComp, err := Discover(filepath.Join(suiteDir, "main"))
	if err != nil {
		t.Fatalf("Discover from component failed: %v", err)
	}
	if locsComp.Component != "main" || len(locsComp.Components) != 1 {
		t.Errorf("expected component main, got %v", locsComp.Components)
	}
	if len(locsComp.PackagesFiles) != 1 {
		t.Errorf("expected 1 PackagesFile, got %d", len(locsComp.PackagesFiles))
	}
}
