//go:build windows

package util

// OBJECTIVES: Provide a Windows build stub for POSIX umask configuration.

// SetUmask is a no-op on Windows where POSIX umasks do not apply.
func SetUmask(mask int) int {
	return 0
}
