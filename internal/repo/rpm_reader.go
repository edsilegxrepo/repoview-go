package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/util"

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
func ReadRPMDetails(rpmPath string) (*models.PackageDetails, error) {
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

	details := &models.PackageDetails{}

	// 1. Basic & Extended Header Metadata
	details.BuildHost, _ = header.GetString(rpmutils.BUILDHOST)
	details.SourcePackage, _ = header.GetString(rpmutils.SOURCERPM)
	details.InstalledSize, _ = header.InstalledSize()
	if packager, err := header.GetString(rpmutils.PACKAGER); err == nil {
		details.Packager = strings.TrimSpace(packager)
	}
	if bugURL, err := header.GetString(rpmutils.BUGURL); err == nil {
		details.BugURL = strings.TrimSpace(bugURL)
	}
	if vcs, err := header.GetString(rpmutils.VCS); err == nil {
		details.VCS = strings.TrimSpace(vcs)
	}

	// 2. Digital Signatures (preserve primary details and summarize multiple signatures if present)
	if len(sigs) > 0 {
		sigSummaries := make([]string, 0, len(sigs))
		for i, sig := range sigs {
			hashName := strings.ReplaceAll(sig.Hash.String(), "-", "")
			timeStr := sig.CreationTime.Format("Mon 02 Jan 2006 03:04:05 PM MST")
			summary := fmt.Sprintf("RSA/%s, %s, Key ID %x", hashName, timeStr, sig.KeyId)
			sigSummaries = append(sigSummaries, summary)
			if i == 0 {
				details.KeyID = fmt.Sprintf("%x", sig.KeyId)
				details.SigType = fmt.Sprintf("RSA/%s", hashName)
				details.SigDate = timeStr
			}
		}
		details.Signature = strings.Join(sigSummaries, " | ")
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

	scriptlets := &models.PackageScriptlets{
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

	// File lists are loaded strictly on-demand via ReadRPMFiles during package page rendering
	// to prevent holding millions of PackageFile entries in heap memory simultaneously.
	return details, nil
}

// ReadRPMFiles extracts only the file list with modes and sizes from an RPM header.
// Used for on-demand package page rendering to avoid keeping millions of file entries in memory.
func ReadRPMFiles(rpmPath string) ([]models.PackageFile, error) {
	// #nosec G304 -- rpmPath is validated against directory traversal by repository discovery
	f, err := os.Open(filepath.Clean(rpmPath))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	header, err := rpmutils.ReadHeader(f)
	if err != nil {
		return nil, err
	}

	files, err := header.GetFiles()
	if err != nil || len(files) == 0 {
		return nil, err
	}

	res := make([]models.PackageFile, 0, len(files))
	for _, fi := range files {
		res = append(res, models.PackageFile{
			Mode:       formatFileMode(fi.Mode()),
			User:       fi.UserName(),
			Group:      fi.GroupName(),
			Size:       fi.Size(),
			Name:       fi.Name(),
			LinkTarget: fi.Linkname(),
			Flags:      rpmFileFlags(fi.Flags()),
		})
	}
	return res, nil
}

func rpmFileFlags(flagMask int) []string {
	if flagMask <= 0 {
		return nil
	}
	var flags []string
	if flagMask&rpmutils.RPMFILE_CONFIG != 0 {
		flags = append(flags, "config")
	}
	if flagMask&rpmutils.RPMFILE_DOC != 0 {
		flags = append(flags, "doc")
	}
	if flagMask&rpmutils.RPMFILE_LICENSE != 0 {
		flags = append(flags, "license")
	}
	if flagMask&rpmutils.RPMFILE_GHOST != 0 {
		flags = append(flags, "ghost")
	}
	return flags
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
	util.ForEachConcurrent(pkgs, 0, func(_ int, p *models.Package) {
		if p == nil || p.LocationHref == "" {
			return
		}
		rpmPath, err := util.ResolveContainedPath(repoDir, p.LocationHref)
		if err != nil {
			return
		}

		if _, err := os.Stat(rpmPath); os.IsNotExist(err) {
			return
		}

		details, err := ReadRPMDetails(rpmPath)
		if err != nil {
			return
		}

		p.Details = details
		if p.BuildHost == "" && details.BuildHost != "" {
			p.BuildHost = details.BuildHost
		}
		if p.InstalledSize == 0 && details.InstalledSize > 0 {
			p.InstalledSize = details.InstalledSize
		}
		if p.Packager == "" && details.Packager != "" {
			p.Packager = details.Packager
		}
		if p.Bugs == "" && details.BugURL != "" {
			p.Bugs = details.BugURL
		}
		if p.SourcePackage == "" && details.SourcePackage != "" {
			p.SourcePackage = details.SourcePackage
		}
		if p.SourceRPM == "" && details.SourcePackage != "" {
			p.SourceRPM = details.SourcePackage
		}
		if p.Format == "" {
			p.Format = models.FormatRPM
		}
	})
}
