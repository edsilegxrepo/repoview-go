package render

// OBJECTIVES:
// Decouple HTML/XML/JSON/asset emission from direct os.WriteFile calls so generators
// can target atomic local disk directories, fast non-fsynced derived writes, or custom sinks.
//
// CORE COMPONENTS:
//   - OutputSink: Baseline write, delete, and existence abstraction.
//   - FastOutputSink: Optional interface omitting per-file fsync for derived HTML artifacts.
//   - AtomicLocalSink: Default path-contained temp-write + rename storage backend.
//
// FUNCTIONALITY:
//   - Enforces util.ResolveContainedPath containment on every write/delete/stat call.
//   - Ensures parent directories exist with web-readable permissions before renaming.
//
// DATA FLOW:
//   Renderer -> OutputSink / FastOutputSink -> AtomicLocalSink -> util.AtomicWriteFile[Fast]

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/edsilegxrepo/repoview/internal/models"
	"github.com/edsilegxrepo/repoview/internal/util"
)

// --- Storage Abstraction & Contained Atomic Sink ---

// OutputSink abstracts static file persistence so RepoView can write either to local disk
// or to custom storage backends (in-memory buffers, object storage, archives).
type OutputSink interface {
	WriteFile(relPath string, content []byte) error
	RemoveFile(relPath string) error
	Exists(relPath string) bool
}

// FastOutputSink is an optional extension of OutputSink for derived HTML pages that can
// omit per-file fsync while retaining atomic temp-file replacement.
type FastOutputSink interface {
	OutputSink
	WriteFileFast(relPath string, content []byte) error
}

// AtomicLocalSink persists generated assets under RootDir with path-traversal containment
// and atomic temp-write + rename guarantees.
type AtomicLocalSink struct {
	RootDir string
}

// WriteFile writes content atomically to RootDir/relPath with fsync durability.
func (s *AtomicLocalSink) WriteFile(relPath string, content []byte) error {
	return s.writeFileMode(relPath, content, true)
}

// WriteFileFast writes content atomically to RootDir/relPath without per-file fsync.
func (s *AtomicLocalSink) WriteFileFast(relPath string, content []byte) error {
	return s.writeFileMode(relPath, content, false)
}

func (s *AtomicLocalSink) writeFileMode(relPath string, content []byte, syncFile bool) error {
	fullPath, err := util.ResolveContainedPath(s.RootDir, relPath)
	if err != nil {
		return fmt.Errorf("%w: %w", models.ErrIntegrity, err)
	}
	if err := util.EnsureDir(filepath.Dir(fullPath)); err != nil {
		return fmt.Errorf("%w: failed to create directory for %s: %w", models.ErrIO, fullPath, err)
	}
	writeFn := util.AtomicWriteFileFast
	if syncFile {
		writeFn = util.AtomicWriteFile
	}
	if err := writeFn(fullPath, content, util.DefaultFilePerm); err != nil {
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
