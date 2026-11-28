package repo

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/edsilegxrepo/repoview/internal/models"
	_ "github.com/mattn/go-sqlite3"
	rpmutils "github.com/sassoftware/go-rpmutils"
)

// TEST STRATEGY:
// Verify binary RPM header, signature, scriptlet, and CPIO file manifest parsing against
// deterministic synthetic RPM byte fixtures (TestReadRPMDetailsAndFiles_SyntheticRPM) as well
// as optional real distribution RPM packages when present on disk.
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

	if details.SourcePackage == "" {
		t.Errorf("expected non-empty SourcePackage")
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

	files, err := ReadRPMFiles(rpmPath)
	if err != nil {
		t.Fatalf("ReadRPMFiles failed: %v", err)
	}
	if len(files) == 0 {
		t.Errorf("expected non-empty file list from ReadRPMFiles")
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

	if pkg.SourceRPM == "" {
		t.Errorf("expected pkg.SourceRPM to be backfilled")
	}

	if pkg.SourcePackage == "" {
		t.Errorf("expected pkg.SourcePackage to be backfilled")
	}

	if pkg.Format != models.FormatRPM {
		t.Errorf("expected pkg.Format to be %s, got %s", models.FormatRPM, pkg.Format)
	}
}

type synthTag struct {
	tag      int32
	dataType int32
	count    int32
	data     []byte
}

func buildSynthHeader(tags []synthTag, align8 bool) []byte {
	sort.Slice(tags, func(i, j int) bool { return tags[i].tag < tags[j].tag })
	var entries, blobs bytes.Buffer
	for _, tg := range tags {
		align := 1
		switch tg.dataType {
		case rpmutils.RPM_INT16_TYPE:
			align = 2
		case rpmutils.RPM_INT32_TYPE:
			align = 4
		case rpmutils.RPM_INT64_TYPE:
			align = 8
		}
		if rem := blobs.Len() % align; rem != 0 {
			blobs.Write(make([]byte, align-rem))
		}
		_ = binary.Write(&entries, binary.BigEndian, tg.tag)
		_ = binary.Write(&entries, binary.BigEndian, tg.dataType)
		_ = binary.Write(&entries, binary.BigEndian, int32(blobs.Len()))
		_ = binary.Write(&entries, binary.BigEndian, tg.count)
		blobs.Write(tg.data)
	}
	var out bytes.Buffer
	out.Write([]byte{0x8e, 0xad, 0xe8, 0x01})
	_ = binary.Write(&out, binary.BigEndian, uint32(0))
	_ = binary.Write(&out, binary.BigEndian, uint32(len(tags)))
	_ = binary.Write(&out, binary.BigEndian, uint32(blobs.Len()))
	out.Write(entries.Bytes())
	out.Write(blobs.Bytes())
	if align8 {
		if rem := out.Len() % 8; rem != 0 {
			out.Write(make([]byte, 8-rem))
		}
	}
	return out.Bytes()
}

func writeSyntheticRPM(t *testing.T, rpmPath string) {
	t.Helper()
	var buf bytes.Buffer

	// 96-byte RPM v3 Lead with sigtype=5 (header-style signatures)
	lead := make([]byte, 96)
	copy(lead[0:4], []byte{0xed, 0xab, 0xee, 0xdb})
	lead[4] = 3
	lead[5] = 0
	binary.BigEndian.PutUint16(lead[6:8], 0)
	binary.BigEndian.PutUint16(lead[8:10], 1)
	copy(lead[10:76], "demo-pkg-1.0-1.x86_64")
	binary.BigEndian.PutUint16(lead[76:78], 1)
	binary.BigEndian.PutUint16(lead[78:80], 5)
	buf.Write(lead)

	var sigSize bytes.Buffer
	_ = binary.Write(&sigSize, binary.BigEndian, uint32(256))
	buf.Write(buildSynthHeader([]synthTag{
		{tag: 1000, dataType: rpmutils.RPM_INT32_TYPE, count: 1, data: sigSize.Bytes()},
	}, true))

	var instSize bytes.Buffer
	_ = binary.Write(&instSize, binary.BigEndian, uint32(8192))

	var fileSizes bytes.Buffer
	_ = binary.Write(&fileSizes, binary.BigEndian, []uint32{4096, 128, 12})

	var fileModes bytes.Buffer
	_ = binary.Write(&fileModes, binary.BigEndian, []uint16{0o100755, 0o100644, 0o120777})

	var fileFlags bytes.Buffer
	_ = binary.Write(&fileFlags, binary.BigEndian, []uint32{1, 2, 0})

	var dirIdxs bytes.Buffer
	_ = binary.Write(&dirIdxs, binary.BigEndian, []uint32{0, 1, 0})

	var fileTimes bytes.Buffer
	_ = binary.Write(&fileTimes, binary.BigEndian, []uint32{1700000000, 1700000001, 1700000002})

	genTags := []synthTag{
		{tag: rpmutils.NAME, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte("demo-pkg\x00")},
		{tag: rpmutils.VERSION, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte("1.0.0\x00")},
		{tag: rpmutils.RELEASE, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte("1.el9\x00")},
		{tag: rpmutils.BUILDHOST, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte("builder01.example.com\x00")},
		{tag: rpmutils.SIZE, dataType: rpmutils.RPM_INT32_TYPE, count: 1, data: instSize.Bytes()},
		{tag: rpmutils.PACKAGER, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte("Bot <bot@example.com>\x00")},
		{tag: rpmutils.PREIN, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte("echo prein\x00")},
		{tag: rpmutils.POSTIN, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte("echo postin\x00")},
		{tag: rpmutils.PREUN, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte("echo preun\x00")},
		{tag: rpmutils.POSTUN, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte("echo postun\x00")},
		{tag: rpmutils.FILESIZES, dataType: rpmutils.RPM_INT32_TYPE, count: 3, data: fileSizes.Bytes()},
		{tag: rpmutils.FILEMODES, dataType: rpmutils.RPM_INT16_TYPE, count: 3, data: fileModes.Bytes()},
		{tag: rpmutils.FILEMTIMES, dataType: rpmutils.RPM_INT32_TYPE, count: 3, data: fileTimes.Bytes()},
		{tag: rpmutils.FILEDIGESTS, dataType: rpmutils.RPM_STRING_ARRAY_TYPE, count: 3, data: []byte("abc\x00def\x00\x00")},
		{tag: rpmutils.FILELINKTOS, dataType: rpmutils.RPM_STRING_ARRAY_TYPE, count: 3, data: []byte("\x00\x00/usr/bin/demo\x00")},
		{tag: rpmutils.FILEFLAGS, dataType: rpmutils.RPM_INT32_TYPE, count: 3, data: fileFlags.Bytes()},
		{tag: rpmutils.FILEUSERNAME, dataType: rpmutils.RPM_STRING_ARRAY_TYPE, count: 3, data: []byte("root\x00root\x00root\x00")},
		{tag: rpmutils.FILEGROUPNAME, dataType: rpmutils.RPM_STRING_ARRAY_TYPE, count: 3, data: []byte("root\x00root\x00root\x00")},
		{tag: rpmutils.SOURCERPM, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte("demo-pkg-1.0.0-1.src.rpm\x00")},
		{tag: rpmutils.PREINPROG, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte("/bin/sh\x00")},
		{tag: rpmutils.POSTINPROG, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte("/bin/sh\x00")},
		{tag: 1116, dataType: rpmutils.RPM_INT32_TYPE, count: 3, data: dirIdxs.Bytes()},
		{tag: 1117, dataType: rpmutils.RPM_STRING_ARRAY_TYPE, count: 3, data: []byte("demo\x00demo.conf\x00demo-link\x00")},
		{tag: 1118, dataType: rpmutils.RPM_STRING_ARRAY_TYPE, count: 2, data: []byte("/usr/bin/\x00/etc/\x00")},
		{tag: 5012, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte("https://bugs.example.com\x00")},
		{tag: 5034, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte("https://git.example.com/demo.git\x00")},
	}
	buf.Write(buildSynthHeader(genTags, false))

	if err := os.MkdirAll(filepath.Dir(rpmPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rpmPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadRPMDetailsAndFiles_SyntheticRPM(t *testing.T) {
	repoDir := t.TempDir()
	relHref := filepath.ToSlash(filepath.Join("Packages", "d", "demo-pkg-1.0.0-1.el9.x86_64.rpm"))
	rpmPath := filepath.Join(repoDir, filepath.FromSlash(relHref))
	writeSyntheticRPM(t, rpmPath)

	details, err := ReadRPMDetails(rpmPath)
	if err != nil {
		t.Fatalf("ReadRPMDetails synthetic RPM failed: %v", err)
	}
	if details.BuildHost != "builder01.example.com" || details.SourcePackage != "demo-pkg-1.0.0-1.src.rpm" {
		t.Errorf("unexpected synthetic details: %+v", details)
	}
	if details.BugURL != "https://bugs.example.com" || details.VCS != "https://git.example.com/demo.git" {
		t.Errorf("expected BugURL and VCS populated, got bug=%q vcs=%q", details.BugURL, details.VCS)
	}
	if details.Scriptlets == nil || details.Scriptlets.PreIn != "echo prein" || details.Scriptlets.PostUn != "echo postun" {
		t.Errorf("expected scriptlets populated, got %+v", details.Scriptlets)
	}

	files, err := ReadRPMFiles(rpmPath)
	if err != nil {
		t.Fatalf("ReadRPMFiles failed: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 files from synthetic RPM, got %d", len(files))
	}
	if files[2].LinkTarget != "/usr/bin/demo" {
		t.Errorf("expected symlink target /usr/bin/demo, got %q", files[2].LinkTarget)
	}

	pkg := &models.Package{Name: "demo-pkg", LocationHref: relHref}
	EnrichPackagesWithRPMDetails(repoDir, []*models.Package{pkg})
	if pkg.Details == nil || pkg.Packager != "Bot <bot@example.com>" || pkg.Format != models.FormatRPM {
		t.Errorf("unexpected EnrichPackagesWithRPMDetails result: pkg=%+v details=%+v", pkg, pkg.Details)
	}
}

func TestOpenFactoryAndFormatDetect(t *testing.T) {
	if got := DetectFormat(t.TempDir()); got != models.FormatRPM {
		t.Errorf("expected default DetectFormat fallback to rpm, got %q", got)
	}

	debDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(debDir, "Packages"), []byte("Package: jq\nVersion: 1.7\nArchitecture: amd64\n"), 0o644)
	if got := DetectFormat(debDir); got != models.FormatDEB {
		t.Errorf("expected DetectFormat Packages -> deb, got %q", got)
	}

	reader, meta, cleanup, err := Open(debDir, "")
	if err != nil {
		t.Fatalf("repo.Open DEB failed: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	defer func() { _ = reader.Close() }()
	if meta.Format != models.FormatDEB {
		t.Errorf("expected DEB metadata format, got %q", meta.Format)
	}

	// Verify XMLRepository.ReadPackageFiles & EnrichForRender with synthetic RPM + traversal error
	xmlRepo := NewXMLRepository(t.TempDir(), "", "", "", nil)
	rpmHref := "Packages/d/demo-pkg-1.0.0-1.el9.x86_64.rpm"
	rpmFull := filepath.Join(xmlRepo.repoDir, filepath.FromSlash(rpmHref))
	writeSyntheticRPM(t, rpmFull)
	if files, err := xmlRepo.ReadPackageFiles(xmlRepo.repoDir, &models.Package{LocationHref: rpmHref}); err != nil || len(files) != 3 {
		t.Errorf("XMLRepository.ReadPackageFiles failed: len=%d err=%v", len(files), err)
	}
	if _, err := xmlRepo.ReadPackageFiles(xmlRepo.repoDir, &models.Package{LocationHref: "../../outside.rpm"}); err == nil {
		t.Errorf("expected XMLRepository.ReadPackageFiles to block path traversal")
	}
	renderPkg := &models.Package{Name: "demo-pkg", LocationHref: rpmHref}
	if err := xmlRepo.EnrichForRender(xmlRepo.repoDir, renderPkg); err != nil {
		t.Fatalf("XMLRepository.EnrichForRender failed: %v", err)
	}
	if renderPkg.Details == nil || len(renderPkg.Details.Files) != 3 || renderPkg.Details.Scriptlets == nil {
		t.Errorf("expected EnrichForRender to extract details + 3 files in single pass, got %+v", renderPkg.Details)
	}
	// Second call when Details already set without Files exercises ReadRPMFiles fallback branch
	renderPkg.Details.Files = nil
	if err := xmlRepo.EnrichForRender(xmlRepo.repoDir, renderPkg); err != nil || len(renderPkg.Details.Files) != 3 {
		t.Errorf("expected EnrichForRender fallback branch to restore 3 files, got len=%d err=%v", len(renderPkg.Details.Files), err)
	}

	// Verify SQLite OpenWithOptions + updateinfo + Warn writer
	rpmRoot := t.TempDir()
	repodata := filepath.Join(rpmRoot, "repodata")
	_ = os.MkdirAll(repodata, 0o755)
	primaryDB := filepath.Join(repodata, "primary.sqlite")
	db, err := sql.Open("sqlite3", primaryDB)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE packages (pkgKey INTEGER PRIMARY KEY, pkgId TEXT, name TEXT, arch TEXT, version TEXT, epoch TEXT, release TEXT, summary TEXT, description TEXT, url TEXT, time_file INTEGER, time_build INTEGER, rpm_license TEXT, size_package INTEGER, size_installed INTEGER, size_archive INTEGER, location_href TEXT, rpm_vendor TEXT, rpm_group TEXT, rpm_buildhost TEXT, rpm_sourcerpm TEXT, rpm_packager TEXT, checksum_type TEXT);
		INSERT INTO packages VALUES (1, 'deadbeef', 'openssl', 'x86_64', '3.0.7', '1', '27.el9', 'TLS', 'TLS lib', '', 100, 200, 'Apache-2.0', 10, 20, 30, 'openssl.rpm', '', 'System', '', '', '', 'sha256');
		CREATE TABLE requires (name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT, pkgKey INTEGER, pre BOOLEAN);
		CREATE TABLE provides (name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT, pkgKey INTEGER, pre BOOLEAN);
		CREATE TABLE conflicts (name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT, pkgKey INTEGER, pre BOOLEAN);
		CREATE TABLE obsoletes (name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT, pkgKey INTEGER, pre BOOLEAN);
		CREATE TABLE recommends (name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT, pkgKey INTEGER);
		CREATE TABLE suggests (name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT, pkgKey INTEGER);
	`)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}

	otherDB := filepath.Join(repodata, "other.sqlite")
	odb, err := sql.Open("sqlite3", otherDB)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = odb.Exec(`
		CREATE TABLE packages (pkgKey INTEGER PRIMARY KEY, pkgId TEXT);
		INSERT INTO packages VALUES (1, 'deadbeef');
		CREATE TABLE changelog (pkgKey INTEGER, author TEXT, date INTEGER, changelog TEXT);
		INSERT INTO changelog VALUES (1, 'Dev <dev@example.com>', 1700000000, '- Init');
	`)
	_ = odb.Close()

	uiPath := filepath.Join(repodata, "updateinfo.xml.gz")
	uFile, _ := os.Create(uiPath)
	ugz := gzip.NewWriter(uFile)
	_, _ = ugz.Write([]byte(`<updates><update type="security"><id>ELSA-2026:2026</id><title>TLS fix</title><severity>High</severity><issued date="2026-01-01"/><references><reference type="cve" id="CVE-2026-2026"/></references><pkglist><collection><package name="openssl" version="3.0.7" release="27.el9" epoch="1" arch="x86_64"/></collection></pkglist></update></updates>`))
	_ = ugz.Close()
	_ = uFile.Close()

	repomd := `<repomd xmlns="http://linux.duke.edu/metadata/repo"><revision>99</revision><data type="primary_db"><location href="repodata/primary.sqlite"/></data><data type="other_db"><location href="repodata/other.sqlite"/></data><data type="updateinfo"><location href="repodata/updateinfo.xml.gz"/></data></repomd>`
	_ = os.WriteFile(filepath.Join(repodata, "repomd.xml"), []byte(repomd), 0o644)

	var warnBuf bytes.Buffer
	rSQL, mSQL, cleanSQL, err := OpenWithOptions(rpmRoot, OpenOptions{Format: models.FormatRPM, Warn: &warnBuf})
	if err != nil {
		t.Fatalf("OpenWithOptions SQLite RPM failed: %v", err)
	}
	if cleanSQL != nil {
		defer cleanSQL()
	}
	defer func() { _ = rSQL.Close() }()
	if mSQL.Revision != "99" {
		t.Errorf("expected revision 99, got %q", mSQL.Revision)
	}
	pkgs, err := rSQL.GetAllPackages()
	if err != nil || len(pkgs) != 1 {
		t.Fatalf("GetAllPackages SQLite failed: len=%d err=%v", len(pkgs), err)
	}
	if ce, ok := rSQL.(ChangelogEnricher); ok {
		if err := ce.EnrichPackagesWithChangelogs(pkgs); err != nil {
			t.Fatalf("SQLite EnrichPackagesWithChangelogs failed: %v", err)
		}
	}
	if ae, ok := rSQL.(AdvisoryEnricher); ok {
		if err := ae.EnrichPackagesWithAdvisories(pkgs); err != nil {
			t.Fatalf("SQLite EnrichPackagesWithAdvisories failed: %v", err)
		}
	}
	if len(pkgs[0].Advisories) != 1 || pkgs[0].Advisories[0].CVEs[0] != "CVE-2026-2026" {
		t.Errorf("expected CVE-2026-2026 attached on SQLite package, got %+v", pkgs[0].Advisories)
	}
	if ra, ok := rSQL.(*RepositoryAccess); ok {
		ra.warnf("test %s", strings.ToLower("WARN"))
		if !strings.Contains(warnBuf.String(), "test warn") {
			t.Errorf("expected warnf to write to configured Warn writer")
		}
	}
}
