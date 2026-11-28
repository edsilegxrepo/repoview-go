//go:build !windows

package portal

// OBJECTIVES: Extract POSIX (dev, ino) filesystem identifiers for O(1) symlink cycle
// detection and multi-mount alias deduplication during portal directory scans.

import (
	"os"
	"syscall"
)

type fileID struct {
	dev uint64
	ino uint64
}

func getFileID(_ string, fi os.FileInfo) (fileID, bool) {
	if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fileID{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}, true
	}
	return fileID{}, false
}
