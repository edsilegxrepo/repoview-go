package deb

// OBJECTIVES:
// Inspect local .deb and .ddeb ar archives in pure Go (no dpkg-deb dependency) to
// extract build timestamps, maintainer scripts, conffiles, file manifests, changelogs,
// and DEP-5 license identifiers.
//
// CORE COMPONENTS:
//   - ReadDebTimestamp / readArTimestampFromFile: Reads 60-byte ar header build epochs.
//   - ReadDebDetails / ReadDebDetailsWithTimestamp: Extracts control.tar.* scripts & triggers.
//   - ReadDebRenderMetadata: Single-pass combined reader for control + data tar streams.
//   - ReadDebFilesMetadata / streamDataTarMetadata: Extracts files, copyright, and changelog.
//
// FUNCTIONALITY:
//   - Caps scriptlet reads at 1 MiB and copyright reads at 256 KiB.
//   - Marks files declared in DEBIAN/conffiles with the "config" role flag.
//
// DATA FLOW:
//   .deb/.ddeb file -> ar header + control.tar + data.tar -> models.PackageDetails & []PackageFile

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/edsilegxrepo/repoview/internal/models"
	pdeb "pault.ag/go/debian/deb"
)

// --- Archive Timestamps & Control Member Extraction ---

// ReadDebTimestamp extracts the package build timestamp from the .deb archive.
// It inspects the first ar archive member ("debian-binary"), which per Debian policy
// is placed first in the archive, and reads the 12-byte decimal ASCII timestamp header.
// If reading the ar header fails, it falls back to the file modification time on disk.
func ReadDebTimestamp(debPath string) int64 {
	// #nosec G304 -- debPath is validated against directory traversal by repository discovery
	f, err := os.Open(filepath.Clean(debPath))
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	return readArTimestampFromFile(f)
}

func readArTimestampFromFile(f *os.File) int64 {
	// Ar file header is 8 bytes ("!<arch>\n") followed by 60-byte member headers:
	// name (16 bytes), timestamp (12 bytes), ...
	var buf [68]byte
	if _, err := f.Seek(0, io.SeekStart); err == nil {
		n, err := io.ReadFull(f, buf[:])
		if err == nil && n >= 68 && string(buf[:8]) == "!<arch>\n" {
			// Bytes 8 to 24 are member name (16 bytes), Bytes 24 to 36 are timestamp (12 bytes)
			tsStr := strings.TrimSpace(string(buf[24:36]))
			if ts, parseErr := strconv.ParseInt(tsStr, 10, 64); parseErr == nil && ts > 0 {
				return ts
			}
		}
	}
	if fi, err := f.Stat(); err == nil {
		return fi.ModTime().Unix()
	}
	return 0
}

// ReadDebDetails extracts maintainer scripts from control.tar.* inside a .deb archive.
func ReadDebDetails(debPath string) (*models.PackageDetails, error) {
	details, _, err := ReadDebDetailsWithTimestamp(debPath)
	return details, err
}

// ReadDebDetailsWithTimestamp extracts maintainer scripts and the ar build timestamp
// from a .deb archive in a single file open pass.
func ReadDebDetailsWithTimestamp(debPath string) (*models.PackageDetails, int64, error) {
	// #nosec G304 -- debPath is validated against directory traversal by repository discovery
	f, err := os.Open(filepath.Clean(debPath))
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()

	ts := readArTimestampFromFile(f)

	debFile, err := pdeb.Load(f, debPath)
	if err != nil {
		return nil, ts, err
	}

	details := extractDebControlDetails(debFile)
	return details, ts, nil
}

// ReadDebRenderMetadata extracts ar timestamp, control details, file manifests, changelog,
// and fallback DEP-5 license from a single .deb archive open pass.
func ReadDebRenderMetadata(debPath string) (*models.PackageDetails, int64, []models.PackageFile, *models.ChangelogEntry, string, error) {
	// #nosec G304 -- debPath is validated against directory traversal by repository discovery
	f, err := os.Open(filepath.Clean(debPath))
	if err != nil {
		return nil, 0, nil, nil, "", err
	}
	defer func() { _ = f.Close() }()

	ts := readArTimestampFromFile(f)

	debFile, err := pdeb.Load(f, debPath)
	if err != nil {
		return nil, ts, nil, nil, "", err
	}

	details := extractDebControlDetails(debFile)
	var conffiles map[string]bool
	if details != nil {
		conffiles = details.Conffiles
	}
	files, changelog, detectedLicense := streamDataTarMetadata(debFile.Data, conffiles)
	return details, ts, files, changelog, detectedLicense, nil
}

func extractDebControlDetails(debFile *pdeb.Deb) *models.PackageDetails {
	details := &models.PackageDetails{}
	scriptlets := &models.PackageScriptlets{}

	// Access control.tar member directly from the Ar container
	var controlMember *pdeb.ArEntry
	controlMemberKey := "control.tar." + debFile.ControlExt
	if debFile.ControlExt == "" {
		controlMemberKey = "control.tar"
	}
	if member, ok := debFile.ArContent[controlMemberKey]; ok {
		controlMember = member
	} else {
		for k, v := range debFile.ArContent {
			if strings.HasPrefix(k, "control.tar") {
				controlMember = v
				break
			}
		}
	}

	if controlMember != nil {
		if controlMember.Data != nil {
			_, _ = controlMember.Data.Seek(0, io.SeekStart)
		}
		archive, tarCloser, err := controlMember.Tarfile()
		if err == nil {
			defer func() {
				_ = tarCloser.Close()
			}()
			const maxControlScriptBytes = 1 * 1024 * 1024 // 1 MiB ceiling per maintainer script
			for {
				header, err := archive.Next()
				if err != nil {
					break
				}
				baseName := path.Base(path.Clean(header.Name))
				content, _ := io.ReadAll(io.LimitReader(archive, maxControlScriptBytes))
				switch baseName {
				case "preinst":
					scriptlets.PreIn = strings.TrimSpace(string(content))
				case "postinst":
					scriptlets.PostIn = strings.TrimSpace(string(content))
				case "prerm":
					scriptlets.PreUn = strings.TrimSpace(string(content))
				case "postrm":
					scriptlets.PostUn = strings.TrimSpace(string(content))
				case "triggers":
					if len(strings.TrimSpace(string(content))) > 0 {
						details.HasTriggers = true
					}
				case "conffiles":
					if details.Conffiles == nil {
						details.Conffiles = make(map[string]bool)
					}
					for _, line := range strings.Split(string(content), "\n") {
						trimmed := strings.TrimSpace(line)
						if trimmed == "" || strings.HasPrefix(trimmed, "#") {
							continue
						}
						if !strings.HasPrefix(trimmed, "/") {
							trimmed = "/" + trimmed
						}
						details.Conffiles[path.Clean(trimmed)] = true
					}
				}
			}
		}
	}

	if scriptlets.HasAny() {
		details.Scriptlets = scriptlets
	}
	return details
}

// ReadDebFiles streams file headers from debFile.Data (*tar.Reader) on-demand.
func ReadDebFiles(debPath string) ([]models.PackageFile, error) {
	files, _, err := ReadDebFilesAndChangelog(debPath)
	return files, err
}

// ReadDebFilesAndChangelog streams file headers from debFile.Data (*tar.Reader) on-demand,
// and simultaneously extracts the package changelog and DEP-5 copyright license in a single pass.
func ReadDebFilesAndChangelog(debPath string) ([]models.PackageFile, *models.ChangelogEntry, error) {
	files, changelog, _, err := ReadDebFilesMetadata(debPath, nil)
	return files, changelog, err
}

// ReadDebFilesMetadata streams file headers from debFile.Data (*tar.Reader) on-demand,
// annotating conffiles, symlink targets, changelog entry, and fallback DEP-5 License.
func ReadDebFilesMetadata(debPath string, conffiles map[string]bool) ([]models.PackageFile, *models.ChangelogEntry, string, error) {
	debFile, closer, err := pdeb.LoadFile(debPath)
	if err != nil {
		return nil, nil, "", err
	}
	defer func() {
		_ = closer()
	}()

	files, changelog, detectedLicense := streamDataTarMetadata(debFile.Data, conffiles)
	return files, changelog, detectedLicense, nil
}

func streamDataTarMetadata(dataTar *tar.Reader, conffiles map[string]bool) ([]models.PackageFile, *models.ChangelogEntry, string) {
	if dataTar == nil {
		return nil, nil, ""
	}
	var files []models.PackageFile
	var changelog *models.ChangelogEntry
	var detectedLicense string

	for {
		header, err := dataTar.Next()
		if err != nil {
			break
		}
		name := path.Clean(header.Name)
		if name == "." {
			continue
		}
		if !strings.HasPrefix(name, "/") {
			name = "/" + name
		}
		var flags []string
		if conffiles != nil && conffiles[name] {
			flags = append(flags, "config")
		}
		files = append(files, models.PackageFile{
			Name:       name,
			Mode:       header.FileInfo().Mode().String(),
			Size:       header.Size,
			User:       header.Uname,
			Group:      header.Gname,
			LinkTarget: header.Linkname,
			Flags:      flags,
		})

		base := path.Base(name)
		if detectedLicense == "" && base == "copyright" && strings.Contains(name, "/usr/share/doc/") {
			detectedLicense = parseDEP5License(io.LimitReader(dataTar, 256*1024))
		}

		// Extract changelog in single-pass if not already found
		if changelog == nil && (strings.HasPrefix(base, "changelog.Debian") || base == "changelog.gz" || base == "changelog") {
			var r io.Reader = dataTar
			if strings.HasSuffix(base, ".gz") {
				if gzr, err := gzip.NewReader(dataTar); err == nil {
					if entry, err := ParseChangelog(io.LimitReader(gzr, 2*1024*1024)); err == nil && entry != nil {
						changelog = entry
					}
					_ = gzr.Close()
				}
			} else {
				if entry, err := ParseChangelog(io.LimitReader(r, 2*1024*1024)); err == nil && entry != nil {
					changelog = entry
				}
			}
		}
	}
	return files, changelog, detectedLicense
}

// parseDEP5License extracts concise license identifiers from a Debian DEP-5 copyright file.
func parseDEP5License(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	var found []string
	seen := make(map[string]bool)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "License:") {
			continue
		}
		lic := strings.TrimSpace(strings.TrimPrefix(line, "License:"))
		if lic == "" {
			continue
		}
		// Keep short SPDX-like header line (ignore narrative multi-sentence blocks)
		if len(lic) > 80 {
			continue
		}
		if !seen[lic] {
			seen[lic] = true
			found = append(found, lic)
			if len(found) >= 3 {
				break
			}
		}
	}
	return strings.Join(found, ", ")
}
