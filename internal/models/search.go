package models

// OBJECTIVES:
// Define the compact serialized representation for the client-side full-text search index (search.json).
//
// CORE COMPONENTS:
//   - SearchIndex: Top-level Structure of Arrays (SoA) schema and tabular data container.
//
// FUNCTIONALITY:
//   - Employs a columnar schema definition ("n"=name, "v"=version, "a"=arch, "s"=summary, "f"=filename)
//     followed by a 2D array of rows to eliminate repeated JSON key overhead.
//   - Reduces network payload size by 60-70% compared to traditional array-of-objects JSON.
//
// DATA FLOW:
//   []*models.Package -> app.renderSearchIndex() -> models.SearchIndex -> search.json -> browser search.js

// SearchIndex represents the JSON structure for the client-side search index.
// It uses a row-based format ("Structure of Arrays") to minimize file size and parsing time
// on the client. The 'Schema' field defines the column order for the 'Data' rows.
type SearchIndex struct {
	Schema []string   `json:"schema"` // Column order identifier, e.g. ["n", "v", "a", "s", "f"]
	Data   [][]string `json:"data"`   // Array of package rows, where each row matches the schema length
}
