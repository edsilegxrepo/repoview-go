package deb

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"

	"pault.ag/go/debian/control"
)

// ReleaseInfo captures structured metadata parsed from a Debian Release or InRelease file.
type ReleaseInfo struct {
	Origin        string `control:"Origin"`
	Label         string `control:"Label"`
	Suite         string `control:"Suite"`
	Codename      string `control:"Codename"`
	Date          string `control:"Date"`
	ValidUntil    string `control:"Valid-Until"`
	Description   string `control:"Description"`
	Architectures string `control:"Architectures"`
	Components    string `control:"Components"`
}

// ParseReleaseMetadata unmarshals Release or InRelease metadata using pault.ag/go/debian/control.
// If the file is clearsigned (InRelease), PGP armour headers/signature blocks are stripped first.
func ParseReleaseMetadata(releasePath string) (*ReleaseInfo, error) {
	// #nosec G304 -- releasePath is discovered from validated Debian repository hierarchy
	f, err := os.Open(filepath.Clean(releasePath))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	const maxReleaseBytes = 8 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(f, maxReleaseBytes))
	if err != nil {
		return nil, err
	}

	cleaned := extractClearsignedParagraph(data)
	var info ReleaseInfo
	if err := control.Unmarshal(&info, bufio.NewReader(bytes.NewReader(cleaned))); err != nil {
		return nil, err
	}
	return &info, nil
}

// extractClearsignedParagraph strips PGP clearsign envelope lines if present so control.Unmarshal succeeds.
func extractClearsignedParagraph(data []byte) []byte {
	const beginHeader = "-----BEGIN PGP SIGNED MESSAGE-----"
	const beginSig = "-----BEGIN PGP SIGNATURE-----"
	if !bytes.Contains(data, []byte(beginHeader)) {
		return data
	}

	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var out []string
	inBody := false
	pastBlank := false
	for _, line := range lines {
		if !inBody {
			if strings.HasPrefix(line, beginHeader) {
				inBody = true
			}
			continue
		}
		if !pastBlank {
			if strings.TrimSpace(line) == "" {
				pastBlank = true
			}
			continue
		}
		if strings.HasPrefix(line, beginSig) {
			break
		}
		line = strings.TrimPrefix(line, "- ")
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
}
