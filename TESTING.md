# RepoView-Go Test Suite Documentation

Comprehensive guide to the architecture, design principles, test execution, coverage benchmarks, and verification workflows for **RepoView-Go**.

---

## Table of Contents

1. [Architecture, Design and Principles](#1-architecture-design-and-principles)
   - [Architectural Flow Chart](#architectural-flow-chart)
   - [Core Testing Principles](#core-testing-principles)
2. [Logic Flow of the Tests](#2-logic-flow-of-the-tests)
   - [Logical Sequence Chart](#logical-sequence-chart)
   - [Positive vs. Negative Testing Matrix](#positive-vs-negative-testing-matrix)
3. [Technical Requirements and Setup](#3-technical-requirements-and-setup)
   - [Prerequisites & Dependencies](#prerequisites--dependencies)
   - [Environment Variables](#environment-variables)
   - [Build Constraints & Tags](#build-constraints--tags)
   - [Cross-Platform Portability (Linux & Windows/WSL)](#cross-platform-portability-linux--windowswsl)
4. [Test Package Tree Structure](#4-test-package-tree-structure)
5. [List of Tests](#5-list-of-tests)
   - [CLI & Flag Parsing (`cmd/repoview`)](#cli--flag-parsing-cmdrepoview)
   - [Application Pipeline & Safety (`internal/app`)](#application-pipeline--safety-internalapp)
   - [Business Logic, Filtering & Grouping (`internal/logic`)](#business-logic-filtering--grouping-internallogic)
   - [Domain Models & XML (`internal/models`)](#domain-models--xml-internalmodels)
   - [Template Rendering & Assets (`internal/render`)](#template-rendering--assets-internalrender)
   - [Repository Parsers, Decompression & SQLite (`internal/repo`)](#repository-parsers-decompression--sqlite-internalrepo)
   - [Cache State Management & Concurrency (`internal/state`)](#cache-state-management--concurrency-internalstate)
   - [Formatting & Sanitization Utilities (`internal/util`)](#formatting--sanitization-utilities-internalutil)
   - [Live End-to-End Integration Suite (`tests/integration`)](#live-end-to-end-integration-suite-testsintegration)
6. [Code Coverage Report](#6-code-coverage-report)
   - [Current Coverage Statistics](#current-coverage-statistics)
   - [How to Measure and Refresh Statistics](#how-to-measure-and-refresh-statistics)
7. [Realistic Data Simulation](#7-realistic-data-simulation)
   - [Live Repository Verification](#live-repository-verification)
   - [Live HTTP Server & Endpoint Listeners](#live-http-server--endpoint-listeners)
   - [CLI Subprocess Binary Execution](#cli-subprocess-binary-execution)
8. [How to Run the Tests](#8-how-to-run-the-tests)
   - [Bash (Linux / macOS / WSL)](#bash-linux--macos--wsl)
   - [PowerShell (Windows native / WSL bridge)](#powershell-windows-native--wsl-bridge)
9. [Maintenance and Troubleshooting](#9-maintenance-and-troubleshooting)

---

## 1. Architecture, Design and Principles

RepoView-Go employs a multi-tiered testing strategy structured to detect deep architectural regressions, concurrency hazards, memory leaks, malicious inputs, and real-world repodata inconsistencies.

### Architectural Flow Chart

```mermaid
flowchart TB
    subgraph TestExecution["Test Runner (go test)"]
        UT["Unit Tests (Default)\n• Sub-second execution (&lt;0.2s)\n• Zero external dependencies\n• Synthetic in-memory fixtures"]
        IT["Integration Tests (-tags=integration)\n• Real RPM Repositories (&gt;2,000 pkgs)\n• Unmocked SQLite3 & Decompression\n• Live HTTP Server & Listeners\n• Compiled Binary Subprocess"]
    end

    subgraph UnitLayer["Unit Test Isolation Layer"]
        CLI_T["cmd/repoview/main_test.go"]
        APP_T["internal/app/*_test.go"]
        LOGIC_T["internal/logic/*_test.go"]
        MODELS_T["internal/models/*_test.go"]
        RENDER_T["internal/render/*_test.go"]
        REPO_T["internal/repo/*_test.go"]
        STATE_T["internal/state/*_test.go"]
        UTIL_T["internal/util/*_test.go"]
    end

    subgraph IntegrationLayer["E2E Integration Layer"]
        E2E["tests/integration/e2e_test.go"]
        REAL_REPO[("Real RPM Repository\n/u01/wwwroot/test/el9/base/x86_64")]
        EPHEMERAL_HTTP["net/http Ephemeral Server\nhttp://127.0.0.1:{random_port}"]
        HTTP_CLIENT["HTTP Client Verification\n• index.html\n• search.json\n• pkg.html\n• latest-feed.xml"]
        CLI_BIN["Compiled repoview Binary\nSubprocess exec.Command"]
    end

    subgraph Safety["Zero Repo Pollution Guard"]
        TDIR["testing.T.TempDir()\nEphemeral auto-cleaned sandbox"]
    end

    UT --> UnitLayer
    IT --> IntegrationLayer

    UnitLayer --> TDIR
    IntegrationLayer --> TDIR
    IntegrationLayer --> REAL_REPO
    IntegrationLayer --> EPHEMERAL_HTTP --> HTTP_CLIENT
    IntegrationLayer --> CLI_BIN
```

### Core Testing Principles

1. **Defect Discovery Over Convenience**: Tests are deliberately written to uncover hard-to-find defects, boundary edge cases, format variations (e.g., XML element vs. attribute revisions in `repomd.xml`), path traversals, and corrupt payload recovery. Tests never bypass or mask genuine code flaws.
2. **Strict Zero Repository Pollution**: Every test generating disk artifacts MUST allocate directories exclusively via `t.TempDir()`. No test leaves behind `.state.json`, temporary databases, or stray HTML pages in the working tree.
3. **Isolation and Dual-Speed Execution**:
   - **Fast Tier (Default)**: Unit tests execute the complete generator pipeline on synthetic in-memory mock repositories in under **50ms**, making continuous TDD fast and friction-free.
   - **Live Integration Tier (`-tags=integration`)**: Validates real-life production repositories containing thousands of RPMs without mocking underlying SQLite or decompression operations.
4. **Thread-Safety & Race Verification**: High-concurrency operations (such as multi-goroutine state cache access) are subjected to heavy concurrent loads and validated with Go's race detector (`go test -race ./...`).
5. **Cross-Platform Compatibility**: Code and tests are designed to execute seamlessly on Linux and Windows (via WSL or PowerShell). Filepath operations strictly utilize `filepath.Clean` and normalized slash separators.

---

## 2. Logic Flow of the Tests

### Logical Sequence Chart

```mermaid
sequenceDiagram
    autonumber
    actor Dev as Developer / CI
    participant Runner as go test Runner
    participant Fixture as TempDir & Fixture Factory
    participant Target as SUT (RepoView-Go Code)
    participant Disk as Ephemeral Disk (t.TempDir)
    participant LiveHTTP as Live net/http Server

    Dev->>Runner: go test ./... (or -tags=integration)
    Runner->>Fixture: Initialize isolated test environment (t.TempDir)
    
    alt Positive Test Path
        Fixture->>Target: Feed valid repomd.xml, primary.sqlite, comps.xml
        Target->>Target: Parse, Decompress, Filter, EVR Sort, Enrich
        Target->>Disk: Atomically write HTML, RSS, and search.json
        Target-->>Runner: Return nil error
        Runner->>Disk: Verify output files exist and match schema
    else Negative Test Path
        Fixture->>Target: Feed corrupt file / path traversal / invalid flag
        Target->>Target: Detect safety violation or parse corruption
        Target-->>Runner: Return explicit wrapped error
        Runner->>Runner: Assert expected error substring
    else Live Integration Path (-tags=integration)
        Fixture->>Target: Point to real repository (/u01/wwwroot/...)
        Target->>Disk: Generate 2,000+ package pages
        Runner->>LiveHTTP: Start listener on 127.0.0.1:0 serving output
        Runner->>LiveHTTP: Issue HTTP GET for HTML, JSON, RSS
        LiveHTTP-->>Runner: Return HTTP 200 OK + Valid Payload
        Runner->>Target: Run incremental pass (verify cache hits)
    end

    Runner->>Fixture: t.TempDir() automatic cleanup
    Runner-->>Dev: PASS (Coverage Report & Timing)
```

### Positive vs. Negative Testing Matrix

| Component | Positive Testing Scenarios | Negative Testing Scenarios |
| :--- | :--- | :--- |
| **CLI (`cmd/repoview`)** | `--version`, valid options, string list flags (`-A`, `-X`) | Missing flags, non-existent repos, pointing output to root `/`, directory safety violations |
| **Decompression (`repo`)** | Standard Gzip, Zstandard (`.zst`), XZ (`.xz`), uncompressed streams | Corrupted header bytes, truncated archives, unsupported compression algorithms |
| **Repository Metadata (`repomd.xml`)** | `<revision>` as XML text element, `revision` as root attribute | Path traversal attempts (`../../etc/passwd`), unsupported metadata version (`v2`), missing primary/other databases |
| **Comps Groups (`comps.xml`)** | Full group tree, localized names, default package inclusions | Non-existent comps path, malformed/truncated XML |
| **SQLite Engines (`sqlite.go`)** | Bulk package querying, multi-table batch changelog enrichment, dependency queries | Missing SQLite tables, corrupt database files, orphaned foreign keys |
| **EVR Version Sorting (`logic`)** | Upstream RPM EVR comparisons, multi-segment numeric/alphabetic versions | Missing epoch (treated as 0), tilde (`~`) pre-release ordering, caret (`^`) post-release ordering |
| **Filtering (`filter.go`)** | Glob inclusion, glob exclusion, arch filtering (`x86_64`, `noarch`) | Invalid glob patterns (e.g. malformed syntax handling) |
| **State Cache (`state.go`)** | Atomic JSON swap, content hashing, stale file tracking | Corrupted `.state.json` recovery, concurrent writes across 50 goroutines |
| **HTML/RSS/Search Rendering (`render`)** | Full template rendering, custom template overrides, static asset delivery | Invalid template syntax, missing assets, empty package collections |

---

## 3. Technical Requirements and Setup

### Prerequisites & Dependencies

- **Go**: Version `1.21` or higher (`1.22+` recommended).
- **C Compiler**: A standard C compiler (`gcc`, `clang`, or `musl-gcc`) is required for CGo to compile `github.com/mattn/go-sqlite3`.
- **CGo**: Must be enabled (`CGO_ENABLED=1`).
- **Operating System**: Linux (native), macOS, or Windows via WSL (Windows Subsystem for Linux).

### Environment Variables

| Variable | Default Value | Purpose |
| :--- | :--- | :--- |
| `CGO_ENABLED` | `1` | Enables CGo compilation required for SQLite3 bindings. |
| `REPOVIEW_TEST_REPO` | `/u01/wwwroot/test/el9/base/x86_64` | Path to a local RPM repository containing valid `repodata/` for live integration tests. |

### Build Constraints & Tags

- Standard unit tests require **no flags** and execute with:
  ```bash
  go test ./...
  ```
- End-to-end integration tests are gated with the Go build constraint:
  ```go
  //go:build integration
  ```
  and are executed by specifying:
  ```bash
  go test -tags=integration ./tests/integration/...
  ```

### Cross-Platform Portability (Linux & Windows/WSL)

- **Linux / macOS**: Run directly in standard bash or zsh terminals.
- **Windows**:
  - **Recommended**: Run inside WSL (Ubuntu/Debian) to guarantee native CGo and file permission parity.
  - **PowerShell**: Commands can invoke `wsl go test ...` or native Windows Go when a Windows GCC toolchain (e.g. MinGW-w64 / TDM-GCC) is installed.

---

## 4. Test Package Tree Structure

```text
repoview-go/
├── cmd/
│   └── repoview/
│       ├── main.go
│       └── main_test.go                   # CLI argument parsing, flags, safety exits
├── internal/
│   ├── app/
│   │   ├── generator.go
│   │   ├── generator_test.go              # Output directory safety validation
│   │   └── generator_unit_test.go         # Full pipeline unit test (mock repo), errors
│   ├── logic/
│   │   ├── filter.go
│   │   ├── filter_test.go                 # Package inclusion/exclusion & arch filters
│   │   ├── grouping.go
│   │   ├── grouping_test.go               # Comps tree, RPM groups, letter groups, inference
│   │   ├── sorting.go
│   │   └── sorting_test.go                # RPM EVR comparison and epoch parsing
│   ├── models/
│   │   ├── comps.go
│   │   ├── comps_test.go                  # Comps XML unmarshaling and localization
│   │   ├── package.go
│   │   ├── package_test.go                # Dependencies, scriptlets, NVRA, filenames
│   │   ├── repomd.go
│   │   ├── repomd_test.go                 # Repomd dual-format revision parsing
│   │   ├── search.go
│   │   └── search_test.go                 # Search index JSON serialization
│   ├── render/
│   │   ├── renderer.go
│   │   └── renderer_test.go               # HTML templates, RSS, search index, custom assets
│   ├── repo/
│   │   ├── comps.go
│   │   ├── comps_test.go                  # Comps XML parsing and error handling
│   │   ├── decompress.go
│   │   ├── decompress_test.go             # Gzip, Zstd, XZ decompression & sniffing
│   │   ├── repomd.go
│   │   ├── repomd_test.go                 # Repomd parsing, path traversal, validation
│   │   ├── rpm_reader.go
│   │   ├── rpm_reader_test.go             # RPM header reader & package enrichment
│   │   ├── sqlite.go
│   │   └── sqlite_test.go                 # SQLite lifecycle, changelogs, dependencies
│   ├── state/
│   │   ├── state.go
│   │   └── state_test.go                  # Atomic save, concurrency, corrupt recovery
│   └── util/
│       ├── format.go
│       ├── format_test.go                 # Human sizes, RSS RFC822 dates, letter keys
│       ├── sanitize.go
│       └── sanitize_test.go               # Filename and URL path sanitization
└── tests/
    └── integration/
        └── e2e_test.go                    # Live E2E suite, HTTP server, CLI subprocess
```

---

## 5. List of Tests

### CLI & Flag Parsing (`cmd/repoview`)

| Logical Group | Test Name | Technical Purpose / Description | Success Criteria (PASS/FAIL) |
| :--- | :--- | :--- | :--- |
| CLI / Metadata | `TestRun_Version` | Executes `run(["--version"], stdout, stderr)`. | **PASS**: Exit code 0, stdout contains `"RepoView-Go v"`. |
| CLI / Validation | `TestRun_NoArgs` | Executes `run([], stdout, stderr)` without required arguments. | **PASS**: Exit code 1, stderr contains usage message. |
| CLI / Validation | `TestRun_InvalidFlag` | Passes an unrecognized flag `--unknown-option`. | **PASS**: Exit code 1, stderr reports unknown flag error. |
| CLI / Filesystem | `TestRun_MissingRepo` | Passes a non-existent repository path. | **PASS**: Exit code 1, stderr reports repository directory not found. |
| CLI / Filesystem | `TestRun_NotADirectory` | Passes a regular file path instead of a directory. | **PASS**: Exit code 1, stderr reports target is not a directory. |
| CLI / Safety | `TestRun_SafetyViolation` | Attempts to set output directory equal to the repo directory. | **PASS**: Exit code 1, stderr warns against catastrophic deletion. |
| CLI / Flags | `TestStringList` | Verifies `stringList` flag collector handles comma-separated and repeated values. | **PASS**: String slice populated correctly; `String()` produces comma-separated list. |

### Application Pipeline & Safety (`internal/app`)

| Logical Group | Test Name | Technical Purpose / Description | Success Criteria (PASS/FAIL) |
| :--- | :--- | :--- | :--- |
| Safety Guards | `TestValidateOutputDirSafety` | Validates safety barriers preventing accidental wiping of source repos or root. | **PASS**: Errors when outputDir == repoDir, parent dir, or root `/`; succeeds on safe child/sibling paths. |
| Full Pipeline | `TestGenerator_FullPipeline` | Builds a complete synthetic mock repository and runs the entire generator. | **PASS**: Generates `index.html`, `mockapp.html`, `search.json`, `latest-feed.xml`, and executes incremental second run cleanly. |
| Repository Discovery | `TestDetectSiblingRepos` | Creates adjacent sibling repository directories and tests automatic navigation linking. | **PASS**: Accurately detects and returns sibling repo paths and names. |
| Pipeline Errors | `TestGenerator_Errors` | Evaluates generator resilience when provided an invalid repo path. | **PASS**: Returns clean error without panicking or leaking resources. |

### Business Logic, Filtering & Grouping (`internal/logic`)

| Logical Group | Test Name | Technical Purpose / Description | Success Criteria (PASS/FAIL) |
| :--- | :--- | :--- | :--- |
| Package Filtering | `TestFilterPackages_Passthrough` | Filters packages with empty include/exclude options. | **PASS**: All input packages are retained unaltered. |
| Package Filtering | `TestFilterPackages_InvalidGlob` | Supplies malformed glob syntax in exclusion options. | **PASS**: Returns error indicating invalid glob pattern. |
| Package Filtering | `TestFilterPackages_ArchitectureExclusion` | Filters out non-matching architectures (e.g., `-A x86_64`). | **PASS**: Keeps matching architecture and `noarch`; discards others (e.g., `aarch64`). |
| Package Filtering | `TestFilterPackages_NameAndNVRAGlobs` | Tests exclusion globs on package names and NVRA patterns. | **PASS**: Excluded packages are filtered out; remaining packages match expected list. |
| Comps Grouping | `TestBuildGroupTree` | Builds hierarchical categories and group trees from comps collections. | **PASS**: Hierarchical nodes reflect parent-child categories; handles orphan groups. |
| Group Inference | `TestInferGroupForPackage` | Evaluates group inference fallback heuristic based on package names/keywords. | **PASS**: Matches known patterns (e.g. `python-*`, `*-devel`, `*-doc`) to appropriate groups. |
| RPM Groups | `TestGetRpmGroups_Inference` | Groups packages by RPM header `Group` metadata with heuristic inference fallback. | **PASS**: Packages categorized under correct RPM group cards. |
| Letter Indexing | `TestGetLetterGroups` | Groups packages alphabetically by initial letter. | **PASS**: Groups correctly keyed `A-Z` and `#` for numbers/symbols. |
| Comps Integration | `TestCompsGroups` | Builds grouping structures utilizing parsed comps definition. | **PASS**: Packages assigned to defined comps groups; active state flags accurately set. |
| EVR Sorting | `TestSortPackagesByEVR` | Sorts a list of package versions using RPM EVR semantics. | **PASS**: Packages sorted in ascending/descending EVR order matching RPM specification. |
| EVR Edge Cases | `TestCompareEVR_EdgeCases` | Evaluates edge cases in EVR comparison: tilde pre-releases, caret post-releases, epoch mismatches. | **PASS**: Correct relative order: `1.0~rc1 < 1.0 < 1.0^post1 < 1.1`. |
| Epoch Parsing | `TestParseEpoch` | Parses string epochs (`""`, `"0"`, `"2"`, invalid strings). | **PASS**: Blank returns 0; valid numbers parsed; invalid strings default to 0. |

### Domain Models & XML (`internal/models`)

| Logical Group | Test Name | Technical Purpose / Description | Success Criteria (PASS/FAIL) |
| :--- | :--- | :--- | :--- |
| Comps Model | `TestCompsLocalization` | Unmarshals XML comps file with localized strings (`name xml:lang="de"`). | **PASS**: Returns English name by default, German name when requested. |
| Package Model | `TestFormattedRelation` | Formats RPM relation entries (flags `<ge>`, `<eq>`, versions). | **PASS**: Formats string representation cleanly (e.g., `libfoo >= 1.2.0`). |
| Package Model | `TestPackageDependencies_HasAny` | Tests `HasAny()` on package requires, provides, conflicts, obsoletes. | **PASS**: Returns `true` if any slice contains items; `false` when all empty. |
| Package Model | `TestRPMScriptlets_HasAny` | Tests `HasAny()` on pre/post install and uninstall shell scriptlets. | **PASS**: Returns `true` if any scriptlet string is non-empty. |
| Package Model | `TestPackage_EVR_And_Filenames` | Validates `EVR()`, `Filename()`, and `RPMFilename()` helpers. | **PASS**: Produces canonical NVRA strings and standardized `.html` filenames. |
| Repomd Model | `TestRepomdUnmarshaling` | Parses `repomd.xml` containing revision as element and as attribute. | **PASS**: Unmarshals both styles successfully into the `Revision` field. |
| Search Model | `TestSearchIndexSerialization` | Tests search document creation and JSON marshaling. | **PASS**: Produces valid JSON structure containing name, summary, and URL. |

### Template Rendering & Assets (`internal/render`)

| Logical Group | Test Name | Technical Purpose / Description | Success Criteria (PASS/FAIL) |
| :--- | :--- | :--- | :--- |
| Rendering Engine | `TestTemplateRendering` | Renders index, group, and package pages using embedded Go templates. | **PASS**: Valid HTML5 generated containing expected metadata and layout elements. |
| Feeds & Index | `TestRenderer_RSS_And_SearchIndex` | Generates `latest-feed.xml` RSS 2.0 and `search.json`. | **PASS**: XML passes RSS schema validation; JSON parses with all indexed packages. |
| Customization | `TestRenderer_CustomTemplates_And_Assets` | Exercises custom template directory override and static asset copying. | **PASS**: Overridden templates render custom tags; assets copied without corruption. |
| Template Helpers | `TestRenderer_TemplateFunctions` | Tests custom template functions (`humanSize`, `rssTime`, `formatEVR`). | **PASS**: All template helper functions return expected formatting. |

### Repository Parsers, Decompression & SQLite (`internal/repo`)

| Logical Group | Test Name | Technical Purpose / Description | Success Criteria (PASS/FAIL) |
| :--- | :--- | :--- | :--- |
| Comps Parser | `TestParseComps_Valid` | Parses a well-formed `comps.xml` document. | **PASS**: Extracts categories, groups, and package lists without error. |
| Comps Parser | `TestParseComps_NotFound` | Attempts to parse non-existent comps file. | **PASS**: Returns `os.ErrNotExist` error. |
| Comps Parser | `TestParseComps_Corrupt` | Attempts to parse malformed XML content. | **PASS**: Returns XML syntax error. |
| Decompression | `TestDecompressFile_Gzip` | Decompresses a `.gz` archive to target file. | **PASS**: Target file created with uncompressed payload matching original. |
| Decompression | `TestDecompressFile_Zstd` | Decompresses a `.zst` archive to target file. | **PASS**: Decompressed payload matches original uncompressed content. |
| Decompression | `TestDecompressFile_XZ` | Decompresses a `.xz` archive to target file. | **PASS**: Decompressed payload matches original uncompressed content. |
| Decompression | `TestDecompressFile_Uncompressed` | Processes already uncompressed file. | **PASS**: Copies file byte-for-byte without error. |
| Decompression | `TestDecompressFile_Corrupt` | Attempts to decompress corrupt compressed archive. | **PASS**: Fails with decompression error; does not panic. |
| Decompression | `TestIsCompressed` | Checks file extension compression detection logic. | **PASS**: Returns true for `.gz`, `.zst`, `.xz`, `.bz2`; false for `.sqlite`, `.xml`. |
| Repomd Parser | `TestParseRepomd_Valid` | Parses valid `repomd.xml` locating primary and other sqlite records. | **PASS**: Extracts database locations and revision timestamp. |
| Repomd Parser | `TestParseRepomd_PathTraversal` | Injects `../../evil.sqlite` into `repomd.xml` location path. | **PASS**: Detects traversal attempt and rejects with security error. |
| Repomd Parser | `TestParseRepomd_UnsupportedVersion` | Injects `<repomd version="2.0">`. | **PASS**: Rejects unsupported metadata version with descriptive error. |
| Repomd Parser | `TestParseRepomd_MissingPrimaryOrOther` | Validates error when mandatory `primary_db` or `other_db` is missing. | **PASS**: Returns explicit error describing missing database metadata. |
| RPM Header Reader | `TestReadRPMDetails` | Reads package headers directly from an on-disk RPM file. | **PASS**: Extracts description, scriptlets, file lists, and package dependencies. |
| RPM Header Reader | `TestEnrichPackagesWithRPMDetails` | Enriches package structs with on-disk RPM header information. | **PASS**: Populates fields in batch; gracefully handles missing files. |
| SQLite Engine | `TestRepositoryAccess_Lifecycle` | Tests SQLite connection, package queries, changelog batching, dependency queries. | **PASS**: Queries return accurately; connections closed cleanly without leak. |
| SQLite Engine | `TestCleanAuthor` | Tests changelog author cleaning regex. | **PASS**: Strips excess brackets, email wrappers, and invalid characters. |

### Cache State Management & Concurrency (`internal/state`)

| Logical Group | Test Name | Technical Purpose / Description | Success Criteria (PASS/FAIL) |
| :--- | :--- | :--- | :--- |
| State Cache | `TestStateStoreAtomicSave` | Validates hash comparison, dirty tracking, and atomic file replacement. | **PASS**: HasChanged returns true for new/altered files, false for identical; saves atomically via temp file. |
| Concurrency | `TestStateStoreConcurrency` | Launches 50 concurrent goroutines querying and modifying state cache. | **PASS**: Zero data races detected under `go test -race`; all writes synchronized. |
| Cache Pruning | `TestStateStore_StaleFiles_And_Remove` | Detects stale files present in state cache but absent from current run. | **PASS**: Accurately reports stale filenames and removes unreferenced entries. |
| Fault Recovery | `TestStateStore_CorruptJSON_Recovery` | Tests loading an empty or malformed `.state.json` file. | **PASS**: Gracefully recovers by initializing empty state store; does not fail execution. |

### Formatting & Sanitization Utilities (`internal/util`)

| Logical Group | Test Name | Technical Purpose / Description | Success Criteria (PASS/FAIL) |
| :--- | :--- | :--- | :--- |
| Utilities | `TestHumanSize` | Formats byte counts into human-readable strings (B, KB, MB, GB). | **PASS**: Formats values accurately (e.g. `1048576` -> `"1.0 MB"`). |
| Utilities | `TestRSSTime` | Formats Unix timestamps into RFC 822 / RFC 1123 RSS date strings. | **PASS**: Produces valid RSS date format (e.g. `"Mon, 02 Jan 2006 15:04:05 MST"`). |
| Utilities | `TestFirstLetter` | Extracts normalized first letter key for alphabetical grouping. | **PASS**: Returns uppercase letter `A-Z` or `#` for numeric/symbol characters. |
| Security | `TestSanitizeFilename` | Sanitizes package names into safe filesystem filenames. | **PASS**: Strips directory separators, null bytes, and dangerous characters. |

### Live End-to-End Integration Suite (`tests/integration`)

| Logical Group | Test Name | Technical Purpose / Description | Success Criteria (PASS/FAIL) |
| :--- | :--- | :--- | :--- |
| E2E / Live Repo | `TestLive_EndToEndWorkflow/Live_IndexHTML` | Runs generator against real repository (2,036 RPMs) and verifies `index.html`. | **PASS**: Generation completes without error; `index.html` exists and exceeds 10 KB. |
| E2E / Live Server | `TestLive_EndToEndWorkflow/Live_SearchJSON` | Boots ephemeral HTTP server and verifies `/search.json` endpoint over HTTP. | **PASS**: HTTP GET returns 200 OK, `application/json` MIME type, parses valid package list. |
| E2E / Live Server | `TestLive_EndToEndWorkflow/Live_PackagePage` | Verifies real package page endpoint (e.g. `/nginx.html`) over live HTTP server. | **PASS**: HTTP GET returns 200 OK, valid HTML5 structure, contains package summary. |
| E2E / Live Server | `TestLive_EndToEndWorkflow/Live_RSSFeed` | Verifies `/latest-feed.xml` endpoint over live HTTP server. | **PASS**: HTTP GET returns 200 OK, contains `<rss version="2.0">`. |
| E2E / Incremental | `TestLive_EndToEndWorkflow/Live_IncrementalRun` | Performs secondary execution against identical repo to verify state caching. | **PASS**: Secondary pass succeeds; file timestamps indicate unchanged files skipped. |
| E2E / Subprocess | `TestLive_SubprocessCLI` | Compiles real `repoview` binary and executes CLI command via `os/exec`. | **PASS**: Binary builds, CLI runs with `--repo` and `--output-dir`, exits with code 0. |

---

## 6. Code Coverage Report

### Current Coverage Statistics

RepoView-Go enforces an architectural quality standard where **every package must exceed 80% statement coverage**.

| Package | Purpose | Statements Covered | Percentage | Status |
| :--- | :--- | :---: | :---: | :---: |
| `cmd/repoview` | CLI Entrypoint, flags, options, exit codes | 41 / 47 | **87.2%** | ✅ PASS (>80%) |
| `internal/app` | Core generator, pipeline workflow, safety checks | 182 / 216 | **84.3%** | ✅ PASS (>80%) |
| `internal/logic` | EVR sorting, filtering, comps & RPM grouping | 196 / 240 | **81.7%** | ✅ PASS (>80%) |
| `internal/models` | Domain models, XML unmarshaling, search index | 47 / 51 | **92.2%** | ✅ PASS (>80%) |
| `internal/render` | HTML/RSS template rendering, asset delivery | 147 / 178 | **82.6%** | ✅ PASS (>80%) |
| `internal/repo` | Repomd, comps, RPM headers, SQLite access | 200 / 247 | **81.0%** | ✅ PASS (>80%) |
| `internal/state` | Persistent state cache, dirty tracking, pruning | 55 / 64 | **85.9%** | ✅ PASS (>80%) |
| `internal/util` | Human formatting, date conversions, sanitization | 24 / 27 | **88.9%** | ✅ PASS (>80%) |
| **Total Codebase** | **Complete Project Statement Coverage** | **892 / 1070** | **83.5%** | **✅ PASS (>80%)** |

> [!NOTE]
> All unit tests execute in under **0.2 seconds** aggregate time, ensuring developer productivity remains unhindered.

### How to Measure and Refresh Statistics

To measure code coverage and refresh the statistics table:

```bash
# 1. Run coverage across all unit test packages and generate profile
go test -coverprofile=/tmp/coverage.out ./...

# 2. View coverage breakdown by function and package
go tool cover -func=/tmp/coverage.out

# 3. View total percentage summary
go tool cover -func=/tmp/coverage.out | grep "total:"

# 4. (Optional) Open interactive browser heatmap to inspect uncovered lines
go tool cover -html=/tmp/coverage.out
```

---

## 7. Realistic Data Simulation

In accordance with strict production requirements, dependencies in the integration tier are **never mocked**. Integration tests validate 100% of functionality against real production datasets and network sockets.

### Live Repository Verification

- **Real Production Dataset**: Executed against `/u01/wwwroot/test/el9/base/x86_64` containing **2,036 real RPM packages**.
- **Real SQLite Databases**: Directly queries `repodata/*-primary.sqlite` and `repodata/*-other.sqlite`.
- **Real Decompression**: Executes actual streaming decompression (`gzip`, `zstd`, `xz`) on live metadata archives.
- **Header Parsing**: Inspects authentic RPM headers on disk to parse scriptlets, requires, provides, and changelogs.

### Live HTTP Server & Endpoint Listeners

Rather than merely verifying that files exist on disk, `tests/integration/e2e_test.go` spins up a live HTTP server:

```go
// Starts an authentic net/http listener on an ephemeral loopback port
server := &http.Server{Handler: http.FileServer(http.Dir(outputDir))}
listener, err := net.Listen("tcp", "127.0.0.1:0")
```

The test acts as an authentic HTTP client issuing network requests to `http://127.0.0.1:{port}/`:
1. `GET /index.html` — Validates HTTP 200 OK, HTML5 doctype, and repo header content.
2. `GET /search.json` — Validates `application/json` Content-Type, unmarshals search index records, and asserts presence of known packages.
3. `GET /nginx.html` — Validates individual package detail page rendering, EVR display, changelogs, dependencies, and scriptlets.
4. `GET /latest-feed.xml` — Validates RSS 2.0 XML schema and item elements.

### CLI Subprocess Binary Execution

The integration test suite also verifies the compiled binary in an isolated subprocess:
1. Compiles the binary using `go build -o /tmp/repoview ./cmd/repoview`.
2. Executes the binary with arguments:
   ```bash
   /tmp/repoview --repo /u01/wwwroot/test/el9/base/x86_64 --output-dir <t.TempDir> --verbose
   ```
3. Asserts return code `0` and verifies all static assets and HTML pages are produced.

---

## 8. How to Run the Tests

### Bash (Linux / macOS / WSL)

```bash
# -------------------------------------------------------------
# 1. Fast Unit Tests (Runs all packages in <0.2s)
# -------------------------------------------------------------
go test -v ./...

# -------------------------------------------------------------
# 2. Concurrency & Race Detector
# -------------------------------------------------------------
go test -race -v ./...

# -------------------------------------------------------------
# 3. Generate Coverage Report & Validate 80%+ Requirement
# -------------------------------------------------------------
go test -coverprofile=/tmp/coverage.out ./...
go tool cover -func=/tmp/coverage.out

# -------------------------------------------------------------
# 4. Live E2E Integration Tests (Requires real repo)
# -------------------------------------------------------------
# Point to custom repository if not in default location:
export REPOVIEW_TEST_REPO="/u01/wwwroot/test/el9/base/x86_64"

go test -tags=integration -v ./tests/integration/...

# -------------------------------------------------------------
# 5. Run Everything (Unit + Integration + Race)
# -------------------------------------------------------------
go test -tags=integration -race -v ./...
```

### PowerShell (Windows native / WSL bridge)

```powershell
# -------------------------------------------------------------
# 1. Fast Unit Tests (via WSL bridge)
# -------------------------------------------------------------
wsl go test -v ./...

# -------------------------------------------------------------
# 2. Concurrency & Race Detector (via WSL bridge)
# -------------------------------------------------------------
wsl go test -race -v ./...

# -------------------------------------------------------------
# 3. Generate Coverage Report (via WSL bridge)
# -------------------------------------------------------------
wsl go test -coverprofile=/tmp/coverage.out ./...
wsl go tool cover -func=/tmp/coverage.out

# -------------------------------------------------------------
# 4. Live E2E Integration Tests (via WSL bridge)
# -------------------------------------------------------------
$env:REPOVIEW_TEST_REPO="/u01/wwwroot/test/el9/base/x86_64"
wsl go test -tags=integration -v ./tests/integration/...

# -------------------------------------------------------------
# 5. Native Windows Execution (Requires MinGW-w64 GCC installed)
# -------------------------------------------------------------
$env:CGO_ENABLED="1"
go test -v ./...
```

---

## 9. Maintenance and Troubleshooting

### Common Issues & Resolutions

| Issue / Symptom | Root Cause | Solution |
| :--- | :--- | :--- |
| `Binary 'gcc' not found in $PATH` / `CGO_ENABLED=0` | SQLite3 driver requires CGo compilation. | Ensure `gcc` or `clang` is installed (`apt install build-essential` or install MinGW-w64 on Windows). Ensure `CGO_ENABLED=1`. |
| `Integration test skipped: repo path does not exist` | Default test repository `/u01/wwwroot/test/el9/base/x86_64` is absent. | Set `export REPOVIEW_TEST_REPO="/path/to/any/valid/rpm/repo"` before executing `go test -tags=integration`. |
| `bind: address already in use` in integration tests | Fixed port collisions when starting test HTTP servers. | Tests must bind to `127.0.0.1:0` to allow the kernel to allocate an ephemeral free port. |
| Test leaves stray `.state.json` or files | Test wrote to working tree instead of temp dir. | Strictly use `t.TempDir()`. Never hardcode relative output paths like `"./output"` in tests. |
| Package coverage drops below 80% | New code added without accompanying unit tests. | Run `go tool cover -html=/tmp/coverage.out` to view red (uncovered) blocks. Add targeted unit tests for error branches and edge cases. |

### Rule for Code Modifications

Whenever source code is modified or new features are introduced:
1. Ensure new tests are added covering positive, negative, and edge paths.
2. Verify that all tests pass cleanly: `go test -race ./...`.
3. Refresh the coverage profile: `go test -coverprofile=/tmp/coverage.out ./...`.
4. Update the **Current Coverage Statistics** table and **List of Tests** table in this document (`TESTING.md`) before submitting changes.
