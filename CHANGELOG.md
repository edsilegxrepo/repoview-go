# Changelog

All notable changes to the `repoview-go` project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v0.4.0] - 2026-11-26

### Security Hardening, Rich Metadata, Declarative Workflows & Architecture Refactoring

#### Security & Data Safety
- **Path Containment & Archive Integrity**: Added `util.ResolveContainedPath` / `util.ResolveRealPath` symlink-safe containment across RPM/DEB payloads and `repomd.xml` entries, SHA-256 verification for metadata and `.deb` packages, and `validateOutputDirSafety` blocking protected `repodata/` and `dists/` directories.
- **XSS, XXE & Bomb Protection**: Enforced `safeURL` validation, JS `shellQuote` and control-character sanitization, strict XML decoding with 4 GiB decompression ceilings, and atomic temp-write + `fsync` + rename persistence via `util.AtomicWriteFile`.
- **Cross-Platform Permissions**: Moved umask enforcement exclusively to process startup, restricted internal `state.json` writes to `0600`, and normalized case-insensitive filename collision handling.

#### Workflow, Declarative Configuration & Repository Discovery
- **Declarative Config (`repoview.yaml` & Expanded `portal.yaml`)**: Added per-repository `repoview.yaml` config support and extended `portal.yaml` with `repositories`, per-repo overrides (`distro`, `channel`, `arch`, `suite`, `base_url`, `gpgkey`, `ignore_package`, `exclude_arch`, `hidden`), and custom taxonomy `path_patterns` / `distro_aliases`.
- **Incremental Fingerprinting & Single-Pass Portal**: Added repository-level fingerprint short-circuiting (`__repoview_fingerprint__`) in `state.json` and single-pass `repoview.json` descriptor updates in `repoview portal`, eliminating redundant rescans.
- **CLI & Pluggable Output**: Added `--suite`, `--arch`, `--gpgkey`, and `--workers` flags, consistent `Packages.bz2`/`.zst` discovery, `primary.xml`/`other.xml` fallback when SQLite DBs are absent, `OutputSink` (`AtomicLocalSink`), and `0`–`8` granular exit codes.

#### Rich RPM/DEB Metadata & Client Search
- **Extended Package Metadata**: Added RPM `pkgId`, `repomd.Revision`, weak dependencies (`Recommends`/`Suggests`/`Enhances`/`Supplements`), multi-entry changelogs, `BUGURL`/`VCS`/file flags, and `updateinfo.xml` CVE advisories; added DEB `Release`/`InRelease` parsing, `Priority`, `MultiArch`, `Tags`, `BuiltUsing`, `Bugs`, `Origin`, `conffiles`, and DEP-5 `copyright` license extraction.
- **Enhanced Search Engine**: Expanded `search.json` schema with group/license/provides/tags/CVE tokens, adding relevance ranking, structured query qualifiers (`arch:`, `group:`, `license:`, `provides:`, `tag:`, `cve:`), and keyboard navigation in the search UI.

#### Architecture, Modularity & Performance
- **Shared Core & Decoupling**: Extracted `internal/taxonomy`, `internal/util` (`AtomicWriteFile`, `ForEachConcurrent`, `DecompressFile`), `repo.Open` / `OpenWithOptions`, minimal `RepoReader` capability interfaces, and shared HTML template partials (`_head.html`, `_sidebar.html`, `_topbar.html`).
- **Concurrency, SQLite & Race Safety**: Added SQLite read-only URI mode with bounded batch queries (`chunk = 500`) and single-pass `GROUP_CONCAT` changelogs, mutex-protected concurrent logging (`logMu` / `syncWriter`), and thread-safe portal scan warnings.

#### Tooling, Dependencies & Test Coverage
- **Hygiene & Dependencies**: Migrated `gopkg.in/yaml.v3` to `go.yaml.in/yaml/v3`, retained `go-sqlite3` with CGo aligned in `build.json`, updated `biome.json` (`2.6.9`), removed dead helpers/lint smells, and aligned `README.md`, `ARCHITECTURE.md`, and `TESTING.md`.
- **Cross-Platform Verification**: Expanded tests across all 11 Go packages (including synthetic RPM binary fixtures and the full `0`–`8` exit-code matrix), exceeding 80% statement coverage in every package with clean `-race`, integration, `govulncheck`, `gosec`, `golangci-lint`, `staticcheck`, and `biome` passes on Windows and WSL Linux.

## [v0.3.0] - 2026-09-18

### Multi-Repository Catalog Portal, Taxonomy Inference & High-Performance Concurrency

A major feature release introducing a unified **Multi-Repository Catalog & Browsing Portal** (`repoview portal`), automated distribution and channel taxonomy inference, self-describing repository contracts, persistent bounded worker pools, and production Nginx web server deployment specifications.

#### Multi-Repository Portal & Discovery Engine (`repoview portal`)
- **Topology-Agnostic Crawler**: Added high-speed recursive scanning traversing deep hierarchies (`<distro>/<channel>/<arch>`), flat repository layouts, and symlinked directory trees with configurable `--max-depth`.
- **Branch Pruning**: Automatically skips massive raw payload directories (`pool/`, `SRPMS/`, `debug/`, `.git`) preventing crawler slowdowns on enterprise mirrors.
- **POSIX Inode & Device Cycle Protection**: Tracks kernel `(dev, ino)` tuples to eliminate symlink cycles and deduplicate multiple symlinks to identical storage paths across dispersed filesystems.
- **Debian Multi-Component Suite Aggregation**: Automatically merges multi-component Debian suites (`main`, `contrib`, `non-free`) into a unified repository card displaying all supported architectures and aggregate package counts.
- **Self-Describing Metadata Contracts (`repoview.json`)**: Every single-repo generation pass writes an atomic, format-agnostic descriptor enabling sub-millisecond catalog discovery without rescanning SQLite databases or Debian indexes.
- **Parent Portal Backlinks**: Single repository index pages automatically discover upstream portals (`portal.yaml` or HTML signature) and display a functional `← All Repositories` breadcrumb link.
- **Flexible Configuration (`portal.yaml`)**: Supports declarative YAML configuration with automated upward directory climbing and a `--dump-config` bootstrapper.

#### Taxonomy Inference & Distribution Branding
- **Automated Distro & Channel Inference**: Automatically extracts and classifies distribution families (`el8`, `el9`, `el10`, `fedora`, `ubu22`, `ubu24`, `deb11`, `deb12`, `alpine`, `arch`, `suse`, `custom`) and channel taxonomies (`base`, `custom`, `extras`, `updates`, `security`, `testing`, `stable`).
- **Multi-Architecture Detection**: Detects supported hardware architectures from paths and package metadata with concrete architecture prioritization.
- **Official Distro SVG Icons**: Integrates embedded, crisp SVG distribution branding icons into catalog repository cards.

#### Unified Portal UI & RSS Aggregation
- **Responsive Glassmorphic UI**: Searchable and filterable catalog with live multi-attribute filtering (distro, channel, architecture), client package manager setup snippets (`.repo` and `.sources`), and deep-linkable URL hash state.
- **Parallel RSS 2.0 Feed Aggregation**: Concurrent fan-in/fan-out reader aggregating top releases across all child repositories into a unified `/portal-feed.xml`.

#### Concurrency & Performance Overhaul
- **Persistent Bounded Worker Pools**: Converted parallel operations across `internal/app`, `internal/portal`, and `cmd/repoview` to fixed worker pools (`runtime.NumCPU() * 2`) draining closed buffered job channels.
- **Thread-Local Lock Batching**: Reduced mutex contention from $\mathcal{O}(N)$ to $\mathcal{O}(W)$ by accumulating rendered filenames in thread-local storage and committing once per worker at shutdown.
- **Parallel Multi-Repo Rendering**: Added `--render-missing` parallel rendering worker pool for un-rendered repositories.


## [v0.2.0] - 2026-09-17

### Debian Repository Support & Architectural Generalization

A major release expanding **RepoView-Go** into a multi-format repository browser with native Debian/Ubuntu (`.deb`) support, format-adaptive UI workflows, unified domain models, and web-accessible permission standards.

#### Debian (`.deb`) Support & Pipeline
- **Native Debian Ingestion**: Added full support for Debian repositories parsing `deb822` `Packages` and `Release` indexes using `pault.ag/go/debian`.
- **Multi-Component Aggregation**: Discovers and aggregates packages across multi-component suites (`main`, `universe`, `multiverse`, `restricted`) and `binary-all` with cross-component deduplication.
- **Deep `.deb` Binary Inspection**: Extracts maintainer scripts (`preinst`, `postinst`, `prerm`, `postrm`), installed file manifests, and changelogs (`changelog.Debian.gz`) from `ar`/`tar` archive members with seek position recovery.
- **Build Timestamp Extraction**: Direct extraction of archive creation timestamps from `ar` headers, eliminating `1969-12-31` Unix epoch fallback dates.
- **Section Taxonomy Mapping**: Mapped all 33 Debian Policy sections to canonical Repoview categories with automated heuristic inference for unclassified packages.
- **Debian Version Sorting**: Integrated exact Debian EVR comparison semantics via `pault.ag/go/debian/version`.
- **Automatic Format Detection**: Added repository auto-detection with CLI override flag (`--format auto|rpm|deb`).

#### Domain Models & Unified Ingestion
- **Format-Agnostic Abstractions**: Modernized data models to `PackageDetails`, `PackageFile`, `PackageScriptlets`, and format-neutral `SourcePackage`.
- **Unified `RepoReader` Interface**: Standardized ingestion and on-demand file inspection across both SQLite/RPM and Deb822/DEB backends.
- **Zero RPM Regressions**: Preserved full parity for RPM database fields (`SourceRPM`, `RpmGroup`), CPIO file manifests, GPG signature verification (`rpm -vK`), and Comps XML grouping.

#### User Interface & Experience
- **Dark Theme Default**: Switched default visual mode to dark theme - **Format-Adaptive Install Tabs**: Displays tailored client install commands (`apt` / `dpkg` on Debian; `dnf` / `yum` on RPM) with 1-click clipboard copy.
- **Deb822 Sources Generation**: Client setup modal automatically outputs modern Deb822 `.sources` configuration snippets alongside standard RPM `.repo` files.

#### Security, Permissions & Quality Assurance
- **Web-Accessible Permissions Hardening**: Enforced standard umask (`0022`), `0755` directory traversal, and `0644` file permissions (`util.EnsureDir`, `util.WriteWebFile`) across all generated static output files.

## [v0.1.0] - 2026-09-16

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
