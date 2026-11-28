package util

// OBJECTIVES:
// Standardize file mode constants, umask behavior, and web-readable directory/file
// creation across POSIX and Windows targets.
//
// CORE COMPONENTS:
//   - DefaultUmask / DefaultDirPerm / DefaultFilePerm: Baseline permissions for static hosting.
//   - EnsureDir / WriteWebFile / SetUmask: Platform-built implementations in perm_*.go and umask_*.go.
//
// FUNCTIONALITY:
//   - Guarantees generated static sites remain readable/traversable by unprivileged
//     web servers (0755 dirs, 0644 files) on POSIX while respecting NTFS ACLs on Windows.
//
// DATA FLOW:
//   process startup -> SetUmask(DefaultUmask) ; asset/dir writes -> EnsureDir / WriteWebFile

import (
	"os"
)

// --- Web Hosting Permission Constants ---

const (
	// DefaultUmask represents the standard POSIX umask (0022) for web site generation,
	// allowing 0755 directory modes and 0644 file modes by default.
	DefaultUmask = 0o022

	// DefaultDirPerm represents standard directory permissions (0755 / rwxr-xr-x)
	// required for static web servers (such as Nginx, Apache, Caddy) to enter and traverse directories.
	DefaultDirPerm os.FileMode = 0o755

	// DefaultFilePerm represents standard file permissions (0644 / rw-r--r--)
	// required for static web servers to read HTML, CSS, JS, JSON, XML, and other static assets.
	DefaultFilePerm os.FileMode = 0o644
)
