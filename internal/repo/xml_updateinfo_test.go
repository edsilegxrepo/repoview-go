package repo

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/edsilegxrepo/repoview/internal/models"
)

func writeGzipFile(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	gz := gzip.NewWriter(f)
	if _, err := gz.Write(data); err != nil {
		t.Fatalf("failed to write gzip %s: %v", path, err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("failed to close gzip %s: %v", path, err)
	}
}

func TestXMLRepositoryAndUpdateInfo(t *testing.T) {
	repoDir := t.TempDir()
	repodata := filepath.Join(repoDir, "repodata")
	if err := os.MkdirAll(repodata, 0o755); err != nil {
		t.Fatal(err)
	}

	primaryXML := `<?xml version="1.0" encoding="UTF-8"?>
<metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm" packages="1">
  <package type="rpm">
    <name>openssl</name>
    <arch>x86_64</arch>
    <version epoch="1" ver="3.0.7" rel="27.el9"/>
    <checksum type="sha256" pkgid="YES">deadbeef123456</checksum>
    <summary>Utilities from the general purpose cryptography library</summary>
    <description>The OpenSSL toolkit</description>
    <packager>Builder Bot</packager>
    <url>https://www.openssl.org/</url>
    <time file="1700000100" build="1700000000"/>
    <size package="12345" installed="45678" archive="50000"/>
    <location href="Packages/o/openssl-3.0.7-27.el9.x86_64.rpm"/>
    <format>
      <rpm:license>Apache-2.0</rpm:license>
      <rpm:vendor>Enterprise Linux</rpm:vendor>
      <rpm:group>Applications/System</rpm:group>
      <rpm:buildhost>build01.example.com</rpm:buildhost>
      <rpm:sourcerpm>openssl-3.0.7-27.el9.src.rpm</rpm:sourcerpm>
      <rpm:requires>
        <rpm:entry name="libc.so.6()(64bit)"/>
      </rpm:requires>
      <rpm:provides>
        <rpm:entry name="openssl" flags="EQ" epoch="1" ver="3.0.7" rel="27.el9"/>
      </rpm:provides>
      <rpm:recommends>
        <rpm:entry name="ca-certificates"/>
      </rpm:recommends>
      <rpm:suggests>
        <rpm:entry name="openssl-perl"/>
      </rpm:suggests>
    </format>
  </package>
</metadata>`

	otherXML := `<?xml version="1.0" encoding="UTF-8"?>
<otherdata xmlns="http://linux.duke.edu/metadata/other" packages="1">
  <package pkgid="deadbeef123456" name="openssl" arch="x86_64">
    <version epoch="1" ver="3.0.7" rel="27.el9"/>
    <changelog author="Security Team &lt;sec@example.com&gt; - 1:3.0.7-27" date="1700000000">- Fix CVE-2026-1234</changelog>
    <changelog author="Maintainer &lt;dev@example.com&gt; - 1:3.0.7-26" date="1699000000">- Rebase to 3.0.7</changelog>
  </package>
</otherdata>`

	updateInfoXML := `<?xml version="1.0" encoding="UTF-8"?>
<updates>
  <update from="sec@example.com" status="final" type="security" version="2">
    <id>ELSA-2026:0001</id>
    <title>Important: openssl security update</title>
    <severity>Important</severity>
    <issued date="2026-01-15 12:00:00"/>
    <references>
      <reference href="https://example.com/CVE-2026-1234" id="CVE-2026-1234" type="cve"/>
    </references>
    <pkglist>
      <collection short="el9">
        <package name="openssl" version="3.0.7" release="27.el9" epoch="1" arch="x86_64"/>
      </collection>
    </pkglist>
  </update>
</updates>`

	primaryPath := filepath.Join(repodata, "primary.xml.gz")
	otherPath := filepath.Join(repodata, "other.xml.gz")
	updatePath := filepath.Join(repodata, "updateinfo.xml.gz")
	writeGzipFile(t, primaryPath, []byte(primaryXML))
	writeGzipFile(t, otherPath, []byte(otherXML))
	writeGzipFile(t, updatePath, []byte(updateInfoXML))

	repomdXML := `<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <revision>20260115</revision>
  <data type="primary">
    <location href="repodata/primary.xml.gz"/>
  </data>
  <data type="other">
    <location href="repodata/other.xml.gz"/>
  </data>
  <data type="updateinfo">
    <location href="repodata/updateinfo.xml.gz"/>
  </data>
</repomd>`
	if err := os.WriteFile(filepath.Join(repodata, "repomd.xml"), []byte(repomdXML), 0o644); err != nil {
		t.Fatal(err)
	}

	reader, meta, cleanup, err := OpenWithOptions(repoDir, OpenOptions{Format: models.FormatRPM})
	if err != nil {
		t.Fatalf("OpenWithOptions RPM XML fallback failed: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	defer func() { _ = reader.Close() }()

	if meta == nil || meta.Revision != "20260115" {
		t.Errorf("expected repomd Revision 20260115, got %+v", meta)
	}

	pkgs, err := reader.GetAllPackages()
	if err != nil {
		t.Fatalf("GetAllPackages failed: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 package, got %d", len(pkgs))
	}
	pkg := pkgs[0]
	if pkg.SHA256 != "deadbeef123456" || pkg.ChecksumType != "sha256" {
		t.Errorf("unexpected checksum fields: sha256=%q type=%q", pkg.SHA256, pkg.ChecksumType)
	}
	if pkg.Packager != "Builder Bot" || pkg.SizeArchive != 50000 || pkg.TimeFile != 1700000100 {
		t.Errorf("unexpected extended fields: packager=%q archive=%d timeFile=%d", pkg.Packager, pkg.SizeArchive, pkg.TimeFile)
	}

	ce, ok := reader.(ChangelogEnricher)
	if !ok {
		t.Fatalf("expected XMLRepository to implement ChangelogEnricher")
	}
	if err := ce.EnrichPackagesWithChangelogs(pkgs); err != nil {
		t.Fatalf("EnrichPackagesWithChangelogs failed: %v", err)
	}
	ae, ok := reader.(AdvisoryEnricher)
	if !ok {
		t.Fatalf("expected XMLRepository to implement AdvisoryEnricher")
	}
	if err := ae.EnrichPackagesWithAdvisories(pkgs); err != nil {
		t.Fatalf("EnrichPackagesWithAdvisories failed: %v", err)
	}
	reader.EnrichPackageDetails(pkgs)
	if len(pkg.Changelogs) != 2 || pkg.Changelog == nil {
		t.Errorf("expected 2 multi-entry changelogs and latest changelog, got %d / %v", len(pkg.Changelogs), pkg.Changelog)
	}
	if len(pkg.Advisories) != 1 || pkg.Advisories[0].ID != "ELSA-2026:0001" || len(pkg.Advisories[0].CVEs) != 1 || pkg.Advisories[0].CVEs[0] != "CVE-2026-1234" {
		t.Errorf("expected CVE-2026-1234 advisory attached, got %+v", pkg.Advisories)
	}

	dr, ok := reader.(DependencyReader)
	if !ok {
		t.Fatalf("expected XMLRepository to implement DependencyReader")
	}
	deps, err := dr.GetPackageDependencies(pkg.PkgKey)
	if err != nil {
		t.Fatalf("GetPackageDependencies failed: %v", err)
	}
	if len(deps.Requires) != 1 || len(deps.Recommends) != 1 || len(deps.Suggests) != 1 {
		t.Errorf("unexpected XML deps breakdown: %+v", deps)
	}
}
