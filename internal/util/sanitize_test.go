package util

import "testing"

// TEST STRATEGY:
// Verify cross-platform filename sanitization under Linux and Windows filesystem rules.
//
// Test coverage includes:
//   - Standard package names without modification.
//   - Forward and backward slashes mapped to dots.
//   - Internal spaces mapped to underscores.
//   - Prohibited Windows characters (< > : " / \ | ? *) mapped to safe underscores.
//   - Edge trimming of dots and whitespace to avoid Windows API path errors.
//   - Case-insensitive wrapping of legacy DOS device names (CON, AUX, COM1-9, LPT1-9).
//   - Empty and dot-only inputs fallback to safe "unnamed" placeholder.

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"normal-pkg", "normal-pkg"},
		{"group/subgroup", "group.subgroup"},
		{"path\\with\\backslashes", "path.with.backslashes"},
		{"name with spaces", "name_with_spaces"},
		{"colon:asterisk*question?quote\"lt<gt>pipe|", "colon_asterisk_question_quote_lt_gt_pipe_"},
		{"..leading.and.trailing..", "leading.and.trailing"},
		{"CON", "_CON_"},
		{"aux", "_aux_"},
		{"com1", "_com1_"},
		{"lpt9", "_lpt9_"},
		{"CON.pkg", "_CON.pkg_"},
		{"lpt1.tar.gz", "_lpt1.tar.gz_"},
		{"", "unnamed"},
		{"...", "unnamed"},
	}

	for _, tt := range tests {
		got := SanitizeFilename(tt.input)
		if got != tt.expected {
			t.Errorf("SanitizeFilename(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func TestSanitizeSingleLine_And_ShellQuote(t *testing.T) {
	if got := SanitizeSingleLine("My\nRepo\r\ngpgcheck=0\tTitle\x00"); got != "MyRepogpgcheck=0 Title" {
		t.Errorf("SanitizeSingleLine unexpected: %q", got)
	}

	if got := ShellQuote("nginx"); got != "nginx" {
		t.Errorf("expected safe arg unquoted, got %q", got)
	}
	if got := ShellQuote("pkg;rm -rf /"); got != "'pkg;rm -rf /'" {
		t.Errorf("expected single-quoted dangerous arg, got %q", got)
	}
	if got := ShellQuote("it's$(id)\nevil"); got != `'it'\''s$(id)evil'` {
		t.Errorf("expected escaped single quote and stripped newline, got %q", got)
	}
	if got := ShellQuote("-leading-dash"); got != "'-leading-dash'" {
		t.Errorf("expected leading dash quoted, got %q", got)
	}
	if got := ShellQuote(""); got != "''" {
		t.Errorf("expected empty string quoted as '', got %q", got)
	}
}
