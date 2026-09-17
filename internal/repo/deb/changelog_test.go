package deb

import (
	"strings"
	"testing"
)

func TestParseChangelog(t *testing.T) {
	raw := `repoview (1.2.0-1) unstable; urgency=medium

  * Add full Debian package and repository support.
  * Improve version comparison performance.

 -- Maintainer Name <maintainer@example.com>  Thu, 17 Sep 2026 10:00:00 +0000

repoview (1.1.0-1) unstable; urgency=low

  * Initial release.

 -- Maintainer Name <maintainer@example.com>  Wed, 16 Sep 2026 10:00:00 +0000
`

	entry, err := ParseChangelog(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("unexpected error parsing changelog: %v", err)
	}
	if entry == nil {
		t.Fatalf("expected changelog entry, got nil")
	}

	if !strings.Contains(entry.Author, "Maintainer Name") {
		t.Errorf("expected author to contain Maintainer Name, got %s", entry.Author)
	}
	if !strings.Contains(entry.Changelog, "Add full Debian package") {
		t.Errorf("expected changelog text to contain changes, got %s", entry.Changelog)
	}
	if entry.Date == 0 {
		t.Errorf("expected non-zero timestamp, got %d", entry.Date)
	}
}

func TestParseChangelog_Empty(t *testing.T) {
	entry, err := ParseChangelog(strings.NewReader(""))
	if err == nil && entry != nil {
		t.Errorf("expected error or nil for empty changelog, got %v", entry)
	}
}
