package util

import (
	"os"
)

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
