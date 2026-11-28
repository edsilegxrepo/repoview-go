//go:build integration

package integration

// OBJECTIVES:
// Provide deterministic, cross-platform pure-Go fixture builders for RPM, SQLite, XML,
// Debian (.deb), and APT debug (.ddeb) repositories with zero external packaging CLI tools.
//
// CORE COMPONENTS:
//   - Binary Builders: makeRPMBytes (lead + signature/header + CPIO newc) and makeDebArBytes (ar + control/data tar).
//   - WriteRPMRepo: Builds primary/other/filelists SQLite DBs + XML fallbacks + comps.xml + updateinfo.xml.
//   - WriteDebRepo: Builds multi-suite/component/debug Packages* indexes and Release / InRelease manifests.
//
// DATA FLOW:
//   SynthRPMSpec / SynthDebSpec -> in-memory archives & checksummed metadata -> t.TempDir() test fixture

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/md5" // #nosec G501 -- MD5 used strictly for synthetic Debian control md5sums fixture member
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	_ "github.com/mattn/go-sqlite3"
	rpmutils "github.com/sassoftware/go-rpmutils"
	"github.com/ulikunitz/xz"
)

var deterministicFixtureTime = time.Unix(1700000000, 0).UTC()

// samplePackagesBz2Base64 is a deterministic bzip2-compressed Debian Packages file
// (stdlib compress/bzip2 is decode-only and go.mod has no bzip2 writer).
const samplePackagesBz2Base64 = "QlpoOTFBWSZTWdhMZRAAAB7fgAAQQAN9FXwCSQA+797wIACEGoanqanqbU8j1RpoBtRp6nqaeoNT0VPI2pD1PU0ZMQABtQulrXHOEtIiftjVq8yuJqywsCQjeiJOqzxN+DlowVI6aYkAtSrpUH305tddwIQoEzMC+WWzN+rOTyy+U3pE3N9qj6faI/ss1m8aVdHYD7ok7BXhyPxcQUJAV2IhRQJFx0BH1DObdWhDI2yTCwM0SVpvkKwlz3p/F3JFOFCQ2ExlEA=="

// WrittenArchiveInfo records the exact path, byte length, and SHA-256 checksum of a generated archive.
type WrittenArchiveInfo struct {
	RelHref string
	AbsPath string
	Size    int64
	SHA256  string
}

// ---------- Compressed Buffer & Archive Helpers ----------

func compressBytes(t *testing.T, ext string, raw []byte) []byte {
	t.Helper()
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "", "none":
		return raw
	case "gz":
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		gw.ModTime = deterministicFixtureTime
		if _, err := gw.Write(raw); err != nil {
			t.Fatalf("gzip write failed: %v", err)
		}
		if err := gw.Close(); err != nil {
			t.Fatalf("gzip close failed: %v", err)
		}
		return buf.Bytes()
	case "xz":
		var buf bytes.Buffer
		xw, err := xz.NewWriter(&buf)
		if err != nil {
			t.Fatalf("xz NewWriter failed: %v", err)
		}
		if _, err := xw.Write(raw); err != nil {
			t.Fatalf("xz write failed: %v", err)
		}
		if err := xw.Close(); err != nil {
			t.Fatalf("xz close failed: %v", err)
		}
		return buf.Bytes()
	case "zst", "zstd":
		var buf bytes.Buffer
		zw, err := zstd.NewWriter(&buf)
		if err != nil {
			t.Fatalf("zstd NewWriter failed: %v", err)
		}
		if _, err := zw.Write(raw); err != nil {
			t.Fatalf("zstd write failed: %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("zstd close failed: %v", err)
		}
		return buf.Bytes()
	default:
		t.Fatalf("unsupported dynamic compression format %q", ext)
		return nil
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func md5Hex(data []byte) string {
	sum := md5.Sum(data) // #nosec G401 -- test fixture Debian md5sums file
	return hex.EncodeToString(sum[:])
}

// ---------- RPM Pure-Go Synthetic Builders ----------

type SynthRPMFile struct {
	Dir           string // e.g. "/usr/bin/"
	Name          string // e.g. "demo"
	Mode          uint16 // e.g. 0o100755, 0o100644, 0o120777
	Size          uint32
	Flags         uint32 // 1=config, 2=doc
	SymlinkTarget string
}

type SynthChangelog struct {
	Author string
	Date   int64
	Text   string
}

type SynthRPMDep struct {
	Name    string
	Flags   string
	Epoch   string
	Version string
	Release string
	Pre     bool
}

type SynthRPMSpec struct {
	Name          string
	Epoch         string
	Version       string
	Release       string
	Arch          string
	Summary       string
	Description   string
	URL           string
	License       string
	Packager      string
	Vendor        string
	Group         string
	BuildHost     string
	SourceRPM     string
	BugURL        string
	VCS           string
	InstalledSize int64
	TimeBuild     int64
	TimeFile      int64
	LocationHref  string
	PreIn         string
	PostIn        string
	PreUn         string
	PostUn        string
	Requires      []SynthRPMDep
	Provides      []SynthRPMDep
	Conflicts     []SynthRPMDep
	Obsoletes     []SynthRPMDep
	Recommends    []SynthRPMDep
	Suggests      []SynthRPMDep
	Enhances      []SynthRPMDep
	Supplements   []SynthRPMDep
	Changelogs    []SynthChangelog
	Files         []SynthRPMFile
	SkipRPMBinary bool
}

type SynthRPMGroup struct {
	ID          string
	Name        string
	Description string
	Packages    []string
}

type SynthAdvisory struct {
	ID       string
	Title    string
	Type     string // security, bugfix, enhancement
	Severity string
	Issued   string
	CVEs     []string
	Packages []SynthRPMSpec
}

type RPMRepoSpec struct {
	Mode       string // "sqlite" (extended 23-col), "sqlite-basic" (18-col), "xml" (--no-database)
	Revision   string
	Packages   []SynthRPMSpec
	Groups     []SynthRPMGroup
	Advisories []SynthAdvisory
	XMLComp    string // optional "gz" (default), "zst", "xz"
}

type synthHeaderTag struct {
	tag      int32
	dataType int32
	count    int32
	data     []byte
}

func buildSynthRPMHeader(tags []synthHeaderTag, align8 bool) []byte {
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

// BuildSyntheticRPM writes a valid RPM v3 header container at rpmPath and returns its byte size and SHA-256 digest.
func BuildSyntheticRPM(t *testing.T, rpmPath string, spec SynthRPMSpec) (int64, string) {
	t.Helper()
	if spec.Arch == "" {
		spec.Arch = "x86_64"
	}
	if spec.BuildHost == "" {
		spec.BuildHost = "builder01.example.com"
	}
	if spec.Packager == "" {
		spec.Packager = "Release Engineering <releng@example.com>"
	}
	if spec.SourceRPM == "" {
		spec.SourceRPM = fmt.Sprintf("%s-%s-%s.src.rpm", spec.Name, spec.Version, spec.Release)
	}
	if spec.InstalledSize == 0 {
		spec.InstalledSize = 8192
	}

	var buf bytes.Buffer

	// 96-byte RPM v3 Lead (sigtype=5 for header-style signature)
	lead := make([]byte, 96)
	copy(lead[0:4], []byte{0xed, 0xab, 0xee, 0xdb})
	lead[4] = 3
	lead[5] = 0
	binary.BigEndian.PutUint16(lead[6:8], 0)
	binary.BigEndian.PutUint16(lead[8:10], 1)
	nevra := fmt.Sprintf("%s-%s-%s.%s", spec.Name, spec.Version, spec.Release, spec.Arch)
	copy(lead[10:76], nevra)
	binary.BigEndian.PutUint16(lead[76:78], 1)
	binary.BigEndian.PutUint16(lead[78:80], 5)
	buf.Write(lead)

	var sigSize bytes.Buffer
	_ = binary.Write(&sigSize, binary.BigEndian, uint32(256))
	buf.Write(buildSynthRPMHeader([]synthHeaderTag{
		{tag: 1000, dataType: rpmutils.RPM_INT32_TYPE, count: 1, data: sigSize.Bytes()},
	}, true))

	var instSize bytes.Buffer
	_ = binary.Write(&instSize, binary.BigEndian, uint32(spec.InstalledSize))

	genTags := []synthHeaderTag{
		{tag: rpmutils.NAME, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte(spec.Name + "\x00")},
		{tag: rpmutils.VERSION, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte(spec.Version + "\x00")},
		{tag: rpmutils.RELEASE, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte(spec.Release + "\x00")},
		{tag: rpmutils.BUILDHOST, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte(spec.BuildHost + "\x00")},
		{tag: rpmutils.SIZE, dataType: rpmutils.RPM_INT32_TYPE, count: 1, data: instSize.Bytes()},
		{tag: rpmutils.PACKAGER, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte(spec.Packager + "\x00")},
		{tag: rpmutils.SOURCERPM, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte(spec.SourceRPM + "\x00")},
	}
	if spec.PreIn != "" {
		genTags = append(genTags,
			synthHeaderTag{tag: rpmutils.PREIN, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte(spec.PreIn + "\x00")},
			synthHeaderTag{tag: rpmutils.PREINPROG, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte("/bin/sh\x00")},
		)
	}
	if spec.PostIn != "" {
		genTags = append(genTags,
			synthHeaderTag{tag: rpmutils.POSTIN, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte(spec.PostIn + "\x00")},
			synthHeaderTag{tag: rpmutils.POSTINPROG, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte("/bin/sh\x00")},
		)
	}
	if spec.PreUn != "" {
		genTags = append(genTags, synthHeaderTag{tag: rpmutils.PREUN, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte(spec.PreUn + "\x00")})
	}
	if spec.PostUn != "" {
		genTags = append(genTags, synthHeaderTag{tag: rpmutils.POSTUN, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte(spec.PostUn + "\x00")})
	}
	if spec.BugURL != "" {
		genTags = append(genTags, synthHeaderTag{tag: 5012, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte(spec.BugURL + "\x00")})
	}
	if spec.VCS != "" {
		genTags = append(genTags, synthHeaderTag{tag: 5034, dataType: rpmutils.RPM_STRING_TYPE, count: 1, data: []byte(spec.VCS + "\x00")})
	}

	if len(spec.Files) > 0 {
		n := len(spec.Files)
		var fSizes, fFlags, dirIdxs, fTimes bytes.Buffer
		var fModes bytes.Buffer
		var fDigests, fLinks, fUsers, fGroups, fBasenames bytes.Buffer
		dirIdxMap := make(map[string]uint32)
		var dirList []string

		for i, f := range spec.Files {
			dir := f.Dir
			if dir == "" {
				dir = "/usr/bin/"
			}
			if !strings.HasSuffix(dir, "/") {
				dir += "/"
			}
			dIdx, exists := dirIdxMap[dir]
			if !exists {
				dIdx = uint32(len(dirList))
				dirIdxMap[dir] = dIdx
				dirList = append(dirList, dir)
			}
			_ = binary.Write(&dirIdxs, binary.BigEndian, dIdx)
			_ = binary.Write(&fSizes, binary.BigEndian, f.Size)
			mode := f.Mode
			if mode == 0 {
				mode = 0o100755
			}
			_ = binary.Write(&fModes, binary.BigEndian, mode)
			_ = binary.Write(&fFlags, binary.BigEndian, f.Flags)
			_ = binary.Write(&fTimes, binary.BigEndian, uint32(1700000000+i))
			fmt.Fprintf(&fDigests, "deadbeef%02d\x00", i)
			fLinks.WriteString(f.SymlinkTarget + "\x00")
			fUsers.WriteString("root\x00")
			fGroups.WriteString("root\x00")
			fBasenames.WriteString(f.Name + "\x00")
		}
		var fDirNames bytes.Buffer
		for _, d := range dirList {
			fDirNames.WriteString(d + "\x00")
		}

		genTags = append(genTags,
			synthHeaderTag{tag: rpmutils.FILESIZES, dataType: rpmutils.RPM_INT32_TYPE, count: int32(n), data: fSizes.Bytes()},
			synthHeaderTag{tag: rpmutils.FILEMODES, dataType: rpmutils.RPM_INT16_TYPE, count: int32(n), data: fModes.Bytes()},
			synthHeaderTag{tag: rpmutils.FILEMTIMES, dataType: rpmutils.RPM_INT32_TYPE, count: int32(n), data: fTimes.Bytes()},
			synthHeaderTag{tag: rpmutils.FILEDIGESTS, dataType: rpmutils.RPM_STRING_ARRAY_TYPE, count: int32(n), data: fDigests.Bytes()},
			synthHeaderTag{tag: rpmutils.FILELINKTOS, dataType: rpmutils.RPM_STRING_ARRAY_TYPE, count: int32(n), data: fLinks.Bytes()},
			synthHeaderTag{tag: rpmutils.FILEFLAGS, dataType: rpmutils.RPM_INT32_TYPE, count: int32(n), data: fFlags.Bytes()},
			synthHeaderTag{tag: rpmutils.FILEUSERNAME, dataType: rpmutils.RPM_STRING_ARRAY_TYPE, count: int32(n), data: fUsers.Bytes()},
			synthHeaderTag{tag: rpmutils.FILEGROUPNAME, dataType: rpmutils.RPM_STRING_ARRAY_TYPE, count: int32(n), data: fGroups.Bytes()},
			synthHeaderTag{tag: 1116, dataType: rpmutils.RPM_INT32_TYPE, count: int32(n), data: dirIdxs.Bytes()},
			synthHeaderTag{tag: 1117, dataType: rpmutils.RPM_STRING_ARRAY_TYPE, count: int32(n), data: fBasenames.Bytes()},
			synthHeaderTag{tag: 1118, dataType: rpmutils.RPM_STRING_ARRAY_TYPE, count: int32(len(dirList)), data: fDirNames.Bytes()},
		)
	}

	buf.Write(buildSynthRPMHeader(genTags, false))

	if err := os.MkdirAll(filepath.Dir(rpmPath), 0o755); err != nil {
		t.Fatalf("failed to mkdir for RPM %s: %v", rpmPath, err)
	}
	raw := buf.Bytes()
	if err := os.WriteFile(rpmPath, raw, 0o644); err != nil {
		t.Fatalf("failed to write synthetic RPM %s: %v", rpmPath, err)
	}
	return int64(len(raw)), sha256Hex(raw)
}

type repomdEntrySpec struct {
	DataType string
	RelHref  string
	RawBytes []byte
}

// WriteRPMRepo materializes an RPM repository (SQLite or XML-only, optional comps/updateinfo, and SHA-256 repomd.xml).
func WriteRPMRepo(t *testing.T, repoDir string, spec RPMRepoSpec) map[string]WrittenArchiveInfo {
	t.Helper()

	repodataDir := filepath.Join(repoDir, "repodata")
	if err := os.MkdirAll(repodataDir, 0o755); err != nil {
		t.Fatalf("failed to create repodata dir: %v", err)
	}

	written := make(map[string]WrittenArchiveInfo)
	type resolvedPkg struct {
		spec   SynthRPMSpec
		pkgKey int64
		size   int64
		sha256 string
		href   string
	}
	resolved := make([]resolvedPkg, 0, len(spec.Packages))

	for idx, p := range spec.Packages {
		if p.Epoch == "" {
			p.Epoch = "0"
		}
		if p.Arch == "" {
			p.Arch = "x86_64"
		}
		if p.License == "" {
			p.License = "MIT"
		}
		if p.Group == "" {
			p.Group = "Applications/System"
		}
		if p.TimeBuild == 0 {
			p.TimeBuild = deterministicFixtureTime.Unix() + int64(idx*60)
		}
		if p.TimeFile == 0 {
			p.TimeFile = p.TimeBuild + 10
		}
		if p.InstalledSize == 0 {
			p.InstalledSize = 8192
		}
		if p.SourceRPM == "" {
			p.SourceRPM = fmt.Sprintf("%s-%s-%s.src.rpm", p.Name, p.Version, p.Release)
		}
		if p.Packager == "" {
			p.Packager = "Release Bot <releng@example.com>"
		}
		if p.Vendor == "" {
			p.Vendor = "Enterprise Linux"
		}
		if p.BuildHost == "" {
			p.BuildHost = "builder01.example.com"
		}

		href := p.LocationHref
		if href == "" {
			first := "x"
			if len(p.Name) > 0 {
				first = strings.ToLower(p.Name[:1])
			}
			href = fmt.Sprintf("Packages/%s/%s-%s-%s.%s.rpm", first, p.Name, p.Version, p.Release, p.Arch)
		}

		var pkgSize int64 = 4096
		pkgDigest := fmt.Sprintf("%064x", idx+1)
		if !p.SkipRPMBinary && !strings.Contains(href, "..") {
			rpmAbs := filepath.Join(repoDir, filepath.FromSlash(href))
			pkgSize, pkgDigest = BuildSyntheticRPM(t, rpmAbs, p)
			written[href] = WrittenArchiveInfo{
				RelHref: href,
				AbsPath: rpmAbs,
				Size:    pkgSize,
				SHA256:  pkgDigest,
			}
		}

		resolved = append(resolved, resolvedPkg{
			spec:   p,
			pkgKey: int64(idx + 1),
			size:   pkgSize,
			sha256: pkgDigest,
			href:   href,
		})
	}

	var mdEntries []repomdEntrySpec

	mode := spec.Mode
	if mode == "" {
		mode = "sqlite"
	}

	if mode == "xml" {
		compExt := spec.XMLComp
		if compExt == "" {
			compExt = "gz"
		}
		var primaryXML, otherXML bytes.Buffer
		primaryXML.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
		primaryXML.WriteString(fmt.Sprintf(`<metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm" packages="%d">`, len(resolved)) + "\n")
		otherXML.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
		otherXML.WriteString(fmt.Sprintf(`<otherdata xmlns="http://linux.duke.edu/metadata/other" packages="%d">`, len(resolved)) + "\n")

		writeXMLEntries := func(tag string, deps []SynthRPMDep) string {
			if len(deps) == 0 {
				return ""
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "<rpm:%s>", tag)
			for _, d := range deps {
				fmt.Fprintf(&sb, `<rpm:entry name="%s"`, html.EscapeString(d.Name))
				if d.Flags != "" {
					fmt.Fprintf(&sb, ` flags="%s" epoch="%s" ver="%s" rel="%s"`,
						html.EscapeString(d.Flags), html.EscapeString(d.Epoch), html.EscapeString(d.Version), html.EscapeString(d.Release))
				}
				if d.Pre {
					sb.WriteString(` pre="1"`)
				}
				sb.WriteString("/>")
			}
			fmt.Fprintf(&sb, "</rpm:%s>", tag)
			return sb.String()
		}

		for _, rp := range resolved {
			p := rp.spec
			fmt.Fprintf(&primaryXML, `  <package type="rpm">
    <name>%s</name><arch>%s</arch>
    <version epoch="%s" ver="%s" rel="%s"/>
    <checksum type="sha256" pkgid="YES">%s</checksum>
    <summary>%s</summary><description>%s</description>
    <packager>%s</packager><url>%s</url>
    <time file="%d" build="%d"/>
    <size package="%d" installed="%d" archive="%d"/>
    <location href="%s"/>
    <format>
      <rpm:license>%s</rpm:license><rpm:vendor>%s</rpm:vendor><rpm:group>%s</rpm:group>
      <rpm:buildhost>%s</rpm:buildhost><rpm:sourcerpm>%s</rpm:sourcerpm>
      %s%s%s%s%s%s%s%s
    </format>
  </package>
`,
				html.EscapeString(p.Name), html.EscapeString(p.Arch),
				html.EscapeString(p.Epoch), html.EscapeString(p.Version), html.EscapeString(p.Release),
				rp.sha256,
				html.EscapeString(p.Summary), html.EscapeString(p.Description),
				html.EscapeString(p.Packager), html.EscapeString(p.URL),
				p.TimeFile, p.TimeBuild, rp.size, p.InstalledSize, p.InstalledSize+1024,
				html.EscapeString(rp.href),
				html.EscapeString(p.License), html.EscapeString(p.Vendor), html.EscapeString(p.Group),
				html.EscapeString(p.BuildHost), html.EscapeString(p.SourceRPM),
				writeXMLEntries("requires", p.Requires),
				writeXMLEntries("provides", p.Provides),
				writeXMLEntries("conflicts", p.Conflicts),
				writeXMLEntries("obsoletes", p.Obsoletes),
				writeXMLEntries("recommends", p.Recommends),
				writeXMLEntries("suggests", p.Suggests),
				writeXMLEntries("enhances", p.Enhances),
				writeXMLEntries("supplements", p.Supplements),
			)

			fmt.Fprintf(&otherXML, `  <package pkgid="%s" name="%s" arch="%s"><version epoch="%s" ver="%s" rel="%s"/>`,
				rp.sha256, html.EscapeString(p.Name), html.EscapeString(p.Arch),
				html.EscapeString(p.Epoch), html.EscapeString(p.Version), html.EscapeString(p.Release))
			for _, ch := range p.Changelogs {
				fmt.Fprintf(&otherXML, `<changelog author="%s" date="%d">%s</changelog>`,
					html.EscapeString(ch.Author), ch.Date, html.EscapeString(ch.Text))
			}
			otherXML.WriteString("</package>\n")
		}
		primaryXML.WriteString("</metadata>\n")
		otherXML.WriteString("</otherdata>\n")

		pBytes := compressBytes(t, compExt, primaryXML.Bytes())
		oBytes := compressBytes(t, compExt, otherXML.Bytes())
		pHref := "repodata/primary.xml." + compExt
		oHref := "repodata/other.xml." + compExt
		if err := os.WriteFile(filepath.Join(repoDir, filepath.FromSlash(pHref)), pBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repoDir, filepath.FromSlash(oHref)), oBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		mdEntries = append(mdEntries,
			repomdEntrySpec{DataType: "primary", RelHref: pHref, RawBytes: pBytes},
			repomdEntrySpec{DataType: "other", RelHref: oHref, RawBytes: oBytes},
		)
	} else {
		primaryPath := filepath.Join(repodataDir, "primary.sqlite")
		otherPath := filepath.Join(repodataDir, "other.sqlite")

		pdb, err := sql.Open("sqlite3", primaryPath)
		if err != nil {
			t.Fatalf("failed opening primary.sqlite: %v", err)
		}
		extended := mode != "sqlite-basic"
		pkgCols := `pkgKey INTEGER PRIMARY KEY, name TEXT, epoch TEXT, version TEXT, release TEXT, arch TEXT,
			summary TEXT, description TEXT, url TEXT, time_build INTEGER, rpm_license TEXT,
			rpm_sourcerpm TEXT, size_package INTEGER, location_href TEXT, rpm_vendor TEXT, rpm_group TEXT,
			rpm_buildhost TEXT, size_installed INTEGER`
		if extended {
			pkgCols += `, pkgId TEXT, checksum_type TEXT, rpm_packager TEXT, time_file INTEGER, size_archive INTEGER`
		}
		ddl := fmt.Sprintf(`
		CREATE TABLE packages (%s);
		CREATE TABLE requires (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT, pre BOOLEAN);
		CREATE TABLE provides (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT);
		CREATE TABLE conflicts (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT);
		CREATE TABLE obsoletes (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT);`, pkgCols)
		if extended {
			ddl += `
			CREATE TABLE recommends (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT);
			CREATE TABLE suggests (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT);
			CREATE TABLE enhances (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT);
			CREATE TABLE supplements (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT);`
		}
		if _, err := pdb.Exec(ddl); err != nil {
			_ = pdb.Close()
			t.Fatalf("failed creating sqlite tables: %v", err)
		}

		for _, rp := range resolved {
			p := rp.spec
			if extended {
				_, err = pdb.Exec(`INSERT INTO packages (pkgKey, name, epoch, version, release, arch, summary, description, url, time_build, rpm_license, rpm_sourcerpm, size_package, location_href, rpm_vendor, rpm_group, rpm_buildhost, size_installed, pkgId, checksum_type, rpm_packager, time_file, size_archive)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					rp.pkgKey, p.Name, p.Epoch, p.Version, p.Release, p.Arch, p.Summary, p.Description, p.URL, p.TimeBuild, p.License, p.SourceRPM, rp.size, rp.href, p.Vendor, p.Group, p.BuildHost, p.InstalledSize, rp.sha256, "sha256", p.Packager, p.TimeFile, p.InstalledSize+1024)
			} else {
				_, err = pdb.Exec(`INSERT INTO packages (pkgKey, name, epoch, version, release, arch, summary, description, url, time_build, rpm_license, rpm_sourcerpm, size_package, location_href, rpm_vendor, rpm_group, rpm_buildhost, size_installed)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					rp.pkgKey, p.Name, p.Epoch, p.Version, p.Release, p.Arch, p.Summary, p.Description, p.URL, p.TimeBuild, p.License, p.SourceRPM, rp.size, rp.href, p.Vendor, p.Group, p.BuildHost, p.InstalledSize)
			}
			if err != nil {
				_ = pdb.Close()
				t.Fatalf("failed inserting package %s: %v", p.Name, err)
			}

			for _, d := range p.Requires {
				_, _ = pdb.Exec(`INSERT INTO requires VALUES (?, ?, ?, ?, ?, ?, ?)`, rp.pkgKey, d.Name, d.Flags, d.Epoch, d.Version, d.Release, d.Pre)
			}
			insertDepList := func(table string, deps []SynthRPMDep) {
				for _, d := range deps {
					_, _ = pdb.Exec(`INSERT INTO `+table+` VALUES (?, ?, ?, ?, ?, ?)`, rp.pkgKey, d.Name, d.Flags, d.Epoch, d.Version, d.Release)
				}
			}
			insertDepList("provides", p.Provides)
			insertDepList("conflicts", p.Conflicts)
			insertDepList("obsoletes", p.Obsoletes)
			if extended {
				insertDepList("recommends", p.Recommends)
				insertDepList("suggests", p.Suggests)
				insertDepList("enhances", p.Enhances)
				insertDepList("supplements", p.Supplements)
			}
		}
		_ = pdb.Close()

		odb, err := sql.Open("sqlite3", otherPath)
		if err != nil {
			t.Fatalf("failed opening other.sqlite: %v", err)
		}
		if _, err := odb.Exec(`CREATE TABLE changelog (pkgKey INTEGER, author TEXT, date INTEGER, changelog TEXT)`); err != nil {
			_ = odb.Close()
			t.Fatal(err)
		}
		for _, rp := range resolved {
			for _, ch := range rp.spec.Changelogs {
				_, _ = odb.Exec(`INSERT INTO changelog VALUES (?, ?, ?, ?)`, rp.pkgKey, ch.Author, ch.Date, ch.Text)
			}
		}
		_ = odb.Close()

		pRaw, _ := os.ReadFile(primaryPath)
		oRaw, _ := os.ReadFile(otherPath)
		mdEntries = append(mdEntries,
			repomdEntrySpec{DataType: "primary_db", RelHref: "repodata/primary.sqlite", RawBytes: pRaw},
			repomdEntrySpec{DataType: "other_db", RelHref: "repodata/other.sqlite", RawBytes: oRaw},
		)
	}

	if len(spec.Groups) > 0 {
		var comps bytes.Buffer
		comps.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<comps>\n")
		for _, g := range spec.Groups {
			fmt.Fprintf(&comps, "  <group>\n    <id>%s</id>\n    <name>%s</name>\n    <description>%s</description>\n    <packagelist>\n",
				html.EscapeString(g.ID), html.EscapeString(g.Name), html.EscapeString(g.Description))
			for _, pn := range g.Packages {
				fmt.Fprintf(&comps, "      <packagereq type=\"mandatory\">%s</packagereq>\n", html.EscapeString(pn))
			}
			comps.WriteString("    </packagelist>\n  </group>\n")
		}
		comps.WriteString("</comps>\n")
		cBytes := comps.Bytes()
		cHref := "repodata/comps.xml"
		if err := os.WriteFile(filepath.Join(repoDir, filepath.FromSlash(cHref)), cBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		mdEntries = append(mdEntries, repomdEntrySpec{DataType: "group", RelHref: cHref, RawBytes: cBytes})
	}

	if len(spec.Advisories) > 0 {
		var ubuf bytes.Buffer
		ubuf.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<updates>\n")
		for _, adv := range spec.Advisories {
			tName := adv.Type
			if tName == "" {
				tName = "security"
			}
			sev := adv.Severity
			if sev == "" {
				sev = "Important"
			}
			issued := adv.Issued
			if issued == "" {
				issued = "2026-01-15 12:00:00"
			}
			fmt.Fprintf(&ubuf, `  <update from="sec@example.com" status="final" type="%s" version="2">
    <id>%s</id>
    <title>%s</title>
    <severity>%s</severity>
    <issued date="%s"/>
    <references>
`, html.EscapeString(tName), html.EscapeString(adv.ID), html.EscapeString(adv.Title), html.EscapeString(sev), html.EscapeString(issued))
			for _, cve := range adv.CVEs {
				fmt.Fprintf(&ubuf, `      <reference href="https://example.com/%s" id="%s" type="cve"/>`+"\n", html.EscapeString(cve), html.EscapeString(cve))
			}
			ubuf.WriteString("    </references>\n    <pkglist>\n      <collection short=\"el\">\n")
			for _, p := range adv.Packages {
				ep := p.Epoch
				if ep == "" {
					ep = "0"
				}
				ar := p.Arch
				if ar == "" {
					ar = "x86_64"
				}
				fmt.Fprintf(&ubuf, `        <package name="%s" version="%s" release="%s" epoch="%s" arch="%s"/>`+"\n",
					html.EscapeString(p.Name), html.EscapeString(p.Version), html.EscapeString(p.Release), html.EscapeString(ep), html.EscapeString(ar))
			}
			ubuf.WriteString("      </collection>\n    </pkglist>\n  </update>\n")
		}
		ubuf.WriteString("</updates>\n")
		uBytes := compressBytes(t, "gz", ubuf.Bytes())
		uHref := "repodata/updateinfo.xml.gz"
		if err := os.WriteFile(filepath.Join(repoDir, filepath.FromSlash(uHref)), uBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		mdEntries = append(mdEntries, repomdEntrySpec{DataType: "updateinfo", RelHref: uHref, RawBytes: uBytes})
	}

	var repomd bytes.Buffer
	repomd.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<repomd xmlns=\"http://linux.duke.edu/metadata/repo\">\n")
	if spec.Revision != "" {
		fmt.Fprintf(&repomd, "  <revision>%s</revision>\n", html.EscapeString(spec.Revision))
	}
	for _, entry := range mdEntries {
		fmt.Fprintf(&repomd, `  <data type="%s">
    <checksum type="sha256">%s</checksum>
    <location href="%s"/>
    <timestamp>%d</timestamp>
    <size>%d</size>
  </data>
`, html.EscapeString(entry.DataType), sha256Hex(entry.RawBytes), html.EscapeString(entry.RelHref), deterministicFixtureTime.Unix(), len(entry.RawBytes))
	}
	repomd.WriteString("</repomd>\n")
	if err := os.WriteFile(filepath.Join(repodataDir, "repomd.xml"), repomd.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	return written
}

// ---------- DEB / DDEB Pure-Go Synthetic Builders ----------

type SynthDebFileSpec struct {
	Path          string // Archive path e.g. "./usr/bin/app"
	Content       []byte
	Mode          int64
	SymlinkTarget string
}

type SynthDebSpec struct {
	Name            string
	Version         string
	Arch            string
	Section         string
	Priority        string
	Maintainer      string
	Homepage        string
	Source          string
	InstalledSize   int
	MultiArch       string
	BuiltUsing      string
	Tag             string
	Depends         string
	Recommends      string
	Suggests        string
	Conflicts       string
	Breaks          string
	Replaces        string
	Description     string
	LongDescription string
	Conffiles       []string
	PreInst         string
	PostInst        string
	PreRm           string
	PostRm          string
	Triggers        string
	Changelog       string
	Copyright       string
	ExtraFiles      []SynthDebFileSpec
	BuildIDs        []string // For .ddeb archives: emits /usr/lib/debug/.build-id/<2-char>/<rest>.debug
	ControlComp     string   // "gz" (default), "xz", "zst"
	DataComp        string   // "gz" (default), "xz", "zst"
	Extension       string   // ".deb" (default) or ".ddeb"
	Filename        string   // Optional override for Packages Filename: field
	SkipBinaryWrite bool
}

func makeDeterministicTar(t *testing.T, ext string, entries []SynthDebFileSpec) []byte {
	t.Helper()
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)

	for _, e := range entries {
		mode := e.Mode
		if mode == 0 {
			mode = 0o755
		}
		hdr := &tar.Header{
			Name:    e.Path,
			Mode:    mode,
			ModTime: deterministicFixtureTime,
			Uname:   "root",
			Gname:   "root",
		}
		if e.SymlinkTarget != "" {
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = e.SymlinkTarget
			hdr.Mode = 0o777
		} else {
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(e.Content))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("failed writing tar header %s: %v", e.Path, err)
		}
		if hdr.Typeflag == tar.TypeReg && len(e.Content) > 0 {
			if _, err := tw.Write(e.Content); err != nil {
				t.Fatalf("failed writing tar body %s: %v", e.Path, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("failed closing tar writer: %v", err)
	}
	return compressBytes(t, ext, tarBuf.Bytes())
}

// BuildSyntheticDeb builds an authentic ar .deb or .ddeb archive supporting .gz, .xz, and .zst members,
// writes it to outPath (unless SkipBinaryWrite), and returns byte size, SHA-256 hex digest, and Deb822 Packages stanza.
func BuildSyntheticDeb(t *testing.T, outPath, relHref string, spec SynthDebSpec) (int64, string, string) {
	t.Helper()
	if spec.Arch == "" {
		spec.Arch = "amd64"
	}
	if spec.Section == "" {
		if strings.HasSuffix(spec.Extension, ".ddeb") {
			spec.Section = "debug"
		} else {
			spec.Section = "utils"
		}
	}
	if spec.Priority == "" {
		spec.Priority = "optional"
	}
	if spec.Maintainer == "" {
		spec.Maintainer = "Debian Maintainer <maint@example.com>"
	}
	if spec.InstalledSize == 0 {
		spec.InstalledSize = 512
	}
	if spec.Description == "" {
		spec.Description = "Synthetic test package " + spec.Name
	}
	if spec.LongDescription == "" {
		spec.LongDescription = "Deterministic integration test package."
	}
	cExt := spec.ControlComp
	if cExt == "" {
		cExt = "gz"
	}
	dExt := spec.DataComp
	if dExt == "" {
		dExt = "gz"
	}

	// Build data entries first so md5sums reflects real payloads
	var dataEntries []SynthDebFileSpec
	dataEntries = append(dataEntries, spec.ExtraFiles...)

	if len(spec.ExtraFiles) == 0 && len(spec.BuildIDs) == 0 {
		dataEntries = append(dataEntries, SynthDebFileSpec{
			Path:    "./usr/bin/" + spec.Name,
			Mode:    0o755,
			Content: []byte("#!/bin/sh\necho " + spec.Name + "\n"),
		})
	}
	for _, bid := range spec.BuildIDs {
		cleanID := strings.TrimSpace(bid)
		if len(cleanID) > 4 {
			dataEntries = append(dataEntries, SynthDebFileSpec{
				Path:    fmt.Sprintf("./usr/lib/debug/.build-id/%s/%s.debug", cleanID[:2], cleanID[2:]),
				Mode:    0o644,
				Content: []byte("ELF-DEBUG-" + cleanID + "\n"),
			})
		}
	}
	if spec.Changelog != "" {
		gzLog := compressBytes(t, "gz", []byte(spec.Changelog))
		dataEntries = append(dataEntries, SynthDebFileSpec{
			Path:    fmt.Sprintf("./usr/share/doc/%s/changelog.Debian.gz", spec.Name),
			Mode:    0o644,
			Content: gzLog,
		})
	}
	if spec.Copyright != "" {
		dataEntries = append(dataEntries, SynthDebFileSpec{
			Path:    fmt.Sprintf("./usr/share/doc/%s/copyright", spec.Name),
			Mode:    0o644,
			Content: []byte(spec.Copyright),
		})
	}

	var md5Buf strings.Builder
	for _, de := range dataEntries {
		if de.SymlinkTarget == "" {
			cleanPath := strings.TrimPrefix(strings.TrimPrefix(de.Path, "./"), "/")
			fmt.Fprintf(&md5Buf, "%s  %s\n", md5Hex(de.Content), cleanPath)
		}
	}

	var controlFields strings.Builder
	fmt.Fprintf(&controlFields, "Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: %s\nInstalled-Size: %d\nSection: %s\nPriority: %s\n",
		spec.Name, spec.Version, spec.Arch, spec.Maintainer, spec.InstalledSize, spec.Section, spec.Priority)
	if spec.Source != "" {
		fmt.Fprintf(&controlFields, "Source: %s\n", spec.Source)
	}
	if spec.Homepage != "" {
		fmt.Fprintf(&controlFields, "Homepage: %s\n", spec.Homepage)
	}
	if spec.MultiArch != "" {
		fmt.Fprintf(&controlFields, "Multi-Arch: %s\n", spec.MultiArch)
	}
	if spec.BuiltUsing != "" {
		fmt.Fprintf(&controlFields, "Built-Using: %s\n", spec.BuiltUsing)
	}
	if spec.Tag != "" {
		fmt.Fprintf(&controlFields, "Tag: %s\n", spec.Tag)
	}
	if spec.Depends != "" {
		fmt.Fprintf(&controlFields, "Depends: %s\n", spec.Depends)
	}
	if spec.Recommends != "" {
		fmt.Fprintf(&controlFields, "Recommends: %s\n", spec.Recommends)
	}
	if spec.Suggests != "" {
		fmt.Fprintf(&controlFields, "Suggests: %s\n", spec.Suggests)
	}
	if spec.Conflicts != "" {
		fmt.Fprintf(&controlFields, "Conflicts: %s\n", spec.Conflicts)
	}
	if spec.Breaks != "" {
		fmt.Fprintf(&controlFields, "Breaks: %s\n", spec.Breaks)
	}
	if spec.Replaces != "" {
		fmt.Fprintf(&controlFields, "Replaces: %s\n", spec.Replaces)
	}
	fmt.Fprintf(&controlFields, "Description: %s\n %s\n", spec.Description, spec.LongDescription)

	controlEntries := []SynthDebFileSpec{
		{Path: "./control", Mode: 0o644, Content: []byte(controlFields.String())},
		{Path: "./md5sums", Mode: 0o644, Content: []byte(md5Buf.String())},
	}
	if len(spec.Conffiles) > 0 {
		controlEntries = append(controlEntries, SynthDebFileSpec{
			Path:    "./conffiles",
			Mode:    0o644,
			Content: []byte(strings.Join(spec.Conffiles, "\n") + "\n"),
		})
	}
	if spec.PreInst != "" {
		controlEntries = append(controlEntries, SynthDebFileSpec{Path: "./preinst", Mode: 0o755, Content: []byte(spec.PreInst)})
	}
	if spec.PostInst != "" {
		controlEntries = append(controlEntries, SynthDebFileSpec{Path: "./postinst", Mode: 0o755, Content: []byte(spec.PostInst)})
	}
	if spec.PreRm != "" {
		controlEntries = append(controlEntries, SynthDebFileSpec{Path: "./prerm", Mode: 0o755, Content: []byte(spec.PreRm)})
	}
	if spec.PostRm != "" {
		controlEntries = append(controlEntries, SynthDebFileSpec{Path: "./postrm", Mode: 0o755, Content: []byte(spec.PostRm)})
	}
	if spec.Triggers != "" {
		controlEntries = append(controlEntries, SynthDebFileSpec{Path: "./triggers", Mode: 0o644, Content: []byte(spec.Triggers)})
	}

	controlTar := makeDeterministicTar(t, cExt, controlEntries)
	dataTar := makeDeterministicTar(t, dExt, dataEntries)

	var arBuf bytes.Buffer
	arBuf.WriteString("!<arch>\n")
	writeArMember := func(name string, data []byte) {
		hdr := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8o%-10d`\n",
			name, deterministicFixtureTime.Unix(), 0, 0, 0o644, len(data))
		arBuf.WriteString(hdr)
		arBuf.Write(data)
		if len(data)%2 != 0 {
			arBuf.WriteByte('\n')
		}
	}
	writeArMember("debian-binary", []byte("2.0\n"))
	writeArMember("control.tar."+cExt, controlTar)
	writeArMember("data.tar."+dExt, dataTar)

	archiveBytes := arBuf.Bytes()
	size := int64(len(archiveBytes))
	digest := sha256Hex(archiveBytes)

	if !spec.SkipBinaryWrite {
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			t.Fatalf("failed mkdir for deb %s: %v", outPath, err)
		}
		if err := os.WriteFile(outPath, archiveBytes, 0o644); err != nil {
			t.Fatalf("failed writing synthetic deb %s: %v", outPath, err)
		}
	}

	href := spec.Filename
	if href == "" {
		href = relHref
	}
	var stanza strings.Builder
	stanza.WriteString(controlFields.String())
	fmt.Fprintf(&stanza, "Filename: %s\nSize: %d\nSHA256: %s\n\n", href, size, digest)

	return size, digest, stanza.String()
}

// DebIndexGroup describes one component+arch index to materialize inside a Debian/Ubuntu repository.
type DebIndexGroup struct {
	Suite        string // Empty => flat repository
	Codename     string
	Component    string // Empty => flat repository (or e.g. "main", "universe")
	IsDebug      bool   // When true under APT dists/, places index under <comp>/debug/binary-<arch>
	Arch         string
	IndexComp    string // "", "gz", "xz", "zst", "bz2"
	UseInRelease bool   // Wraps suite Release in PGP clearsigned InRelease instead of plain Release
	OmitRelease  bool   // Omits Release/InRelease to verify fallback arch discovery (e.g. DDEB-02)
	PoolFlatRoot bool   // If true in flat mode, drops archive next to Packages
	Label        string
	Origin       string
	Description  string
	Packages     []SynthDebSpec
}

// WriteDebRepo writes one or more Debian index groups (suites/components/arches or flat layouts)
// into repoDir and returns WrittenArchiveInfo keyed by package Filename href.
func WriteDebRepo(t *testing.T, repoDir string, groups []DebIndexGroup) map[string]WrittenArchiveInfo {
	t.Helper()
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("failed creating deb repo dir %s: %v", repoDir, err)
	}

	written := make(map[string]WrittenArchiveInfo)
	type suiteMeta struct {
		codename     string
		origin       string
		label        string
		description  string
		components   map[string]bool
		archs        map[string]bool
		useInRelease bool
		omitRelease  bool
	}
	suites := make(map[string]*suiteMeta)

	for _, g := range groups {
		arch := g.Arch
		if arch == "" {
			arch = "amd64"
		}
		isFlat := g.Suite == ""

		var indexDir string
		if isFlat {
			indexDir = repoDir
		} else {
			comp := g.Component
			if comp == "" {
				comp = "main"
			}
			if g.IsDebug {
				indexDir = filepath.Join(repoDir, "dists", g.Suite, comp, "debug", "binary-"+arch)
			} else {
				indexDir = filepath.Join(repoDir, "dists", g.Suite, comp, "binary-"+arch)
			}

			sm, exists := suites[g.Suite]
			if !exists {
				sm = &suiteMeta{
					codename:    g.Codename,
					origin:      g.Origin,
					label:       g.Label,
					description: g.Description,
					components:  make(map[string]bool),
					archs:       make(map[string]bool),
				}
				if sm.codename == "" {
					sm.codename = g.Suite
				}
				suites[g.Suite] = sm
			}
			sm.components[comp] = true
			sm.archs[arch] = true
			if g.UseInRelease {
				sm.useInRelease = true
			}
			if g.OmitRelease {
				sm.omitRelease = true
			}
		}

		if err := os.MkdirAll(indexDir, 0o755); err != nil {
			t.Fatalf("failed creating deb index dir %s: %v", indexDir, err)
		}

		if g.IndexComp == "bz2" && len(g.Packages) == 0 {
			rawBz2, err := base64.StdEncoding.DecodeString(samplePackagesBz2Base64)
			if err != nil {
				t.Fatalf("failed decoding sample bz2 fixture: %v", err)
			}
			if err := os.WriteFile(filepath.Join(indexDir, "Packages.bz2"), rawBz2, 0o644); err != nil {
				t.Fatalf("failed writing Packages.bz2: %v", err)
			}
		} else {
			var packagesText strings.Builder
			for _, p := range g.Packages {
				pkgArch := p.Arch
				if pkgArch == "" {
					pkgArch = arch
				}
				ext := p.Extension
				if ext == "" {
					ext = ".deb"
				}
				fileNameOnly := fmt.Sprintf("%s_%s_%s%s", p.Name, p.Version, pkgArch, ext)
				var relHref string
				if isFlat {
					relHref = fileNameOnly
				} else {
					comp := g.Component
					if comp == "" {
						comp = "main"
					}
					first := "x"
					if len(p.Name) > 0 {
						first = strings.ToLower(p.Name[:1])
					}
					relHref = fmt.Sprintf("pool/%s/%s/%s/%s", comp, first, p.Name, fileNameOnly)
				}
				if p.Filename != "" {
					relHref = p.Filename
				}

				absPath := filepath.Join(repoDir, filepath.FromSlash(relHref))
				if strings.Contains(relHref, "..") {
					absPath = filepath.Join(repoDir, fileNameOnly)
				}
				sz, digest, stanza := BuildSyntheticDeb(t, absPath, relHref, p)
				packagesText.WriteString(stanza)
				if !p.SkipBinaryWrite && !strings.Contains(relHref, "..") {
					written[relHref] = WrittenArchiveInfo{
						RelHref: relHref,
						AbsPath: absPath,
						Size:    sz,
						SHA256:  digest,
					}
				}
			}

			compExt := g.IndexComp
			if compExt == "" {
				compExt = "gz"
			}
			idxName := "Packages"
			var idxBytes []byte
			switch compExt {
			case "plain", "none":
				idxBytes = []byte(packagesText.String())
			case "bz2":
				// Use deterministic pre-encoded bzip2 fixture when explicitly requested without custom overrides
				rawBz2, err := base64.StdEncoding.DecodeString(samplePackagesBz2Base64)
				if err != nil {
					t.Fatalf("failed decoding sample bz2 fixture: %v", err)
				}
				idxName = "Packages.bz2"
				idxBytes = rawBz2
			default:
				idxName = "Packages." + compExt
				idxBytes = compressBytes(t, compExt, []byte(packagesText.String()))
			}
			if err := os.WriteFile(filepath.Join(indexDir, idxName), idxBytes, 0o644); err != nil {
				t.Fatalf("failed writing %s: %v", idxName, err)
			}
		}

		if isFlat && !g.OmitRelease {
			origin := g.Origin
			if origin == "" {
				origin = "RepoView-Flat"
			}
			label := g.Label
			if label == "" {
				label = "Flat APT Repo"
			}
			desc := g.Description
			if desc == "" {
				desc = "Synthetic flat Debian archive"
			}
			relContent := fmt.Sprintf("Origin: %s\nLabel: %s\nArchitectures: %s\nDescription: %s\n", origin, label, arch, desc)
			relName := "Release"
			if g.UseInRelease {
				relName = "InRelease"
				relContent = wrapClearsignedPGP(relContent)
			}
			if err := os.WriteFile(filepath.Join(repoDir, relName), []byte(relContent), 0o644); err != nil {
				t.Fatalf("failed writing flat %s: %v", relName, err)
			}
		}
	}

	for suiteName, sm := range suites {
		if sm.omitRelease {
			continue
		}
		var compList, archList []string
		for c := range sm.components {
			compList = append(compList, c)
		}
		for a := range sm.archs {
			archList = append(archList, a)
		}
		sort.Strings(compList)
		sort.Strings(archList)

		origin := sm.origin
		if origin == "" {
			origin = "RepoView"
		}
		label := sm.label
		if label == "" {
			label = suiteName
		}
		desc := sm.description
		if desc == "" {
			desc = "Synthetic " + suiteName + " repository"
		}

		relContent := fmt.Sprintf("Origin: %s\nLabel: %s\nSuite: %s\nCodename: %s\nArchitectures: %s\nComponents: %s\nDescription: %s\n",
			origin, label, suiteName, sm.codename, strings.Join(archList, " "), strings.Join(compList, " "), desc)
		relName := "Release"
		if sm.useInRelease {
			relName = "InRelease"
			relContent = wrapClearsignedPGP(relContent)
		}
		if err := os.WriteFile(filepath.Join(repoDir, "dists", suiteName, relName), []byte(relContent), 0o644); err != nil {
			t.Fatalf("failed writing %s for suite %s: %v", relName, suiteName, err)
		}
	}

	return written
}

func wrapClearsignedPGP(payload string) string {
	return "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA256\n\n" +
		strings.TrimRight(payload, "\n") + "\n" +
		"-----BEGIN PGP SIGNATURE-----\nVersion: GnuPG v2\n\niQEzBAEBCAAdFiEEtesttesttesttesttesttesttestAAoJEBtest\n=Test\n-----END PGP SIGNATURE-----\n"
}

// ---------- Multi-Distro Enterprise Mirror Matrix Builder ----------

type EnterpriseMirrorOpts struct {
	IncludeEL8        bool
	IncludeEL9        bool
	IncludeEL10Slug   bool
	IncludeUbuntu24   bool
	IncludeUbuntu26   bool
	IncludeDebian12   bool
	IncludeDebian13   bool
	IncludeDDEB       bool
	IncludeCustomCorp bool
	IncludeSymlinks   bool
}

type EnterpriseMirrorInfo struct {
	RootDir      string
	RPMRepoPaths []string
	DEBRepoPaths []string
	SymlinkPaths []string
	RPMArchives  map[string]WrittenArchiveInfo
	DEBArchives  map[string]WrittenArchiveInfo
}

// BuildEnterpriseMatrix composes realistic multi-distro mirror topologies inside t.TempDir().
func BuildEnterpriseMatrix(t *testing.T, opts EnterpriseMirrorOpts) EnterpriseMirrorInfo {
	t.Helper()
	root := t.TempDir()
	info := EnterpriseMirrorInfo{
		RootDir:     root,
		RPMArchives: make(map[string]WrittenArchiveInfo),
		DEBArchives: make(map[string]WrittenArchiveInfo),
	}

	addRPM := func(rel string, spec RPMRepoSpec) {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		info.RPMRepoPaths = append(info.RPMRepoPaths, abs)
		for k, v := range WriteRPMRepo(t, abs, spec) {
			info.RPMArchives[rel+"/"+k] = v
		}
	}
	addDEB := func(rel string, groups []DebIndexGroup) {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		info.DEBRepoPaths = append(info.DEBRepoPaths, abs)
		for k, v := range WriteDebRepo(t, abs, groups) {
			info.DEBArchives[rel+"/"+k] = v
		}
	}

	if opts.IncludeEL8 {
		addRPM("el8/base/x86_64", RPMRepoSpec{
			Revision: "el8-base-2026",
			Packages: []SynthRPMSpec{{
				Name: "basesystem", Epoch: "0", Version: "11", Release: "13.el8", Arch: "x86_64",
				Summary: "EL8 skeleton package", Description: "Core EL8 layout.", Group: "System Environment/Base",
				Files: []SynthRPMFile{{Dir: "/etc/", Name: "redhat-release", Mode: 0o100644, Size: 64, Flags: 1}},
			}},
		})
		addRPM("el8/extras/x86_64", RPMRepoSpec{
			Revision: "el8-extras-2026",
			Packages: []SynthRPMSpec{{
				Name: "epel-release", Epoch: "0", Version: "8", Release: "21.el8", Arch: "x86_64",
				Summary: "EL8 extra repository config", Description: "Extra utilities for EL8.", Group: "System Environment/Base",
			}},
		})
	}

	if opts.IncludeEL9 {
		for _, ch := range []string{"baseos", "appstream"} {
			for _, arch := range []string{"x86_64", "aarch64"} {
				pkgName := "systemd"
				if ch == "appstream" {
					pkgName = "nginx"
				}
				addRPM(fmt.Sprintf("el9/%s/%s", ch, arch), RPMRepoSpec{
					Revision: "el9-" + ch + "-" + arch,
					Packages: []SynthRPMSpec{{
						Name: pkgName, Epoch: "0", Version: "1.24.0", Release: "1.el9", Arch: arch,
						Summary: "EL9 " + ch + " package", Description: "EL9 service package.", Group: "Applications/Internet",
						Provides: []SynthRPMDep{{Name: pkgName + "-core", Flags: "EQ", Epoch: "0", Version: "1.24.0", Release: "1.el9"}},
					}},
				})
			}
		}
	}

	if opts.IncludeEL10Slug {
		addRPM("el10-base.x86_64", RPMRepoSpec{
			Revision: "el10-1",
			Packages: []SynthRPMSpec{{
				Name: "dnf5", Epoch: "0", Version: "5.2.0", Release: "1.el10", Arch: "x86_64",
				Summary: "Next-gen EL10 package manager", Description: "Fast C++ package manager for EL10.", Group: "System Environment/Base",
			}},
		})
	}

	if opts.IncludeUbuntu24 {
		addDEB("ubu24", []DebIndexGroup{
			{
				Suite: "noble", Codename: "noble", Component: "main", Arch: "amd64", IndexComp: "zst",
				Origin: "Ubuntu", Description: "Ubuntu 24.04 Main",
				Packages: []SynthDebSpec{{
					Name: "nginx", Version: "1.24.0-2ubuntu7", Arch: "amd64", Section: "httpd",
					ControlComp: "zst", DataComp: "zst", Description: "High performance HTTP server",
				}},
			},
			{
				Suite: "noble", Codename: "noble", Component: "universe", Arch: "amd64", IndexComp: "gz",
				Origin: "Ubuntu", Description: "Ubuntu 24.04 Universe",
				Packages: []SynthDebSpec{{
					Name: "jq", Version: "1.7.1-3", Arch: "amd64", Section: "utils",
					ControlComp: "zst", DataComp: "zst", Description: "Command-line JSON processor",
				}},
			},
		})
	}

	if opts.IncludeUbuntu26 {
		addDEB("ubu26/custom", []DebIndexGroup{{
			Arch: "amd64", IndexComp: "gz", Origin: "Ubuntu", Label: "Ubuntu 26.04 Custom",
			Packages: []SynthDebSpec{{
				Name: "caddy", Version: "2.8.4-1ubuntu1", Arch: "amd64", Section: "httpd",
				ControlComp: "zst", DataComp: "zst", Description: "Modern TLS web server",
			}},
		}})
	}

	if opts.IncludeDebian12 {
		addDEB("deb12", []DebIndexGroup{{
			Suite: "bookworm", Codename: "bookworm", Component: "main", Arch: "amd64", IndexComp: "xz",
			Origin: "Debian", Description: "Debian 12 Bookworm",
			Packages: []SynthDebSpec{{
				Name: "curl", Version: "7.88.1-10", Arch: "amd64", Section: "web",
				ControlComp: "xz", DataComp: "xz", Description: "Transfer data with URLs",
			}},
		}})
	}

	if opts.IncludeDebian13 {
		addDEB("deb13", []DebIndexGroup{
			{
				Suite: "trixie", Codename: "trixie", Component: "main", Arch: "amd64", IndexComp: "xz",
				Origin: "Debian", Description: "Debian 13 Trixie Main",
				Packages: []SynthDebSpec{{
					Name: "openssl", Version: "3.4.0-1", Arch: "amd64", Section: "libs",
					ControlComp: "xz", DataComp: "xz", Description: "Secure Sockets Layer toolkit",
				}},
			},
			{
				Suite: "trixie", Codename: "trixie", Component: "contrib", Arch: "amd64", IndexComp: "xz",
				Origin: "Debian", Description: "Debian 13 Trixie Contrib",
				Packages: []SynthDebSpec{{
					Name: "zfs-dkms", Version: "2.2.7-1", Arch: "amd64", Section: "contrib/kernel",
					ControlComp: "xz", DataComp: "xz", Description: "OpenZFS kernel modules",
				}},
			},
		})
	}

	if opts.IncludeDDEB {
		addDEB("ddebs", []DebIndexGroup{{
			Suite: "noble", Codename: "noble", Component: "main", IsDebug: true, Arch: "amd64", IndexComp: "gz",
			Origin: "Ubuntu-Debug", Description: "Ubuntu 24.04 Debug Symbols",
			Packages: []SynthDebSpec{{
				Name: "nginx-dbgsym", Version: "1.24.0-2ubuntu7", Arch: "amd64", Section: "debug",
				Extension: ".ddeb", ControlComp: "zst", DataComp: "zst",
				BuildIDs:    []string{"deadbeefcafebabe1234567890abcdef"},
				Description: "debug symbols for nginx",
			}},
		}})
	}

	if opts.IncludeCustomCorp {
		addRPM("corp/platform/el9/base/x86_64", RPMRepoSpec{
			Revision: "corp-1",
			Packages: []SynthRPMSpec{{
				Name: "corp-agent", Epoch: "0", Version: "3.1.0", Release: "1.el9", Arch: "x86_64",
				Summary: "Corp telemetry agent", Description: "Internal corporate monitoring agent.", Group: "Applications/System",
			}},
		})
	}

	if opts.IncludeSymlinks {
		if opts.IncludeEL9 {
			linkPath := filepath.Join(root, "el9", "latest")
			target := "appstream"
			if err := os.Symlink(target, linkPath); err == nil {
				info.SymlinkPaths = append(info.SymlinkPaths, linkPath)
			}
		}
	}

	return info
}
