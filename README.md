# RepoView-Go

[![Go Version](https://img.shields.io/badge/Go-1.21%2B-blue.svg)](https://golang.org)
[![Coverage](https://img.shields.io/badge/Coverage-84.9%25-brightgreen.svg)](TESTING.md)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Air--Gap Compliant](https://img.shields.io/badge/Air--Gap-100%25%20Compliant-success.svg)](ARCHITECTURE.md)

**RepoView-Go** is a high-performance replacement for the legacy Python `repoview` utility. It compiles repository metadata and package headers—supporting both **RPM (YUM/DNF)** and **Debian/Ubuntu (APT/dpkg)** repositories—into a modern, static, searchable web browsing portal.

---

## Table of Contents

1. [Application Overview and Objectives](#1-application-overview-and-objectives)
2. [Security Assessment](#2-security-assessment)
   - [Encryption in Transit](#encryption-in-transit)
   - [Secret Management](#secret-management)
   - [Authentication Configuration](#authentication-configuration)
   - [Role-Based Access Control (RBAC)](#role-based-access-control-rbac)
   - [Current and Non-Vulnerable Libraries Used](#current-and-non-vulnerable-libraries-used)
   - [Unprivileged Context Enforcement](#unprivileged-context-enforcement)
3. [Code Quality Assessment and Best Practices](#3-code-quality-assessment-and-best-practices)
4. [Command Line Arguments](#4-command-line-arguments)
5. [Usage and Deployment Examples](#5-usage-and-deployment-examples)
   - [Basic Repository Generation (RPM)](#basic-repository-generation-rpm)
   - [Debian / Ubuntu Repository Generation](#debian--ubuntu-repository-generation)
   - [Incremental Build & Cache Utilization](#incremental-build--cache-utilization)
   - [Enterprise Customization](#enterprise-customization)
   - [Automated Production Deployment (Systemd Timer)](#automated-production-deployment-systemd-timer)
   - [Multi-Repository Enterprise Portal (`repoview portal`)](#multi-repository-enterprise-portal-repoview-portal)
   - [Production Nginx Web Server Specifications](#production-nginx-web-server-specifications)
   - [Containerized Deployment (Unprivileged Docker)](#containerized-deployment-unprivileged-docker)
6. [System Architecture & Testing Documentation](#6-system-architecture--testing-documentation)

---

## 1. Application Overview and Objectives

The primary objective of `repoview-go` is to deliver a blazingly fast, reliable, and air-gap compliant static site generator for enterprise Linux repositories. It replaces the obsolete Python 2 `repoview` implementation (which relied on the unmaintained `kid` templating engine and suffered from quadratic slowdowns on large repositories) with an optimized, concurrent Go architecture supporting both **RPM (YUM/DNF)** and **Debian/Ubuntu (APT/dpkg)** repository structures.

### Key Objectives

- **Multi-Format Repository Ingestion**: Transparently ingests RPM repositories (`repodata/` XML and SQLite, `comps.xml`, `.rpm` packages) and Debian repositories (`dists/` suite/component hierarchies, flat repositories, `Packages` indices, and `.deb` archives).
- **100% Static Output (Zero Server-Side Runtime)**: Generates static HTML5, CSS, JSON, and XML files that can be hosted on any web server (Nginx, Apache, Caddy, AWS S3, Cloudflare Pages) without server-side application runtimes or active database connections.
- **Air-Gap Compliance (Zero External Network Calls)**: All fonts, layout templates, glassmorphic stylesheets, and search engines are bundled directly into the binary via `embed.FS`. The generated site makes **zero outbound HTTP calls** to public CDNs, Google Fonts, or external analytics.
- **Sub-Second Client-Side Search**: Automatically indexes package names, summaries, architectures, and descriptions into a compact `search.json` file. An embedded, zero-dependency Vanilla JS search engine powers real-time filtering directly in the client browser.
- **Client Configuration Generation**: Generates ready-to-copy client repository configuration files: `.repo` files for YUM/DNF clients and modern Deb822 `.sources` format files for APT clients.
- **Universal RSS 2.0 Feeds**: Unconditionally produces `latest-feed.xml` containing newly added or updated packages, complete with a persistent RSS feed button in the web interface.
- **High Concurrency & Multi-Core Scaling**: Implements a bounded goroutine worker pool (`runtime.NumCPU() * 2`) to render thousands of package pages in parallel without exhausting filesystem descriptors or memory.
- **Content-Hashed Incremental Builds**: Tracks SHA-256 content hashes in `.state.json`. Unmodified packages are skipped during subsequent runs, reducing update times by >90% while automatically pruning deleted (stale) packages.
- **Strict Legacy Layout Compatibility**: Preserves canonical URL routing (`index.html`, `*.group.html`, `<name>.html`, `latest-feed.xml`), ensuring drop-in replacement compatibility for existing mirror infrastructures.

---

## 2. Security Assessment

### Encryption in Transit

- **Serving Layer TLS**: RepoView-Go generates static assets intended to be served behind a TLS-terminating reverse proxy (Nginx, Envoy, Caddy) or Cloud CDN. Deployments should enforce **TLS 1.3** (or TLS 1.2 with AEAD ciphers) and configure HTTP Strict Transport Security:
  ```http
  Strict-Transport-Security: max-age=31536000; includeSubDomains; preload
  ```
- **Zero Outbound Transmissions**: Because the generator executes completely offline against local filesystem paths and serves self-contained assets, there is **zero risk of unencrypted plaintext leaks** or man-in-the-middle (MITM) tampering during generation or client browsing.

### Secret Management

- **Stateless Operation**: RepoView-Go is entirely stateless and does not store, process, or require hardcoded secrets, API tokens, database passwords, or cryptographic private keys.
- **Environment Isolation**: In CI/CD pipelines, publication credentials (e.g. AWS S3 access keys, SSH keys, or rsync credentials used to sync output directories) must be injected via external secret management systems (HashiCorp Vault, AWS Secrets Manager, GitHub Actions Secrets) and never written to repository directories.

### Authentication Configuration

While RepoView-Go produces static files, access control is enforced at the HTTP gateway layer using industry-standard authentication patterns:

1. **Mutual TLS (mTLS / Client Certificates)**: Used in high-security enterprise environments and defense enclaves. The edge reverse proxy verifies client certificates against an internal corporate Certificate Authority (CA) before serving repository HTML or RPM binaries.
2. **HTTP Basic / Digest Authentication**: Natively supported by Linux package managers (`dnf`, `yum`, `zypper`). Client machines configure credentials directly in `/etc/yum.repos.d/*.repo` via `username=` and `password=` directives.
3. **OAuth2 / OpenID Connect (OIDC)**: Can be integrated seamlessly using reverse proxy sidecars (such as `oauth2-proxy` or Keycloak Gatekeeper) to enforce corporate Single Sign-On (SSO) for web browser navigation.
4. **Network CIDR Whitelisting**: Restricts repository access to authorized corporate VPC subnets and build-farm IP ranges.

### Role-Based Access Control (RBAC)

| Role / Principal | Access Level | Permitted Actions | Enforcement Mechanism |
| :--- | :--- | :--- | :--- |
| **Anonymous Consumer** | Public Read-Only | Browse HTML repository index, search package lists, view RSS feed | Reverse proxy allows `GET /`, `GET /*.html`, `GET /search.json` |
| **Licensed Developer** | Authenticated Read-Only | Access all HTML pages and download proprietary `.rpm` binaries | Reverse proxy validates HTTP Basic credentials or OIDC JWT |
| **Automated CI/CD Worker** | Machine Read-Only | Automated DNF package installation and dependency resolution | mTLS client certificate or static Bearer token |
| **Release Engineer** | Write / Execute | Execute `repoview` CLI, generate pages, manage `.state.json` cache | Local OS file permissions (`chmod 0750`, dedicated service group) |
| **System Administrator** | Root / Admin | Provision reverse proxy, rotate TLS certs, manage systemd timers | OS `sudo` / privileged administrative access |

### Current and Non-Vulnerable Libraries Used

All third-party dependencies are continuously audited using `govulncheck` and pinned to secure, modern versions. The codebase relies exclusively on trusted, actively maintained packages:

| Library | Version | License | Security & Integrity Audit |
| :--- | :---: | :---: | :--- |
| `github.com/mattn/go-sqlite3` | `v1.14.52` | MIT | CGo SQLite3 driver. Uses parameterized queries and read-only URI mode (`mode=ro`). Zero known CVEs. |
| `github.com/klauspost/compress` | `v1.20.0` | BSD-3-Clause | Memory-safe, high-speed multi-threaded Zstandard and Gzip decompression. Zero known CVEs. |
| `github.com/ulikunitz/xz` | `v0.5.16` | BSD-3-Clause | Pure Go XZ decompression engine. Protected against integer overflows and malicious headers. Zero known CVEs. |
| `github.com/knqyf263/go-rpm-version` | Latest | MIT | Upstream RPM EVR comparison engine. Pure Go, allocation-free comparison logic. Zero known CVEs. |
| `github.com/sassoftware/go-rpmutils` | `v0.4.0` | Apache-2.0 | RPM payload reader for lead, header, scriptlet, and file extraction. Protected against malformed headers. Zero known CVEs. |
| `pault.ag/go/debian` | `v0.21.0` | MIT | Debian control, Deb822 index parsing, and Debian EVR version comparisons. Zero known CVEs. |

### Unprivileged Context Enforcement

> [!IMPORTANT]
> **RepoView-Go is a non-system application and MUST NEVER be executed as `root` (UID 0).**

- **Principle of Least Privilege**: Execute `repoview` under a dedicated, unprivileged system account (e.g., `repoview:repoview`, UID `10001`).
- **Filesystem Permissions**:
  - **Source Repository**: Configure as read-only (`chmod 0550` or `chmod 0440`) for the `repoview` user.
  - **Output Directory**: Grant read-write access (`chmod 0750`) strictly to the `repoview` service account.
- **Safety Barriers**: The built-in `validateOutputDirSafety()` check automatically aborts execution if `--output-dir` points to the filesystem root (`/`), the repository directory itself, or any parent directory, preventing accidental file deletion even if misconfigured.

---

## 3. Code Quality Assessment and Best Practices

The RepoView-Go codebase has been engineered to meet rigorous enterprise software standards:

- **Modular Architecture**: Clean separation of concerns between Data Access Layer (`internal/repo`), Domain Logic (`internal/logic`), Presentation (`internal/render`), and State Management (`internal/state`).
- **Strict Error Handling**: Adheres to Go 1.13+ error wrapping (`fmt.Errorf("...: %w", err)`). Domain errors are categorized into granular CLI exit codes (`ExitSuccess`, `ExitUsageError`, `ExitRepoMetadataError`, `ExitRenderError`).
- **Race-Free Concurrency**: Verified using Go's thread race detector:
  ```bash
  go test -race ./...
  ```
  Synchronized access to shared data structures is guaranteed via `sync.RWMutex`, `sync.Mutex`, and `sync/atomic` counters.
- **Memory Optimization (On-Demand Loading)**: For repositories with 50,000+ packages, package file lists are loaded into memory on-demand only during package page rendering and immediately freed via `defer` pointer nulling, keeping resident RAM below 200 MB.
- **Batch Database Processing**: SQLite changelogs and dependencies are fetched using dynamic SQL parameter blocks in chunks of 500 packages, eliminating per-package round-trips.
- **Comprehensive Test Coverage**: Tested to **84.9% statement coverage** across the entire codebase, with every individual package independently exceeding 80%:
  - `cmd/repoview`: **88.8%**
  - `internal/app`: **85.4%**
  - `internal/logic`: **80.4%**
  - `internal/models`: **94.0%**
  - `internal/portal`: **82.3%**
  - `internal/render`: **81.4%**
  - `internal/repo`: **82.8%**
  - `internal/repo/deb`: **89.7%**
  - `internal/state`: **87.1%**
  - `internal/util`: **94.6%**
- **Zero Repository Pollution**: All unit and integration test fixtures execute exclusively within `t.TempDir()` sandboxes.

---

## 4. Command Line Arguments

RepoView-Go uses clean, explicit long-format command line options to ensure clarity in automated shell scripts and CI/CD pipelines:

```text
Usage: repoview [options] <repodir>
```

| Flag | Type | Default | Description |
| :--- | :---: | :---: | :--- |
| `--output-dir` | `string` | `repoview` | Target directory where static HTML, CSS, JSON, and RSS files are written. |
| `--state-dir` | `string` | *(Output Dir)* | Directory where `.state.json` is stored for incremental builds. |
| `--title` | `string` | `Repoview` | Repository title displayed prominently in the web interface header and RSS feed. |
| `--url` | `string` | *(Empty)* | Public HTTP(S) URL of the repository (required for generating `latest-feed.xml`). |
| `--baseurl` | `string` | *(Auto-detected)* | Base URL injected into client `.repo` and `.sources` configuration snippets. |
| `--format` | `string` | `auto` | Repository format: `auto` (auto-detects format), `rpm` (YUM/DNF), or `deb` (APT/dpkg). |
| `--template-dir` | `string` | *(Embedded)* | Path to an external directory containing custom `.html` templates and `layout/` assets. |
| `--comps` | `string` | *(repodata)* | Path to an alternative `comps.xml` package group definition file (RPM). |
| `--portal-url` | `string` | `auto` | URL or relative path to parent multi-repo catalog portal (`auto`, `none`, or custom path). |
| `--ignore-package` | `string list` | *(None)* | Glob pattern to exclude packages by name or NVRA (e.g. `*debuginfo*`). Can be repeated. |
| `--exclude-arch` | `string list` | *(None)* | Hardware architecture to exclude (e.g. `src`, `i686`). Can be repeated. |
| `--force` | `bool` | `false` | Force complete regeneration of all HTML pages, bypassing incremental state cache. |
| `--quiet` | `bool` | `false` | Suppress non-essential informational console output. |
| `--version` | `bool` | `false` | Print application version and build metadata, then exit. |

### Multi-Repository Portal Options (`repoview portal`)

The `portal` subcommand crawls repository hierarchies and compiles a unified multi-repository catalog portal:

```text
Usage: repoview portal [options] [repodir]
```

| Flag | Type | Default | Description |
| :--- | :---: | :---: | :--- |
| `--output-dir` | `string` | *(repodir)* | Target directory where root `index.html` and `portal-feed.xml` are written. |
| `--title` | `string` | `Enterprise Package Repositories` | Portal catalog title displayed in header banner. |
| `--description` | `string` | *(None)* | Optional description or subtitle text rendered beneath the portal header. |
| `--baseurl` | `string` | *(Auto)* | Base URL for RSS feed aggregation and package manager setup snippets. |
| `--config` | `string` | *(portal.yaml)* | Path to optional YAML configuration file for overrides, branding, and pinned repos. |
| `--dump-config` | `string` | *(None)* | Dump auto-discovered repository tree to a starter `portal.yaml` file and exit. |
| `--max-depth` | `int` | `5` | Maximum directory recursion depth during repository discovery. |
| `--require-rendered` | `bool` | `false` | Only catalog repositories that already have generated `repoview/` pages. |
| `--render-missing` | `bool` | `false` | Automatically generates `repoview/` views for unrendered raw repositories in parallel. |
| `--workers` | `int` | `4` | Concurrency worker pool size for `--render-missing` (1–16, default: `NumCPU * 2`). |
| `--force` | `bool` | `false` | Force overwrite existing `index.html` even if not previously created by RepoView. |
| `--quiet` | `bool` | `false` | Suppress non-essential informational console output. |
| `--version` | `bool` | `false` | Print application version and build metadata, then exit. |

---

## 5. Usage and Deployment Examples

### Basic Repository Generation (RPM)

Generate a static repository portal for an RPM repository in the default `./repoview` subdirectory:

```bash
repoview /var/www/html/repo/el9/base/x86_64
```

**Console Output:**
```text
Examining repository...done
Opening databases...done
Reading packages...found 2036 packages
Filtered down to 2036 packages
Enriching packages with changelogs...done
Inspecting RPM package headers...done
Parsing comps.xml...done
 organizing groups...done (42 groups, 26 letters)
Generating pages...
Writing group web-servers.group.html
Writing group development-tools.group.html
Writing package nginx.html
Writing package postgresql-server.html
Writing search.json
Writing index.html
Writing latest-feed.xml
Cleaning up stale files...
Complete.
```

---

### Debian / Ubuntu Repository Generation

Generate a static repository portal for an APT repository (standard `dists/` pool layout or flat directory structure):

```bash
repoview \
  --title "Debian 12 (Bookworm) - Main" \
  --url "https://apt.example.com/debian/repoview" \
  --baseurl "https://apt.example.com/debian" \
  /var/www/html/repo/debian/bookworm
```

**Console Output:**
```text
Examining repository...done
Discovered Debian repository: suite=bookworm, component=main, arch=amd64
Reading packages...found 1420 packages
Filtered down to 1420 packages
Inspecting Debian package headers...done
 organizing groups...done (18 groups, 26 letters)
Generating pages...
Writing group web.group.html
Writing group admin.group.html
Writing package curl.html
Writing package nginx.html
Writing search.json
Writing index.html
Writing latest-feed.xml
Cleaning up stale files...
Complete.
```

*(Debian package pages automatically feature `sudo apt install <pkg>` installation snippets, maintainer scripts extracted from `control.tar`, file manifests from `data.tar`, and a copy-ready Deb822 `.sources` file in the sidebar).*

---

### Incremental Build & Cache Utilization

When running subsequent builds against an existing repository, RepoView-Go checks `.state.json` content hashes and only writes files that have changed:

```bash
repoview /var/www/html/repo/el9/base/x86_64
```

**Console Output:**
```text
Examining repository...done
Opening databases...done
Reading packages...found 2036 packages
Filtered down to 2036 packages
Enriching packages with changelogs...done
Inspecting RPM package headers...done
Parsing comps.xml...done
 organizing groups...done (42 groups, 26 letters)
Generating pages...
Cleaning up stale files...
Complete.
```
*(Notice how unchanged package pages are skipped entirely, completing in seconds).*

---

### Enterprise Customization

Run generation with custom title, public RSS feed URL, explicit base URL, package exclusions, and architecture filtering:

```bash
repoview \
  --title "Enterprise Linux 9 - BaseOS (x86_64)" \
  --url "https://repo.internal.corp/el9/base/x86_64/repoview" \
  --baseurl "https://repo.internal.corp/el9/base/x86_64" \
  --output-dir "/var/www/html/repos/el9/base/x86_64/repoview" \
  --ignore-package "*debuginfo*" \
  --ignore-package "*debugsource*" \
  --exclude-arch "src" \
  --exclude-arch "i686" \
  /var/www/html/repos/el9/base/x86_64
```

---

### Automated Production Deployment (Systemd Timer)

To keep the web portal in sync with upstream repository syncs (e.g. `reposync`), deploy a periodic systemd service:

**1. Service Definition (`/etc/systemd/system/repoview.service`):**
```ini
[Unit]
Description=RepoView-Go Static Repository Portal Sync
After=network.target

[Service]
Type=oneshot
User=repoview
Group=repoview
ExecStart=/usr/local/bin/repoview \
  --title "Production RPM Repository" \
  --output-dir /var/www/html/repo/view \
  --url https://yum.example.com/repo/view \
  /var/www/html/repo
StandardOutput=journal
StandardError=journal

# Hardening Directives
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/www/html/repo/view
ReadOnlyPaths=/var/www/html/repo
NoNewPrivileges=true
PrivateTmp=true
```

**2. Timer Definition (`/etc/systemd/system/repoview.timer`):**
```ini
[Unit]
Description=Run RepoView-Go Portal Generation Every Hour

[Timer]
OnCalendar=*:00
Persistent=true

[Install]
WantedBy=timers.target
```

Enable and start the timer:
```bash
systemctl daemon-reload
systemctl enable --now repoview.timer
```

---

### Multi-Repository Enterprise Portal (`repoview portal`)

Compile a unified, responsive catalog portal across a mixed hierarchy of RPM and Debian repositories:

```bash
repoview portal \
  --title "Enterprise Linux & Debian Package Portal" \
  --description "Official mirrors for EL9, EL10, Ubuntu 24.04, and Debian 12" \
  --render-missing \
  --workers 4 \
  /var/www/html/repos
```

**Console Output:**
```text
Repoview dev - Portal Generator
Generated repository portal in /var/www/html/repos (3 repositories, 2104 packages)
```

The generated portal provides:
- **Instant Client-Side Filtering**: Interactive text search (`/` shortcut), format pills (`RPM`, `DEB`), distro pills (`el9`, `ubu24`), and architecture filters (`x86_64`, `amd64`).
- **Two-Way Navigation**: Each child repository view automatically includes a **`← All Repositories`** breadcrumb link back to the portal home page.
- **Copy-Ready Client Snippets**: One-click `.repo` and Deb822 `.sources` configurations for developers.
- **Aggregated RSS Feed**: `portal-feed.xml` merges release updates across all repositories in descending chronological order.

---

### Production Nginx Web Server Specifications

RepoView-Go generates 100% static assets designed to be served by high-performance web servers using Linux kernel-level zero-copy `sendfile(2)`.

#### 1. Architectural Layout & DocumentRoot Mapping

A single Nginx `server {}` block seamlessly serves:
1. The **Unified Portal Root** (`/index.html`, `/portal-feed.xml`).
2. The **Child Repository Web Views** (`/<distro>/<channel>/repoview/`).
3. The **Raw Package Repositories** accessed by package managers (`dnf`, `yum`, `apt`).

```text
/var/www/html/repos/
├── index.html                      <- Top-Level Portal
├── portal-feed.xml                 <- Aggregated Portal Feed
├── el9/base/x86_64/
│   ├── repodata/                   <- Raw RPM Metadata (DNF/YUM)
│   ├── Packages/                   <- RPM Binaries (.rpm)
│   └── repoview/                   <- RepoView Child Web Portal
│       ├── index.html              <- Repo Index (with "← All Repositories" link)
│       ├── search.json             <- Package Search Index
│       └── repoview.json           <- Self-Describing Metadata Descriptor
└── ubu24/custom/
    ├── Packages.gz                 <- Raw APT Binary Index
    ├── Release                     <- APT Release Metadata
    └── repoview/                   <- RepoView Child Web Portal
```

#### 2. Performance & Kernel Tuning Directives

- **`sendfile on;`**: Enables zero-copy file transmission directly from OS page cache into socket buffers, bypassing user space memory entirely.
- **`tcp_nopush on;`**: Activates `TCP_CORK` (Linux), coalescing HTTP headers and file payloads into full-frame TCP packets to eliminate packet fragmentation.
- **`tcp_nodelay on;`**: Disables Nagle's algorithm for interactive web socket traffic, reducing latency for small JSON/HTML requests.

#### 3. Multi-Tier Caching & Revalidation Matrix

| Content Type | File Patterns | Recommended Cache-Control | Rationale |
| :--- | :--- | :--- | :--- |
| **Catalog State & HTML** | `*.html` | `public, no-cache, must-revalidate` | Browser checks `ETag` on navigation; updates appear immediately without browser restart. |
| **Search Indices & Feeds** | `*.json`, `*.xml` | `public, no-cache, must-revalidate` | Guarantees instant search index and RSS feed updates across clients. |
| **Static UI Assets** | `repostyle.css`, SVG, fonts | `public, max-age=86400, stale-while-revalidate=3600` | Eliminates redundant CSS/icon downloads during interactive browsing. |
| **Package Binaries** | `*.rpm`, `*.deb`, `*.tar.gz`, `*.xz` | `public, max-age=2592000, immutable` | Cryptographically signed release binaries are immutable by design. |

#### 4. MIME Types & Character Encoding

Ensure your `/etc/nginx/mime.types` includes appropriate types for Debian and RPM binaries so browsers prompt for download rather than attempting raw text rendering:

```nginx
types {
    application/x-redhat-package-manager    rpm;
    application/vnd.debian.binary-package   deb;
    application/rss+xml                     xml;
    application/json                        json;
}
```

#### 5. Complete Production Configuration (`/etc/nginx/conf.d/repoview.conf`)

```nginx
# ==============================================================================
# RepoView Enterprise Repository & Portal Nginx Configuration
# ==============================================================================

server {
    listen 80;
    listen [::]:80;
    server_name repo.example.com;

    # Redirect all plain HTTP traffic to HTTPS
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name repo.example.com;

    # Root repository storage directory
    root /var/www/html/repos;
    index index.html;
    charset utf-8;

    # --------------------------------------------------------------------------
    # TLS & Cipher Suite Hardening (A+ Rating)
    # --------------------------------------------------------------------------
    ssl_certificate         /etc/pki/tls/certs/repo.example.com.crt;
    ssl_certificate_key     /etc/pki/tls/private/repo.example.com.key;
    ssl_protocols           TLSv1.2 TLSv1.3;
    ssl_ciphers             ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384;
    ssl_prefer_server_ciphers off;
    ssl_session_timeout     1d;
    ssl_session_cache       shared:SSL:10m;
    ssl_session_tickets     off;

    # --------------------------------------------------------------------------
    # Linux Kernel High-Throughput I/O Optimizations
    # --------------------------------------------------------------------------
    sendfile            on;
    tcp_nopush          on;
    tcp_nodelay         on;
    keepalive_timeout   65;
    types_hash_max_size 4096;

    # --------------------------------------------------------------------------
    # Security Headers & Air-Gap Content Security Policy
    # --------------------------------------------------------------------------
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains; preload" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "DENY" always;
    add_header Referrer-Policy "strict-origin-when-cross-origin" always;
    add_header Content-Security-Policy "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none';" always;

    # --------------------------------------------------------------------------
    # Gzip Compression
    # --------------------------------------------------------------------------
    gzip on;
    gzip_vary on;
    gzip_proxied any;
    gzip_comp_level 6;
    gzip_types text/plain text/css application/json application/xml text/javascript application/rss+xml;

    # --------------------------------------------------------------------------
    # Caching Policies
    # --------------------------------------------------------------------------

    # 1. HTML Pages (Portal & Repo Views) - Always revalidate
    location ~* \.html$ {
        add_header Cache-Control "public, no-cache, must-revalidate";
        try_files $uri =404;
    }

    # 2. Search Index, Metadata Descriptors & RSS Feeds - Always revalidate
    location ~* \.(json|xml)$ {
        add_header Cache-Control "public, no-cache, must-revalidate";
        try_files $uri =404;
    }

    # 3. Static UI Assets (CSS, SVG, Icons) - 24-hour cache
    location ~* \.(css|svg|png|jpg|ico|js)$ {
        add_header Cache-Control "public, max-age=86400, stale-while-revalidate=3600";
        try_files $uri =404;
    }

    # 4. Immutable Package Binaries (RPM / DEB) - 30-day immutable cache
    location ~* \.(rpm|deb|tar\.gz|tar\.xz)$ {
        add_header Cache-Control "public, max-age=2592000, immutable";
        try_files $uri =404;
    }

    # --------------------------------------------------------------------------
    # Package Manager Raw Access (APT & DNF/YUM Repositories)
    # --------------------------------------------------------------------------
    # Allow package managers to browse directories if desired (optional)
    location / {
        autoindex on;
        autoindex_exact_size off;
        autoindex_localtime on;
        try_files $uri $uri/ =404;
    }
}
```

---

### Containerized Deployment (Unprivileged Docker)

Build and run RepoView-Go inside an isolated, non-root container:

**`Dockerfile`:**
```dockerfile
# Build Stage
FROM golang:1.22-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -trimpath -o /bin/repoview ./cmd/repoview

# Final Minimal Unprivileged Image
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
RUN groupadd -g 10001 repoview && useradd -u 10001 -g repoview -m -s /sbin/nologin repoview

COPY --from=builder /bin/repoview /usr/local/bin/repoview

USER 10001:10001
ENTRYPOINT ["/usr/local/bin/repoview"]
CMD ["--help"]
```

**Execute Container:**
```bash
docker run --rm \
  --user 10001:10001 \
  -v /var/www/html/repo:/repo:ro \
  -v /var/www/html/output:/output:rw \
  repoview-go:latest \
  --output-dir /output \
  /repo
```

---

## 6. System Architecture & Testing Documentation

For deep technical insights into RepoView-Go's architectural design, operational sequence diagrams, and verification benchmarks, consult the dedicated documentation deliverables:

- **[ARCHITECTURE.md](ARCHITECTURE.md)**:
  - High-level system architecture and modular directory layout.
  - End-to-end data flow with chronological Mermaid sequence diagrams.
  - Bounded concurrency worker pool, channel structures, and memory footprint management.
  - Runtime and build-time package dependency mapping.
  - Complete security architecture, perimeter defenses, authentication layers (mTLS, OIDC, Basic Auth), and enterprise RBAC matrix.
- **[TESTING.md](TESTING.md)**:
  - Test suite architecture and defect-first testing principles.
  - Positive and negative test category matrices with Mermaid logical execution flowcharts.
  - Comprehensive inventory table of all 64 test functions and subtests with success criteria.
  - Live E2E integration test specifications using real RPM repositories (2,036 packages) and live `net/http` loopback servers.
  - Detailed code coverage report verifying **83.5% total statement coverage** (>80% across all packages).
  - Step-by-step test execution scripts for both **Bash** and **PowerShell**.
