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

// EnsureDir creates a directory hierarchy with DefaultDirPerm (0755) and explicitly
// applies os.Chmod so that restrictive process umasks (e.g. 027 or 077) do not strip
// directory traversal permissions for unprivileged web servers.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, DefaultDirPerm); err != nil {
		return err
	}
	return os.Chmod(dir, DefaultDirPerm)
}

// WriteWebFile writes data to a file with DefaultFilePerm (0644) and explicitly
// applies os.Chmod so that restrictive process umasks do not strip read permissions
// for unprivileged web servers.
func WriteWebFile(path string, content []byte) error {
	// #nosec G306 -- static web assets require 0644 permissions for web servers
	if err := os.WriteFile(path, content, DefaultFilePerm); err != nil {
		return err
	}
	return os.Chmod(path, DefaultFilePerm)
}
