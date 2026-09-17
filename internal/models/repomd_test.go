package models

import (
	"encoding/xml"
	"strings"
	"testing"
)

// TEST STRATEGY:
// Validate XML unmarshaling of repomd.xml root elements and <data> records.

func TestRepomdUnmarshaling(t *testing.T) {
	rawXML := `<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo" revision="1695000000">
  <data type="primary_db">
    <checksum type="sha256">abcdef1234567890</checksum>
    <location href="repodata/primary.sqlite.gz"/>
    <timestamp>1695000000</timestamp>
    <size>1048576</size>
    <database_version>10</database_version>
  </data>
  <data type="other_db">
    <checksum type="sha256">1234567890abcdef</checksum>
    <location href="repodata/other.sqlite.gz"/>
    <timestamp>1695000000</timestamp>
    <size>524288</size>
  </data>
</repomd>`

	var r Repomd
	if err := xml.NewDecoder(strings.NewReader(rawXML)).Decode(&r); err != nil {
		t.Fatalf("failed to decode repomd XML: %v", err)
	}

	if r.Revision != "1695000000" {
		t.Errorf("expected revision '1695000000', got %q", r.Revision)
	}
	if len(r.Data) != 2 {
		t.Fatalf("expected 2 data records, got %d", len(r.Data))
	}
	if r.Data[0].Type != "primary_db" || r.Data[0].Location.Href != "repodata/primary.sqlite.gz" {
		t.Errorf("unexpected primary_db record: %+v", r.Data[0])
	}
	if r.Data[0].Checksum.Value != "abcdef1234567890" || r.Data[0].DatabaseVersion != "10" {
		t.Errorf("unexpected checksum or db version: %+v", r.Data[0])
	}
}
