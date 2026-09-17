//go:build windows

package util

// SetUmask is a no-op on Windows where POSIX umasks do not apply.
func SetUmask(mask int) int {
	return 0
}
