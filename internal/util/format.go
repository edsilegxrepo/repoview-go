// Package util provides general-purpose string, byte formatting, temporal conversion,
// and filename sanitization helpers utilized throughout repoview-go.
//
// OBJECTIVES:
// Deliver lightweight, allocation-conscious formatting routines for byte sizes,
// timestamps, and character manipulation required by HTML templates and XML feeds.
//
// CORE COMPONENTS:
//   - HumanSize: Converts raw byte counts into human-readable binary units (KiB, MiB, GiB, TiB).
//   - FirstLetter: Extracts uppercase leading rune for alphabet indexing.
//   - YMD: Formats Unix timestamps to ISO calendar dates (YYYY-MM-DD).
//   - RSSTime: Formats Unix timestamps into RFC1123Z UTC strings for RSS 2.0.
//
// FUNCTIONALITY:
//   - Formats file and package sizes with appropriate precision across 5 magnitude scales.
//   - Handles multibyte Unicode character boundaries safely when extracting alphabet letters.
//   - Converts Unix epoch timestamps into standardized temporal representations.
//
// DATA FLOW:
//
//	int64 / string -> Format functions -> Display strings rendered into HTML/RSS
package util

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// HumanSize returns the size in human-readable units (Bytes, KiB, MiB, GiB, TiB).
// It formats sizes below 1 KiB as exact bytes, KiB without fractional decimals,
// and MiB, GiB, and TiB with 1 or 2 fractional decimal digits.
// Python equivalent: _humansize
func HumanSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d Bytes", bytes)
	}
	const (
		kib = 1024
		mib = kib * 1024
		gib = mib * 1024
		tib = gib * 1024
	)
	switch {
	case bytes >= tib:
		return fmt.Sprintf("%.2f TiB", float64(bytes)/float64(tib))
	case bytes >= gib:
		return fmt.Sprintf("%.2f GiB", float64(bytes)/float64(gib))
	case bytes >= mib:
		return fmt.Sprintf("%.1f MiB", float64(bytes)/float64(mib))
	default:
		return fmt.Sprintf("%d KiB", bytes/kib)
	}
}

// FirstLetter extracts the first character of a string converted to uppercase,
// handling Unicode runes correctly. Returns "?" if the string is empty or only whitespace.
// Used for bucketing packages into alphabetical index navigation groups.
func FirstLetter(text string) string {
	text = strings.TrimSpace(text)
	if len(text) == 0 {
		return "?"
	}
	r := []rune(text)
	return string(unicode.ToUpper(r[0]))
}

// YMD formats a Unix timestamp into an ISO calendar date string (YYYY-MM-DD).
// Used for package build dates, changelog entries, and release tags.
// Python equivalent: _ymd
func YMD(stamp int64) string {
	t := time.Unix(stamp, 0)
	return t.Format("2006-01-02")
}

// RSSTime formats a Unix timestamp into RFC1123Z UTC format required for RSS 2.0 feeds.
// Example: "Mon, 02 Jan 2006 15:04:05 +0000".
func RSSTime(stamp int64) string {
	t := time.Unix(stamp, 0).UTC()
	return t.Format(time.RFC1123Z)
}
