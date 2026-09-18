//go:build windows

package portal

import "os"

type fileID struct{}

func getFileID(fi os.FileInfo) (fileID, bool) {
	return fileID{}, false
}
