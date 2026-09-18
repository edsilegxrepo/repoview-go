package portal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edsilegxrepo/repoview/internal/models"
)

// TEST STRATEGY:
// Validate the auto-discovery engine across all 4 supported repository topologies,
// symlink deduplication, cycle protection, branch pruning (pool/ skips), and Debian Suite aggregation.

func TestScanner_TopologiesAndPruning(t *testing.T) {
	tempRoot := t.TempDir()

	// 1. Topology 1: Nested Hierarchical Trees
	// el8/base/x86_64 (raw RPM)
	el8Dir := filepath.Join(tempRoot, "el8", "base", "x86_64")
	_ = os.MkdirAll(filepath.Join(el8Dir, "repodata"), 0o755)

	// el9/base/x86_64 (rendered RPM with repoview.json)
	el9Dir := filepath.Join(tempRoot, "el9", "base", "x86_64")
	_ = os.MkdirAll(filepath.Join(el9Dir, "repoview"), 0o755)
	_ = os.MkdirAll(filepath.Join(el9Dir, "repodata"), 0o755)
	el9Desc := models.RepoDescriptor{
		Title:           "Enterprise Linux 9 Base",
		Format:          "rpm",
		Arch:            "x86_64",
		Distro:          "el9",
		Channel:         "base",
		PackageCount:    150,
		LastBuild:       time.Now().UTC(),
		BaseURL:         "https://repo.example.com/el9/",
		RepoviewVersion: "0.2.0",
	}
	descBytes, _ := json.Marshal(el9Desc)
	_ = os.WriteFile(filepath.Join(el9Dir, "repoview", "repoview.json"), descBytes, 0o644)
	_ = os.WriteFile(filepath.Join(el9Dir, "repoview", "index.html"), []byte(`<html><meta name="generator" content="RepoView"><title>EL9</title></html>`), 0o644)

	// 2. Topology 2: Flat / Mixed Format Slugs
	// el-9-aarch64 (raw RPM)
	el9Aarch := filepath.Join(tempRoot, "el-9-aarch64")
	_ = os.MkdirAll(filepath.Join(el9Aarch, "repodata"), 0o755)

	// ubuntu-24.04-amd64 (raw flat Debian with Packages.gz)
	ubuFlat := filepath.Join(tempRoot, "ubuntu-24.04-amd64")
	_ = os.MkdirAll(ubuFlat, 0o755)
	_ = os.WriteFile(filepath.Join(ubuFlat, "Packages.gz"), []byte("dummy"), 0o644)

	// 3. Topology 3: Dedicated Repoview Tree (Decoupled, no RPMs)
	el10Dir := filepath.Join(tempRoot, "el10-base.x86_64")
	_ = os.MkdirAll(el10Dir, 0o755)
	el10Desc := models.RepoDescriptor{
		Title:           "Enterprise Linux 10 Base",
		Format:          "rpm",
		Arch:            "x86_64",
		Distro:          "el10",
		Channel:         "base",
		PackageCount:    500,
		LastBuild:       time.Now().UTC(),
		RepoviewVersion: "0.2.0",
	}
	el10Bytes, _ := json.Marshal(el10Desc)
	_ = os.WriteFile(filepath.Join(el10Dir, "repoview.json"), el10Bytes, 0o644)
	_ = os.WriteFile(filepath.Join(el10Dir, "index.html"), []byte(`<html><meta name="generator" content="RepoView"><title>EL10</title></html>`), 0o644)

	// 4. Topology 4: Debian Multi-Suite / Multi-Component Pools
	nobleMain := filepath.Join(tempRoot, "ubuntu", "dists", "noble", "main", "binary-amd64")
	_ = os.MkdirAll(nobleMain, 0o755)
	_ = os.WriteFile(filepath.Join(nobleMain, "Packages.gz"), []byte("dummy"), 0o644)

	nobleUniverse := filepath.Join(tempRoot, "ubuntu", "dists", "noble", "universe", "binary-amd64")
	_ = os.MkdirAll(nobleUniverse, 0o755)
	_ = os.WriteFile(filepath.Join(nobleUniverse, "Packages.gz"), []byte("dummy"), 0o644)

	// Pool directory with thousands of simulated package subdirs - MUST BE PRUNED
	poolDir := filepath.Join(tempRoot, "ubuntu", "pool", "main", "g", "glibc")
	_ = os.MkdirAll(poolDir, 0o755)
	_ = os.WriteFile(filepath.Join(poolDir, "glibc.deb"), []byte("dummy"), 0o644)

	// SRPMS directory - MUST BE PRUNED
	srpmsDir := filepath.Join(tempRoot, "el8", "SRPMS")
	_ = os.MkdirAll(srpmsDir, 0o755)
	_ = os.WriteFile(filepath.Join(srpmsDir, "pkg.src.rpm"), []byte("dummy"), 0o644)

	// 5. Excluded directory matching custom glob
	debugDir := filepath.Join(tempRoot, "el9-debuginfo-x86_64")
	_ = os.MkdirAll(filepath.Join(debugDir, "repodata"), 0o755)

	// Run scanner
	scanner := NewScanner(ScanConfig{
		RootDir:         tempRoot,
		MaxDepth:        6,
		RequireRendered: false,
		ExcludePatterns: []string{"*debuginfo*"},
	})

	repos, err := scanner.Scan()
	if err != nil {
		t.Fatalf("scanner.Scan() failed: %v", err)
	}

	// Verify excluded directories were pruned
	for _, r := range repos {
		if strings.Contains(r.RelPath, "pool") {
			t.Errorf("pool directory was not pruned: %s", r.RelPath)
		}
		if strings.Contains(r.RelPath, "SRPMS") {
			t.Errorf("SRPMS directory was not pruned: %s", r.RelPath)
		}
		if strings.Contains(r.RelPath, "debuginfo") {
			t.Errorf("debuginfo directory was not excluded: %s", r.RelPath)
		}
	}

	// Check Debian Suite Grouping (noble main + universe merged into 1 card)
	var nobleRepo *DiscoveredRepo
	for _, r := range repos {
		if r.Suite == "noble" {
			nobleRepo = r
			break
		}
	}

	if nobleRepo == nil {
		t.Fatalf("expected aggregated noble suite repo, but found none")
	}
	if len(nobleRepo.Components) != 2 {
		t.Errorf("expected 2 components for noble suite, got %d (%v)", len(nobleRepo.Components), nobleRepo.Components)
	}
	if nobleRepo.Components[0] != "main" || nobleRepo.Components[1] != "universe" {
		t.Errorf("expected [main universe], got %v", nobleRepo.Components)
	}
	if nobleRepo.Arch != "amd64" {
		t.Errorf("expected arch amd64, got %s", nobleRepo.Arch)
	}

	// Verify el9 repo was parsed with descriptor
	var el9Found *DiscoveredRepo
	for _, r := range repos {
		if r.Distro == "el9" && r.Arch == "x86_64" {
			el9Found = r
			break
		}
	}
	if el9Found == nil {
		t.Fatalf("expected el9 repo to be discovered")
	}
	if !el9Found.IsRendered {
		t.Errorf("expected el9 to have IsRendered=true")
	}
	if el9Found.PackageCount != 150 {
		t.Errorf("expected package count 150, got %d", el9Found.PackageCount)
	}
}

func TestScanner_RequireRendered(t *testing.T) {
	tempRoot := t.TempDir()

	// 1. Unrendered raw repo
	rawDir := filepath.Join(tempRoot, "raw-repo")
	_ = os.MkdirAll(filepath.Join(rawDir, "repodata"), 0o755)

	// 2. Rendered repo
	renderedDir := filepath.Join(tempRoot, "rendered-repo")
	_ = os.MkdirAll(renderedDir, 0o755)
	_ = os.WriteFile(filepath.Join(renderedDir, "index.html"), []byte(`<html><meta name="generator" content="RepoView"></html>`), 0o644)

	scanner := NewScanner(ScanConfig{
		RootDir:         tempRoot,
		MaxDepth:        3,
		RequireRendered: true,
	})

	repos, err := scanner.Scan()
	if err != nil {
		t.Fatalf("scanner.Scan() failed: %v", err)
	}

	if len(repos) != 1 {
		t.Fatalf("expected 1 repo with RequireRendered=true, got %d", len(repos))
	}
	if repos[0].RelPath != "rendered-repo" {
		t.Errorf("expected rendered-repo, got %s", repos[0].RelPath)
	}
}

func TestScanner_SymlinkDeduplicationAndCycleGuard(t *testing.T) {
	tempRoot := t.TempDir()

	// Real repo: el9/base/x86_64
	realRepoDir := filepath.Join(tempRoot, "el9", "base", "x86_64")
	_ = os.MkdirAll(filepath.Join(realRepoDir, "repodata"), 0o755)

	// Symlink alias: el-current -> el9/base/x86_64
	aliasDir := filepath.Join(tempRoot, "el-current")
	relTarget, _ := filepath.Rel(tempRoot, realRepoDir)
	if err := os.Symlink(relTarget, aliasDir); err != nil {
		t.Skipf("skipping symlink test on unsupported platform: %v", err)
	}

	// Recursive loop symlink: cycle -> .
	loopDir := filepath.Join(tempRoot, "cycle")
	_ = os.Symlink(".", loopDir)

	scanner := NewScanner(ScanConfig{
		RootDir:  tempRoot,
		MaxDepth: 5,
	})

	start := time.Now()
	repos, err := scanner.Scan()
	duration := time.Since(start)

	if err != nil {
		t.Fatalf("scanner.Scan() with symlinks failed: %v", err)
	}

	// Traversal must complete fast without infinite loops
	if duration > 500*time.Millisecond {
		t.Errorf("scanner took too long (%v), possible cycle leak", duration)
	}

	// There should be exactly 1 repository discovered, with 1 alias
	if len(repos) != 1 {
		t.Fatalf("expected 1 deduplicated repository, got %d", len(repos))
	}

	repo := repos[0]
	if len(repo.Aliases) == 0 {
		t.Errorf("expected symlink alias to be recorded on repo, got empty aliases")
	}
	hasAlias := false
	for _, a := range repo.Aliases {
		if a == "el-current" {
			hasAlias = true
			break
		}
	}
	if !hasAlias {
		t.Errorf("expected 'el-current' in aliases, got %v", repo.Aliases)
	}
}

func TestScanner_MaxDepth(t *testing.T) {
	tempRoot := t.TempDir()

	// Level 1: a/
	// Level 2: a/b/
	// Level 3: a/b/c/
	// Level 4: a/b/c/d/ (contains repodata)
	deepRepo := filepath.Join(tempRoot, "a", "b", "c", "d")
	_ = os.MkdirAll(filepath.Join(deepRepo, "repodata"), 0o755)

	// Scan with MaxDepth = 2 (should NOT reach deepRepo)
	s1 := NewScanner(ScanConfig{RootDir: tempRoot, MaxDepth: 2})
	repos1, err := s1.Scan()
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if len(repos1) != 0 {
		t.Errorf("expected 0 repos with MaxDepth=2, got %d", len(repos1))
	}

	// Scan with MaxDepth = 5 (should reach deepRepo)
	s2 := NewScanner(ScanConfig{RootDir: tempRoot, MaxDepth: 5})
	repos2, err := s2.Scan()
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if len(repos2) != 1 {
		t.Errorf("expected 1 repo with MaxDepth=5, got %d", len(repos2))
	}
}

func TestScanner_Errors(t *testing.T) {
	// Non-existent root
	s := NewScanner(ScanConfig{RootDir: filepath.Join(t.TempDir(), "does_not_exist")})
	if _, err := s.Scan(); err == nil {
		t.Errorf("expected error for non-existent root, got nil")
	}

	// File instead of directory
	tempFile := filepath.Join(t.TempDir(), "file.txt")
	_ = os.WriteFile(tempFile, []byte("test"), 0o644)
	sFile := NewScanner(ScanConfig{RootDir: tempFile})
	if _, err := sFile.Scan(); err == nil {
		t.Errorf("expected error when root is a file, got nil")
	}
}

func TestAggregateDebianSuites_MultiArch(t *testing.T) {
	repos := []*DiscoveredRepo{
		{
			Format:       models.FormatDEB,
			Distro:       "ubuntu",
			Suite:        "noble",
			Arch:         "amd64",
			Channel:      "main",
			Components:   []string{"main"},
			PackageCount: 100,
		},
		{
			Format:       models.FormatDEB,
			Distro:       "ubuntu",
			Suite:        "noble",
			Arch:         "amd64",
			Channel:      "universe",
			Components:   []string{"universe"},
			PackageCount: 250,
		},
		{
			Format:       models.FormatDEB,
			Distro:       "ubuntu",
			Suite:        "noble",
			Arch:         "arm64",
			Channel:      "main",
			Components:   []string{"main"},
			PackageCount: 95,
		},
	}

	aggregated := AggregateDebianSuites(repos)

	// Should have 2 cards: noble [amd64] and noble [arm64]
	if len(aggregated) != 2 {
		t.Fatalf("expected 2 aggregated cards, got %d", len(aggregated))
	}

	var amd64Card, arm64Card *DiscoveredRepo
	for _, card := range aggregated {
		switch card.Arch {
		case "amd64":
			amd64Card = card
		case "arm64":
			arm64Card = card
		}
	}

	if amd64Card == nil || arm64Card == nil {
		t.Fatalf("missing amd64 or arm64 card in aggregated results")
	}

	if amd64Card.PackageCount != 350 {
		t.Errorf("amd64Card.PackageCount = %d; want 350", amd64Card.PackageCount)
	}
	if len(amd64Card.Components) != 2 {
		t.Errorf("amd64Card.Components count = %d; want 2", len(amd64Card.Components))
	}
	if arm64Card.PackageCount != 95 {
		t.Errorf("arm64Card.PackageCount = %d; want 95", arm64Card.PackageCount)
	}
}

func TestScanner_SymlinkedDirectoryTree_DispersedStorage(t *testing.T) {
	// External storage mount simulation (completely outside portalRoot)
	externalStorage := t.TempDir()
	el8Path := filepath.Join(externalStorage, "el8", "base", "x86_64")
	_ = os.MkdirAll(filepath.Join(el8Path, "repodata"), 0o755)
	el9Path := filepath.Join(externalStorage, "el9", "base", "x86_64")
	_ = os.MkdirAll(filepath.Join(el9Path, "repodata"), 0o755)

	portalRoot := t.TempDir()
	// Local repo in portalRoot
	localRepo := filepath.Join(portalRoot, "local-repo")
	_ = os.MkdirAll(filepath.Join(localRepo, "repodata"), 0o755)

	// Symlink external storage tree into portalRoot
	symlinkMount := filepath.Join(portalRoot, "mirrors")
	if err := os.Symlink(externalStorage, symlinkMount); err != nil {
		t.Skipf("skipping symlink test on unsupported platform: %v", err)
	}

	scanner := NewScanner(ScanConfig{
		RootDir:  portalRoot,
		MaxDepth: 6,
	})

	repos, err := scanner.Scan()
	if err != nil {
		t.Fatalf("scanner.Scan() failed on symlinked directory tree: %v", err)
	}

	if len(repos) != 3 {
		t.Fatalf("expected 3 repositories (1 local + 2 from symlinked mount), got %d", len(repos))
	}

	relPathMap := make(map[string]*DiscoveredRepo)
	for _, r := range repos {
		relPathMap[r.RelPath] = r
		// Verify no self-aliasing
		for _, a := range r.Aliases {
			if a == r.RelPath {
				t.Errorf("repo %s self-aliased in Aliases: %v", r.RelPath, r.Aliases)
			}
		}
	}

	if _, ok := relPathMap["local-repo"]; !ok {
		t.Errorf("missing local-repo in scan results: %v", relPathMap)
	}

	el8Repo, ok := relPathMap["mirrors/el8/base/x86_64"]
	if !ok {
		t.Errorf("missing mirrors/el8/base/x86_64 in scan results: %v", relPathMap)
	} else {
		if strings.Contains(el8Repo.RelPath, "..") {
			t.Errorf("RelPath leaked out-of-tree relative escape: %s", el8Repo.RelPath)
		}
		if el8Repo.TargetURL != "mirrors/el8/base/x86_64/repoview/index.html" {
			t.Errorf("expected TargetURL 'mirrors/el8/base/x86_64/repoview/index.html', got %s", el8Repo.TargetURL)
		}
	}

	if _, ok := relPathMap["mirrors/el9/base/x86_64"]; !ok {
		t.Errorf("missing mirrors/el9/base/x86_64 in scan results: %v", relPathMap)
	}
}

func TestScanner_SymlinkSkipDirSiblingPreservation(t *testing.T) {
	portalRoot := t.TempDir()

	// repo1 (lexicographically first)
	repo1 := filepath.Join(portalRoot, "10-repo1")
	_ = os.MkdirAll(filepath.Join(repo1, "repodata"), 0o755)

	// Symlink matching exclusion pattern (lexicographically middle)
	outsideDir := t.TempDir()
	excludedSymlink := filepath.Join(portalRoot, "20-debuginfo-symlink")
	if err := os.Symlink(outsideDir, excludedSymlink); err != nil {
		t.Skipf("skipping symlink test on unsupported platform: %v", err)
	}

	// repo2 (lexicographically after excluded symlink)
	repo2 := filepath.Join(portalRoot, "30-repo2")
	_ = os.MkdirAll(filepath.Join(repo2, "repodata"), 0o755)

	scanner := NewScanner(ScanConfig{
		RootDir:         portalRoot,
		MaxDepth:        4,
		ExcludePatterns: []string{"*debuginfo*"},
	})

	repos, err := scanner.Scan()
	if err != nil {
		t.Fatalf("scanner.Scan() failed: %v", err)
	}

	if len(repos) != 2 {
		t.Fatalf("expected 2 repositories (sibling after excluded symlink preserved), got %d", len(repos))
	}

	foundRepo2 := false
	for _, r := range repos {
		if r.RelPath == "30-repo2" {
			foundRepo2 = true
		}
	}
	if !foundRepo2 {
		t.Errorf("sibling repo after excluded symlink was aborted!")
	}
}

func TestAggregateDebianSuites_MainPriority(t *testing.T) {
	// universe appears first in scan order, main appears second
	repos := []*DiscoveredRepo{
		{
			Format:     models.FormatDEB,
			Distro:     "ubuntu",
			Suite:      "noble",
			Arch:       "amd64",
			Channel:    "universe",
			Components: []string{"universe"},
			Path:       "/var/www/ubuntu/dists/noble/universe/binary-amd64",
			RelPath:    "ubuntu/dists/noble/universe/binary-amd64",
			TargetURL:  "ubuntu/dists/noble/universe/binary-amd64/repoview/index.html",
		},
		{
			Format:     models.FormatDEB,
			Distro:     "ubuntu",
			Suite:      "noble",
			Arch:       "amd64",
			Channel:    "main",
			Components: []string{"main"},
			Path:       "/var/www/ubuntu/dists/noble/main/binary-amd64",
			RelPath:    "ubuntu/dists/noble/main/binary-amd64",
			TargetURL:  "ubuntu/dists/noble/main/binary-amd64/repoview/index.html",
		},
	}

	aggregated := AggregateDebianSuites(repos)
	if len(aggregated) != 1 {
		t.Fatalf("expected 1 aggregated suite repo, got %d", len(aggregated))
	}

	card := aggregated[0]
	if card.RelPath != "ubuntu/dists/noble/main/binary-amd64" {
		t.Errorf("expected RelPath to prioritize main component, got %s", card.RelPath)
	}
	if card.TargetURL != "ubuntu/dists/noble/main/binary-amd64/repoview/index.html" {
		t.Errorf("expected TargetURL to prioritize main component, got %s", card.TargetURL)
	}
}

func TestDiscoveredRepo_BuildConfigSnippet(t *testing.T) {
	// 1. RPM without baseURL
	rpmRepo := &DiscoveredRepo{
		Format:  models.FormatRPM,
		Title:   "Enterprise Linux 9 Base",
		Channel: "base",
		RelPath: "el9/base/x86_64",
	}
	rpmSnippet := rpmRepo.BuildConfigSnippet("")
	expectedRPM := "[base]\nname=Enterprise Linux 9 Base\nbaseurl=el9/base/x86_64/\nenabled=1\ngpgcheck=0"
	if rpmSnippet != expectedRPM {
		t.Errorf("rpmSnippet = %q; want %q", rpmSnippet, expectedRPM)
	}

	// 2. RPM with baseURL
	rpmWithBase := rpmRepo.BuildConfigSnippet("https://repo.example.com")
	expectedRPMBase := "[base]\nname=Enterprise Linux 9 Base\nbaseurl=https://repo.example.com/el9/base/x86_64/\nenabled=1\ngpgcheck=0"
	if rpmWithBase != expectedRPMBase {
		t.Errorf("rpmWithBase = %q; want %q", rpmWithBase, expectedRPMBase)
	}

	// 3. Debian Suite with baseURL
	debRepo := &DiscoveredRepo{
		Format:     models.FormatDEB,
		Title:      "Ubuntu 24.04 (noble) [amd64]",
		Distro:     "ubuntu",
		Suite:      "noble",
		Arch:       "amd64",
		Components: []string{"main", "universe"},
		RelPath:    "ubuntu/dists/noble/main/binary-amd64",
	}
	debSnippet := debRepo.BuildConfigSnippet("https://repo.example.com")
	expectedDEB := "Types: deb\nURIs: https://repo.example.com/ubuntu/\nSuites: noble\nComponents: main universe"
	if debSnippet != expectedDEB {
		t.Errorf("debSnippet = %q; want %q", debSnippet, expectedDEB)
	}

	// 4. Flat Debian without baseURL
	flatDeb := &DiscoveredRepo{
		Format:  models.FormatDEB,
		Title:   "Flat Debian",
		Distro:  "ubuntu-flat",
		Arch:    "amd64",
		RelPath: "ubuntu-24.04-amd64",
	}
	flatSnippet := flatDeb.BuildConfigSnippet("")
	expectedFlat := "Types: deb\nURIs: ubuntu-24.04-amd64/\nSuites: ./"
	if flatSnippet != expectedFlat {
		t.Errorf("flatSnippet = %q; want %q", flatSnippet, expectedFlat)
	}
}
