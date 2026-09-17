# RepoView-Go System Architecture Document

Architectural specification, technical design choices, concurrency patterns, security model, and component interactions for **RepoView-Go**.

---

## Table of Contents

1. [Architecture and Design Choices](#1-architecture-and-design-choices)
   - [High-Level Architecture Diagram](#high-level-architecture-diagram)
   - [Modular Directory Structure](#modular-directory-structure)
   - [Architectural Design Patterns](#architectural-design-patterns)
   - [Core Design Principles & Paradigms](#core-design-principles--paradigms)
   - [Foundational Assumptions](#foundational-assumptions)
   - [Edge Case Handling](#edge-case-handling)
   - [Performance & Efficiency Engineering](#performance--efficiency-engineering)
2. [Data Flow and Control Logic](#2-data-flow-and-control-logic)
   - [End-to-End Operational Flow](#end-to-end-operational-flow)
   - [Code Relations and Package Interaction](#code-relations-and-package-interaction)
   - [System Sequence Diagram](#system-sequence-diagram)
3. [Performance and Scalability](#3-performance-and-scalability)
   - [Concurrency Model & Goroutine Worker Pool](#concurrency-model--goroutine-worker-pool)
   - [Channel Structures & Synchronization Primitives](#channel-structures--synchronization-primitives)
   - [Memory Footprint & Heap Allocation Optimization](#memory-footprint--heap-allocation-optimization)
   - [SQLite Connection Pooling & Prepared Statement Caching](#sqlite-connection-pooling--prepared-statement-caching)
4. [Dependencies](#4-dependencies)
   - [Runtime Modules & External Libraries](#runtime-modules--external-libraries)
   - [Build Tooling & System Dependencies](#build-tooling--system-dependencies)
   - [Package Dependency Diagram](#package-dependency-diagram)
5. [Security Architecture](#5-security-architecture)
   - [Static Site Generation Security Paradigm](#static-site-generation-security-paradigm)
   - [Generator Defenses & Input Sanitization](#generator-defenses--input-sanitization)
   - [Serving Layer Security Architecture](#serving-layer-security-architecture)
   - [Authentication Layers & Supported Methods](#authentication-layers--supported-methods)
   - [Role-Based Access Control (RBAC) Matrix](#role-based-access-control-rbac-matrix)
   - [Security Architecture Diagram](#security-architecture-diagram)

---

## 1. Architecture and Design Choices

RepoView-Go is a high-performance, air-gap compliant static site generator engineered to transform RPM repository metadata (`repodata/`) and package headers into a modern, responsive, and easily navigable web portal.

### High-Level Architecture Diagram

```mermaid
flowchart TB
    subgraph Storage["Source RPM Repository"]
        REPOMD["repodata/repomd.xml"]
        PRIMARY_DB[("primary.sqlite\n(.gz/.bz2/.xz/.zst)")]
        OTHER_DB[("other.sqlite\n(changelogs)")]
        COMPS["comps.xml\n(categories & groups)"]
        RPMS["*.rpm Packages\n(headers, scriptlets, files)"]
    end

    subgraph CLI["CLI Entrypoint (cmd/repoview)"]
        MAIN["main.go\n• Flag Parsing\n• Safety Validation\n• Exit Code Mapping"]
    end

    subgraph CoreEngine["Application Core (internal/app)"]
        GEN["Generator (generator.go)\n• Orchestration Pipeline\n• Sibling Discovery\n• Stale Cleanup"]
        SAFETY["Safety Validator\n• Root Guard\n• Self-Destruct Guard"]
    end

    subgraph Ingestion["Repository Access Layer (internal/repo)"]
        DECOMP["Decompressor (decompress.go)\n• Gzip, Zstd, XZ, Bz2\n• Ephemeral Temp Extraction"]
        PARSE_MD["Repomd Parser (repomd.go)\n• XML Extraction\n• Path Traversal Neutralization"]
        PARSE_COMPS["Comps Parser (comps.go)\n• Hierarchical Group Trees\n• Localized String Handling"]
        SQLITE["SQLite Access (sqlite.go)\n• Connection Pooling\n• Batch Changelog Queries\n• Prepared Statement Cache"]
        RPM_INSPECT["RPM Reader (rpm_reader.go)\n• Header Inspection\n• Scriptlet Extraction\n• On-Demand File List Extraction"]
    end

    subgraph Logic["Domain & Business Logic (internal/logic & internal/models)"]
        MODELS["Models (internal/models)\n• Package, NVRA, EVR\n• Repomd, Comps, SearchDoc"]
        FILTER["Filter Service (filter.go)\n• Glob Name/NVRA Filtering\n• Hardware Arch Filtering"]
        GROUP["Grouping Service (grouping.go)\n• Comps Category Hierarchy\n• RPM Group Inference Heuristics\n• Alphabetical Letter Buckets"]
        SORT["Sorting Service (sorting.go)\n• RPM EVR Spec Comparison\n• Epoch Supremacy & Tie-Breaking"]
    end

    subgraph StateManagement["State & Cache Layer (internal/state)"]
        STATE["StateStore (state.go)\n• SHA-256 Content Hashing\n• Atomic Temp File Swap\n• Stale File Tracking\n• Thread-Safe Concurrent Read/Write"]
    end

    subgraph Presentation["Presentation Layer (internal/render)"]
        RENDERER["Renderer (renderer.go)\n• Embedded Go HTML Templates\n• Helper Functions (HumanSize, RSSTime)\n• Custom Template Overrides\n• Static Asset Copying"]
        ASSETS["Embedded Assets\n• CSS (Theme, Glassmorphism, Responsive)\n• Vanilla JS Search & Filter Engine\n• Zero External CDNs"]
    end

    subgraph Output["Generated Static Site (outputDir)"]
        INDEX_HTML["index.html"]
        GROUP_HTML["*.group.html"]
        PKG_HTML["*.html (Package Details)"]
        SEARCH_JSON["search.json (Fast In-Memory Index)"]
        RSS_XML["latest-feed.xml (RSS 2.0 Feed)"]
        STATIC_FILES["Static CSS / JS / Favicon"]
    end

    REPOMD & PRIMARY_DB & OTHER_DB & COMPS & RPMS --> Ingestion
    MAIN --> SAFETY --> GEN
    GEN --> Ingestion
    Ingestion --> Logic
    Logic --> Presentation
    GEN --> Presentation
    GEN --> StateManagement
    Presentation --> StateManagement
    StateManagement --> Output
    Presentation --> Output
```

### Modular Directory Structure

The application follows a clean, modular architecture separating concerns between data access, domain business logic, state caching, and presentation:

- **`cmd/repoview`**: Entry point and CLI frontend. Handles long-only argument parsing, flag accumulation, directory validation, and exit code mapping before invoking the orchestration layer.
- **`internal/app`**: Core orchestration layer. The `Generator` struct manages the end-to-end workflow: repository safety validation, metadata loading, package filtering, group tree organization, sibling discovery, parallel rendering scheduling, and stale file cleanup.
- **`internal/repo`**: Data Access Layer (DAL). Handles:
  - Parsing `repomd.xml` to locate repository database locations while enforcing path traversal barriers.
  - Transparent stream decompression for `.gz`, `.bz2`, `.xz`, and `.zst` archives with automatic ephemeral cleanup.
  - Efficient querying of SQLite databases (`primary.sqlite`, `other.sqlite`) using connection pooling, batch parameter blocks, and prepared statement caching.
  - Parsing `comps.xml` for category and group definitions.
  - Direct RPM lead and header inspection for scriptlets, signatures, and on-demand file list extraction.
- **`internal/logic`**: Domain and Business Logic Layer. Contains:
  - **Grouping**: Organizes packages by Comps categories/groups, RPM header groups with heuristic fallback inference, and alphabetical letter buckets.
  - **Sorting**: Implements strict upstream RPM Epoch-Version-Release (EVR) comparison semantics using `go-rpm-version`.
  - **Filtering**: Applies glob-based package name/NVRA matching and hardware architecture exclusions (`--ignore-package`, `--exclude-arch`).
- **`internal/render`**: Presentation Layer. Renders HTML5 pages, RSS 2.0 feeds, and search indices using Go's standard `html/template`. Layout templates and static assets (CSS, Vanilla JS) are compiled into the binary via `embed.FS` for complete portability.
- **`internal/state`**: State and Cache Management. Maintains an incremental state store (`.state.json`) with SHA-256 content hashing to avoid redundant disk writes, detect modified pages, and identify stale/orphaned files for pruning.
- **`internal/models`**: Domain data structures representing repository metadata, packages, dependencies, scriptlets, comps definitions, and search index documents.
- **`internal/util`**: Low-level formatting, temporal conversions (RFC 822 / RFC 1123), binary byte size calculations (KiB, MiB, GiB, TiB), and secure filename sanitization routines.

### Architectural Design Patterns

- **Bounded Worker Pool (Semaphore Pattern)**: The page generation phase leverages a buffered channel semaphore (`sem := make(chan struct{}, NumCPU*2)`) to achieve maximum multi-core parallelism while capping concurrent memory and file descriptor consumption.
- **Dependency Injection & Struct-Based Configuration**: Components (`Generator`, `RepositoryAccess`, `Renderer`, `StateStore`) receive explicit configuration structs, eliminating global state and enabling in-process testing without global side effects.
- **Defensive Input Sanitization**: All filenames, group keys, and URL components derived from untrusted package metadata undergo strict whitelist sanitization (`[a-zA-Z0-9._-]`) to neutralize directory traversal and null-byte injection attacks.
- **Two-Phase Atomic File Commit**: State files and rendered pages are written to temporary sibling files before being atomically renamed into place, guaranteeing resilience against power failures or abrupt termination.

### Core Design Principles & Paradigms

1. **Static Site Generator (SSG) Paradigm**:
   - Rather than serving dynamic requests through an application server with attached databases, RepoView-Go executes ahead-of-time (AOT) batch generation.
   - The resulting output consists strictly of static HTML5, CSS, JSON, and XML files. This completely eliminates runtime attack surfaces, database connection exhaustion, server-side memory leaks, and dynamic query latency.
2. **Air-Gap Compliance (Zero External Network Calls)**:
   - Enterprise repositories frequently reside in high-security, network-isolated environments (e.g., DoD air-gapped enclaves, offline air-cooled datacenters, VPC private subnets).
   - RepoView-Go bundles 100% of its presentation dependencies (fonts, styles, icons, search scripts) as embedded Go assets (`embed.FS`). Zero external requests to CDN fonts, analytics, or third-party CDNs are emitted.
3. **Dual-Format Metadata Compatibility**:
   - RPM repositories generated across distinct tooling generations (original `createrepo`, modern `createrepo_c`, or DNF/RHEL repositories) exhibit format variations. RepoView-Go supports XML revision tracking as both an attribute (`<repomd revision="...">`) and a child element (`<revision>...</revision>`).
4. **State-Driven Incremental Builds**:
   - Repository regeneration avoids redundant disk I/O. The `StateStore` tracks SHA-256 content hashes of all generated artifacts. If package metadata has not changed, write operations are omitted, reducing run times by >90% on subsequent updates.
5. **Streaming & Bounded Resident Memory**:
   - For repositories containing tens of thousands of packages, eagerly loading all package file lists into memory triggers catastrophic heap growth. File lists are extracted on-demand during parallel package rendering and released immediately via defer-nulling.

### Foundational Assumptions

- **Repository Structure**: The input target must be a readable directory conforming to standard RPM repository conventions (containing a `repodata/` directory with `repomd.xml` and SQLite databases).
- **Filesystem Permissions**: The user executing `repoview` possesses write permissions to `--output-dir` and `--state-dir`.
- **Operating Environment**: Compiled for Linux or Windows (via WSL or CGo-enabled native build) with access to a C compiler for SQLite3 integration.

### Edge Case Handling

| Domain | Edge Case | Mitigation / Implementation |
| :--- | :--- | :--- |
| **Directory Safety** | User passes `--output-dir .` or `--output-dir /` | `validateOutputDirSafety()` aborts before execution if output directory matches repo directory, is an ancestor/parent, is the filesystem root (`/`), or contains existing `repodata/`. |
| **Path Traversal** | Malicious `repomd.xml` contains `<location href="../../etc/passwd"/>` | `ParseRepomd()` cleans all relative paths and enforces strict directory prefix matching against the repo base. |
| **Metadata Corruption** | Truncated `.state.json` or unreadable SQLite database | `StateStore` catches JSON parsing errors and automatically falls back to an empty cache state; SQLite queries emit structured domain errors and cleanly rollback. |
| **Compression Formats** | Compressed repodata in Gzip, Zstd, XZ, or Bzip2 | Dynamic header magic sniffing detects algorithm regardless of file extension; stream decompression handles truncated archives gracefully. |
| **Missing Comps XML** | Repository lacks group categorization file | `GroupingService` implements a 3-tier fallback: (1) Comps XML, (2) Heuristic RPM Group name inference, (3) Alphabetical initial letter grouping. |
| **EVR Comparisons** | Tildes (`~`), Carets (`^`), and missing Epochs | `CompareEVR()` complies with RPM specification: `~` sorts before empty version (pre-release), `^` sorts after (snapshot), missing epochs default to 0 without string allocation. |
| **Stale Artifacts** | Packages removed from upstream repository | `Generator.cleanupStale()` cross-references previous state store entries with the current execution and unlinks orphaned HTML/JSON files. |

### Performance & Efficiency Engineering

- **Batch Changelog Processing**: Instead of issuing individual SQLite SELECT queries per package, `enrichBatch()` constructs dynamic parameter blocks querying 500 packages at a time, cutting query overhead by orders of magnitude.
- **Worker Semaphore Concurrency**: Package page rendering executes across a bounded goroutine worker pool sized dynamically to `runtime.NumCPU() * 2`.
- **Allocation-Free Hot Paths**: First-letter extraction, EVR epoch parsing, and relation formatting are optimized to minimize string allocations on the Go garbage collector.

---

## 2. Data Flow and Control Logic

### End-to-End Operational Flow

```text
[1. CLI Entrypoint]
  │   Parse long-only flags, validate paths, map exit codes.
  ▼
[2. Safety Guard Validation]
  │   Confirm outputDir != repoDir, outputDir != root, outputDir != repoParent.
  ▼
[3. Metadata Decompression & Extraction]
  │   Parse repomd.xml -> Decompress primary.sqlite & other.sqlite to t.TempDir().
  ▼
[4. SQLite Ingestion & Batch Enrichment]
  │   Load packages -> Batch enrich changelogs (chunks of 500) -> Inspect RPM headers.
  ▼
[5. Categorization & Hierarchy Logic]
  │   Parse comps.xml -> Build Group Tree -> Infer missing groups -> Alphabetical indexing.
  ▼
[6. Parallel Page Rendering (Worker Pool)]
  │   sem := make(chan struct{}, NumCPU * 2)
  │   Render package HTML pages (on-demand file lists) -> HasChanged() hash check -> Write.
  ▼
[7. Static Asset & Index Generation]
  │   Render index.html, *.group.html, search.json, latest-feed.xml, copy embedded CSS/JS.
  ▼
[8. Stale File Pruning & State Persistence]
      Identify unreferenced previous files -> os.Remove() -> Atomically save .state.json.
```

### Code Relations and Package Interaction

```mermaid
graph TD
    subgraph CMD["cmd/repoview"]
        MAIN["main.go (run)"]
    end

    subgraph APP["internal/app"]
        GEN["Generator (Run)"]
        SAFETY["validateOutputDirSafety"]
    end

    subgraph REPO["internal/repo"]
        REPOMD["ParseRepomd"]
        DECOMP["DecompressFile"]
        SQLITE["RepositoryAccess"]
        COMPS_PARSER["ParseComps"]
        RPM["EnrichPackagesWithRPMDetails"]
    end

    subgraph LOGIC["internal/logic"]
        FILTER["FilterPackages"]
        GROUPING["GroupingService"]
        SORTING["SortPackagesByEVR"]
    end

    subgraph MODELS["internal/models"]
        PKG["Package"]
        REPOMD_M["Repomd"]
        COMPS_M["Comps"]
        SEARCH_M["SearchDoc"]
    end

    subgraph STATE["internal/state"]
        STORE["StateStore"]
    end

    subgraph RENDER["internal/render"]
        RENDERER["Renderer"]
    end

    subgraph UTIL["internal/util"]
        FMT["HumanSize / RSSTime"]
        SAN["SanitizeFilename"]
    end

    MAIN --> APP
    GEN --> SAFETY
    GEN --> REPO
    GEN --> LOGIC
    GEN --> STATE
    GEN --> RENDER
    GEN --> UTIL

    REPO --> MODELS
    REPO --> UTIL
    LOGIC --> MODELS
    LOGIC --> UTIL
    RENDER --> MODELS
    RENDER --> UTIL
```

### System Sequence Diagram

```mermaid
sequenceDiagram
    autonumber
    actor User as Operator / CI Runner
    participant Main as cmd/repoview (main.go)
    participant Gen as internal/app (Generator)
    participant Repo as internal/repo (SQLite & Decompress)
    participant Logic as internal/logic (Grouping & Sorting)
    participant State as internal/state (StateStore)
    participant Render as internal/render (Renderer)
    participant Disk as Filesystem (outputDir)

    User->>Main: repoview --output-dir /www/repo /srv/rpm/el9
    Main->>Gen: NewGenerator(Config) -> Run()
    Gen->>Gen: validateOutputDirSafety()
    
    rect rgb(240, 245, 255)
        note over Gen, Repo: Phase 1: Ingestion & Decompression
        Gen->>Repo: ParseRepomd("repodata/repomd.xml")
        Repo-->>Gen: Database locations (primary, other)
        Gen->>Repo: Decompress primary.sqlite & other.sqlite
        Repo-->>Gen: Open RepositoryAccess connection
        Gen->>Repo: GetAllPackages()
        Repo-->>Gen: []Package (2,000+ items)
        Gen->>Repo: EnrichPackagesWithChangelogs(batch: 500)
        Gen->>Repo: EnrichPackagesWithRPMDetails()
    end

    rect rgb(245, 255, 240)
        note over Gen, Logic: Phase 2: Domain Organization
        Gen->>Logic: FilterPackages(excludeArch, ignoreList)
        Gen->>Logic: GroupingService.GetGroups(comps)
        Logic-->>Gen: Comps Tree, RPM Groups, Letter Groups
        Gen->>Logic: SortPackagesByEVR()
    end

    rect rgb(255, 250, 240)
        note over Gen, Render: Phase 3: Parallel Rendering & State Caching
        Gen->>State: NewStateStore(.state.json)
        Gen->>Render: NewRenderer(templates, assets)
        
        loop Bounded Parallel Worker Pool (NumCPU * 2)
            Gen->>Repo: ReadRPMFiles(pkg) [On-Demand]
            Gen->>Render: RenderPackage(pkg, group)
            Render-->>Gen: HTML Content
            Gen->>State: HasChanged(filename, content)
            alt Content Modified or Force Flag
                Gen->>Disk: WriteToFile(filename, content)
            else Unchanged
                Gen->>Gen: Skip Write (I/O Saved)
            end
        end

        Gen->>Render: RenderIndex(), RenderGroup(), RenderRSS(), RenderSearchIndex()
        Gen->>Disk: Write index.html, search.json, latest-feed.xml
        Gen->>Render: WriteAssets() -> Copy CSS & JS
    end

    rect rgb(255, 240, 245)
        note over Gen, State: Phase 4: Stale Cleanup & Cache Sync
        Gen->>State: GetStaleFiles(currentGenerated)
        loop Each Stale File
            Gen->>Disk: os.Remove(staleFile)
        end
        Gen->>State: Save() -> Atomic Rename state.json.tmp -> state.json
    end

    Gen-->>Main: nil (Success)
    Main-->>User: Exit Code 0 (Complete)
```

---

## 3. Performance and Scalability

### Concurrency Model & Goroutine Worker Pool

The generation bottleneck in large RPM repositories stems from template execution, disk I/O, and on-demand file list decompression. To maximize CPU core saturation without triggering thread contention or context-switching thrashing, `internal/app/generator.go` utilizes a **Bounded Semaphore Concurrency Pattern**:

```go
// Worker pool bounded to 2x logical CPU cores
sem := make(chan struct{}, runtime.NumCPU()*2)
var wg sync.WaitGroup

for _, name := range uniqueNames {
    wg.Add(1)
    sem <- struct{}{} // Acquire token (blocks if pool is saturated)
    go func(pkgName string) {
        defer wg.Done()
        defer func() { <-sem }() // Release token

        // Execute package render pipeline in parallel...
    }(name)
}
wg.Wait()
```

### Channel Structures & Synchronization Primitives

1. **Token Semaphore (`chan struct{}`)**:
   - Bounded channel of size `runtime.NumCPU() * 2`.
   - Ensures memory consumption remains stable regardless of whether the repository contains 500 packages or 50,000 packages.
2. **Synchronized File Accumulator (`sync.Mutex`)**:
   - A critical section safeguards the `generatedFiles` slice as goroutines complete rendering.
3. **Lock-Free Atomic Error Counter (`sync/atomic`)**:
   - Render failures increment `errorCount` atomically (`atomic.AddInt64(&errorCount, 1)`), avoiding global lock contention across worker threads.
4. **Read-Write Mutex State Store (`sync.RWMutex`)**:
   - `StateStore` implements granular read-write locking: `HasChanged()` acquires read locks for hash matching and upgrades to write locks only when registering newly dirty files.

### Memory Footprint & Heap Allocation Optimization

- **On-Demand File List Loading**:
  In a 10,000 package repository, loading all files (often 100 to 1,000 files per package) into RAM requires >2 GB of resident heap. RepoView-Go defers file list reading to the specific package worker goroutine, and immediately nulls the pointer after rendering:
  ```go
  files, err := repo.ReadRPMFiles(rpmPath)
  if err == nil {
      latest.Details.Files = files
      defer func() {
          if latest.Details != nil {
              latest.Details.Files = nil // Allow Go GC to reclaim memory immediately
          }
      }()
  }
  ```
- **Prepared Statement Caching**:
  Dependency lookup statements (`SELECT flags, name, epoch, version, release FROM requires WHERE pkgKey = ?`) are prepared once upon database initialization and reused across all goroutines.

### SQLite Connection Pooling & Prepared Statement Caching

- SQLite is opened in read-only URI mode (`file:path?mode=ro&cache=shared`).
- Connection parameters are tuned for batch read performance with busy timeouts to eliminate `SQLITE_BUSY` errors during concurrent accesses.
- Prepared statement handles are stored in `RepositoryAccess` and cleanly finalized via `Close()`.

---

## 4. Dependencies

### Runtime Modules & External Libraries

All external dependencies have been audited for reliability, active maintenance, and absence of risky transitivity.

| Module Path | Version | License | Architectural Role |
| :--- | :---: | :---: | :--- |
| **`github.com/mattn/go-sqlite3`** | `v1.14.52` | MIT | CGo SQLite3 driver utilized for low-latency querying of `primary.sqlite` and `other.sqlite`. |
| **`github.com/klauspost/compress`** | `v1.20.0` | BSD-3-Clause | Highly optimized, multi-threaded Zstandard (`zstd`) and accelerated Gzip decompression engine. |
| **`github.com/ulikunitz/xz`** | `v0.5.16` | BSD-3-Clause | Pure Go XZ decompression library handling `.xz` compressed SQLite databases. |
| **`github.com/knqyf263/go-rpm-version`** | Latest | MIT | Upstream-compliant RPM EVR (Epoch-Version-Release) parsing and comparison engine. |
| **`github.com/sassoftware/go-rpmutils`** | `v0.4.0` | Apache-2.0 | RPM payload reader for extracting RPM lead, signatures, scriptlets, and file lists directly from `.rpm` files. |

### Build Tooling & System Dependencies

- **Go SDK**: Version `1.21` or higher (`1.22+` recommended).
- **C Compiler**: `gcc` or `clang` required by CGo for `go-sqlite3`. On Alpine Linux, `musl-dev` is required.
- **Operating Systems**: Native Linux (CentOS/RHEL, Debian/Ubuntu, Alpine, Fedora), macOS, and Windows (via WSL or MinGW-w64).

### Package Dependency Diagram

```mermaid
graph TD
    subgraph RepoView["RepoView-Go Modules"]
        CMD["github.com/edsilegxrepo/repoview/cmd/repoview"]
        APP["github.com/edsilegxrepo/repoview/internal/app"]
        REPO["github.com/edsilegxrepo/repoview/internal/repo"]
        LOGIC["github.com/edsilegxrepo/repoview/internal/logic"]
        MODELS["github.com/edsilegxrepo/repoview/internal/models"]
        RENDER["github.com/edsilegxrepo/repoview/internal/render"]
        STATE["github.com/edsilegxrepo/repoview/internal/state"]
        UTIL["github.com/edsilegxrepo/repoview/internal/util"]
    end

    subgraph ThirdParty["Third-Party Dependencies"]
        SQLITE3["github.com/mattn/go-sqlite3"]
        COMPRESS["github.com/klauspost/compress\n(zstd / gzip)"]
        XZ["github.com/ulikunitz/xz"]
        RPM_VER["github.com/knqyf263/go-rpm-version"]
        RPM_UTILS["github.com/sassoftware/go-rpmutils"]
    end

    CMD --> APP
    APP --> REPO
    APP --> LOGIC
    APP --> RENDER
    APP --> STATE
    APP --> UTIL

    REPO --> SQLITE3
    REPO --> COMPRESS
    REPO --> XZ
    REPO --> RPM_UTILS

    LOGIC --> RPM_VER
```

---

## 5. Security Architecture

### Static Site Generation Security Paradigm

The foundational security strength of RepoView-Go lies in its **AOT Static Architecture**:

```text
Dynamic Application (Traditional Web App)
  [Browser] ---> [Internet] ---> [Web Server] ---> [App Runtime (PHP/Python/Go)] ---> [Database Server]
                                                            ▲
                                           Target for SQLi, RCE, SSRF, Deserialization

RepoView-Go Architecture
  [Build Pipeline] ---> [repoview CLI] ---> [Static Files on Disk]
                                                   │
  [Browser]       ---> [Reverse Proxy / CDN] ──────┘ (Zero server-side code execution)
```

1. **Immunity to Injection Attacks**: Because the serving layer serves purely static HTML, CSS, and JSON files, there are no server-side SQL queries, LDAP searches, or shell commands executed during client browsing.
2. **Zero Remote Code Execution (RCE) Surface**: The public-facing endpoint has no application runtime, eliminating JVM/Python/Node/PHP memory corruption and deserialization exploits.
3. **DDoS Resilience**: Static assets can be cached at line-rate by Nginx, Cloudflare, AWS CloudFront, or local edge proxies without backend database exhaustion.

### Generator Defenses & Input Sanitization

During the generation phase, the `repoview` binary treats all input repository files as potentially untrusted:

- **Path Traversal Shield**: `ParseRepomd()` ensures that XML `<location href="...">` paths cannot escape the designated repository root. Traversal payloads such as `../../etc/shadow` are rejected immediately.
- **Filename Sanitization**: `util.SanitizeFilename()` converts arbitrary package identifiers and group names into strictly safe filesystem characters (`[a-zA-Z0-9._-]`), neutralizing directory traversal and null-byte injection (`\x00`).
- **Destructive Deletion Barrier**: `validateOutputDirSafety()` validates that the destination directory is not identical to the repo directory, parent tree, or root partition, preventing accidental data loss when `--force` is toggled.

### Serving Layer Security Architecture

When deployed into production, static repository views are published behind an authenticated reverse proxy or object storage gateway.

```mermaid
flowchart TB
    subgraph Clients["Client Access Tiers"]
        ANON["Anonymous / Public Consumer\n(Read-Only Repository Browsing)"]
        DEV["Enterprise Developer / CI Runner\n(Authenticated Package Downloader)"]
        ADMIN["Release Engineer / SecOps\n(Authorized Publisher & Maintainer)"]
    end

    subgraph Perimeter["Security Gateway & Edge Reverse Proxy"]
        WAF["Web Application Firewall (WAF)\n• Rate Limiting\n• IP Geofencing / CIDR Whitelist"]
        TLS["TLS 1.3 Termination\n• Strict Cipher Suites\n• HSTS Enforcement"]
        AUTH_LAYER["Authentication Gateway\n• mTLS (Client Certificate)\n• OAuth2 / OIDC Bearer Token\n• HTTP Basic / Digest Auth"]
        RBAC_GATE["Access Control Engine (RBAC)\n• Policy Validation\n• Header Inspection"]
    end

    subgraph StaticStorage["Air-Gapped / Isolated Storage (outputDir)"]
        WEB_SERVER["Nginx / Caddy / Apache Server\n(Static File Server)"]
        REPO_FILES[("Static Files\n• index.html\n• search.json\n• *.html\n• RPM Binaries")]
    end

    subgraph BuildServer["Build Pipeline / CI Runner (Isolated Execution)"]
        CLI_RUNNER["RepoView-Go CLI Binary\n(Runs with dedicated low-privilege service account)"]
        RAW_REPO[("Local RPM Mirror\n/srv/rpm-storage/")]
    end

    ANON --> Perimeter
    DEV --> Perimeter
    ADMIN --> Perimeter

    WAF --> TLS --> AUTH_LAYER --> RBAC_GATE --> WEB_SERVER
    WEB_SERVER --> REPO_FILES

    CLI_RUNNER --> RAW_REPO
    CLI_RUNNER -- "Atomic Sync" --> REPO_FILES
```

### Authentication Layers & Supported Methods

RepoView-Go repositories support four primary enterprise authentication patterns implemented at the reverse proxy layer (e.g. Nginx, Envoy, Caddy):

1. **Mutual TLS (mTLS / Client Certificates)**:
   - **Use Case**: High-security air-gapped defense networks and zero-trust infrastructure.
   - **Mechanism**: The reverse proxy validates the client's X.509 certificate against an internal corporate Certificate Authority (CA) before granting access.
2. **OAuth2 / OpenID Connect (OIDC) Proxy**:
   - **Use Case**: Corporate SSO integration (Okta, Keycloak, Ping, Azure AD).
   - **Mechanism**: Upstream proxy (e.g., `oauth2-proxy`) intercepts requests and validates Bearer JWT tokens.
3. **HTTP Basic / Digest Authentication**:
   - **Use Case**: Native compatibility with standard package managers (`dnf`, `yum`, `zypper`).
   - **Mechanism**: DNF natively passes credentials configured in `/etc/yum.repos.d/*.repo` via `username=` and `password=` fields.
4. **Network-Level CIDR / IP Whitelisting**:
   - **Use Case**: Production datacenter clusters and internal build farm VPCs.
   - **Mechanism**: Access restricted strictly to known internal subnet ranges.

### Role-Based Access Control (RBAC) Matrix

| Principal / Role | Permissions | Accessible Resources | Enforcement Mechanism |
| :--- | :--- | :--- | :--- |
| **Anonymous Consumer** | Read-Only | Public index, group listings, search metadata (`search.json`), RSS feeds | Reverse proxy allows `GET /`, `GET /*.html`, `GET /search.json` without challenge. |
| **Licensed / Internal Developer** | Read-Only (Full) | All HTML pages, search indices, and downloadable `.rpm` package binaries | Reverse proxy enforces HTTP Basic or OIDC session cookie; restricts download endpoints. |
| **Automated CI/CD Worker** | Read-Only (Automated) | DNF repository metadata (`repodata/`) and dependency RPMs | mTLS machine certificate or static API token in `Authorization` header. |
| **Release Engineer / Publisher** | Write / Execute | CLI binary execution, file generation, `.state.json` management, stale cleanup | Linux filesystem POSIX permissions (`chmod 0750`, dedicated `repoview` system group). |
| **System Administrator** | Admin / Configuration | Reverse proxy configuration, TLS certificates, cron/systemd service units | OS root / sudo access, infrastructure-as-code management. |

### Content Security Policy (CSP) & Client-Side Hardening

Because RepoView-Go requires zero external network calls, servers hosting the generated site can enforce the strictest Content Security Policy headers:

```http
Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none';
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Referrer-Policy: strict-origin-when-cross-origin
```

- **`default-src 'self'`**: Prevents injection of third-party scripts, remote images, or tracking beacons.
- **`object-src 'none'`**: Completely blocks legacy plugins (Flash, Java Applets).
- **`X-Content-Type-Options: nosniff`**: Prevents MIME-type confusion attacks on static assets.
