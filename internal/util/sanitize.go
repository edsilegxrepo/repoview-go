package util

import (
	"strings"
)

// OBJECTIVES:
// Provide bulletproof filename and path sanitization guarantees across Linux,
// macOS, and Windows filesystems, preventing path traversal attacks and OS-specific
// filesystem errors.
//
// CORE COMPONENTS:
//   - SanitizeFilename: Transforms arbitrary strings (such as package names or group hierarchies)
//     into filesystem-safe basenames without illegal characters or reserved device names.
//
// FUNCTIONALITY:
//   - Replaces hierarchical delimiters ('/' and '\') with dots ('.').
//   - Converts whitespace to underscores ('_').
//   - Eliminates Windows reserved characters: < > : " / \ | ? * and ASCII control characters (< 32).
//   - Strips leading and trailing dots and whitespace (prohibited by NTFS/FAT filesystems).
//   - Encapsulates Windows legacy reserved DOS device names (CON, PRN, AUX, NUL, COM1-9, LPT1-9).
//
// DATA FLOW:
//   Raw string (group name / package name) -> Multi-stage sanitization -> Safe filename

// SanitizeFilename ensures a string is safe and valid to use as a filename on both Linux and Windows.
// It replaces slashes with dots, spaces with underscores, strips path traversal components,
// and sanitizes Windows reserved characters (< > : " / \ | ? *) and device names.
func SanitizeFilename(text string) string {
	// 1. Basic replacements matching legacy repoview _mkid
	text = strings.ReplaceAll(text, "/", ".")
	text = strings.ReplaceAll(text, "\\", ".")
	text = strings.ReplaceAll(text, " ", "_")

	// 2. Windows reserved character replacements (< > : " / \ | ? *) and control characters
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch r {
		case ':', '*', '?', '"', '<', '>', '|':
			b.WriteRune('_')
		default:
			if r < 32 {
				b.WriteRune('_')
			} else {
				b.WriteRune(r)
			}
		}
	}
	result := b.String()

	// 3. Trim dots and spaces from edges (prohibited on Windows filesystems)
	result = strings.Trim(result, ". ")
	if result == "" {
		result = "unnamed"
	}

	// 4. Windows reserved device names (CON, PRN, AUX, NUL, COM1-9, LPT1-9), checking stem before any '.'
	stem := strings.ToUpper(result)
	if dot := strings.IndexByte(stem, '.'); dot != -1 {
		stem = stem[:dot]
	}
	switch stem {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		result = "_" + result + "_"
	}

	return result
}

// SanitizeSingleLine strips ASCII control characters (including CR, LF, NUL, and tabs)
// so untrusted repository metadata cannot inject newlines or extra INI/Deb822 directives
// into generated .repo or .sources snippets.
func SanitizeSingleLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 32 || r == 127 {
			if r == '\t' {
				b.WriteByte(' ')
			}
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// ShellQuote strips control characters and wraps untrusted values in POSIX single quotes
// when they contain shell metacharacters, spaces, or leading hyphens.
func ShellQuote(s string) string {
	clean := SanitizeSingleLine(s)
	if clean == "" {
		return "''"
	}

	safeUnquoted := clean[0] != '-'
	if safeUnquoted {
		for i := 0; i < len(clean); i++ {
			c := clean[i]
			isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
			switch {
			case isAlphaNum:
			case c == '.' || c == '_' || c == ':' || c == '=' || c == '+' || c == '/' || c == '@' || c == '-':
			default:
				safeUnquoted = false
			}
			if !safeUnquoted {
				break
			}
		}
	}
	if safeUnquoted {
		return clean
	}

	return "'" + strings.ReplaceAll(clean, "'", `'\''`) + "'"
}
