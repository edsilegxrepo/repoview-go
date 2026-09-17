//go:build !windows

package util

import "syscall"

// SetUmask sets the process umask and returns the previous umask value.
func SetUmask(mask int) int {
	return syscall.Umask(mask)
}
