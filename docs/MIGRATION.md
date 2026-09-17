# Repoview Migration Strategy: Python 2 to Go

This document details the strategic approach, architectural decisions, and technical logic used to migrate the legacy `repoview` utility from Python 2 to a modern Go implementation.

## 1. Context and Motivation

The original `repoview` 0.6.x (https://github.com/sergiomb2/repoview) was originally a Python 2 utility used to generate static HTML indexes for YUM/DNF repositories. It relied on the `kid` templating engine and direct bindings to the `yum` Python libraries. It has been updated in 2025 to python 3 and Genshi templating engine (version 0.7.x).

### Drivers for Migration
*   **End of Life (EOL) Technologies**: Python 2 reached EOL in 2020. The `kid` template engine is long obsolete.
*   **Performance Bottlenecks**: The legacy tool was single-threaded, leading to slow generation times for large repositories (10k+ packages).
*   **Dependency Challenges**: Reliance on system-level Python bindings (`yum`, `rpm-python`) made deployment difficult across different OS versions and containers.
*   **Maintainability**: The codebase lacked strong typing and modern modularity.

## 2. Migration Goals

1.  **Functional Parity**: The new tool maintains semantic and URL structure parity while modernizing the HTML layout with responsive CSS, dark mode, and client-side search, standardizing on explicit long-format CLI options.
2.  **Performance**: Utilize Go's concurrency primitives to drastically reduce execution time.
3.  **Portability**: Deliver a standalone binary with zero external Python or shared runtime dependencies (compiled with embedded assets and SQLite3 CGo bindings).
4.  **Maintainability**: Use a strongly-typed, modular architecture with clear separation of concerns.

## 3. Architecture Transition

The migration moved from a monolithic script approach to a layered architecture.

| Component | Legacy (Python 2) | Modern (Go) |
|-----------|-------------------|-------------|
| **Runtime** | Python 2.7 Interpreter | Go Compiled Binary |
| **Data Access** | `yum` / `rpm-python` bindings | Direct SQLite (`mattn/go-sqlite3` CGo) + XML parsing |
| **Templating** | `kid` (XML-based) | `html/template` (Standard Lib) |
| **Concurrency** | Single-threaded | Worker Pools (Goroutines) |
| **Assets** | External files on disk | Embedded (`embed` package) |

## 4. Technical Implementation Strategy

### 4.1. Data Access Layer (Replacing `yum`)
Instead of relying on `yum` libraries which are heavy and OS-dependent, the Go implementation interacts directly with the repository metadata standards (`rpm-md`).

*   **Repository Discovery**: Parses `repomd.xml` to locate `primary_db`, `other_db`, and comps group metadata. Package file lists are extracted on-demand directly from RPM payload headers (`internal/repo/rpm.go`), avoiding the massive memory overhead of loading `filelists.sqlite`.
*   **Decompression**: Implemented transparent streaming decompression for `.gz`, `.bz2`, `.xz`, and `.zst` (Zstandard) files to handle metadata in memory or strictly streamed to disk, avoiding external `gunzip` calls.
*   **Database Querying**: Uses `mattn/go-sqlite3` to query `primary.sqlite` directly.
    *   *Optimization*: Uses `ATTACH DATABASE` to link `other.sqlite` (changelogs) to the primary connection.
    *   *Optimization*: Implements bulk fetching (batching 500 packages at a time) with subqueries to retrieve latest changelogs efficiently, solving the N+1 query performance bottleneck.
*   **Schema Mapping**: Created Go structs in `internal/models` that mirror the XML and SQLite schemas exactly, ensuring data fidelity.

### 4.2. Business Logic & Parity

To ensure the new tool behaves exactly like the old one, several specific logic patterns were reverse-engineered and ported:

*   **Filename Sanitization**: The legacy tool used a specific regex to sanitize filenames. This was replicated in `internal/util/sanitize.go` to ensure URLs remain consistent.
*   **Grouping Logic**:
    *   **Comps Groups**: Parsed from `comps.xml`.
    *   **RPM Groups**: Fallback if no comps group matches.
    *   **Vendor Grouping**: Specialized grouping logic.
*   **Version Comparison**: Implemented strict RPM EVR (Epoch-Version-Release) comparison logic (using `knqyf263/go-rpm-version`) to correctly sort packages and identify the "latest" version.
*   **Changelog Author Sanitization**: Ported author cleaning regex (`internal/repo/sqlite.go`) to strip email wrappers and match legacy repoview display conventions.

### 4.3. High-Performance Rendering

The rendering pipeline was redesigned for parallelism:

1.  **Template Porting**: The `.kid` templates were manually ported to Go's `html/template`. Logic previously embedded in templates (Python code) was moved to helper functions or the `Generator` logic.
2.  **Concurrency Model**:
    *   A concurrent worker pattern is used to render package detail pages in parallel.
    *   A semaphore channel limits concurrent rendering goroutines (scaled to `runtime.NumCPU() * 2`) to avoid CPU starvation and descriptor exhaustion.
    *   This allows generating thousands of HTML pages in seconds on multi-core machines.

### 4.4. State Management & Incremental Builds

The legacy tool had rudimentary support for skipping existing files. The Go version implements a robust state manager:

*   **State Store**: Maintains a `state.json` file (or `<sha256>.state.json` when `--state-dir` is configured).
*   **Hashing**: Calculates SHA256 hashes of rendered page and asset contents in memory (`content []byte`).
*   **Logic**: If the SHA256 hash matches the persisted state, redundant disk I/O and atomic file writes are skipped. This makes subsequent incremental runs on large repositories nearly instantaneous.

## 5. Security Improvements

*   **Path Traversal Prevention**: Strict sanitization was added to all file path inputs to prevent writing outside the target directory.
*   **Memory Safety**: Go's memory management eliminates entire classes of buffer overflow vulnerabilities present in C-based Python extensions.
*   **Dependency Management**: `go.mod` locks dependencies to specific versions, preventing supply-chain drift.

## 6. Migration Checklist & Status

| Phase | Task | Status |
|-------|------|--------|
| **Analysis** | Reverse-engineer `repoview.py` (v0.6.6 and v0.7.1) | Complete |
| **Core** | Implement Metadata Parsing (XML/SQLite) | Complete |
| **Logic** | Port Grouping & Sorting Algorithms | Complete |
| **UI/UX** | Modernize Templates to Go HTML (Semantic Parity + Responsive UI) | Complete |
| **CLI** | Implement Long-Format Flag Interface (`--output-dir`, `--state-dir`, etc.) | Complete |
| **Optimization** | Implement Concurrent Rendering | Complete |
| **Features** | Theme Support (`--template-dir`) | Complete |
| **Features** | Search Indexing (`search.json`) | Complete |
| **Docs** | Update README and Inline Docs | Complete |

## 7. Future Roadmap

*   **S3 Support**: Add an interface to upload generated assets directly to object storage.
