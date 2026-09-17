package deb

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/edsilegxrepo/repoview/internal/models"
)

const samplePackagesContent = `Package: nginx
Version: 1.24.0-2
Architecture: amd64
Maintainer: Debian Nginx Maintainers <pkg-nginx-maintainers@example.com>
Installed-Size: 1500
Depends: libc6 (>= 2.34), libssl3 (>= 3.0.0)
Recommends: logrotate
Suggests: nginx-doc
Conflicts: nginx-light
Breaks: nginx-full (<< 1.20)
Replaces: nginx-common
Section: httpd
Filename: pool/main/n/nginx/nginx_1.24.0-2_amd64.deb
Size: 524288
SHA256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
Description: small, powerful, scalable web/proxy server
 Nginx is a web server which can also be used as a
 reverse proxy, load balancer, mail proxy and HTTP cache.

Package: curl
Version: 8.5.0-1
Architecture: amd64
Maintainer: Debian cURL Team <curl@example.com>
Installed-Size: 450
Depends: libc6 (>= 2.34), libcurl4 (= 8.5.0-1)
Section: web
Filename: pool/main/c/curl/curl_8.5.0-1_amd64.deb
Size: 262144
SHA256: abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789
Description: command line tool for transferring data with URL syntax
 curl is a tool for transferring data from or to a server.
`

func TestDebRepository_GetAllPackages(t *testing.T) {
	tmpDir := t.TempDir()
	pkgPath := filepath.Join(tmpDir, "Packages")
	if err := os.WriteFile(pkgPath, []byte(samplePackagesContent), 0o644); err != nil {
		t.Fatalf("failed to write Packages: %v", err)
	}

	// Also create a sample deb file on disk to test TimeBuild extraction in GetAllPackages
	debFilePath := filepath.Join(tmpDir, "pool/main/n/nginx/nginx_1.24.0-2_amd64.deb")
	_ = os.MkdirAll(filepath.Dir(debFilePath), 0o755)
	_ = os.WriteFile(debFilePath, []byte("!<arch>\ndebian-binary   1786060748  0     0     100644  4         `\n2.0\n"), 0o644)

	locs := &DebRepoLocations{
		BaseDir:      tmpDir,
		PackagesFile: pkgPath,
		IsFlat:       false,
	}

	repoReader := NewDebRepository(locs, nil)
	defer func() { _ = repoReader.Close() }()

	pkgs, err := repoReader.GetAllPackages()
	if err != nil {
		t.Fatalf("GetAllPackages failed: %v", err)
	}

	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(pkgs))
	}

	// Test nginx
	nginx := pkgs[0]
	if nginx.Name != "nginx" {
		t.Errorf("expected nginx, got %s", nginx.Name)
	}
	if nginx.Format != models.FormatDEB {
		t.Errorf("expected FormatDEB, got %s", nginx.Format)
	}
	if nginx.Version != "1.24.0" || nginx.Release != "2" {
		t.Errorf("expected version 1.24.0-2, got %s-%s", nginx.Version, nginx.Release)
	}
	if nginx.Arch != "amd64" {
		t.Errorf("expected arch amd64, got %s", nginx.Arch)
	}
	if nginx.Summary != "small, powerful, scalable web/proxy server" {
		t.Errorf("unexpected summary: %s", nginx.Summary)
	}
	if nginx.InstalledSize != 1500*1024 {
		t.Errorf("expected installed size %d, got %d", 1500*1024, nginx.InstalledSize)
	}
	if nginx.SizePackage != 524288 {
		t.Errorf("expected package size 524288, got %d", nginx.SizePackage)
	}
	if nginx.Section != "httpd" {
		t.Errorf("expected section httpd, got %s", nginx.Section)
	}
	if nginx.TimeBuild != 1786060748 {
		t.Errorf("expected TimeBuild 1786060748, got %d", nginx.TimeBuild)
	}

	// Verify dependencies
	if nginx.Dependencies == nil {
		t.Fatalf("expected nginx dependencies to be populated")
	}
	if len(nginx.Dependencies.Requires) != 2 {
		t.Errorf("expected 2 requires (Depends), got %d", len(nginx.Dependencies.Requires))
	}
	if len(nginx.Dependencies.Recommends) != 1 {
		t.Errorf("expected 1 recommends, got %d", len(nginx.Dependencies.Recommends))
	}
	if len(nginx.Dependencies.Suggests) != 1 {
		t.Errorf("expected 1 suggests, got %d", len(nginx.Dependencies.Suggests))
	}
	// Conflicts includes Conflicts + Breaks = 2
	if len(nginx.Dependencies.Conflicts) != 2 {
		t.Errorf("expected 2 conflicts (Conflicts + Breaks), got %d", len(nginx.Dependencies.Conflicts))
	}
	if len(nginx.Dependencies.Obsoletes) != 1 {
		t.Errorf("expected 1 obsoletes (Replaces), got %d", len(nginx.Dependencies.Obsoletes))
	}

	// Test GetPackageDependencies via reader
	deps, err := repoReader.GetPackageDependencies(nginx.PkgKey)
	if err != nil {
		t.Fatalf("GetPackageDependencies failed: %v", err)
	}
	if deps == nil || len(deps.Requires) != 2 {
		t.Errorf("expected cached dependencies via GetPackageDependencies")
	}
}

func TestDebRepository_GzippedIndex(t *testing.T) {
	tmpDir := t.TempDir()
	pkgPath := filepath.Join(tmpDir, "Packages.gz")
	f, err := os.Create(pkgPath)
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	gw := gzip.NewWriter(f)
	if _, err := gw.Write([]byte(samplePackagesContent)); err != nil {
		t.Fatalf("failed to write gzip: %v", err)
	}
	_ = gw.Close()
	_ = f.Close()

	locs := &DebRepoLocations{
		BaseDir:      tmpDir,
		PackagesFile: pkgPath,
		IsFlat:       true,
	}

	repoReader := NewDebRepository(locs, nil)
	defer func() { _ = repoReader.Close() }()

	pkgs, err := repoReader.GetAllPackages()
	if err != nil {
		t.Fatalf("GetAllPackages with gzip failed: %v", err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages from gzipped index, got %d", len(pkgs))
	}
}

func TestDebRepository_EdgeCasesAndErrors(t *testing.T) {
	// 1. nil locs
	r1 := NewDebRepository(nil, nil)
	if _, err := r1.GetAllPackages(); err == nil {
		t.Errorf("expected error with nil locs, got nil")
	}

	// 2. empty PackagesFile
	r2 := NewDebRepository(&DebRepoLocations{}, nil)
	if _, err := r2.GetAllPackages(); err == nil {
		t.Errorf("expected error with empty PackagesFile, got nil")
	}

	// 3. Close with cleanup hook
	cleanedUp := false
	r3 := NewDebRepository(nil, func() { cleanedUp = true })
	_ = r3.Close()
	if !cleanedUp {
		t.Errorf("expected cleanup closure to be executed on Close()")
	}

	// 4. EnrichPackagesWithChangelogs & GetChangelogForPackage
	debRepo := r3.(*DebRepository)
	if err := debRepo.EnrichPackagesWithChangelogs(nil); err != nil {
		t.Errorf("expected nil from EnrichPackagesWithChangelogs")
	}
	if cl, err := debRepo.GetChangelogForPackage(123); err != nil || cl != nil {
		t.Errorf("expected nil changelog and nil error, got cl=%v err=%v", cl, err)
	}

	// 5. ReadPackageFiles with empty location href
	emptyHrefPkg := &models.Package{LocationHref: ""}
	files, err := debRepo.ReadPackageFiles(t.TempDir(), emptyHrefPkg)
	if err != nil || len(files) != 0 {
		t.Errorf("expected nil files and error for empty href, got files=%v err=%v", files, err)
	}

	// 6. ReadPackageFiles with missing or path-traversing file
	evilPkg := &models.Package{LocationHref: "../../evil.deb"}
	if _, err := debRepo.ReadPackageFiles(t.TempDir(), evilPkg); err == nil {
		t.Errorf("expected error resolving traversal path, got nil")
	}

	// 7. Non-existent deb
	missingPkg := &models.Package{LocationHref: "nonexistent.deb"}
	if _, err := debRepo.ReadPackageFiles(t.TempDir(), missingPkg); err == nil {
		t.Errorf("expected error for missing deb file, got nil")
	}
}

func TestDebRepository_MultiComponentAggregation(t *testing.T) {
	tmpDir := t.TempDir()

	// Packages in component 'main'
	pkg1Content := `Package: app-main
Version: 1.0.0-1
Architecture: amd64
Section: main/web
Filename: pool/main/a/app-main/app-main_1.0.0-1_amd64.deb
Size: 1024
Description: app in main component
`
	pkg1Path := filepath.Join(tmpDir, "Packages_main")
	if err := os.WriteFile(pkg1Path, []byte(pkg1Content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Packages in component 'universe' (includes a duplicate of app-main to test deduplication)
	pkg2Content := `Package: app-universe
Version: 2.0.0-1
Architecture: amd64
Section: universe/utils
Filename: pool/universe/a/app-universe/app-universe_2.0.0-1_amd64.deb
Size: 2048
Description: app in universe component

Package: app-main
Version: 1.0.0-1
Architecture: amd64
Section: main/web
Filename: pool/main/a/app-main/app-main_1.0.0-1_amd64.deb
Size: 1024
Description: app in main component duplicate
`
	pkg2Path := filepath.Join(tmpDir, "Packages_universe")
	if err := os.WriteFile(pkg2Path, []byte(pkg2Content), 0o644); err != nil {
		t.Fatal(err)
	}

	locs := &DebRepoLocations{
		BaseDir:       tmpDir,
		PackagesFile:  pkg1Path,
		PackagesFiles: []string{pkg1Path, pkg2Path},
		Components:    []string{"main", "universe"},
		Suite:         "noble",
		Arch:          "amd64",
	}

	repoReader := NewDebRepository(locs, nil)
	defer func() { _ = repoReader.Close() }()

	pkgs, err := repoReader.GetAllPackages()
	if err != nil {
		t.Fatalf("GetAllPackages with multi-component failed: %v", err)
	}

	if len(pkgs) != 2 {
		t.Fatalf("expected exactly 2 deduplicated packages across components, got %d", len(pkgs))
	}

	if pkgs[0].Name != "app-main" || pkgs[0].PkgKey != 1 {
		t.Errorf("expected first package app-main with key 1, got name=%s key=%d", pkgs[0].Name, pkgs[0].PkgKey)
	}
	if pkgs[1].Name != "app-universe" || pkgs[1].PkgKey != 2 {
		t.Errorf("expected second package app-universe with key 2, got name=%s key=%d", pkgs[1].Name, pkgs[1].PkgKey)
	}
}
