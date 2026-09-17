package deb

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edsilegxrepo/repoview/internal/models"
)

// Helper to build an in-memory tar.gz
func buildTarGz(files map[string][]byte) []byte {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for name, content := range files {
		hdr := &tar.Header{
			Name:    name,
			Mode:    0o755,
			Size:    int64(len(content)),
			ModTime: time.Now(),
			Uname:   "root",
			Gname:   "root",
		}
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write(content)
	}

	_ = tw.Close()
	_ = gw.Close()
	return buf.Bytes()
}

// Helper to build an in-memory .deb file (ar archive)
func buildDebArchive(controlTarGz, dataTarGz []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("!<arch>\n")

	writeArMember := func(name string, data []byte) {
		hdr := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8o%-10d`\n",
			name, time.Now().Unix(), 0, 0, 0o644, len(data))
		buf.WriteString(hdr)
		buf.Write(data)
		if len(data)%2 != 0 {
			buf.WriteByte('\n')
		}
	}

	writeArMember("debian-binary", []byte("2.0\n"))
	writeArMember("control.tar.gz", controlTarGz)
	writeArMember("data.tar.gz", dataTarGz)

	return buf.Bytes()
}

func TestReadDebDetails_And_Files(t *testing.T) {
	// 1. Prepare changelog text
	rawChangelog := `samplepkg (1.0.0-1) unstable; urgency=medium

  * Release 1.0.0-1 with Debian inspection support.

 -- Debian Dev <dev@example.org>  Thu, 17 Sep 2026 12:00:00 +0000
`
	var gzChangelog bytes.Buffer
	gw := gzip.NewWriter(&gzChangelog)
	_, _ = gw.Write([]byte(rawChangelog))
	_ = gw.Close()

	// 2. Build control.tar.gz
	controlFiles := map[string][]byte{
		"./control":  []byte("Package: samplepkg\nVersion: 1.0.0-1\nArchitecture: amd64\n"),
		"./preinst":  []byte("#!/bin/sh\necho 'preinst running'\n"),
		"./postinst": []byte("#!/bin/sh\necho 'postinst running'\n"),
		"./prerm":    []byte("#!/bin/sh\necho 'prerm running'\n"),
		"./postrm":   []byte("#!/bin/sh\necho 'postrm running'\n"),
	}
	controlTarGz := buildTarGz(controlFiles)

	// 3. Build data.tar.gz
	dataFiles := map[string][]byte{
		"./usr/bin/samplepkg":                           []byte("#!/bin/sh\necho hello\n"),
		"./usr/share/doc/samplepkg/changelog.Debian.gz": gzChangelog.Bytes(),
	}
	dataTarGz := buildTarGz(dataFiles)

	// 4. Build .deb
	debBytes := buildDebArchive(controlTarGz, dataTarGz)

	tmpDir := t.TempDir()
	debPath := filepath.Join(tmpDir, "samplepkg_1.0.0-1_amd64.deb")
	if err := os.WriteFile(debPath, debBytes, 0o644); err != nil {
		t.Fatalf("failed to write deb file: %v", err)
	}

	// 5. Test ReadDebDetails
	details, err := ReadDebDetails(debPath)
	if err != nil {
		t.Fatalf("ReadDebDetails failed: %v", err)
	}
	if details == nil || details.Scriptlets == nil {
		t.Fatalf("expected scriptlets to be populated")
	}

	if !strings.Contains(details.Scriptlets.PreIn, "preinst running") {
		t.Errorf("expected PreIn to contain 'preinst running', got %q", details.Scriptlets.PreIn)
	}
	if !strings.Contains(details.Scriptlets.PostIn, "postinst running") {
		t.Errorf("expected PostIn to contain 'postinst running', got %q", details.Scriptlets.PostIn)
	}
	if !strings.Contains(details.Scriptlets.PreUn, "prerm running") {
		t.Errorf("expected PreUn to contain 'prerm running', got %q", details.Scriptlets.PreUn)
	}
	if !strings.Contains(details.Scriptlets.PostUn, "postrm running") {
		t.Errorf("expected PostUn to contain 'postrm running', got %q", details.Scriptlets.PostUn)
	}

	// 6. Test ReadDebFiles
	files, err := ReadDebFiles(debPath)
	if err != nil {
		t.Fatalf("ReadDebFiles failed: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}

	foundBin := false
	for _, f := range files {
		if f.Name == "/usr/bin/samplepkg" {
			foundBin = true
			if f.Size == 0 {
				t.Errorf("expected non-zero size for binary file")
			}
		}
	}
	if !foundBin {
		t.Errorf("expected /usr/bin/samplepkg in file manifest")
	}

	// 7. Test ExtractChangelogFromDeb
	cl, err := ExtractChangelogFromDeb(debPath)
	if err != nil {
		t.Fatalf("ExtractChangelogFromDeb failed: %v", err)
	}
	if cl == nil {
		t.Fatalf("expected changelog entry, got nil")
	}
	if !strings.Contains(cl.Author, "Debian Dev") {
		t.Errorf("expected author 'Debian Dev', got %q", cl.Author)
	}
	if !strings.Contains(cl.Changelog, "Release 1.0.0-1") {
		t.Errorf("expected changelog content to contain Release 1.0.0-1, got %q", cl.Changelog)
	}

	// 8. Test DebRepository deep inspection integration
	locs := &DebRepoLocations{
		BaseDir: tmpDir,
	}
	debRepo := NewDebRepository(locs, nil)
	pkg := &models.Package{
		Name:         "samplepkg",
		LocationHref: "samplepkg_1.0.0-1_amd64.deb",
	}

	// Verify ReadDebTimestamp directly
	ts := ReadDebTimestamp(debPath)
	if ts <= 0 {
		t.Errorf("expected ReadDebTimestamp > 0, got %d", ts)
	}
	if notFoundTs := ReadDebTimestamp(filepath.Join(tmpDir, "nonexistent.deb")); notFoundTs != 0 {
		t.Errorf("expected ReadDebTimestamp for nonexistent file to be 0, got %d", notFoundTs)
	}

	debRepo.(*DebRepository).EnrichPackageDetails(tmpDir, []*models.Package{pkg})
	if pkg.Details == nil || pkg.Details.Scriptlets == nil {
		t.Fatalf("EnrichPackageDetails failed to populate pkg.Details")
	}
	if pkg.TimeBuild <= 0 {
		t.Errorf("expected pkg.TimeBuild > 0 after EnrichPackageDetails, got %d", pkg.TimeBuild)
	}

	extractedFiles, err := debRepo.ReadPackageFiles(tmpDir, pkg)
	if err != nil {
		t.Fatalf("ReadPackageFiles failed: %v", err)
	}
	if len(extractedFiles) != 2 {
		t.Errorf("expected 2 files from ReadPackageFiles, got %d", len(extractedFiles))
	}
	if pkg.Changelog == nil {
		t.Errorf("expected pkg.Changelog to be populated on-demand by ReadPackageFiles")
	}
}
