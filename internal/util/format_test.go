package util

import (
	"strings"
	"testing"
)

// TEST STRATEGY:
// Validate formatting helpers against boundary conditions, scale transitions,
// and edge-case inputs across byte sizing, temporal conversion, and rune manipulation.
//
// Coverage scope:
//   - TestHumanSize: Exact byte boundaries (<1024, 1 KiB, 2 KiB, 1 MiB, fractional MiB).
//   - TestRSSTime: Exact Unix epoch conversion to RFC1123Z with strict UTC time zone verification (+0000).
//   - TestFirstLetter: Multibyte Unicode runes, whitespace trimming, and empty string handling.

// TestHumanSize validates that byte counts are correctly converted to appropriate human-readable units.
func TestHumanSize(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{500, "500 Bytes"},
		{1024, "1 KiB"},
		{2048, "2 KiB"},
		{1048576, "1.0 MiB"},
		{19922944, "19.0 MiB"},
	}

	for _, tc := range tests {
		got := HumanSize(tc.bytes)
		if got != tc.expected {
			t.Errorf("HumanSize(%d) = %q; want %q", tc.bytes, got, tc.expected)
		}
	}
}

// TestRSSTime validates that Unix timestamps are properly transformed into standard RFC1123Z UTC strings.
func TestRSSTime(t *testing.T) {
	// 1612800000 = Mon, 08 Feb 2021 16:00:00 UTC
	stamp := int64(1612800000)
	got := RSSTime(stamp)
	if !strings.HasSuffix(got, "+0000") {
		t.Errorf("RSSTime(%d) = %q; want UTC suffix +0000", stamp, got)
	}
	expected := "Mon, 08 Feb 2021 16:00:00 +0000"
	if got != expected {
		t.Errorf("RSSTime(%d) = %q; want %q", stamp, got, expected)
	}
}

// TestFirstLetter validates Unicode-safe extraction and casing of the initial character.
func TestFirstLetter(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"kernel", "K"},
		{"389-ds-base", "3"},
		{"  glibc", "G"},
		{"", "?"},
		{"   ", "?"},
		{"über-tool", "Ü"},
	}

	for _, tc := range tests {
		got := FirstLetter(tc.input)
		if got != tc.expected {
			t.Errorf("FirstLetter(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}
