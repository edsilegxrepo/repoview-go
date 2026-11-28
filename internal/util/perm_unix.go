//go:build !windows

package util

// OBJECTIVES: Implement POSIX permission-enforcing directory and static file helpers
// overriding restrictive runtime umasks for public static web serving (0755/0644).

import "os"

// EnsureDir creates a directory hierarchy with DefaultDirPerm (0755) and explicitly
// applies os.Chmod so restrictive process umasks (e.g. 027 or 077) do not strip
// directory traversal permissions for unprivileged web servers.
func EnsureDir(dir string) error {
	// #nosec G301 -- public static repository site requires 0755 directory traversal permissions for web servers
	if err := os.MkdirAll(dir, DefaultDirPerm); err != nil {
		return err
	}
	// #nosec G302 -- explicit 0755 permissions required despite restrictive umasks for static web hosting
	return os.Chmod(dir, DefaultDirPerm)
}

// WriteWebFile writes data to a file with DefaultFilePerm (0644) and explicitly
// applies os.Chmod so restrictive process umasks do not strip read permissions
// for unprivileged web servers.
func WriteWebFile(path string, content []byte) error {
	// #nosec G306 -- static web assets require 0644 permissions for web servers
	if err := os.WriteFile(path, content, DefaultFilePerm); err != nil {
		return err
	}
	// #nosec G302 -- static web assets require 0644 permissions regardless of process umask
	return os.Chmod(path, DefaultFilePerm)
}
