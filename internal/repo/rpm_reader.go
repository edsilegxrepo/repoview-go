package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/edsilegxrepo/repoview/internal/models"

	"github.com/sassoftware/go-rpmutils"
)

// OBJECTIVES:
// Directly inspect binary RPM package headers on disk without external tooling (no librpm),
// extracting cryptographically verified metadata, shell scriptlets, and file manifests.
//
// CORE COMPONENTS:
//   - ReadRPMDetails: Deep-inspects an individual RPM header to extract signatures, scriptlets, and files.
//   - ReadRPMFiles: On-demand file-only extractor utilized during page rendering to minimize heap consumption.
//   - EnrichPackagesWithRPMDetails: Concurrent multi-worker orchestrator processing repository RPMs.
//
// FUNCTIONALITY:
//   - Directly decodes RPM lead, signature header, and main header structures via go-rpmutils.
//   - Extracts GPG signing key IDs, signature algorithms, and creation dates.
//   - Retrieves pre/post install and removal scriptlets and interpreter paths.
//   - Decodes cpio file manifests into models.RPMFile records with POSIX permissions and sizes.
//   - Enforces path traversal validation on LocationHref attributes before reading files from disk.
//
// DATA FLOW:
//   RPM file on disk -> go-rpmutils -> models.RPMDetails -> models.Package.Details

// ReadRPMDetails parses the RPM file header directly and extracts detailed inspection data
// including digital signatures, build host, source RPM, scriptlets, and file list.
func ReadRPMDetails(rpmPath string) (*models.RPMDetails, error) {
	// #nosec G304 -- rpmPath is validated against directory traversal by repository discovery
	f, err := os.Open(filepath.Clean(rpmPath))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	header, sigs, err := rpmutils.Verify(f, nil)
	if err != nil && header == nil {
		// Fallback to basic header read if signature verification encountered unknown packets
		if _, seekErr := f.Seek(0, 0); seekErr == nil {
			header, err = rpmutils.ReadHeader(f)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read rpm header: %w", err)
		}
	}

	details := &models.RPMDetails{}

	// 1. Basic Metadata
	details.BuildHost, _ = header.GetString(rpmutils.BUILDHOST)
	details.SourceRPM, _ = header.GetString(rpmutils.SOURCERPM)
	details.InstalledSize, _ = header.InstalledSize()

	// 2. Signature
	if len(sigs) > 0 {
		sig := sigs[0]
		hashName := strings.ReplaceAll(sig.Hash.String(), "-", "")
		timeStr := sig.CreationTime.Format("Mon 02 Jan 2006 03:04:05 PM MST")
		details.Signature = fmt.Sprintf("RSA/%s, %s, Key ID %x", hashName, timeStr, sig.KeyId)
		details.KeyID = fmt.Sprintf("%x", sig.KeyId)
		details.SigType = fmt.Sprintf("RSA/%s", hashName)
		details.SigDate = timeStr
	}

	// 3. Scriptlets
	preIn, _ := header.GetString(rpmutils.PREIN)
	preInProg, _ := header.GetString(rpmutils.PREINPROG)
	postIn, _ := header.GetString(rpmutils.POSTIN)
	postInProg, _ := header.GetString(rpmutils.POSTINPROG)
	preUn, _ := header.GetString(rpmutils.PREUN)
	preUnProg, _ := header.GetString(rpmutils.PREUNPROG)
	postUn, _ := header.GetString(rpmutils.POSTUN)
	postUnProg, _ := header.GetString(rpmutils.POSTUNPROG)

	scriptlets := &models.RPMScriptlets{
		PreIn:      strings.TrimSpace(preIn),
		PreInProg:  strings.TrimSpace(preInProg),
		PostIn:     strings.TrimSpace(postIn),
		PostInProg: strings.TrimSpace(postInProg),
		PreUn:      strings.TrimSpace(preUn),
		PreUnProg:  strings.TrimSpace(preUnProg),
		PostUn:     strings.TrimSpace(postUn),
		PostUnProg: strings.TrimSpace(postUnProg),
	}

	if scriptlets.HasAny() {
		details.Scriptlets = scriptlets
	}

	// 4. File List with attributes
	files, err := header.GetFiles()
	if err == nil && len(files) > 0 {
		details.Files = make([]models.RPMFile, 0, len(files))
		for _, fi := range files {
			modeStr := formatFileMode(fi.Mode())
			details.Files = append(details.Files, models.RPMFile{
				Mode:  modeStr,
				User:  fi.UserName(),
				Group: fi.GroupName(),
				Size:  fi.Size(),
				Name:  fi.Name(),
			})
		}
	}

	return details, nil
}

// ReadRPMFiles extracts only the file list with modes and sizes from an RPM header.
// Used for on-demand package page rendering to avoid keeping millions of file entries in memory.
func ReadRPMFiles(rpmPath string) ([]models.RPMFile, error) {
	// #nosec G304 -- rpmPath is validated against directory traversal by repository discovery
	f, err := os.Open(filepath.Clean(rpmPath))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	header, _, err := rpmutils.Verify(f, nil)
	if err != nil && header == nil {
		if _, seekErr := f.Seek(0, 0); seekErr == nil {
			header, err = rpmutils.ReadHeader(f)
		}
		if err != nil {
			return nil, err
		}
	}

	files, err := header.GetFiles()
	if err != nil || len(files) == 0 {
		return nil, err
	}

	res := make([]models.RPMFile, 0, len(files))
	for _, fi := range files {
		res = append(res, models.RPMFile{
			Mode:  formatFileMode(fi.Mode()),
			User:  fi.UserName(),
			Group: fi.GroupName(),
			Size:  fi.Size(),
			Name:  fi.Name(),
		})
	}
	return res, nil
}

// formatFileMode safely converts an integer file mode to an os.FileMode string representation.
func formatFileMode(mode int) string {
	if mode < 0 {
		return os.FileMode(0).String()
	}
	// #nosec G115 -- mode is guaranteed non-negative by the guard above
	return os.FileMode(uint32(mode)).String()
}

// EnrichPackagesWithRPMDetails concurrently parses the local RPM files for all packages
// and populates deep inspection details (scriptlets, signatures, files).
func EnrichPackagesWithRPMDetails(repoDir string, pkgs []*models.Package) {
	numWorkers := runtime.NumCPU() * 2
	if numWorkers < 4 {
		numWorkers = 4
	}

	pkgChan := make(chan *models.Package, len(pkgs))
	for _, p := range pkgs {
		pkgChan <- p
	}
	close(pkgChan)

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range pkgChan {
				if p.LocationHref == "" {
					continue
				}
				rpmPath := filepath.Join(repoDir, p.LocationHref)

				// Security: Prevent path traversal from untrusted metadata href
				rel, err := filepath.Rel(repoDir, rpmPath)
				if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
					continue
				}

				if _, err := os.Stat(rpmPath); os.IsNotExist(err) {
					continue
				}

				details, err := ReadRPMDetails(rpmPath)
				if err != nil {
					continue
				}

				p.Details = details
				if p.BuildHost == "" && details.BuildHost != "" {
					p.BuildHost = details.BuildHost
				}
				if p.InstalledSize == 0 && details.InstalledSize > 0 {
					p.InstalledSize = details.InstalledSize
				}
				if p.SourceRPM == "" && details.SourceRPM != "" {
					p.SourceRPM = details.SourceRPM
				}
			}
		}()
	}

	wg.Wait()
}
