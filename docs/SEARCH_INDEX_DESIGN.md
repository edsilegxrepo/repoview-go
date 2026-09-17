# Search Index Architecture and Specification for Repoview-Go

## 1. Objective
To provide instant, client-side search functionality for the generated static website without requiring a backend server. This is achieved by generating a lightweight `search.json` index file containing metadata for all packages in the repository.

## 2. Technical Implementation

### 2.1. Output Format (`search.json`)
The output is a JSON object containing a schema definition and a compact array of arrays (rows). This "Structure of Arrays" approach eliminates repetitive keys, reducing file size by ~30-40%.

**Schema:**
```json
{
  "schema": ["n", "v", "a", "s", "f"],
  "data": [
    ["bash", "5.1.8-2.fc34", "x86_64", "The GNU Bourne Again shell", "bash.html"],
    ...
  ]
}
```

**Optimization Strategy:**
- **Row-based format**: Removes key overhead (`"n":`, `"v":`) for every single package.
- **Compression**: Extremely friendly to Gzip/Brotli.
- **Content**: Includes only Name, EVR, Arch, Summary, Filename.

### 2.2. Generation Logic
The `search.json` generation is integrated into the `Generator.Run` workflow in `internal/app/generator.go`.

**Integration Point:**
It runs during the index rendering phase (`renderIndices`) immediately after package page generation.

**Logic:**
1.  Iterate over the deduplicated list of **latest** package versions (sorted by build time and name).
2.  Extract fields: Name, EVR, Arch, Summary, Filename.
3.  Serialize to JSON via `renderer.RenderSearchIndex`.
4.  Check state changes with `stateStore.HasChanged("search.json", searchContent)` and write to `output_dir/search.json`.
5.  Register `search.json` in `generatedFiles` for orphan cleanup tracking.

### 2.3. CLI Options
Search indexing is enabled by default for all repositories with zero required configuration.

## 3. Client-Side Integration
The frontend search engine is implemented in embedded JavaScript (`internal/render/templates/layout/search.js`) and styled in `layout/repostyle.css`.

**Frontend Workflow:**
1.  A Search input box (`#pkgSearchInput`) is embedded in the header of all pages (`index.html`, `group.html`, `package.html`).
2.  The `search.js` engine provides:
    *   **Lazy Loading**: Fetches `search.json` on search input focus or global keyboard shortcut (`/`, `Ctrl+K`, `Cmd+K`).
    *   **Real-time Filtering**: Filters rows with sub-millisecond latency matching package Name (primary) and Summary (secondary).
    *   **Regex Search**: Supports `/pattern/` queries with bounded lengths and ReDoS protection.
    *   **Security**: Sanitizes all rendered metadata via `escapeHTML()` before DOM insertion to prevent XSS.
    *   **Result Limit**: Caps results to 50 items to maintain high rendering performance.

## 4. Performance & Memory Considerations

### 4.1. Client-Side Memory Consumption
For large repositories, the memory footprint is linear to the number of packages but highly optimized due to the "Row-based" format (removing ~40% overhead compared to object arrays).

*   **10,000 packages**: ~1.5 MB raw JSON (~5 MB in browser memory). **Negligible impact.**
*   **50,000 packages**: ~7.5 MB raw JSON (~25 MB in browser memory). **Safe for modern devices.**
*   **100,000+ packages**: ~15 MB+ raw JSON. This is the threshold where mobile devices might stutter during initial parsing, but desktop performance remains acceptable.

### 4.2. Indexed vs. Searchable Fields
The `search.json` index contains specific fields optimized for display and linking, but only a subset are actively queried to maintain search loop performance.

**Indexed Fields (Stored):**
1.  **Name** (e.g., `bash`)
2.  **EVR (Epoch-Version-Release)** (e.g., `5.1.8-2` or `1:2.0-1`)
3.  **Architecture** (e.g., `x86_64`)
4.  **Summary** (e.g., `The GNU Bourne Again shell`)
5.  **Filename** (Link target)

**Searchable Fields (Queried):**
*   **Name** (Primary match)
*   **Summary** (Secondary match)

**Search Syntax:**
*   **Simple Search**: Standard case-insensitive substring match (e.g., `bash` matches `bash`, `bash-completion`).
*   **Regex Search**: Advanced pattern matching using JavaScript Regex syntax by wrapping query in slashes (e.g., `/^kernel/` for packages starting with kernel, `/python|perl/` for either).

*Note: Version and Architecture are displayed in results but not searched to keep the filtering loop tight.*

## 5. Pros & Cons

**Pros:**
*   **Zero Dependencies**: No external search service (Algolia, Elasticsearch) required.
*   **Fast**: Instant feedback for users.
*   **Static**: Compatible with the static hosting model.

**Cons:**
*   **File Size**: For repositories with 50k+ packages, the JSON file could reach 5-10MB. Gzip/Brotli compression by the web server usually mitigates this (reducing it to ~1-2MB).

## 6. Implementation Status
All search indexing components are fully implemented and verified in the test suite:
1.  **Data Model**: Defined `models.SearchIndex` in `internal/models/search.go` with columnar schema and row data container.
2.  **Renderer**: Implemented `Renderer.RenderSearchIndex` in `internal/render/renderer.go`.
3.  **Generator**: Integrated search indexing into `Generator.renderIndices` in `internal/app/generator.go` with incremental state tracking.
4.  **Client UI**: Embedded interactive `search.js` with keyboard navigation, live debounce, and ReDoS protection in `internal/render/templates/layout/search.js`.
5.  **Test Verification**: Covered by unit tests (`internal/render/renderer_test.go`), pipeline tests (`internal/app/generator_unit_test.go`), and live HTTP integration tests (`tests/integration/e2e_test.go`).
