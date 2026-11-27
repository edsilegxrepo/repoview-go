package render

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/util"
)

// OutputSink abstracts static file persistence so RepoView can write either to local disk
// or to custom storage backends (in-memory buffers, object storage, archives).
type OutputSink interface {
	WriteFile(relPath string, content []byte) error
	RemoveFile(relPath string) error
	Exists(relPath string) bool
}

// AtomicLocalSink persists generated assets under RootDir with path-traversal containment
// and atomic temp-write + fsync + rename guarantees.
type AtomicLocalSink struct {
	RootDir string
}

// WriteFile writes content atomically to RootDir/relPath.
func (s *AtomicLocalSink) WriteFile(relPath string, content []byte) error {
	fullPath, err := util.ResolveContainedPath(s.RootDir, relPath)
	if err != nil {
		return fmt.Errorf("%w: %w", models.ErrIntegrity, err)
	}
	if err := util.EnsureDir(filepath.Dir(fullPath)); err != nil {
		return fmt.Errorf("%w: failed to create directory for %s: %w", models.ErrIO, fullPath, err)
	}
	if err := util.AtomicWriteFile(fullPath, content, util.DefaultFilePerm); err != nil {
		return fmt.Errorf("%w: %w", models.ErrIO, err)
	}
	return nil
}

// RemoveFile deletes a previously generated file under RootDir safely.
func (s *AtomicLocalSink) RemoveFile(relPath string) error {
	fullPath, err := util.ResolveContainedPath(s.RootDir, relPath)
	if err != nil {
		return fmt.Errorf("%w: %w", models.ErrIntegrity, err)
	}
	return os.Remove(fullPath)
}

// Exists reports whether RootDir/relPath exists on disk.
func (s *AtomicLocalSink) Exists(relPath string) bool {
	fullPath, err := util.ResolveContainedPath(s.RootDir, relPath)
	if err != nil {
		return false
	}
	_, err = os.Stat(fullPath)
	return err == nil
}
