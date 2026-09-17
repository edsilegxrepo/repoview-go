package models

import (
	"encoding/json"
	"testing"
)

// TEST STRATEGY:
// Validate JSON serialization and deserialization of the compact search index structure.

func TestSearchIndexSerialization(t *testing.T) {
	idx := SearchIndex{
		Schema: []string{"n", "v", "a", "s", "f"},
		Data: [][]string{
			{"nginx", "1.20.1-1.el9", "x86_64", "High performance web server", "nginx.html"},
			{"bash", "5.1-2.el9", "x86_64", "The GNU Bourne Again shell", "bash.html"},
		},
	}

	data, err := json.Marshal(idx)
	if err != nil {
		t.Fatalf("failed to marshal SearchIndex: %v", err)
	}

	var decoded SearchIndex
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal SearchIndex: %v", err)
	}

	if len(decoded.Schema) != 5 || decoded.Schema[0] != "n" {
		t.Errorf("schema mismatch: %v", decoded.Schema)
	}
	if len(decoded.Data) != 2 || decoded.Data[0][0] != "nginx" {
		t.Errorf("data mismatch: %v", decoded.Data)
	}
}
