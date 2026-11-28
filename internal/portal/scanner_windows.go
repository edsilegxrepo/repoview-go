//go:build windows

package portal

// OBJECTIVES: Extract Windows (VolumeSerialNumber, FileIndex) identifiers via Win32
// GetFileInformationByHandle for junction and directory symlink cycle detection.

import (
	"os"
	"syscall"
)

type fileID struct {
	vol uint32
	idx uint64
}

func getFileID(path string, _ os.FileInfo) (fileID, bool) {
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fileID{}, false
	}
	h, err := syscall.CreateFile(
		ptr,
		0,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return fileID{}, false
	}
	defer func() { _ = syscall.CloseHandle(h) }()

	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		return fileID{}, false
	}
	return fileID{
		vol: info.VolumeSerialNumber,
		idx: uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow),
	}, true
}
