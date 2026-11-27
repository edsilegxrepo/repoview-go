package models

import "errors"

// Domain sentinel errors shared across repoview packages so the CLI can map
// failures to deterministic process exit codes using errors.Is / errors.As.
var (
	// ErrUsage indicates invalid CLI flags, syntax, or arguments (exit code 2).
	ErrUsage = errors.New("invalid usage")

	// ErrRepoMetadata indicates missing, corrupt, or unsupported repository metadata (exit code 3).
	ErrRepoMetadata = errors.New("repository metadata error")

	// ErrTemplate indicates template loading, parsing, or execution failure (exit code 4).
	ErrTemplate = errors.New("template error")

	// ErrIO indicates filesystem read, write, permission, or disk-space failure (exit code 5).
	ErrIO = errors.New("filesystem I/O error")

	// ErrConfig indicates invalid or unreadable configuration files or filter globs (exit code 7).
	ErrConfig = errors.New("configuration error")

	// ErrSafety indicates a safety guard violation such as output overwriting source repo (exit code 8).
	ErrSafety = errors.New("safety violation")

	// ErrIntegrity indicates checksum mismatch, path/symlink escape, or decompression limit violation (exit code 8).
	ErrIntegrity = errors.New("security or integrity violation")
)
