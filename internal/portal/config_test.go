package portal

// OBJECTIVES:
// Verify portal.yaml loading, override path normalization, hidden/pinned flags,
// parent directory climbing, and --dump-config non-clobber/merge behavior.
//
// TEST STRATEGY:
// Uses t.TempDir() fixtures covering YAML overrides, cross-platform slash normalization,
// declared local/external repos, and full round-trip config dumping with and without --force.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edsilegxrepo/repoview/internal/models"
)

func TestPortalConfig_OverrideNormalizationAndHidden(t *testing.T) {
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "portal.yaml")
	yamlData := `title: Enterprise Hub
overrides:
  el9\base\x86_64:
    title: Enterprise Linux 9 BaseOS
    distro: RHEL 9
    channel: baseos
    badge: Production
  hidden-repo:
    hidden: true
repositories:
  - path: custom/path
    title: Declared Local
`
	if err := os.WriteFile(cfgPath, []byte(yamlData), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if len(cfg.Repositories) != 1 || cfg.Repositories[0].Path != "custom/path" {
		t.Errorf("expected 1 declared repository entry, got %+v", cfg.Repositories)
	}

	ov, ok := cfg.LookupOverride("el9/base/x86_64", "/var/repos/el9/base/x86_64")
	if !ok || ov.Title != "Enterprise Linux 9 BaseOS" || ov.Badge != "Production" {
		t.Fatalf("expected LookupOverride to normalize backslash key, got ok=%v ov=%+v", ok, ov)
	}

	catalog := BuildCatalog(cfg.Title, cfg.Description, "", []*DiscoveredRepo{
		{RelPath: "el9/base/x86_64", Path: "/var/repos/el9/base/x86_64", Format: models.FormatRPM, Distro: "el9", PackageCount: 10},
		{RelPath: "hidden-repo", Path: "/var/repos/hidden-repo", Format: models.FormatRPM, Distro: "el9", PackageCount: 5},
	})
	ApplyConfig(catalog, cfg)

	if catalog.TotalRepos != 2 {
		t.Fatalf("expected 2 repos (el9/base/x86_64 + declared custom/path, with hidden-repo excluded), got %d", catalog.TotalRepos)
	}
	for _, r := range catalog.Repos {
		if r.RelPath == "hidden-repo" {
			t.Errorf("expected hidden-repo to be excluded from catalog")
		}
	}
	if catalog.Repos[0].Title != "Enterprise Linux 9 BaseOS" || catalog.Repos[0].Channel != "baseos" {
		t.Errorf("expected ApplyConfig overrides applied, got %+v", catalog.Repos[0])
	}
	if catalog.Repos[1].Title != "Declared Local" {
		t.Errorf("expected declared local repository to be appended, got %+v", catalog.Repos[1])
	}
}

func TestScanner_IsExcludedGlobVsSubstringAndWarnings(t *testing.T) {
	root := t.TempDir()
	// "latest" MUST NOT be excluded by ExcludePatterns: ["test"]
	latestRepo := filepath.Join(root, "latest")
	excludedRepo := filepath.Join(root, "test")
	_ = os.MkdirAll(filepath.Join(latestRepo, "repodata"), 0o755)
	_ = os.MkdirAll(filepath.Join(excludedRepo, "repodata"), 0o755)

	s := NewScanner(ScanConfig{
		RootDir:         root,
		MaxDepth:        4,
		ExcludePatterns: []string{"test"},
	})
	repos, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(repos) != 1 || repos[0].RelPath != "latest" {
		t.Fatalf("expected only 'latest' repo (not blocked by 'test' exclude pattern), got %+v", repos)
	}
	_ = s.Warnings()
}

func TestRenderer_DetachedRootLatestFeedResolution(t *testing.T) {
	rawRoot := t.TempDir()
	portalHome := t.TempDir()

	// Write latest-feed.xml under detached portalHome/repoview/latest-feed.xml (root repo RelPath == ".")
	childFeedDir := filepath.Join(portalHome, "repoview")
	if err := os.MkdirAll(childFeedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	childRSS := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>Root</title><item>
  <title>nginx 1.26.0</title><link>nginx.html</link><guid>nginx-1</guid><pubDate>Thu, 15 Jan 2026 12:00:00 +0000</pubDate>
</item></channel></rss>`
	if err := os.WriteFile(filepath.Join(childFeedDir, "latest-feed.xml"), []byte(childRSS), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog := &Catalog{
		Title: "Detached Hub",
		Repos: []*DiscoveredRepo{
			{
				Title:      "Root Repo",
				RelPath:    ".",
				Path:       rawRoot,
				TargetURL:  "repoview/index.html",
				Format:     models.FormatRPM,
				IsRendered: true,
			},
		},
	}

	r, err := NewRenderer("")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.WritePortal(portalHome, rawRoot, catalog, false); err != nil {
		t.Fatalf("WritePortal failed: %v", err)
	}
	feedBytes, err := os.ReadFile(filepath.Join(portalHome, "portal-feed.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(feedBytes), "repoview/nginx.html") {
		t.Errorf("expected detached portal-feed.xml to resolve link to repoview/nginx.html, got:\n%s", string(feedBytes))
	}
}

func TestPortalConfig_DumpFindPinnedAndSafetyErrors(t *testing.T) {
	dir := t.TempDir()
	dumpPath := filepath.Join(dir, "portal.yaml")

	cat := createMockCatalog()
	if err := DumpConfig(dumpPath, cat); err != nil {
		t.Fatalf("DumpConfig failed: %v", err)
	}
	found, err := FindPortalConfigIn(dir)
	if err != nil || found != dumpPath {
		t.Fatalf("FindPortalConfigIn failed: found=%q err=%v", found, err)
	}

	cfg := DefaultPortalConfig()
	cfg.Pinned = []string{"ubuntu/dists/noble", "el9/base/x86_64"}
	cfg.Overrides["ubuntu/dists/noble"] = RepoOverrideConfig{
		Title:         "Ubuntu Pinned",
		Description:   "LTS packages",
		Distro:        "ubuntu",
		Channel:       "main",
		Suite:         "noble",
		Arch:          "amd64",
		Format:        "deb",
		BaseURL:       "https://apt.example.com",
		GPGKey:        "https://apt.example.com/key.gpg",
		IgnorePackage: []string{"*-dbg"},
		ExcludeArch:   []string{"i386"},
		Comps:         "/tmp/comps.xml",
	}
	ApplyConfig(cat, cfg)
	if cat.Repos[0].RelPath != "ubuntu/dists/noble" || cat.Repos[0].Title != "Ubuntu Pinned" {
		t.Errorf("expected Pinned repo ubuntu/dists/noble first with overrides, got %+v", cat.Repos[0])
	}

	safety := &ErrSafetyOverwrite{Path: "/tmp/index.html"}
	if !strings.Contains(safety.Error(), "index.html") || safety.Unwrap() != models.ErrSafety {
		t.Errorf("unexpected ErrSafetyOverwrite behavior: %q / %v", safety.Error(), safety.Unwrap())
	}
}

func TestPortal_CustomTemplateAndMultiRepoDiscoverySort(t *testing.T) {
	// Custom template renderer coverage
	tmplDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmplDir, "portal.html"), []byte("CUSTOM: {{ .Catalog.Title }}"), 0o644)
	cr, err := NewRenderer(tmplDir)
	if err != nil {
		t.Fatalf("NewRenderer(custom) failed: %v", err)
	}
	out, err := cr.RenderHTML(createMockCatalog())
	if err != nil || !strings.Contains(string(out), "CUSTOM:") {
		t.Errorf("custom template RenderHTML failed: %v / %s", err, string(out))
	}
	if _, err := NewRenderer(filepath.Join(t.TempDir(), "nonexistent")); err == nil {
		t.Errorf("expected error for missing custom template dir")
	}

	// Multi-repo scan covering Debian meta/Packages arch sniffing and sortRepos ordering
	root := t.TempDir()
	debSuite := filepath.Join(root, "debian", "dists", "bookworm")
	_ = os.MkdirAll(filepath.Join(debSuite, "main", "binary-amd64"), 0o755)
	_ = os.WriteFile(filepath.Join(debSuite, "Release"), []byte("Origin: Debian\nSuite: bookworm\nCodename: bookworm\nArchitectures: amd64\nComponents: main\n"), 0o644)
	_ = os.WriteFile(filepath.Join(debSuite, "main", "binary-amd64", "Packages"), []byte("Package: vim\nVersion: 9.0\nArchitecture: amd64\n"), 0o644)

	rpmBase := filepath.Join(root, "el9", "base", "x86_64")
	rpmArm := filepath.Join(root, "el9", "base", "aarch64")
	rpmExtras := filepath.Join(root, "el9", "extras", "x86_64")
	for _, p := range []string{rpmBase, rpmArm, rpmExtras} {
		_ = os.MkdirAll(filepath.Join(p, "repodata"), 0o755)
	}

	s := NewScanner(ScanConfig{RootDir: root, MaxDepth: 6})
	repos, err := s.Scan()
	if err != nil {
		t.Fatalf("multi-repo Scan failed: %v", err)
	}
	if len(repos) < 4 {
		t.Fatalf("expected >=4 discovered repos, got %d", len(repos))
	}

	for _, r := range repos {
		if u := r.RepoDirURL("https://repo.example.com"); u == "" {
			t.Errorf("expected non-empty RepoDirURL for %s", r.RelPath)
		}
		if r.Format == models.FormatDEB {
			if u := r.DebArchiveURL("https://repo.example.com"); u == "" {
				t.Errorf("expected non-empty DebArchiveURL for DEB repo")
			}
			if r.Arch != "amd64" || sniffFormat(filepath.Join(root, filepath.FromSlash(r.RelPath))) != models.FormatDEB {
				t.Errorf("expected amd64 DEB repo detection, got format=%s arch=%s", r.Format, r.Arch)
			}
		}
	}

	// Verify flat DEB Release + Packages arch inference and RequireRendered skip
	flatRoot := t.TempDir()
	flatRepo := filepath.Join(flatRoot, "flat-apt-channel")
	_ = os.MkdirAll(flatRepo, 0o755)
	_ = os.WriteFile(filepath.Join(flatRepo, "Release"), []byte("Origin: Custom\nArchitectures: riscv64\n"), 0o644)
	_ = os.WriteFile(filepath.Join(flatRepo, "Packages"), []byte("Package: tool\nVersion: 1.0\nArchitecture: riscv64\n"), 0o644)

	sFlat := NewScanner(ScanConfig{RootDir: flatRoot, MaxDepth: 4})
	flatRepos, err := sFlat.Scan()
	if err != nil || len(flatRepos) != 1 || flatRepos[0].Arch != "riscv64" {
		t.Fatalf("expected flat DEB repo with riscv64 arch from Release, got repos=%+v err=%v", flatRepos, err)
	}
	// RequireRendered=true excludes unrendered repo
	sReq := NewScanner(ScanConfig{RootDir: flatRoot, MaxDepth: 4, RequireRendered: true})
	if reqRepos, err := sReq.Scan(); err != nil || len(reqRepos) != 0 {
		t.Errorf("expected 0 rendered repos when RequireRendered=true, got %d (err=%v)", len(reqRepos), err)
	}

	// Verify LocalRepos inference when Title/Distro/Arch/Format omitted
	cat2 := BuildCatalog("T", "D", "", nil)
	cfg2 := DefaultPortalConfig()
	cfg2.Repositories = []LocalRepoConfig{{Path: "centos-9-extras.aarch64"}, {Path: "skip-hidden", Hidden: true}}
	ApplyConfig(cat2, cfg2)
	if len(cat2.Repos) != 1 || cat2.Repos[0].Arch != "aarch64" {
		t.Errorf("expected 1 inferred declared local repo aarch64, got %+v", cat2.Repos)
	}

	// Verify pinnedOrder matching via r.Path, plus .deb/.rpm & InRelease arch sniffing
	pkgOnlyRoot := t.TempDir()
	pkgOnlyRepo := filepath.Join(pkgOnlyRoot, "custom-pkg-only")
	_ = os.MkdirAll(pkgOnlyRepo, 0o755)
	_ = os.WriteFile(filepath.Join(pkgOnlyRepo, "Packages"), []byte("Package: jq\nVersion: 1.7\n"), 0o644)
	_ = os.WriteFile(filepath.Join(pkgOnlyRepo, "jq_1.7_riscv64.deb"), []byte("dummy"), 0o644)
	_ = os.WriteFile(filepath.Join(pkgOnlyRepo, "curl-8.0-1.riscv64.rpm"), []byte("dummy"), 0o644)
	_ = os.WriteFile(filepath.Join(pkgOnlyRepo, "InRelease"), []byte("Architecture: riscv64\n"), 0o644)
	sPkgOnly := NewScanner(ScanConfig{RootDir: pkgOnlyRoot, MaxDepth: 4})
	pkgRepos, err := sPkgOnly.Scan()
	if err != nil || len(pkgRepos) != 1 || pkgRepos[0].Arch != "riscv64" {
		t.Fatalf("expected riscv64 arch from InRelease/package files, got %+v err=%v", pkgRepos, err)
	}
	if got := detectArchFromPackageFiles(pkgOnlyRepo); got != "riscv64" {
		t.Errorf("expected detectArchFromPackageFiles = riscv64, got %q", got)
	}
	linkAlias := filepath.Join(pkgOnlyRoot, "alias-repo")
	if err := os.Symlink(pkgOnlyRepo, linkAlias); err == nil {
		if _, err := NewScanner(ScanConfig{RootDir: pkgOnlyRoot, MaxDepth: 4}).Scan(); err != nil {
			t.Errorf("unexpected error scanning symlinked alias: %v", err)
		}
	}
	catPin := BuildCatalog("T", "D", "", []*DiscoveredRepo{
		{Path: "/p/b", RelPath: "b", Format: models.FormatRPM},
		{Path: "/p/a", RelPath: "a", Format: models.FormatRPM},
	})
	cfgPin := DefaultPortalConfig()
	cfgPin.Pinned = []string{"/p/a", "nonexistent"}
	ApplyConfig(catPin, cfgPin)
	if catPin.Repos[0].Path != "/p/a" {
		t.Errorf("expected /p/a pinned first by Path, got %+v", catPin.Repos[0])
	}
}

func TestDumpConfigWithOptions_FullMetadataNonClobberAndMerge(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "portal.yaml")

	baseCfg := DefaultPortalConfig()
	baseCfg.PortalURL = "https://portal.corp.local"
	baseCfg.RepoURL = "https://mirrors.corp.local"
	baseCfg.PortalHome = "/srv/portal-home"
	baseCfg.Overrides["el9/base/x86_64"] = RepoOverrideConfig{
		Title:         "Hand-Tuned EL9",
		Badge:         "Custom",
		IgnorePackage: []string{"*-debuginfo"},
	}
	baseCfg.Overrides["hidden/channel"] = RepoOverrideConfig{
		Hidden: true,
	}

	rawRepos := []*DiscoveredRepo{
		{
			RelPath:     "el9/base/x86_64",
			Path:        "/mirrors/el9/base/x86_64",
			Title:       "el9 base",
			Distro:      "el9",
			Channel:     "base",
			Arch:        "x86_64",
			Format:      models.FormatRPM,
			Description: "Base packages",
		},
		{
			RelPath:    "ubuntu/dists/noble/main/binary-amd64",
			Path:       "/mirrors/ubuntu/dists/noble/main/binary-amd64",
			Title:      "ubuntu noble",
			Distro:     "ubuntu",
			Channel:    "main",
			Suite:      "noble",
			Components: []string{"main", "universe"},
			Arch:       "amd64",
			Format:     models.FormatDEB,
		},
		{
			RelPath: "hidden/channel",
			Path:    "/mirrors/hidden/channel",
			Distro:  "el8",
			Channel: "hidden",
			Arch:    "x86_64",
			Format:  models.FormatRPM,
		},
	}

	cat := BuildCatalog("My Hub", "Hub Desc", baseCfg.PortalURL, rawRepos)
	if err := DumpConfigWithOptions(outPath, cat, DumpConfigOptions{
		ExistingConfig: baseCfg,
		RawRepos:       rawRepos,
	}); err != nil {
		t.Fatalf("DumpConfigWithOptions failed: %v", err)
	}

	// Verify non-clobber guard when Force is false
	if err := DumpConfigWithOptions(outPath, cat, DumpConfigOptions{
		ExistingConfig: baseCfg,
		RawRepos:       rawRepos,
		Force:          false,
	}); err == nil {
		t.Fatalf("expected error overwriting existing file without Force=true")
	}

	// Verify Force=true reloads & merges safely without losing hand-tuned overrides
	loaded, err := LoadConfig(outPath)
	if err != nil {
		t.Fatalf("LoadConfig on dumped portal.yaml failed: %v", err)
	}
	if loaded.PortalURL != "https://portal.corp.local" || loaded.RepoURL != "https://mirrors.corp.local" || loaded.PortalHome != "/srv/portal-home" {
		t.Errorf("expected preserved portal CLI/config fields, got %+v", loaded)
	}
	if len(loaded.Pinned) != 0 {
		t.Errorf("expected no unconditional auto-pinned entries, got %v", loaded.Pinned)
	}

	el9Ov, ok := loaded.Overrides["el9/base/x86_64"]
	if !ok {
		t.Fatalf("expected el9/base/x86_64 override")
	}
	if el9Ov.Title != "Hand-Tuned EL9" || el9Ov.Badge != "Custom" || len(el9Ov.IgnorePackage) != 1 {
		t.Errorf("expected existing hand-tuned fields preserved on merge, got %+v", el9Ov)
	}
	if el9Ov.Distro != "el9" || el9Ov.Channel != "base" || el9Ov.Arch != "x86_64" || el9Ov.Format != "rpm" || el9Ov.Icon != "redhat" {
		t.Errorf("expected missing inferred metadata populated cleanly, got %+v", el9Ov)
	}

	ubuOv, ok := loaded.Overrides["ubuntu/dists/noble/main/binary-amd64"]
	if !ok || ubuOv.Suite != "noble" || len(ubuOv.Components) != 2 || ubuOv.Badge != "" {
		t.Errorf("expected full inferred DEB metadata without hardcoded Production badge, got %+v", ubuOv)
	}

	if hidOv, ok := loaded.Overrides["hidden/channel"]; !ok || !hidOv.Hidden {
		t.Errorf("expected hidden override preserved, got ok=%v ov=%+v", ok, hidOv)
	}

	// Verify overwrite with Force=true when ExistingConfig is nil merges directly from disk
	if err := DumpConfigWithOptions(outPath, BuildCatalog("", "", "", []*DiscoveredRepo{
		{RelPath: "deb13/custom", Distro: "deb13", Channel: "custom", Arch: "amd64", Format: models.FormatDEB},
	}), DumpConfigOptions{Force: true}); err != nil {
		t.Fatalf("DumpConfigWithOptions(Force=true) failed: %v", err)
	}
	mergedReloaded, err := LoadConfig(outPath)
	if err != nil {
		t.Fatalf("LoadConfig after merge failed: %v", err)
	}
	if _, ok := mergedReloaded.Overrides["el9/base/x86_64"]; !ok {
		t.Errorf("expected previous el9 override retained during Force merge")
	}
	if debOv, ok := mergedReloaded.Overrides["deb13/custom"]; !ok || debOv.Icon != "debian" {
		t.Errorf("expected newly discovered deb13/custom merged with debian icon, got %+v", debOv)
	}
}
