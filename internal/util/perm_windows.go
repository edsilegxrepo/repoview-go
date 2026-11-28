//go:build windows

package util

// OBJECTIVES: Implement Windows NTFS directory and static file creation helpers
// relying on ACL inheritance without unnecessary POSIX chmod calls.

import "os"

// EnsureDir creates a directory hierarchy on Windows NTFS where POSIX rwx bits
// are not enforced; ACL inheritance governs read/traverse access.
func EnsureDir(dir string) error {
	// #nosec G301 -- public static repository site requires 0755 directory permissions on POSIX and inherited ACLs on Windows
	return os.MkdirAll(dir, DefaultDirPerm)
}

// WriteWebFile writes data to a file on Windows NTFS without issuing redundant
// POSIX os.Chmod syscalls.
func WriteWebFile(path string, content []byte) error {
	// #nosec G306 -- static web assets require 0644 permissions
	return os.WriteFile(path, content, DefaultFilePerm)
}
