//go:build !windows

package util

// OBJECTIVES: Configure process-wide POSIX file creation mask (umask) at startup.

import "syscall"

// SetUmask sets the process umask and returns the previous umask value.
func SetUmask(mask int) int {
	return syscall.Umask(mask)
}
