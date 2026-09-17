package deb

import (
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

// ReadDebTimestamp extracts the package build timestamp from the .deb archive.
// It inspects the first ar archive member ("debian-binary"), which per Debian policy
// is placed first in the archive, and reads the 12-byte decimal ASCII timestamp header.
// If reading the ar header fails, it falls back to the file modification time on disk.
func ReadDebTimestamp(debPath string) int64 {
	if ts := readArMemberTimestamp(debPath); ts > 0 {
		return ts
	}
	if fi, err := os.Stat(debPath); err == nil {
		return fi.ModTime().Unix()
	}
	return 0
}

func readArMemberTimestamp(debPath string) int64 {
	// #nosec G304 -- debPath is validated against directory traversal by repository discovery
	f, err := os.Open(filepath.Clean(debPath))
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	// Ar file header is 8 bytes ("!<arch>\n") followed by 60-byte member headers:
	// name (16 bytes), timestamp (12 bytes), ...
	var buf [68]byte
	n, err := io.ReadFull(f, buf[:])
	if err != nil || n < 68 {
		return 0
	}

	if string(buf[:8]) != "!<arch>\n" {
		return 0
	}

	// Bytes 8 to 24 are member name (16 bytes), Bytes 24 to 36 are timestamp (12 bytes)
	tsStr := strings.TrimSpace(string(buf[24:36]))
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil || ts <= 0 {
		return 0
	}
	return ts
}

// ReadDebDetails extracts maintainer scripts from control.tar.* inside a .deb archive.
func ReadDebDetails(debPath string) (*models.PackageDetails, error) {
	debFile, closer, err := pdeb.LoadFile(debPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = closer()
	}()

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
			for {
				header, err := archive.Next()
				if err != nil {
					break
				}
				baseName := path.Base(path.Clean(header.Name))
				content, _ := io.ReadAll(archive)
				switch baseName {
				case "preinst":
					scriptlets.PreIn = strings.TrimSpace(string(content))
				case "postinst":
					scriptlets.PostIn = strings.TrimSpace(string(content))
				case "prerm":
					scriptlets.PreUn = strings.TrimSpace(string(content))
				case "postrm":
					scriptlets.PostUn = strings.TrimSpace(string(content))
				}
			}
		}
	}

	if scriptlets.HasAny() {
		details.Scriptlets = scriptlets
	}
	return details, nil
}

// ReadDebFiles streams file headers from debFile.Data (*tar.Reader) on-demand.
func ReadDebFiles(debPath string) ([]models.PackageFile, error) {
	files, _, err := ReadDebFilesAndChangelog(debPath)
	return files, err
}

// ReadDebFilesAndChangelog streams file headers from debFile.Data (*tar.Reader) on-demand,
// and simultaneously extracts the package changelog in a single pass if found in data.tar.
func ReadDebFilesAndChangelog(debPath string) ([]models.PackageFile, *models.ChangelogEntry, error) {
	debFile, closer, err := pdeb.LoadFile(debPath)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		_ = closer()
	}()

	var files []models.PackageFile
	var changelog *models.ChangelogEntry

	for {
		header, err := debFile.Data.Next()
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
		files = append(files, models.PackageFile{
			Name:  name,
			Mode:  header.FileInfo().Mode().String(),
			Size:  header.Size,
			User:  header.Uname,
			Group: header.Gname,
		})

		// Extract changelog in single-pass if not already found
		base := path.Base(name)
		if changelog == nil && (strings.HasPrefix(base, "changelog.Debian") || base == "changelog.gz" || base == "changelog") {
			var r io.Reader = debFile.Data
			if strings.HasSuffix(base, ".gz") {
				if gzr, err := gzip.NewReader(debFile.Data); err == nil {
					if entry, err := ParseChangelog(gzr); err == nil && entry != nil {
						changelog = entry
					}
					_ = gzr.Close()
				}
			} else {
				if entry, err := ParseChangelog(r); err == nil && entry != nil {
					changelog = entry
				}
			}
		}
	}
	return files, changelog, nil
}
