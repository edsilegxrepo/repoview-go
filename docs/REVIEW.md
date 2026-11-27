# RepoView-Go Comprehensive Code Review & Remediation Plan

**Review Date:** 2026-11-26  
**Scope:** Entire codebase (`cmd/repoview`, `internal/*`, `tests/integration/*`, templates, JS/CSS assets, build/lint configs, dependencies, and docs)  
**Target Environments:** Linux and Windows (`amd64` / `arm64`, air-gapped and connected)  
**Status:** **PLAN ONLY — awaiting approval before implementation**

---

## Table of Contents

1. [Executive Summary & Verification Baseline](#1-executive-summary--verification-baseline)
2. [Detailed Findings](#2-detailed-findings)
   - [2.1 Security & Supply Chain](#21-security--supply-chain)
   - [2.2 Logic, Concurrency, Memory & Data Loss Risks](#22-logic-concurrency-memory--data-loss-risks)
   - [2.3 Performance, Scalability & Resource Utilization](#23-performance-scalability--resource-utilization)
   - [2.4 Cross-Platform Support (Windows & Linux)](#24-cross-platform-support-windows--linux)
   - [2.5 Modularity, Maintainability, DRY & Code Hygiene](#25-modularity-maintainability-dry--code-hygiene)
   - [2.6 Granular Exit Codes & Diagnostics](#26-granular-exit-codes--diagnostics)
   - [2.7 Workflow, Repository Discovery, Declarative Structure & Reusability](#27-workflow-repository-discovery-declarative-structure--reusability)
   - [2.8 RPM & DEB Metadata Utilization Audit (Rich Display & Search)](#28-rpm--deb-metadata-utilization-audit-rich-display--search)
3. [Prioritized Recommendations Matrix](#3-prioritized-recommendations-matrix)
4. [Phased Implementation Plan & Checklist](#4-phased-implementation-plan--checklist)
5. [Approval Gate](#5-approval-gate)

---

## 1. Executive Summary & Verification Baseline

RepoView-Go has a strong architectural foundation: clean package boundaries (`app`, `portal`, `repo`, `logic`, `models`, `render`, `state`, `util`), air-gap compliant embedded assets (`embed.FS`), bounded worker pools for parallel generation, and zero known dependency CVEs.

However, deep static analysis, cross-platform test execution on Windows, and line-by-line inspection uncovered **critical memory/performance defects, data-loss edge cases, security sanitization gaps, Windows test failures, silent error swallowing, DRY violations, and fragile exit code mapping**.

### Observed Tooling & Test Results (Baseline on `go1.27.4 windows/amd64`, `CGO_ENABLED=1`)

| Check | Outcome | Key Observations |
| :--- | :--- | :--- |
| `go test -cover ./...` | **FAIL (2 pkg)** | `internal/util` (`TestPermissions_ConstantsAndHelpers`) and `internal/render` (`TestTemplateRendering`) fail on Windows due to POSIX mode assertions (`0777` vs `0755`, `0666` vs `0644`). 3 symlink tests in `internal/portal` skipped on Windows due to symlink privilege. |
| `go vet ./...` | **PASS** | 0 issues reported. |
| `staticcheck ./...` | **PASS** | 0 issues reported. |
| `govulncheck ./...` | **PASS** | `No vulnerabilities found.` |
| `gosec ./...` | **5 issues** | 5 medium-severity CWE-276 findings in `internal/util/perm.go` (`L25, L28, L39`) and `internal/state/state.go` (`L113, L117`). |
| `golangci-lint` (default) | **PASS** | 0 issues with default ruleset. |
| `golangci-lint` (extended: `unused,unparam,rowserrcheck,sqlclosecheck,nilerr,gocritic,dupl,revive`) | **33 issues** | 3 `rowserrcheck`, 3 `sqlclosecheck`, 1 `nilerr`, 4 `unparam`, 4 `gocritic`, 2 `dupl`, 16 `revive` (including unused parameters and stuttering names). |
| `biome check .` | **FAIL (3 errors, 1 info)** | `biome.json` schema mismatch (`2.5.9` vs `2.6.9`); `build.json` has duplicate `"entrypoint"` key and formatting drift; `internal/render/templates/layout/repostyle.css` formatting drift at `L670`. |

### Observed Statement Coverage by Package

| Package | Coverage | Notes |
| :--- | :---: | :--- |
| `cmd/repoview` | 84.8% | `run()` and `runPortal()` well covered; error-mapping branches partially covered. |
| `internal/app` | 82.5% | `setupRenderer` error/panic paths and `renderPackages` failure paths untested. |
| `internal/logic` | 76.0% | Below the documented 80% target (`README.md`). |
| `internal/models` | 92.0% | Strong coverage. |
| `internal/portal` | 73.7% | Below the documented 80% target (symlink tests skipped on Windows). |
| `internal/render` | 77.2% (FAIL) | Fails on Windows permission assertions. |
| `internal/repo` | 64.4% | Lowest coverage; `rpm_reader.go` skips when `/u01/wwwroot/...` is absent. |
| `internal/repo/deb` | 87.6% | Strong coverage with synthetic `.deb` fixtures. |
| `internal/state` | 81.0% | Good coverage. |
| `internal/util` | 93.6% (FAIL) | Fails on Windows permission assertions. |

---

## 2. Detailed Findings

### 2.1 Security & Supply Chain

#### [SEC-01] Critical — Unescaped XML/CDATA Injection in `rss.xml` via `text/template`
- **Locations:** `internal/render/renderer.go:38,167-183,249-265`, `internal/render/templates/rss.xml:4-45`
- **Details:** `rss.xml` is compiled with `text/template` (not `encoding/xml` or HTML/XML escaping) to preserve `<![CDATA[...]]>`. All package and repository fields (`Repo.Title`, `URL`, `Name`, `Summary`, `Description`, `LocationHref`, `Changelog.Author`, `Changelog.Changelog`) are emitted raw.
  - A malicious package `Description`, `Summary`, or `Changelog` containing `]]>` closes the `<![CDATA[...]]>` block early, enabling **arbitrary XML element injection and stored XSS** when rendered by feed readers or browsers.
  - Unescaped `<`, `&`, or `"` in `<title>`, `<link>`, `<guid>`, or `<a href="...">` inside CDATA breaks XML validity or injects HTML attributes/scripts.
- **Recommendation:** Either construct the RSS feed using `encoding/xml` (matching `internal/portal/renderer.go:221-229`) or register strict XML-escaping and CDATA-splitting (`]]>` -> `]]]]><![CDATA[>`) + HTML-escaping template functions in `XMLTmpls`.

#### [SEC-02] High — Command / INI Stanza Injection in Generated Copyable Commands & Repo Snippets
- **Locations:** `internal/render/templates/package.html:182-200,634-635`, `internal/render/templates/layout/search.js:443-471`, `internal/render/templates/index.html:287-326`, `internal/portal/types.go:170-193`
- **Details:**
  - `package.html` and `search.js` build shell commands (`sudo dnf install <pkg>`, `sudo apt install <pkg>`, `sudo dpkg -i <file>`, `wget <url>`, `curl -O <url>`, `rpm -vK <archive>`) directly from `Pkg.Name`, `Pkg.LocationHref`, and `ArchiveFilename()`. If an untrusted repository contains a crafted `Name` or `LocationHref` (e.g., containing `;`, `` ` ``, `$(...)`, newlines, or spaces), copying and pasting the command executes unintended shell commands.
  - `index.html` and `portal.types.go` interpolate `Repo.Title`, `Repo.Channel`, `Repo.BaseURL`, `Suite`, and `Components` raw into `.repo` (INI) and `.sources` (Deb822) stanzas and `sudo tee ... << 'EOF'` commands. Newlines in `Title` or `BaseURL` allow INI/Deb822 directive injection (e.g., injecting `gpgcheck=0` or `proxy=`).
  - Client snippets hardcode **`gpgcheck=0`** (`index.html:296,324`, `portal/types.go:192`) and **`Trusted: yes`** (`index.html:290,316`), disabling cryptographic signature verification on client hosts by default.
- **Recommendation:** Strip/reject control characters and newlines from metadata used in snippets; shell-quote arguments (`'...'` single-quote escaping) in `search.js` and `package.html`; validate package names/hrefs against strict allowlists; and consider opt-in `--gpgkey` / `gpgcheck=1` support instead of unconditionally disabling GPG verification.

#### [SEC-03] High — Symlink Traversal Gaps & Missing `os.Root` / `EvalSymlinks` Containment
- **Locations:**
  - `internal/repo/repomd.go:69-78` (`ParseRepomd`)
  - `internal/repo/rpm_reader.go:191-194` (`EnrichPackagesWithRPMDetails`)
  - `internal/repo/sqlite.go:437-440` (`ReadPackageFiles`)
  - `internal/repo/deb/repository.go:208-212` (`resolveDebPath`)
  - `internal/render/renderer.go:295-298,361-379` (`WriteToFile`, `WriteAssets`)
  - `internal/app/generator.go:812-850` (`validateOutputDirSafety`)
- **Details:**
  1. Path containment checks use `filepath.Rel(base, target)` and `strings.HasPrefix(rel, "..")` without resolving symlinks (`filepath.EvalSymlinks`). A symlink inside `repodata/`, `Packages/`, or `TemplateDir/layout/` pointing outside the root (e.g., `/etc/passwd` or a parent directory) bypasses `filepath.Rel` and is opened/read or overwritten.
  2. In `validateOutputDirSafety()`, if `outputDir` is a symlink to `repoDir` (or an ancestor of `repoDir`), `filepath.Abs` sees distinct paths, allowing `--force` (`os.RemoveAll`) to **delete the source repository**.
  3. `strings.HasPrefix(rel, "..")` falsely rejects legitimate filenames starting with `..` (e.g., `..foo`), whereas `filepath.IsLocal(rel)` (or Go 1.24+ `os.Root`) is exact. On Windows, `ParseRepomd:76` checks `strings.HasPrefix(rel, "/")` but omits `filepath.IsAbs(rel)` (missing drive-letter paths like `C:\...` when `repodir` is relative).
- **Recommendation:** Centralize path containment in a shared `internal/util` helper using `filepath.IsLocal` + `filepath.EvalSymlinks` (resolving existing ancestor directories for non-existent target files) or Go 1.24+ `os.Root` where applicable.

#### [SEC-04] High — Missing Cryptographic Checksum Verification for Repository Metadata
- **Locations:** `internal/repo/repomd.go:64-95`, `internal/models/repomd.go:61-63`, `internal/repo/decompress.go:42`, `internal/repo/deb/repository.go:52-66`
- **Details:** `models.Data` parses `<checksum type="sha256">...</checksum>` and `<size>` from `repomd.xml`, and Debian `Release`/`InRelease` files contain SHA256 digests for `Packages*` files, yet **neither RPM nor DEB ingestion verifies file digests or expected sizes** before decompressing and opening SQLite/Packages files. Tampered or truncated metadata on a mirror goes undetected.
- **Recommendation:** Verify `repomd.xml` checksums (`sha256`, `sha512`, and legacy `sha1` with warning) during `ParseRepomd`/decompression, and optionally verify `Release` SHA256 hashes for Debian `Packages*` indices.

#### [SEC-05] High — Unbounded Reads & Decompression Bomb Vectors in Archive/Metadata Parsers
- **Locations:**
  - `internal/repo/deb/details.go:104`: `content, _ := io.ReadAll(archive)` reads `control.tar` scriptlets with **no size cap**.
  - `internal/repo/deb/changelog.go:48-57` and `internal/repo/deb/details.go:170-180`: `gzip.NewReader` reads `changelog.Debian.gz` with **no `io.LimitReader`**.
  - `internal/portal/renderer.go:254`: `os.ReadFile` reads child `latest-feed.xml` unbounded.
  - `internal/portal/config.go:65`: `os.ReadFile` reads `portal.yaml` unbounded.
  - `internal/repo/decompress.go:36,95`: `MaxDecompressedSize` (4 GiB) is written to `os.TempDir()`, which on Linux systems with `tmpfs` `/tmp` (or systemd `PrivateTmp=true`) can exhaust RAM.
- **Recommendation:** Bound `io.ReadAll` calls for scriptlets (e.g., 1 MiB), changelogs (e.g., 2 MiB), `portal.yaml` (e.g., 2 MiB), and child RSS feeds (e.g., 5 MiB) via `io.LimitReader`; make `MaxDecompressedSize` configurable or tighten default for XML/Packages files.

#### [SEC-06] Medium — SQLite Untrusted Input Hardening (`ATTACH` Mode & `trusted_schema`)
- **Locations:** `internal/repo/sqlite.go:51-75`
- **Details:**
  - `NewRepositoryAccess` opens `primaryPath` with `?mode=ro&cache=shared&_busy_timeout=5000` without a `file:` URI prefix or query-escaping (`?` or `#` in directory names can break DSN parsing).
  - `ATTACH DATABASE ? AS other` attaches `otherPath` without URI `mode=ro`, and neither `PRAGMA query_only = ON` nor `PRAGMA trusted_schema = OFF` is executed. A maliciously crafted `primary.sqlite` containing malicious triggers, views, or virtual tables could attempt unintended writes or schema side effects on `other.sqlite`.
- **Recommendation:** Construct proper `file:` URIs with `url.PathEscape` / `url.Values` for both `primary` and `ATTACH`, and enforce `PRAGMA query_only = ON; PRAGMA trusted_schema = OFF;`.

#### [SEC-07] Medium — `gosec` CWE-276 Permission Findings & `state.json` Web Exposure
- **Locations:** `internal/util/perm.go:25,28,39`, `internal/state/state.go:113,117`, `internal/app/generator.go:380-392`
- **Details:**
  - `gosec` flags 5 CWE-276 issues on `os.MkdirAll`, `os.Chmod`, and `os.OpenFile` with `0755`/`0644`. While public HTML/CSS/JS files require `0755`/`0644` (already annotated on `WriteWebFile`'s `WriteFile`), `EnsureDir`'s `os.MkdirAll`/`os.Chmod` and `WriteWebFile`'s `os.Chmod` lack `#nosec` justifications.
  - More importantly, when `--state-dir` is omitted, `state.json` is written inside `OutputDir` (`generator.go:380-381`) with `0644` permissions. Under the recommended Nginx config (`README.md:530`, `location ~* \.(json|xml)$`), `state.json` is publicly downloadable from the web server, leaking build metadata and file inventories.
- **Recommendation:** Move default `state.json` outside web-served root or restrict its permissions (`0600` when `--state-dir` is separate) and document Nginx `location ~* state\.json$ { deny all; }` rule; add targeted `#nosec` annotations for intentional web-readable assets.

#### [SEC-08] Medium — URL Scheme Validation & External Link Hardening
- **Locations:** `internal/render/templates/package.html:42,216,674`, `internal/render/templates/index.html:49`, `internal/render/templates/group.html:42`, `internal/portal/templates/portal.html:595`, `internal/render/templates/layout/search.js:252`
- **Details:**
  - `portal.html:595`, `package.html:42`, `index.html:49`, `group.html:42` use `target="_blank"` without `rel="noopener noreferrer"` on RSS links.
  - `search.js:252` uses `encodeURI(filename || "").replace(/["'<>]/g, "")`; enforcing `/^[a-zA-Z0-9._-]+\.html$/` ensures `search.json` tampering cannot inject relative path jumps or unexpected schemes.
  - `portal.yaml` `external_repos` URLs and `--url`/`--baseurl` accept unvalidated strings; validating `http://`/`https://` (or safe relative paths) prevents malformed configs and warns on plaintext `http://` when appropriate.

#### [SEC-09] Medium — Dependency & Build Configuration Hygiene
- **Locations:** `go.mod`, `build.json:6-7,18`
- **Details:**
  - `github.com/xi2/xz v0.0.0-20171230120015-48954b6210f8` (indirect dependency of `go-rpmutils`) has been unmaintained since 2017; `repoview` already depends directly on `github.com/ulikunitz/xz v0.5.17`.
  - `github.com/mattn/go-sqlite3 v1.14.53` requires **CGo** (`gcc`/`musl-dev`), which directly contradicts `build.json:18` (`"cgo": false`) and complicates static cross-compilation between Linux and Windows.
  - `gopkg.in/yaml.v3 v3.0.1` is archived upstream (consider migrating to maintained `go.yaml.in/yaml/v3` or `sigs.k8s.io/yaml`).
  - `build.json:6-7` has duplicate `"entrypoint"` keys (`"."` and `"./cmd/repoview"`), causing a Biome `noDuplicateObjectKeys` lint error.
- **Recommendation:** Fix `build.json` (`cgo` alignment + remove duplicate `entrypoint`), evaluate pure-Go SQLite (`modernc.org/sqlite` or `github.com/ncruces/go-sqlite3` WASM) vs documented CGo requirement, and plan migration away from `gopkg.in/yaml.v3`.

#### [SEC-10] Info — Encryption in Transit & Secrets Posture
- **Details:** Confirmed by full codebase review: `repoview-go` performs zero outbound network requests, handles no passwords, API keys, or private signing keys, and does not embed credentials. Encryption in transit applies solely to the serving reverse proxy (documented in `README.md` / `ARCHITECTURE.md`) and user-configured URLs (`--url`, `--baseurl`, `--portal-url`, `--repo-url`).

---

### 2.2 Logic, Concurrency, Memory & Data Loss Risks

#### [LOG-01] Critical — Memory Bloat Bug Defeats "On-Demand File Loading"
- **Locations:** `internal/repo/rpm_reader.go:100-113`, `internal/app/generator.go:147-149,515-531`
- **Details:**
  - `Generator.Run()` calls `reader.EnrichPackageDetails(g.config.RepoDir, allPkgs)` (`generator.go:148`), which invokes `ReadRPMDetails()` on **every RPM version of every package** in the repository.
  - Inside `ReadRPMDetails()` (`rpm_reader.go:100-113`), `header.GetFiles()` is called eagerly and stored in `details.Files` for **every package**, holding millions of `PackageFile` structs in heap memory simultaneously.
  - Then, in `renderSinglePackage()` (`generator.go:517`), `if latest.Details == nil || len(latest.Details.Files) == 0` is **FALSE** for every non-empty RPM because `EnrichPackageDetails` already populated `Files`! Consequently, the `defer func() { latest.Details.Files = nil }()` on `L524-528` **never executes**, and `versions[1:]` also retain their `Files` slices forever!
- **Recommendation:** Remove `header.GetFiles()` from `ReadRPMDetails()` completely so `EnrichPackageDetails` loads only scalar metadata, signatures, and scriptlets; load `Files` exclusively on-demand in `renderSinglePackage()` via `ReadPackageFiles()` and unconditionally nil `latest.Details.Files = nil` in a `defer` (plus ensure `AllVersions[1:]` never populate `Files`).

#### [LOG-02] Critical — Nil Pointer Panic Risk on `StateStore` Open Failure
- **Locations:** `internal/app/generator.go:397-400,424,548,586,610,623,639,858`, `internal/state/state.go:62-65`
- **Details:** When `os.Open` fails in `state.NewStateStore(statePath)` (e.g., permission denied on existing `state.json`), it returns `(nil, err)`. In `setupRenderer` (`generator.go:397-400`), `Generator` logs a warning and **continues with `stateStore == nil`**, immediately panicking (`nil pointer dereference`) on `stateStore.HasChanged(...)` at `L424`.
- **Recommendation:** Either fail fast and return the error (as `ExitIOError`) or return a valid empty/fallback `StateStore` with an explicit warning.

#### [LOG-03] Critical — Data Loss Risk with `--force` Prior to Validation/Rendering
- **Locations:** `internal/app/generator.go:370-374,402-409`
- **Details:** `setupRenderer` executes `os.RemoveAll(g.config.OutputDir)` at `L372` **before**:
  1. `render.NewRenderer` parses custom templates (`L402`, which can fail with `ExitTemplateError`),
  2. `WriteAssets()` succeeds (`L407`), and
  3. Any HTML pages are rendered.
  If `--template-dir` has a template syntax error or disk write fails, the entire pre-existing published static site (and `state.json` if `StateDir == OutputDir`) is permanently wiped! Additionally, `os.RemoveAll` error is discarded (`_ = os.RemoveAll(...)`).
- **Recommendation:** Do not wipe `OutputDir` up front. Parse templates first, bypass `stateStore.HasChanged` when `g.config.Force` is true (`force` already rewrites all generated files atomically), and prune removed files via `cleanupStale` at the end only if generation succeeded.

#### [LOG-04] High — Incremental `StateStore` Corruption on Failed Disk Writes
- **Locations:** `internal/app/generator.go:218-233,424-431,586-593,623-630`, `internal/state/state.go:83-98`
- **Details:**
  - `stateStore.HasChanged(filename, content)` mutates `s.state[filename] = hash` **before** `renderer.WriteToFile(filename, content)` is called.
  - In `renderGroups` (`L427-429`), `renderIndices` (`L589-591`, `L626-628`), and `renderDescriptor` (`L219`), if `WriteToFile` fails (disk full, permissions), the error is only logged, `filename` is still added to `generatedFiles`, `stateStore.Save()` persists the new hash as if the file was written, and `Generator.Run()` returns `nil` (`ExitSuccess`)! Subsequent incremental runs will think the missing/stale file is up to date and never regenerate it.
- **Recommendation:** Separate `NeedsWrite(filename, content) (hash string, changed bool)` from `MarkWritten(filename, hash)` so state is committed only after `WriteToFile` succeeds, and propagate write errors instead of swallowing them.

#### [LOG-05] High — Unchecked SQLite Errors, Missing `rows.Err()`, and Statement Leak Paths
- **Locations:** `internal/repo/sqlite.go:78-117,324-423`
- **Details:**
  - `GetPackageDependencies` (`L324-423`) checks `if err == nil` for `requires`, `provides`, `conflicts`, and `obsoletes` queries, silently ignoring query failures, `rows.Scan` failures, and `rows.Err()` (flagged by `rowserrcheck`). A corrupted SQLite DB silently produces packages with zero dependencies.
  - `NewRepositoryAccess` (`L78-117`) manually closes prepared statements in cascading `if err != nil` blocks without `defer` rollback on error (flagged by `sqlclosecheck`).
- **Recommendation:** Check `rows.Err()` and return errors from `GetPackageDependencies` (handling them in `renderSinglePackage` so `errorCount` increments); use a helper slice/loop or `defer` cleanup on partial initialization failure in `NewRepositoryAccess`.

#### [LOG-06] High — Missing Windows Fallback in `portal/renderer.go` `atomicWriteFile`
- **Location:** `internal/portal/renderer.go:405`
- **Details:** Unlike `internal/state/state.go:135-141` and `internal/render/renderer.go:334-340`, `portal.atomicWriteFile` calls bare `os.Rename(tmpPath, destPath)` without a retry/replace fallback if Windows returns an error replacing an existing file.
- **Recommendation:** Unify all three implementations into a single `util.AtomicWriteFile(destPath string, content []byte, perm os.FileMode) error` with consistent `Sync()`, `Close()`, `Chmod()`, and Windows fallback handling.

#### [LOG-07] High — Global Process State (`log.Printf`, `fmt.Printf`, `SetUmask`) Breaks Parallel Isolation
- **Locations:** `cmd/repoview/main.go:64`, `internal/app/generator.go:113-117,142,219,232,335,338,386,399,428,474,551,584,590,621,627`, `internal/repo/sqlite.go:246`
- **Details:**
  - `main.go:64` mutates process-wide POSIX `umask` inside `run()`, which is unsafe if `run()` is ever invoked concurrently in tests or embedded.
  - `generator.go` uses `fmt.Printf` (`say()` and directly at `L551`) and global `log.Printf` (`os.Stderr`), bypassing the `stdout`/`stderr` `io.Writer`s injected into `run()`. During `repoview portal --render-missing --workers 4`, concurrent `Generator` instances interleave logs directly to global `os.Stderr`.
- **Recommendation:** Add `Stdout` and `Stderr` (or a structured logger) to `app.Config` defaulting to `io.Discard`/`os.Stderr`, and apply `SetUmask` once in `main()`, not in `run()`.

#### [LOG-08] Medium — Silent `os.ReadDir` Error Swallowing in Portal Scanner
- **Location:** `internal/portal/scanner.go:111-114` (flagged by `nilerr`)
- **Details:** When `os.ReadDir(dirPath)` fails (e.g., `EACCES` permission denied on a mirror subdirectory), `crawl()` silently returns `nil` with no warning or diagnostic counter, potentially producing an incomplete portal with exit code `0`.
- **Recommendation:** Record non-fatal scan warnings (or collect `ScanWarnings []string`) and surface them to `stderr` unless `--quiet`.

#### [LOG-09] Medium — Grouping & Taxonomy Edge Cases
- **Locations:** `internal/logic/grouping.go:136-139,350-385,431`, `internal/app/generator.go:351-357`, `internal/repo/deb/discovery.go:88-159`
- **Details:**
  1. `BuildGroupTree` (`grouping.go:136-139`): If two standalone groups collide on trimmed `catName`, packages are merged into the first node, **dropping the second group's `Filename`** from the navigation sidebar.
  2. `GetLetterGroups` (`grouping.go:431`): Uses `rune(name[0])` (first UTF-8 byte) instead of `utf8.DecodeRuneInString` (inconsistent with `util.FirstLetter`).
  3. `generator.go:353`: Extracts letter tokens via `strings.Split(lg.Name, " ")`, coupling `app` to `"Letter X"` display string formatting.
  4. `deb.Discover` (`discovery.go:88-159`): Stops on the **first** suite in `dists/` and **first** architecture in `findTargetArch()`, silently ignoring additional suites (e.g., `bookworm` + `trixie`) or architectures (`amd64` + `arm64`) under the same root when running single-repo `repoview`.
  5. `index.html:196`: Displays `Latest Packages: {{ len .Latest }}` which caps at `30` (`generator.go:596-599`), misleadingly showing "30" instead of total package count on large repositories.
- **Recommendation:** Fix UTF-8 rune decoding and leaf collision handling; expose explicit `Letter` / total package count fields; document or add `--suite`/`--arch` flags for multi-suite `deb.Discover` roots.

#### [LOG-10] Medium — Debian Metadata & Portal Path Edge Cases
- **Locations:** `internal/repo/deb/repository.go:174-195`, `internal/portal/config.go:151-156`, `internal/portal/renderer.go:233-247`, `cmd/repoview/main.go:587-591`
- **Details:**
  - `DebRepository.ReadPackageFiles` mutates `pkg.Changelog` and `pkg.TimeBuild` only for `versions[0]` during `renderSinglePackage`, leaving `AllVersions[1:]` without changelogs.
  - `DebRepository` caches `depsMap` (`repository.go:22,92-94`) for all packages even though `p.Dependencies` is already attached to every `models.Package` by `PackageFromBinaryIndex`—duplicating dependency memory unnecessarily.
  - `portal/config.go:152` and `main.go:587` look up `portalCfg.Overrides[r.RelPath]` without normalizing Windows `\` keys in `portal.yaml` via `filepath.ToSlash`.
  - `portal/renderer.go:235`: When `repo.RelPath == "."` in detached `--portal-home` mode, `readChildFeedItems` checks `portalHome/latest-feed.xml` instead of `portalHome/repoview/latest-feed.xml` (where `main.go:535` wrote it).

---

### 2.3 Performance, Scalability & Resource Utilization

#### [PERF-01] Critical — `rpmutils.Verify(f, nil)` Hashes Entire RPM Payloads (Twice!)
- **Locations:** `internal/repo/rpm_reader.go:45,128`
- **Details:** `ReadRPMDetails` calls `rpmutils.Verify(f, nil)` (`L45`), which reads and hashes the **entire RPM CPIO payload** on disk (even with `nil` keyring) instead of just reading the header! Then `ReadRPMFiles` (`L128`) calls `rpmutils.Verify(f, nil)` **again**! On a repository with 20 GB of `.rpm` packages, repoview reads and checksum-hashes 20–40 GB of payload bytes unnecessarily.
- **Recommendation:** Use `rpmutils.ReadHeader(f)` in `ReadRPMFiles` (reads only the small RPM header at file offset 0, avoiding full-file I/O). Evaluate `ReadRPMDetails` to extract signature header tags via `ReadHeader` or single-pass header inspection when GPG keyring verification isn't configured (note: `rpmutils.Verify` with `nil` keyring still parses signature header digests; measure whether `ReadSignatureHeader` / `Verify` payload skip is supported, or perform `ReadRPMDetails` only once).

#### [PERF-02] High — Sequential `.deb` Double-Open Pass & Unnecessary `ReadDebTimestamp` I/O
- **Locations:** `internal/repo/deb/repository.go:86-90,156-158`
- **Details:** `GetAllPackages()` opens and reads the first 68 bytes of every `.deb` file **sequentially** (`L88`) while parsing `Packages`, and then `EnrichPackageDetails()` opens every `.deb` file **again** in parallel (`L160`).
- **Recommendation:** Remove `ReadDebTimestamp` from `GetAllPackages()`; read the 68-byte `ar` timestamp once inside `EnrichPackageDetails()` using the already-open file handle (or alongside `ReadDebDetails`).

#### [PERF-03] High — Repeated Grouping Maps & $O(N \log N)$ Version String Re-Parsing
- **Locations:** `internal/logic/grouping.go:229,332,415`, `internal/logic/sorting.go:52-113`, `internal/app/generator.go:650-658`
- **Details:**
  - `pkgVersions := make(map[string][]*models.Package)` is constructed and `SortPackagesByEVR` is executed redundantly across `getCompsGroups`/`getRpmGroups`, `GetLetterGroups`, and `organizePackages()`.
  - `CompareVersions` (`sorting.go:57-58`) calls `debversion.Parse(v1Str)` and `debversion.Parse(v2Str)` **inside every comparator invocation** during `sort.Slice` ($O(N \log N)$ allocations and parses instead of $O(N)$ pre-parsing). Same for `rpmver.NewVersion` in `CompareEVR`.
- **Recommendation:** Build and sort `pkgVersions` once and pass it to `GroupingService` (or expose it from `GroupingService`), and pre-parse/cache version keys when sorting multi-version slices.

#### [PERF-04] High — Unbounded Worker Pools & Sequential Bottlenecks
- **Locations:**
  - `internal/app/generator.go:448`: `numWorkers := runtime.NumCPU() * 2` has **no upper bound** (unlike `cmd/repoview/main.go:274` which clamps to 16). On 64–128 vCPU build hosts, this spawns 128–256 workers and SQLite connections simultaneously, risking FD exhaustion (`EMFILE`) and SQLite contention.
  - `internal/app/generator.go:415-434` (`renderGroups`): Group pages are rendered **sequentially** even in large repositories with hundreds of groups.
  - `internal/app/generator.go:268-277`: `primary.sqlite` and `other.sqlite` (and `comps.xml`) are decompressed **sequentially** instead of in parallel.
  - `internal/portal/scanner.go:374-403,744-795`: When `repoview.json` is absent, `search.json` is read from disk and `json.Unmarshal`ed **twice** (`detectArchFromSearchJSON` and `countPackagesFromSearchJSON`).
- **Recommendation:** Clamp worker pools (e.g., configurable `--workers`, bounded `1..32`), parallelize `renderGroups` and `primary`/`other` decompression, and parse `search.json` in a single pass.

#### [PERF-05] Medium — Unconditional `WriteAssets` & Pretty-Printed `state.json` Overhead
- **Locations:** `internal/app/generator.go:407`, `internal/render/renderer.go:348-405`, `internal/state/state.go:119-120`
- **Details:**
  - `WriteAssets()` unconditionally writes `repostyle.css` and `search.js` on every incremental run without checking `StateStore`, causing unnecessary disk writes and mtime churn.
  - `StateStore.Save()` uses `encoder.SetIndent("", "  ")`, inflating `state.json` size by ~40% on 50k+ package repos (and note `README.md:386,412` documents `repoview.state.json.gz`, which is not implemented).
- **Recommendation:** Track layout assets in `StateStore` (or check content hash before writing); optionally support compact or gzip-compressed state files (`state.json.gz`) for large repositories.

---

### 2.4 Cross-Platform Support (Windows & Linux)

#### [OS-01] High — Failing Unit & Integration Tests on Windows (POSIX Permission Checks)
- **Locations:**
  - `internal/util/perm_test.go:36-38,50-52` (**FAILING on Windows**)
  - `internal/render/renderer_test.go:147-149,161-163` (**FAILING on Windows**)
  - `tests/integration/e2e_test.go:98-108` (will fail on Windows)
  - `tests/integration/deb_e2e_test.go:245-255` (will fail on Windows)
- **Details:** On Windows, Go's `os.FileMode.Perm()` returns `0777` for directories and `0666` for non-read-only files because NTFS does not map POSIX `rwx` group/other bits. Running `go test ./...` on Windows fails out of the box. Additionally, `internal/util/perm.go:28,39` issues redundant `os.Chmod` syscalls on Windows.
- **Recommendation:** Split OS permission enforcement/testing cleanly (`perm_unix.go` / `perm_windows.go` or `runtime.GOOS != "windows"` guards in POSIX mode assertions) while preserving strict `0755`/`0644` enforcement and verification on Linux/POSIX.

#### [OS-02] High — Windows Symlink/Junction Cycle Guard Disabled (`scanner_windows.go`)
- **Locations:** `internal/portal/scanner_windows.go:1-11`, `internal/portal/scanner_posix.go:1-20`
- **Details:** `scanner_windows.go` is a stub returning `fileID{}, false`. On Windows, symlink/junction cycles and mirror aliases fall back purely to `filepath.EvalSymlinks` string equality, which is vulnerable to case differences (`C:\Repos` vs `c:\repos`), 8.3 short names, and NTFS junction points.
- **Recommendation:** Implement `getFileID` on Windows using `golang.org/x/sys/windows.GetFileInformationByHandle` (`VolumeSerialNumber`, `FileIndexHigh`, `FileIndexLow`), giving Windows identical mathematical cycle/alias protection to Linux `(dev, ino)` with zero new dependencies (`golang.org/x/sys` is already in `go.mod`).

#### [OS-03] Medium — Windows Filename Collisions, Reserved Device Names with Extensions & Integration Paths
- **Locations:** `internal/util/sanitize.go:58-65`, `tests/integration/e2e_test.go:257`, `tests/integration/portal_e2e_test.go:155,287`
- **Details:**
  - `SanitizeFilename` checks `upper` against `CON`, `PRN`, `AUX`, `NUL`, `COM1-9`, `LPT1-9`, which protects `CON` -> `_CON_.html`, but does not check the stem before the first dot (e.g., `CON.pkg` -> `CON.pkg.html`).
  - On case-insensitive filesystems (Windows NTFS, macOS APFS), packages differing only by case (e.g., `MySQL` and `mysql`, or group `Tools` and `tools`) map to the same filename and overwrite each other silently.
  - `tests/integration/e2e_test.go:257` and `portal_e2e_test.go:155,287` build binary to `repoview` without `.exe` on Windows, causing `exec.Command` to fail on Windows.

#### [OS-04] Medium — Linux & Windows OS-Specific Performance Optimizations Missing
- **Locations:** `internal/repo/sqlite.go:59-75`, `internal/portal/scanner.go:156`
- **Details:**
  - **SQLite PRAGMAs (Linux & Windows):** `NewRepositoryAccess` does not configure read-only performance PRAGMAs (`PRAGMA mmap_size = 268435456;` on 64-bit, `PRAGMA temp_store = MEMORY;`, `PRAGMA cache_size = -65536;`), leaving significant SQLite read throughput on the table.
  - **Directory Traversal Syscall Reduction:** `internal/portal/scanner.go:156` calls `os.Stat(childPath)` for every non-symlink directory even though `entry.Info()` (`os.DirEntry`) on Windows reuses `FindNextFile` metadata with zero extra syscalls, and on Linux `entry.Type()` already distinguishes symlinks from directories (`lstat` avoided).
  - **Linux `sendfile` / TempDir Awareness:** Decompression (`decompress.go:88`) writes multi-hundred-MB SQLite temp files to `os.TempDir()` (`/tmp`, often memory-backed `tmpfs` on Linux) without an option/env override for disk-backed scratch directories when `tmpfs` is small.

---

### 2.5 Modularity, Maintainability, DRY & Code Hygiene

#### [MOD-01] High — Heavy Code Duplication (DRY Violations) Across Core Packages
1. **Taxonomy Engine Duplicated (`internal/app/generator.go:1030-1185` vs `internal/portal/taxonomy.go:10-229`):** `knownArchitectures`, `detectArchFromPath`, `inferDistroAndChannel`, `detectPrimaryArch`, and distro prefix matching are copy-pasted between `app` and `portal` with subtle drift (`""` vs `"unknown"`, `"all"` vs `"x86_64"`). Should be consolidated into a single shared package (e.g., `internal/taxonomy` or `internal/logic`).
2. **Atomic File Writer Duplicated 3x (`internal/state/state.go:111-142`, `internal/render/renderer.go:307-341`, `internal/portal/renderer.go:377-405`):** Three divergent implementations of temp-write + chmod + sync + rename. Consolidate into `internal/util.AtomicWriteFile`.
3. **Worker Pool Boilerplate Duplicated 5x (`cmd/repoview/main.go`, `internal/app/generator.go`, `internal/repo/rpm_reader.go`, `internal/repo/deb/repository.go`, `internal/portal/renderer.go`):** Extract a generic helper `util.ForEachConcurrent[T](items []T, workers int, fn func(T))` or worker pool utility.
4. **SQLite Dependency Queries Duplicated 4x (`internal/repo/sqlite.go:328-420`):** Extract a parameterized `queryDeps(stmt *sql.Stmt, fallbackSQL string, pkgKey int64, hasPre bool)` helper.
5. **Debian Discovery Loop Duplicated 2x (`internal/repo/deb/discovery.go:115-143` vs `182-210`):** Extract `collectComponentPackages(suitePath string, entries []os.DirEntry, targetArch string)`.
6. **CLI `printOption` Duplicated 2x (`cmd/repoview/main.go:111-124` vs `301-313`):** Extract shared `printOption` helper.
7. **HTML Template Chrome Duplicated 3x (`index.html`, `group.html`, `package.html`):** Sidebar, theme bootstrap script, top-bar, and footer (~150 lines each) are duplicated across all 3 templates; extract Go template partials (`_sidebar.html`, `_head.html`).

#### [MOD-02] Medium — Interface Minimization & Coupling (`RepoReader`)
- **Locations:** `internal/repo/reader.go:6-24`, `internal/app/generator.go:243-296`, `internal/repo/deb/repository.go:109-125`
- **Details:**
  - `RepoReader` contains `EnrichPackagesWithChangelogs` (a permanent no-op for DEB) and `EnrichPackageDetails(repoDir, pkgs)` (passes `repoDir` despite the reader already knowing its root).
  - `RepositoryAccess` exposes `GetChangelogForPackage`, which isn't on `RepoReader`, while `DebRepository` implements a dead `GetChangelogForPackage` stub returning `nil, nil` (`pkgKey` unused).
  - `internal/app/generator.go` directly imports `internal/repo/deb` to instantiate `DebRepository`, breaking repository backend encapsulation. A `repo.Open(repoDir string, format models.RepoFormat)` factory would cleanly decouple `app` from concrete backends.

#### [MOD-03] Medium — Unused Code, Unused Parameters & Stuttering Symbols
- **Unused Type:** `portal.RepoRunner` (`internal/portal/types.go:15`) — completely unused.
- **Unused Methods / Aliases:** `DebRepository.GetChangelogForPackage` (`deb/repository.go:116`, unused `pkgKey`), `DebRepository.EnrichPackagesWithChangelogs` (`deb/repository.go:123`, unused `pkgs`), `Package.RPMFilename()` (`models/package.go:257`), `Package.VR()` (`models/package.go:228`, only used in test).
- **Unused Parameters (`unparam` / `revive`):**
  - `internal/app/generator.go:437`: `renderPackages` return value `error` is always `nil`.
  - `internal/portal/scanner.go:221`: `inspectDirectory` `rootDir` parameter is unused.
  - `internal/portal/scanner_windows.go:9`: `fi` unused (until implemented).
  - `internal/util/umask_windows.go:6`: `mask` unused.
  - `internal/app/generator_deb_test.go:49`: `createMockDebianRepository` second return value unused.
  - `internal/state/state_test.go:82`: `idx` unused.
- **Linter Hygiene (`gocritic` / `revive`):**
  - 3 `appendAssign` smells (`generator.go:156`, `config.go:123,203`) where `append(sliceA, sliceB...)` mutates `sliceA`'s backing array if capacity allows!
  - 7 stuttering exported types (`portal.PortalConfig`, `portal.PortalContext`, `deb.DebRepoLocations`, `deb.DebRepository`, `repo.RepoReader`, `repo.RepoLocations`, `state.StateStore`) — note: renaming requires coordinated updates across tests/callers.

#### [MOD-04] Medium — Message Formatting, Build Metadata & Documentation Inconsistencies
- **Console Formatting:** `generator.go:343` prints `" organizing groups..."` with an accidental leading space; `generator.go:551` calls raw `fmt.Printf` instead of `g.say()`; `main.go` writes to injected `stdout`/`stderr` while `generator.go` writes to `os.Stdout` and `log.Printf` (`os.Stderr` with timestamps).
- **Template Footer Links:** `index.html:135`, `group.html:128`, `package.html:133` link to `https://github.com/mshedsilegx/repoview-ng`, whereas `portal.html:802` links to `https://github.com/edsilegxrepo/repoview-go` and `go.mod` uses `github.com/edsilegxrepo/repoview`.
- **Metadata & Docs Drift:**
  - `LICENSE` is **MIT**, `build.json:12` says `"MIT"`, but `README.md:5` badge says **Apache 2.0**.
  - `build.json:18` says `"cgo": false` while `go-sqlite3` requires CGO, and has duplicate `"entrypoint"` keys (`L6-L7`).
  - `biome.json` schema (`2.5.9`) lags CLI (`2.6.9`), and `repostyle.css:670` + `build.json` fail `biome check .`.
  - `README.md` / `ARCHITECTURE.md` claim `.state.json` / `repoview.state.json.gz`, `--max-depth` default `8`, `go-sqlite3 v1.14.52`, and 84.9%/83.5% coverage; actual code uses `state.json`, default `--max-depth` `5`, `go-sqlite3 v1.14.53`, and `internal/logic` (76.0%) / `internal/portal` (73.7%) / `internal/repo` (64.4%) are below 80%.

---

### 2.6 Granular Exit Codes & Diagnostics

#### [EXIT-01] High — Fragile Substring Error Matching & Raw `1` Fallback
- **Locations:** `cmd/repoview/main.go:142-144,213-245,333-335,347-357,485-489,616-620`, `internal/app/generator.go:55-62`
- **Details:**
  1. **Substring Guessing (`main.go:226-243`):** `run()` classifies errors using `strings.Contains(strings.ToLower(err.Error()), ...)`. A permission error (`EACCES`) while opening `repomd.xml` or `primary.sqlite` matches `"repomd"`/`"primary"` first and incorrectly returns `ExitRepoMetadataError (3)` instead of `ExitIOError (5)`. Conversely, valid metadata/IO errors lacking those exact substrings fall through to raw `return 1` (`L242`), which isn't even a named constant!
  2. **Silent Portal Partial Failures (`main.go:616-620`):** When `repoview portal --render-missing` or `--render-all` fails to render one or more child repositories (`len(renderErrors) > 0`), it logs warnings to `stderr` and **exits with `0` (`ExitSuccess`)** instead of `ExitPartialFailure (6)`.
  3. **`--help` Returns Exit Code `2` (`main.go:142,333`):** `flag.NewFlagSet` returns `flag.ErrHelp` on `--help`/`-h`, which is caught as `ExitUsageError (2)` instead of `ExitSuccess (0)`.
  4. **Overloaded `ExitUsageError (2)`:** Invalid `portal.yaml` configuration (`L365, L374`), CLI syntax errors (`L143`), `filepath.Abs` failures (`L350, L456`), and safety violations (`L221, L668`) all collapse into exit code `2`.

#### Proposed Granular Exit Code Taxonomy

| Exit Code | Constant | Diagnostic Meaning | Trigger Conditions |
| :---: | :--- | :--- | :--- |
| `0` | `ExitSuccess` | Completed cleanly | Successful generation, `--version`, `--help` (`flag.ErrHelp`) |
| `1` | `ExitGeneralError` | Unexpected/unclassified internal error | Unhandled internal errors (replaces magic `1`) |
| `2` | `ExitUsageError` | Invalid CLI syntax or arguments | Unknown flags, missing `<repodir>`, invalid `--format`, conflicting `--portal-home`/`--in-place` |
| `3` | `ExitRepoMetadataError` | Missing, corrupt, or unsupported repo metadata | Missing/corrupt `repomd.xml`, SQLite DBs, `comps.xml`, `Packages*`, unsupported `db_version` |
| `4` | `ExitTemplateError` | Template loading, parsing, or execution error | Missing/invalid custom templates, embedded template failure |
| `5` | `ExitIOError` | Filesystem read/write/permission/disk-full error | `os.Open`/`MkdirAll`/`AtomicWriteFile`/`StateStore.Save` I/O failures, `filepath.Abs` failures |
| `6` | `ExitPartialFailure` | Completed with per-item rendering errors | Single-repo package render failures (`errorCount > 0`), portal `--render-missing`/`--render-all` child repo failures |
| `7` | `ExitConfigError` | Configuration file parse/validation error | Missing/malformed `portal.yaml` via `--config` or discovery, invalid `--ignore-package` glob |
| `8` | `ExitSafetyError` | Safety guard or security/integrity violation | `validateOutputDirSafety` violation, foreign `index.html` overwrite without `--force`, path/symlink traversal, checksum mismatch, decompression bomb limit exceeded |

All internal packages (`app`, `portal`, `repo`, `render`, `state`) should wrap errors with typed/sentinel errors (`*CodedError` or `ErrRepoMetadata`, `ErrTemplate`, `ErrIO`, `ErrConfig`, `ErrSafety`, `ErrIntegrity`) so `cmd/repoview` uses **`errors.Is` / `errors.As`** deterministically instead of `strings.Contains`.

---

### 2.7 Workflow, Repository Discovery, Declarative Structure & Reusability

#### Workflow Analysis (`cmd/repoview`, `internal/app`, `internal/portal`)
1. **Single-Repository Pipeline (`app.Generator.Run`):**
   - Executes `validateOutputDirSafety` -> `prepareRepository` -> `loadPackages` -> `FilterPackages` -> `EnrichPackagesWithChangelogs` -> `EnrichPackageDetails` -> `processGroups` -> `setupRenderer` -> `renderGroups` -> `renderPackages` -> `renderIndices` -> `renderDescriptor` -> `cleanupStale` -> `stateStore.Save`.
   - **[WF-01] Incremental Runs Still Do 100% of Ingestion & Rendering Work:** Even when `Force == false` and the repository metadata (`repomd.xml` revision/checksum or `Release`/`Packages` SHA256) has not changed since the last run, `Generator.Run()` still decompresses databases, opens and parses every `.rpm`/`.deb` binary on disk (`EnrichPackageDetails`), renders every HTML template in memory (`RenderPackage`), and only skips the final `WriteToFile` call!
   - **[WF-02] Monolithic Orchestration & Missing Stage Abstractions:** `Generator.Run()` mixes metadata ingestion, filesystem asset emission, sibling layout sniffing, and stdout reporting directly inside one struct (`internal/app/generator.go`), preventing pipeline reuse (e.g., running as an embeddable Go library, streaming to object storage/S3 as noted in `docs/MIGRATION.md`, or dry-run/JSON-only mode).

2. **Multi-Repository Portal Pipeline (`cmd/repoview/main.go:runPortal`):**
   - Executes `FindPortalConfigIn`/`LoadConfig` -> `portal.Scanner.Scan()` -> optional parallel `app.Generator.Run()` (`--render-missing` / `--render-all`) -> **full second `portal.Scanner.Scan()`** -> `BuildCatalog` + `ApplyConfig` -> `portal.Renderer.WritePortal`.
   - **[WF-03] Redundant Double Crawl & Partial Override Propagation:** `runPortal` crawls the entire tree twice when rendering children instead of refreshing only the rendered nodes from their newly written `repoview.json`. Furthermore, during parallel child rendering (`main.go:586-604`), `portalCfg.Overrides` only propagates `Title` to `app.Config`—ignoring `Icon`, `Badge`, `Description`, `BaseURL`, or exclusion rules.

#### Repository Structure Discovery: Current Mechanics & Gaps (`[DISC-01]`)

| Scope | Current Discovery Mechanism | Observed Gaps & Failure Modes |
| :--- | :--- | :--- |
| **Single-Repo RPM** (`repo/repomd.go`, `app/generator.go:789`) | Checks `repodata/repomd.xml`; requires `primary_db` and `other_db` SQLite databases; optionally loads `group`/`group_gz`. | Hard-fails if repository was generated without `-d` (`primary.xml.gz`/`other.xml.gz` XML-only repos are unsupported); ignores `updateinfo` (security advisories/errata) and `modules` (AppStream YAML). |
| **Single-Repo DEB** (`repo/deb/discovery.go`) | 4-tier check: (1) direct `Packages*` (`binary-<arch>` or flat), (2) `dists/`, (3) suite dir, (4) component dir. Aggregates components + `binary-all`. | `findPackagesFile` checks only `Packages`, `.gz`, `.xz`, `.zst` (omits `.bz2` even though `decompress.go` supports it). In multi-suite/multi-arch `dists/` roots, `Discover()` silently picks **only the first suite and first sorted architecture** with no `--suite`/`--arch` selector. Ignores `Contents-<arch>.gz` and `Translation-*` / DEP-11 `Components-*.yml`. |
| **Portal Crawler** (`portal/scanner.go`) | Crawls up to `MaxDepth` (5); prunes `pool/`, `SRPMS/`, `debug/`, `source/`, `.git`, etc.; uses `(dev, ino)` cycle guard (POSIX only); prioritizes `PortalHome` -> `repoview.json` -> `index.html` -> `repodata/` -> `binary-*` -> `hasDebianIndex`. | `isExcluded` (`scanner.go:605-610`) matches **both** `filepath.Match` **and** `strings.Contains(name, pattern)`—excluding `"test"` accidentally skips `"latest"`. `hasDebianIndex` (`L687`) and `sniffFormat` omit `Packages.zst`. Branch pruning stops at repo roots, so nested sub-repos under an outer `repoview` index cannot be discovered. |
| **Taxonomy & Siblings** (`app/generator.go:676-786`, `portal/taxonomy.go`) | Hardcoded path-depth patterns (`<distro>/<channel>/<arch>`, `<distro>/<channel>`, `dists/<suite>/<component>/binary-<arch>`, or `-`/`.` slugs) + fixed `knownArchitectures` / `isLikelyDistro` maps. | Non-standard enterprise layouts (e.g., `/repos/team/prod/el9-rpms` or `arch/distro/channel`) misclassify `distro`/`channel` or miss sibling switchers. Missing modern/niche arches (`ppc64el`, `armel`, `loong64`, `loongarch64`, `sparc64`). |

#### Customizing & Declaring Repository Structure (`[DISC-02]`)
Currently, repository structure is **inferred first and minimally overridable**:
- Single-repo mode has **no declarative config file** (no `repoview.yaml` / `.repoview.yaml`), forcing all customization into long CLI flags (`--title`, `--baseurl`, `--ignore-package`, `--exclude-arch`, `--comps`).
- `portal.yaml` (`internal/portal/config.go:13-40`) supports `overrides` (only `title`, `icon`, `badge`), `pinned`, `exclude`, and `external_repos`, **but lacks**:
  1. **Declarative Local Repository Entries (`repositories` / `local_repos`)**: Users cannot explicitly declare local repository paths, custom layouts (`rpm_xml`, `rpm_sqlite`, `deb_pool`, `deb_flat`), `suite`/`component`/`arch` selectors, or explicit `output_dir`/`portal_url` mappings when auto-discovery cannot infer them or `--max-depth` is insufficient.
  2. **Rich Override Fields**: `RepoOverrideConfig` cannot override `distro`, `channel`, `suite`, `arch`, `format`, `description`, `base_url`, `gpgkey` / `gpgcheck`, `ignore_package`, `exclude_arch`, `comps`, or `hidden: true` (to suppress unwanted discovered dirs cleanly).
  3. **Custom Taxonomy Rules in `portal.yaml`**: No declarative `path_patterns` or `distro_aliases` (e.g., mapping `company-linux-9` -> `redhat` icon, `el9` distro) without recompiling Go code.
  4. **Explicit Sibling Groupings**: Siblings are purely filesystem-derived (`detectSiblingRepos`); `portal.yaml` cannot declare logical sibling sets across decoupled directories or detached `--portal-home` layouts.

#### Reusability Improvements (`[DISC-03]`)
1. **Declarative Repository & Portal Manifest (`repoview.yaml` + unified `portal.yaml` schema):**
   - Support optional per-repo `repoview.yaml` (auto-discovered in `repoDir`, overridable via CLI flags) and expand `portal.yaml` `overrides` / `repositories` to share the same `RepoSpec` struct.
   - Allow `--dump-config` to emit a complete, valid schema covering both local declared repos and discovery overrides.
2. **Pluggable Backend & Discovery Registry (`internal/repo`):**
   - Define a clean `repo.Backend` / `repo.Open(ctx, RepoSource)` registry supporting `rpm-sqlite`, `rpm-xml` (fallback when `-d` was not run), `deb-dists` (with `--suite` / `--arch` selection), and `deb-flat`.
   - Separate `Discover()` (lightweight metadata probe returning `RepoDescriptor` + source fingerprint) from `Open()` so both `app` and `portal` share one discovery engine.
3. **Decoupled Output & Asset Pipeline (`OutputSink` + `Renderer`):**
   - Abstract file writing behind an `OutputSink` interface (defaulting to `AtomicLocalSink`, enabling future S3/OCI/archive sinks without rewriting `Generator`).
   - Expose `Generator` as a clean Go API accepting `context.Context`, `Stdout`/`Stderr` writers (or `slog.Logger`), and `RepoSpec` so CLI, portal batch workers, and third-party Go tools share identical behavior with zero global state.
4. **Metadata Fingerprint Short-Circuiting (Fast Incremental No-Op):**
   - Persist a `repo_fingerprint` (SHA256 of `repomd.xml` + `comps.xml` or `Release` + `Packages*` + `repoview.yaml` + CLI config hash) in `state.json`. When `!Force` and the fingerprint matches, skip decompressing SQLite/opening thousands of `.rpm`/`.deb` archives in <10 ms.

---

### 2.8 RPM & DEB Metadata Utilization Audit (Rich Display & Search)

#### 1. Unused / Underutilized RPM Metadata (`[META-01]`)
Inspection of `internal/repo/sqlite.go`, `internal/repo/rpm_reader.go`, `internal/repo/repomd.go`, and `internal/models/package.go` reveals rich RPM metadata already present in standard repositories (`primary.sqlite`, `other.sqlite`, `repomd.xml`, and `.rpm` headers) that is currently discarded:

| Metadata Source | Available Fields / Tables | Current Status in `repoview-go` | Recommended Display & Search Usage |
| :--- | :--- | :--- | :--- |
| `primary.sqlite` (`packages`) | `pkgId` (SHA256/checksum), `checksum_type`, `rpm_packager`, `time_file`, `size_archive` | **Not queried** (`sqlite.go:154-159`). `Package.SHA256` exists in `models` and `package.html:288` renders it, so RPM pages omit SHA-256 while DEB pages show it! | Query `pkgId`, `rpm_packager`, `time_file`, `size_archive`; display SHA-256 checksum card, Packager/Maintainer, and payload ratio on `package.html`. |
| `primary.sqlite` (weak deps) | `enhances`, `recommends`, `suggests`, `supplements` tables | **Not queried** (`sqlite.go:324-423` queries only `requires`, `provides`, `conflicts`, `obsoletes`), even though `PackageDependencies` already has `Recommends` and `Suggests`! | Query `recommends`, `suggests`, `enhances`, `supplements` (gracefully handling older DB schemas lacking these tables via `sqlite_master` check) and render in `package.html` dependency tabs. |
| `other.sqlite` (`changelog`) | Full multi-entry changelog history per `pkgKey` | **Truncated to `LIMIT 1`** (`sqlite.go:78,271-280`), showing only the single newest changelog entry per version. | Optionally load top *N* (e.g. 5–10) changelog entries per version in `package.html` with collapsible history. |
| `repomd.xml` | `Revision`, `<data type="updateinfo">`, `<data type="modules">` | `Repomd.Revision` is parsed (`models/repomd.go:29`) and **discarded** (`repomd.go:64-94`). `updateinfo.xml*` (CVEs, security/bugfix advisories, severity) is ignored. | Surface `repomd.Revision` in `index.html`/`repoview.json`; parse `updateinfo` to display Security/Bugfix Advisory badges, CVE IDs, and severity on `package.html` and index them in search! |
| `.rpm` binary header (`go-rpmutils`) | `FileInfo` flags (config/doc/ghost), symlink targets, `VCS`, `BUGURL`, `MODULARITYLABEL` | `ReadRPMDetails` extracts only `BuildHost`, `SOURCERPM`, `InstalledSize`, `sigs[0]`, 4 scriptlets, and basic file list. | Show `Bug URL` / `VCS` links, config/doc file badges (`[config]`, `[doc]`), and all GPG signatures (not just `sigs[0]`). |

#### 2. Unused / Underutilized DEB Metadata (`[META-02]`)
Inspection of `internal/models/adapter_deb.go`, `internal/repo/deb/details.go`, `internal/repo/deb/discovery.go`, and `pault.ag/go/debian/control.BinaryIndex` reveals substantial Debian metadata currently dropped:

| Metadata Source | Available Fields / Files | Current Status in `repoview-go` | Recommended Display & Search Usage |
| :--- | :--- | :--- | :--- |
| `control.BinaryIndex` | `Priority`, `MultiArch`, `Tags` (Debtags), `GetEnhances()`, `GetBuiltUsing()`, `MD5sum`, `SHA1`, `DebugBuildIds`, `Values["Bugs"]`, `Values["Origin"]`, `Values["Essential"]` | **Ignored** in `adapter_deb.go:13-70`. Also `adapter_deb.go:53-54` manually re-parses `Values["Recommends"]` & `Values["Provides"]` instead of using `idx.GetRecommends()` / `idx.GetProvides()`. | Map `Priority`, `MultiArch`, `Essential`, `Tags` (Debtags), `Enhances`, `Built-Using`, `Bugs`, `Origin`, and `MD5`/`SHA1`; display priority/multi-arch/essential pills and clickable Debtag chips on `package.html`; use `idx.GetRecommends()`/`idx.GetProvides()`. |
| `Release` / `InRelease` | `Origin`, `Label`, `Codename`, `Date`, `Valid-Until`, `Description`, `Suite`, `Architectures`, `Components` | `discovery.go` locates `ReleaseFile`, and `scanner.go` only reads `Architecture(s)` lines via manual string scanning. | Unmarshal via `control.Unmarshal` to populate repo title/description, build date, suite/codename badges, and `Valid-Until` freshness in `index.html` & `repoview.json`. |
| `.deb` `control.tar.*` | `conffiles`, `triggers`, `md5sums`, `shlibs` / `symbols` | `ReadDebDetails` extracts only `preinst`, `postinst`, `prerm`, `postrm`. | Parse `conffiles` to mark configuration files (`[conffile]`) in the `package.html` file table; show `triggers` presence if non-empty. |
| `.deb` `data.tar.*` | Symlink targets (`header.Linkname`), `/usr/share/doc/<pkg>/copyright` (DEP-5 `License:`) | `ReadDebFilesAndChangelog` omits symlink targets and ignores `copyright` (so DEB packages display **no License**!). Multi-version changelogs (`AllVersions[1:]`) are also skipped (`generator.go:516`). | Append `-> Linkname` for symlinks; extract DEP-5 `License:` summary from `copyright` when `Package.License` is empty; extract latest changelog for all versions of a package or cache per `.deb`. |

#### 3. Rich Search Index & Client Search Enhancements (`[SRCH-01]`)
- **Locations:** `internal/models/search.go`, `internal/render/renderer.go:268-286`, `internal/render/templates/layout/search.js:184-265`, `internal/portal/templates/portal.html:876-968`
- **Current Limitations:**
  1. `search.json` emits only `["n", "v", "a", "s", "f"]` (Name, EVR, Arch, Summary, Filename) and `search.js` searches **only** `name` and `summary` via linear `.includes()` / regex, capped at the first 50 iteration hits without relevance ranking.
  2. Users **cannot search by**: `provides` / shared library capabilities (e.g., searching `libssl.so.3` or `python3dist(...)`), `group` / `section`, `license`, `maintainer` / `vendor`, `source_package`, `tags` (Debtags), `cve` / advisory ID, or `arch` filter tokens!
  3. **UX Gaps:** No match-relevance ranking (exact name match -> prefix match -> word match -> summary/provides/tags match), no keyboard `ArrowUp`/`ArrowDown`/`Enter` navigation in `#searchResults`, and no structured query qualifiers (`arch:arm64`, `license:MIT`, `provides:httpd`, `group:databases`, `tag:...`).
- **Recommended Search Architecture:**
  - Extend `SearchIndex` schema incrementally while keeping payload compact (e.g., `["n", "v", "a", "s", "f", "g", "l", "p", "t"]` for group/section, license, top provides/source, tags/advisories — or a split optional `search-ext.json` loaded asynchronously on demand for >20k packages so initial `search.json` stays <2 MB!).
  - Add relevance scoring (exact > prefix > substring > capability/tag match), qualifier parsing (`arch:`, `group:`, `license:`, `provides:`, `tag:`, `cve:`), highlighted match snippets (safely via DOM `textContent`/escaped spans), and keyboard navigation (`ArrowUp`/`ArrowDown`/`Enter`) in `search.js` (and `portal.html`).

---

## 3. Prioritized Recommendations Matrix

| Priority | ID | Finding Summary | Area | Impact | Effort | Primary Targets |
| :---: | :--- | :--- | :--- | :---: | :---: | :--- |
| **P0** | SEC-01 | Escape XML/CDATA in `rss.xml` (or switch to `encoding/xml`) | Security | Critical | S | `internal/render/renderer.go`, `templates/rss.xml` |
| **P0** | LOG-01 | Stop eager `GetFiles()` in `ReadRPMDetails`; fix `defer` heap leak | Memory / Perf | Critical | M | `internal/repo/rpm_reader.go`, `internal/app/generator.go` |
| **P0** | LOG-02 | Fix `nil` `stateStore` panic when `NewStateStore` fails | Correctness | Critical | S | `internal/app/generator.go`, `internal/state/state.go` |
| **P0** | LOG-03 | Remove pre-emptive `os.RemoveAll` on `--force` before template validation | Data Loss | Critical | S | `internal/app/generator.go` |
| **P0** | SEC-03 | Enforce `EvalSymlinks` + `filepath.IsLocal` / `os.Root` path containment | Security / Data Loss | High | M | `internal/util`, `repomd.go`, `rpm_reader.go`, `sqlite.go`, `deb/repository.go`, `renderer.go`, `generator.go` |
| **P1** | SEC-02 | Sanitize/shell-quote copyable commands & INI/Deb822 snippets; review `gpgcheck=0` | Security | High | M | `package.html`, `search.js`, `index.html`, `portal/types.go` |
| **P1** | SEC-04 | Verify `repomd.xml` and Debian `Release` checksums before ingestion | Security / Integrity | High | M | `internal/repo/repomd.go`, `decompress.go`, `deb/` |
| **P1** | SEC-05 | Add `io.LimitReader` bounds on scriptlets, changelogs, YAML, RSS reads | Security / OOM | High | S | `deb/details.go`, `deb/changelog.go`, `portal/renderer.go`, `portal/config.go` |
| **P1** | LOG-04 | Commit `StateStore` hashes only after `WriteToFile` succeeds; propagate write errors | Data Integrity | High | M | `internal/app/generator.go`, `internal/state/state.go` |
| **P1** | LOG-05 | Check `rows.Err()` & return query errors in `GetPackageDependencies`; `defer` cleanup | Correctness | High | S | `internal/repo/sqlite.go` |
| **P1** | PERF-01 | Use `rpmutils.ReadHeader` in `ReadRPMFiles` instead of full-payload `Verify` | Performance | High | S | `internal/repo/rpm_reader.go` |
| **P1** | EXIT-01 | Replace substring matching with typed sentinel errors; add codes `1, 7, 8`; fix `--help` & `runPortal` partial failure exit codes | Diagnostics | High | M | `cmd/repoview/main.go`, `internal/app`, `internal/portal`, `internal/repo` |
| **P1** | OS-01 | Fix Windows test failures (`perm_test.go`, `renderer_test.go`, `e2e_test.go`, `deb_e2e_test.go`) | Cross-Platform | High | S | `internal/util`, `internal/render`, `tests/integration` |
| **P1** | OS-02 | Implement Windows `GetFileInformationByHandle` cycle/alias guard | Cross-Platform | High | S | `internal/portal/scanner_windows.go` |
| **P2** | SEC-06 | Harden SQLite with `file:` URI + `ATTACH` `mode=ro` + `query_only`/`trusted_schema` | Security | Medium | S | `internal/repo/sqlite.go` |
| **P2** | SEC-07 | Resolve 5 `gosec` CWE-276 findings; protect/relocate `state.json` from web root | Security | Medium | S | `internal/util/perm.go`, `internal/state/state.go`, `generator.go` |
| **P2** | LOG-06 | Unify `atomicWriteFile` into `util.AtomicWriteFile` with Windows fallback | Atomicity / DRY | High | S | `internal/util`, `state.go`, `render/renderer.go`, `portal/renderer.go` |
| **P2** | LOG-07 | Eliminate global `log.Printf`/`fmt.Printf`/`SetUmask` in `run()`/`Generator` | Concurrency / Modularity | High | M | `cmd/repoview/main.go`, `internal/app/generator.go`, `sqlite.go` |
| **P2** | PERF-02 | Remove sequential `ReadDebTimestamp` pass in `deb.GetAllPackages` | Performance | High | S | `internal/repo/deb/repository.go` |
| **P2** | PERF-03 | Deduplicate `pkgVersions` builds; pre-parse EVR keys before sorting | Performance | High | M | `internal/logic/grouping.go`, `sorting.go`, `generator.go` |
| **P2** | PERF-04 | Cap `renderPackages` workers; parallelize `renderGroups` & DB decompression; single-pass `search.json` scan | Performance | Medium | M | `internal/app/generator.go`, `internal/portal/scanner.go` |
| **P2** | OS-04 | Add SQLite read-only mmap/cache PRAGMAs; use `DirEntry.Info()` in scanner | OS Optimization | Medium | S | `internal/repo/sqlite.go`, `internal/portal/scanner.go` |
| **P2** | MOD-01 | Consolidate duplicated taxonomy, worker pools, SQLite dep queries, deb discovery, CLI `printOption`, HTML partials | Maintainability / DRY | High | L | `internal/logic`, `internal/portal`, `internal/app`, `internal/util`, `templates/` |
| **P2** | MOD-02 | Minimize `RepoReader` interface and move backend factory into `internal/repo` | Modularity | Medium | M | `internal/repo/reader.go`, `internal/app/generator.go`, `deb/repository.go` |
| **P2** | WF-01/02/03 | Add repo metadata fingerprint short-circuit; decouple `OutputSink`; eliminate `runPortal` double scan & propagate full overrides | Workflow / Reuse | High | M | `internal/app/generator.go`, `cmd/repoview/main.go`, `internal/portal` |
| **P2** | DISC-01/02/03 | Support `repoview.yaml`, expanded `portal.yaml` overrides/local repos, `--suite`/`--arch` selectors, RPM XML fallback, `Packages.bz2`/`.zst` parity | Discovery / Reuse | High | L | `internal/portal/config.go`, `scanner.go`, `internal/repo`, `cmd/repoview` |
| **P2** | META-01/02 | Extract & display missing RPM (`pkgId`, `packager`, weak deps, `updateinfo` CVEs, `Revision`) & DEB (`Priority`, `MultiArch`, `Tags`, `Enhances`, `BuiltUsing`, `conffiles`, DEP-5 `copyright`, `Release`) metadata | Metadata / UX | High | M | `sqlite.go`, `rpm_reader.go`, `adapter_deb.go`, `deb/details.go`, `package.html` |
| **P2** | SRCH-01 | Enrich `search.json` (capabilities/provides, group, license, tags, CVEs) + ranked search, qualifiers (`arch:`, `provides:`, `license:`), keyboard nav in `search.js` | Search / UX | High | M | `models/search.go`, `render/renderer.go`, `search.js`, `portal.html` |
| **P3** | LOG-08/09/10 | Surface `os.ReadDir` scan warnings; fix `BuildGroupTree` leaf collision, `GetLetterGroups` UTF-8 rune, detached `.` RSS lookup, Windows `Overrides` slash normalization, `Latest` banner count | Correctness | Medium | M | `scanner.go`, `grouping.go`, `portal/config.go`, `portal/renderer.go`, `index.html` |
| **P3** | OS-03 | Check DOS device stem before `.` in `SanitizeFilename`; handle case-insensitive collisions; add `.exe` in integration tests | Cross-Platform | Medium | S | `internal/util/sanitize.go`, `tests/integration/*` |
| **P3** | PERF-05 | Skip rewriting unchanged layout assets via `StateStore`; compact `state.json` option | Performance | Low | S | `internal/app/generator.go`, `internal/state/state.go` |
| **P3** | SEC-08/09/MOD-03/04 | Remove unused code/params; fix `gocritic` `appendAssign`; fix `build.json`/`biome.json`/`repostyle.css`; align `LICENSE`/URLs/docs; evaluate pure-Go SQLite & `yaml.v3` migration; raise `logic`/`portal`/`repo` coverage >=80% | Hygiene / Docs | Medium | M | `build.json`, `biome.json`, `repostyle.css`, `go.mod`, `README.md`, `ARCHITECTURE.md`, tests |

*(Effort scale: **S** = < 0.5 day, **M** = 0.5–2 days, **L** = 2–4 days)*

---

## 4. Phased Implementation Plan & Checklist

> [!IMPORTANT]
> **Execution Rule:** No code changes will be made until this plan is reviewed and approved. Each phase ends with a full verification gate (`go vet`, `go test`, `staticcheck`, `gosec`, `biome check`).

### Phase 1: Critical Safety, Memory, Data Loss & XSS Fixes (P0)
**Goal:** Eliminate immediate security vulnerabilities, OOM/heap bloat, nil panics, and destructive data loss risks.  
**Exit Criteria:** Zero `P0` defects; `rss.xml` safely handles `]]>` and XML metacharacters; RPM file manifests consume memory strictly per active worker; `--force` cannot destroy an existing site on template failure.

- [ ] **1.1** Fix `rss.xml` XML/CDATA injection (`SEC-01`):
  - Add `xmlEscape` and `cdataSafe` (splitting `]]>` as `]]]]><![CDATA[>`) functions to `XMLTmpls`, or migrate `RenderRSS` to `encoding/xml` structs matching `internal/portal/renderer.go`.
  - Add unit tests in `internal/render/renderer_test.go` with adversarial `]]>`, `<script>`, `&`, and quotes in package summary, description, title, and changelog.
- [ ] **1.2** Fix RPM eager file manifest loading & `defer` memory leak (`LOG-01`):
  - Remove `header.GetFiles()` from `ReadRPMDetails` in `internal/repo/rpm_reader.go`.
  - Ensure `renderSinglePackage` in `internal/app/generator.go` loads `Files` solely via `db.ReadPackageFiles` and unconditionally clears `latest.Details.Files = nil` via `defer`.
  - Verify `AllVersions[1:]` never hold `Files` slices.
- [ ] **1.3** Fix `nil` `stateStore` panic & `--force` premature `os.RemoveAll` (`LOG-02`, `LOG-03`):
  - Return error on `state.NewStateStore` open failure (do not continue with `nil`).
  - Remove `os.RemoveAll(g.config.OutputDir)` from `setupRenderer`; rely on `g.config.Force` bypassing `HasChanged` + atomic file writes + `cleanupStale` at the end.
  - Add regression tests in `internal/app/generator_unit_test.go` for invalid `--template-dir` with `--force` preserving existing output.
- [ ] **1.4** Harden path traversal & symlink containment (`SEC-03`):
  - Create `internal/util/path.go` with `ResolveContainedPath(baseDir, relOrTarget string) (string, error)` and `EvalSymlinks` ancestor checking + `filepath.IsLocal`.
  - Apply in `ParseRepomd`, `EnrichPackagesWithRPMDetails`, `ReadPackageFiles`, `DebRepository.resolveDebPath`, `Renderer.WriteToFile`, `Renderer.WriteAssets`, and `validateOutputDirSafety` (including symlinked `outputDir == repoDir`).
  - Add traversal and symlink-escape tests across `repo`, `deb`, `render`, and `app`.

### Phase 2: High-Priority Security, Correctness, Exit Codes & Windows Compatibility (P1)
**Goal:** Enforce metadata integrity, fix silent error swallowing, standardize granular exit codes, and make the full test suite green on both Linux and Windows.  
**Exit Criteria:** All unit tests pass on both Windows and Linux; `gosec` reports 0 unaddressed findings; every CLI failure returns a deterministic, documented exit code via `errors.Is`/`errors.As`.

- [ ] **2.1** Harden snippet generation, `deb` archive bounds & metadata checksums (`SEC-02`, `SEC-04`, `SEC-05`):
  - Sanitize/reject newlines & control chars and shell-escape arguments in `package.html`, `index.html`, `search.js`, and `portal/types.go`; add `--gpgkey` / configurable `gpgcheck` option instead of unconditionally forcing `gpgcheck=0` / `Trusted: yes` where keys are configured.
  - Enforce `io.LimitReader` ceilings on `deb/details.go` (`control.tar` scriptlets), `deb/changelog.go` (`changelog.Debian.gz`), `portal/config.go`, and `portal/renderer.go` (`latest-feed.xml`).
  - Verify `repomd.xml` SHA256/SHA512 checksums in `internal/repo/repomd.go` / `decompress.go`, and Debian `Release` SHA256 hashes when present.
- [ ] **2.2** Fix `StateStore` commit ordering & SQLite error checking (`LOG-04`, `LOG-05`):
  - Split `StateStore.HasChanged` into read-check (`CheckChanged`) + post-write commit (`MarkWritten`) so failed `WriteToFile` calls do not corrupt incremental state; propagate write errors from `renderGroups`, `renderIndices`, `renderDescriptor`, and `stateStore.Save()`.
  - Check `rows.Err()`, scan errors, and query errors in `RepositoryAccess.GetPackageDependencies`; use `defer` statement cleanup in `NewRepositoryAccess`.
- [ ] **2.3** Implement granular domain errors & deterministic exit codes (`EXIT-01`):
  - Define typed/sentinel errors (`ErrUsage`, `ErrRepoMetadata`, `ErrTemplate`, `ErrIO`, `ErrConfig`, `ErrSafety`, `ErrIntegrity`) and exit constants (`ExitSuccess=0`, `ExitGeneralError=1`, `ExitUsageError=2`, `ExitRepoMetadataError=3`, `ExitTemplateError=4`, `ExitIOError=5`, `ExitPartialFailure=6`, `ExitConfigError=7`, `ExitSafetyError=8`).
  - Replace substring matching in `cmd/repoview/main.go` with `errors.Is`/`errors.As`.
  - Return `ExitSuccess` on `flag.ErrHelp` (`--help`) and `ExitPartialFailure` in `runPortal` when `len(renderErrors) > 0`.
- [ ] **2.4** Fix Windows test failures & implement Windows file-ID cycle guard (`OS-01`, `OS-02`, `OS-03`):
  - Update `internal/util/perm.go` (`perm_unix.go` / `perm_windows.go`) and gate POSIX permission checks in `perm_test.go`, `renderer_test.go`, `e2e_test.go`, `deb_e2e_test.go` with `runtime.GOOS != "windows"` (plus Windows read-only/writeability checks).
  - Implement `getFileID` in `internal/portal/scanner_windows.go` via `golang.org/x/sys/windows.GetFileInformationByHandle`.
  - Check DOS device name stem before `.` in `SanitizeFilename` and append `.exe` in `tests/integration` binary builds on Windows.

### Phase 3: Performance, Scalability & OS-Specific Optimizations (P2)
**Goal:** Eliminate redundant I/O and CPU hot paths across RPM, DEB, and Portal pipelines for both Linux and Windows.  
**Exit Criteria:** Measurable reduction in disk I/O (1 pass per `.deb`/`.rpm`), memory allocations, and end-to-end runtime on multi-thousand-package repos.

- [ ] **3.1** Optimize RPM & DEB package readers (`PERF-01`, `PERF-02`, `SEC-06`, `OS-04`):
  - Switch `ReadRPMFiles` to `rpmutils.ReadHeader(f)` to avoid hashing entire RPM payloads.
  - Remove sequential `ReadDebTimestamp` loop from `DebRepository.GetAllPackages()`; extract timestamp once inside `EnrichPackageDetails`.
  - Harden `NewRepositoryAccess` with `file:` URI escaping, `ATTACH` `mode=ro`, `PRAGMA query_only = ON`, `PRAGMA trusted_schema = OFF`, and read-only tuning (`PRAGMA mmap_size`, `PRAGMA temp_store = MEMORY`, `PRAGMA cache_size`).
- [ ] **3.2** Optimize grouping, sorting, concurrency & scanner syscalls (`PERF-03`, `PERF-04`, `PERF-05`, `OS-04`):
  - Pre-build and sort `pkgVersions` once; pre-parse EVR/Deb version keys prior to `sort.Slice`.
  - Clamp `renderPackages` worker pool (`1..16` or configurable `--workers`), parallelize `renderGroups` and `primary`/`other` decompression.
  - Use `entry.Info()` instead of redundant `os.Stat` in `portal/scanner.go`, and parse `search.json` in a single pass when `repoview.json` is missing.
  - Track layout assets in `StateStore` so `WriteAssets` skips unchanged CSS/JS writes.

### Phase 4: Modularity, DRY Consolidation & Architecture Cleanup (P2)
**Goal:** Eliminate cross-package duplication, global state leaks, and interface bloat to bring the codebase to long-term production maintainability.  
**Exit Criteria:** Zero duplicated core algorithms; `Generator` writes exclusively to injected `io.Writer`s; clean `golangci-lint` run across extended linters.

- [ ] **4.1** Unify duplicated subsystems (`MOD-01`, `LOG-06`, `LOG-07`):
  - Extract `util.AtomicWriteFile` (replacing 3 copies in `state`, `render`, `portal`).
  - Extract shared taxonomy package (`internal/taxonomy` or `internal/logic/taxonomy.go`) eliminating duplication between `app/generator.go` and `portal/taxonomy.go`.
  - Extract shared concurrent worker pool helper (`util.ForEachConcurrent`).
  - Refactor `sqlite.go` dependency queries into one helper, `deb/discovery.go` component loops into one helper, and `cmd/repoview/main.go` `printOption` into one helper.
  - Inject `Stdout`/`Stderr` (or structured logger) into `app.Config` instead of global `fmt.Printf`/`log.Printf`; keep `util.SetUmask` solely in `main()`.
  - Extract shared Go HTML template partials (`_sidebar.html`, `_head.html`, `_topbar.html`) across `index.html`, `group.html`, `package.html`.
- [ ] **4.2** Minimize `RepoReader` & remove backend leakage (`MOD-02`):
  - Add `repo.Open(repoDir string, format models.RepoFormat)` factory so `internal/app` does not import `internal/repo/deb`.
  - Remove dead `GetChangelogForPackage` and no-op `EnrichPackagesWithChangelogs` stubs from `DebRepository` (handle optional `ChangelogEnricher` capability interface or perform format-specific enrichment cleanly), and drop redundant `depsMap` caching in `DebRepository`.
- [ ] **4.3** Fix remaining logic & portal edge cases (`LOG-08`, `LOG-09`, `LOG-10`):
  - Surface `os.ReadDir` warnings in `portal/scanner.go`.
  - Fix `BuildGroupTree` leaf collision, `GetLetterGroups` UTF-8 rune decoding (`utf8.DecodeRuneInString`), `index.html` total package count display, `portal/config.go` `filepath.ToSlash` override key normalization, and `portal/renderer.go` detached `.` `latest-feed.xml` lookup.
  - Address case-insensitive filename collisions on Windows/macOS (e.g., deterministic short hash suffix on collision) and `gosec` CWE-276 `state.json` web-root exposure (`SEC-07`).
- [ ] **4.4** Workflow, Declarative Repository Structure & Reusability (`WF-01`, `WF-02`, `WF-03`, `DISC-01`, `DISC-02`, `DISC-03`):
  - Add repository-level fingerprint short-circuiting in `StateStore` (`repomd.xml` + `comps.xml` / `Release` + `Packages*` + config hash) so unchanged incremental runs skip `.rpm`/`.deb` binary inspection and HTML re-rendering.
  - Introduce `repoview.yaml` (single-repo declarative config) and expand `portal.yaml` (`local_repos`/`repositories`, rich `overrides` for `distro`/`channel`/`arch`/`suite`/`base_url`/`gpgkey`/`ignore_package`/`exclude_arch`/`hidden`, and custom taxonomy `path_patterns`).
  - Propagate all override fields during `runPortal` `--render-missing`/`--render-all` and eliminate the full second `Scanner.Scan()` pass by updating catalog entries in memory from rendered `repoview.json` descriptors.
  - Add `--suite` / `--arch` selectors for multi-suite/multi-arch Debian roots, support `Packages.bz2`/`.zst` consistently across `discovery.go` and `portal/scanner.go`, add `primary.xml`/`other.xml` fallback when SQLite DBs are absent, fix `isExcluded` substring over-matching (`"test"` vs `"latest"`), and abstract `OutputSink` for reusable library/storage-backend integration.
- [ ] **4.5** Rich RPM/DEB Metadata Extraction, Display & Search (`META-01`, `META-02`, `SRCH-01`):
  - **RPM:** Query `pkgId` (SHA-256), `rpm_packager`, `time_file`, `size_archive`, and weak dependencies (`recommends`, `suggests`, `enhances`, `supplements`) from `primary.sqlite`; surface `repomd.Revision`, multi-entry changelogs, RPM `BUGURL`/`VCS`, file flags (`[config]`, `[doc]`), and `updateinfo` security/CVE advisories.
  - **DEB:** Map `Priority`, `MultiArch`, `Essential`, `Tags` (Debtags), `Enhances`, `BuiltUsing`, `Bugs`, `Origin`, `MD5sum`/`SHA1` via `control.BinaryIndex`; parse `Release` metadata via `control.Unmarshal`; extract `conffiles`, symlink targets, and DEP-5 `copyright` license from `.deb` archives; use `idx.GetRecommends()`/`idx.GetProvides()` directly.
  - **Search:** Expand `search.json` (or lazy `search-ext.json` for large repos) with `group`/`section`, `license`, `provides`, `tags`, and `advisories`; implement relevance ranking, structured query qualifiers (`arch:`, `provides:`, `license:`, `group:`, `tag:`, `cve:`), and keyboard arrow navigation in `search.js` and `portal.html`.

### Phase 5: Code Hygiene, Tooling, Dependencies, Tests & Documentation Alignment (P3)
**Goal:** Achieve 100% clean linter/formatter checks, remove dead code, align docs/build configs, and exceed 80% coverage across every package.  
**Exit Criteria:** `go test ./...`, `go vet`, `staticcheck`, `golangci-lint`, `gosec`, `govulncheck`, and `biome check .` all exit `0`; all packages >=80% coverage.

- [ ] **5.1** Remove dead code, unused parameters & linter smells (`MOD-03`):
  - Remove unused `portal.RepoRunner`, `Package.RPMFilename()`, `Package.VR()` (if unused), unused `rootDir` in `inspectDirectory`, unused `error` return in `renderPackages`, unused `idx` in `state_test.go`, and unused return in `createMockDebianRepository`.
  - Fix the 3 `gocritic` `appendAssign` bugs (`generator.go:156`, `config.go:123,203`).
- [ ] **5.2** Fix build, lint & asset configuration drift (`SEC-09`, `MOD-04`):
  - Fix `build.json` (remove duplicate `"entrypoint"`, set `"cgo": true` to match `mattn/go-sqlite3`, format with Biome).
  - Keep `github.com/mattn/go-sqlite3` (confirmed by user; RPM/DEB build ecosystem already supports CGo) and plan migration from archived `gopkg.in/yaml.v3` to `go.yaml.in/yaml/v3`.
  - Update `biome.json` schema (`2.6.9`) and format `repostyle.css` (`L670`).
  - Fix console typo (`" organizing groups..."`), standardize repo footer URLs (`edsilegxrepo/repoview-go`), align `README.md` license badge (`MIT`), `state.json` naming, `--max-depth` default (`5`), and dependency versions.
- [ ] **5.3** Expand test coverage & final verification:
  - Raise coverage above 80% in `internal/repo` (currently 64.4% — add synthetic `.rpm` fixture tests so `rpm_reader_test.go` does not skip), `internal/portal` (currently 73.7%), and `internal/logic` (currently 76.0%).
  - Add exit-code matrix tests in `cmd/repoview/main_test.go` covering all 9 exit codes (`0`–`8`).
  - Run full verification suite on both Windows and Linux:
    ```bash
    go test -race -cover ./...
    go vet ./...
    staticcheck ./...
    govulncheck ./...
    gosec ./...
    golangci-lint run
    biome check .
    ```

---

## 5. Approval Gate

This review and implementation plan are complete. **No source code has been modified.**  
**Confirmed Decision:** Retain `github.com/mattn/go-sqlite3` with CGo (`"cgo": true` in `build.json`).  
Please confirm approval of this plan to begin **Phase 1**.
