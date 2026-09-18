//go:build !windows

package portal

import (
	"os"
	"syscall"
)

type fileID struct {
	dev uint64
	ino uint64
}

func getFileID(fi os.FileInfo) (fileID, bool) {
	if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fileID{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}, true
	}
	return fileID{}, false
}
