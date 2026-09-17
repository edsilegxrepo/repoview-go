# Changelog

All notable changes to the `repoview-go` project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-09-16

### Initial Release

The initial production release of **RepoView-Go**, a high-performance, air-gap compliant static site generator engineered as a modern replacement for the legacy Python 2 `repoview` utility.

#### Core Engine & Architecture
- **Complete Go Rewrite**: Replaces obsolete Python 2 and `kid` templating with a compiled, memory-safe Go architecture.
- **Single Static Binary**: Self-contained executable with embedded templates, glassmorphic CSS, and Vanilla JS assets (`embed.FS`).
- **100% Air-Gap Compliant**: Zero external runtime dependencies, CDN calls, or tracking beacons.
- **Cross-Platform Compatibility**: Native support for Linux, macOS, and Windows (via WSL or CGo).

#### Metadata Ingestion & Data Access
- **Transparent Multi-Format Decompression**: On-the-fly streaming decompression for `.gz`, `.bz2`, `.xz`, and `.zst` (`zstd`) metadata archives.
- **SQLite Engine with Batch Enrichment**: Multi-database queries (`primary.sqlite`, `other.sqlite`) with connection pooling, prepared statement caching, and batch changelog loading (500 per chunk) eliminating N+1 query bottlenecks.
- **Dual Repomd Revision Support**: Compatible with both XML element `<revision>` and root attribute `<repomd revision="...">` schemas.
- **Comps Group Parser**: Hierarchical category/group tree extraction with localized name and description support.
- **RPM Header Inspection**: Direct RPM package inspection extracting GPG signatures, scriptlets (pre/post install/uninstall), dependencies, and on-demand file list loading with immediate heap garbage collection.

#### Business Logic & Categorization
- **Three-Tier Package Organization**: Support for Comps categories/groups, RPM header groups with heuristic fallback inference, and alphabetical letter bucketing (`A-Z`, `#`).
- **Strict RPM EVR Sorting**: Full compliance with RPM Epoch-Version-Release comparison semantics using `go-rpm-version` (handling `~` pre-release and `^` snapshot ordering).
- **Flexible Package Filtering**: Glob pattern matching for package names/NVRA (`--ignore-package`) and architecture exclusions (`--exclude-arch`).
- **Sibling Channel Discovery**: Automatic directory scanning and navigation linking for adjacent repository channels and architectures.

#### User Interface & Client Experience
- **Responsive Modern Design**: Mobile-friendly layout with light/dark theme toggle, system theme persistence, and accessible navigation.
- **Interactive Client-Side Search**: Embedded instant search powered by a pre-indexed `search.json` file without backend server requirements.
- **Quick-Copy Repo Snippets**: Copyable client `.repo` configuration blocks with automatic browser base URL detection and `--baseurl` override.
- **Collapsible Inspection Panels**: Clean presentation of dependencies (Requires, Provides, Conflicts, Obsoletes), RPM scriptlets, and file lists.
- **RSS 2.0 Feed**: Automated `latest-feed.xml` generation for package update syndication.

#### Performance & State Caching
- **Bounded Worker Pool**: Multi-core parallel rendering using a buffered semaphore (`runtime.NumCPU() * 2`) to saturate CPU cores while capping memory.
- **Incremental State Cache**: SHA-256 content hashing in `.state.json` skipping unchanged pages on subsequent runs.
- **Automatic Stale File Pruning**: Identifies and safely unlinks orphaned pages when packages are removed from upstream repos.
- **Two-Phase Atomic Disk Writes**: Writes to temporary sibling files before atomic rename to prevent corrupted outputs on interrupted runs.

#### Security & Safety Guards
- **Catastrophic Deletion Guard**: Built-in validation aborting execution if `--output-dir` is root (`/`), repo directory, or an ancestor path.
- **Path Traversal Neutralization**: Strict directory sandboxing for `repomd.xml` database references and package filenames.
- **Whitelist Input Sanitization**: Replaces unsafe characters in generated filenames and URLs (`[a-zA-Z0-9._-]`).
- **Unprivileged Execution Standard**: Designed to run under dedicated non-root service accounts (`repoview:repoview`).

#### CLI & Usability
- **Clean Long-Only Options**: Standardized CLI flags (`--output-dir`, `--state-dir`, `--title`, `--url`, `--baseurl`, `--template-dir`, `--comps`, `--ignore-package`, `--exclude-arch`, `--force`, `--quiet`, `--version`).
- **Granular Exit Codes**: Distinct process status codes for usage errors, repository metadata errors, rendering failures, and success.

#### Testing & Quality Assurance
- **83.5% Codebase Coverage**: Comprehensive unit test suite with all 8 packages independently exceeding the 80% coverage standard.
- **Race Condition Verification**: Verified data-race free under `go test -race ./...`.
- **Zero Repo Pollution**: Strict enforcement of ephemeral `t.TempDir()` across all test fixtures.
- **Live E2E Integration Suite**: Unmocked integration tests (`-tags=integration`) testing against real distribution repositories (2,036 packages) and live `net/http` loopback servers.
