package tui

import (
	"strings"
	"testing"
)

// A string carrying a stack trace must print as a stack trace: real line breaks,
// each continuation line indented to the value's depth. Escaping them back to a
// literal \n (what strconv.Quote does) makes the trace unreadable.
func TestRenderJSONBreaksMultilineStrings(t *testing.T) {
	v := map[string]any{
		"message": "Error: boom\n    at handler (/app/x.js:10:5)\n    at next (/app/y.js:3:1)",
		"tab":     "a\tb",
	}
	got := RenderJSON(v, true)

	if strings.Contains(got, `\n`) {
		t.Fatalf("newlines should be real line breaks, not escaped:\n%s", got)
	}
	want := "\"message\": \"Error: boom\n      at handler (/app/x.js:10:5)\n      at next (/app/y.js:3:1)\""
	if !strings.Contains(got, want) {
		t.Fatalf("multi-line string not laid out as expected:\ngot:\n%s\nwant to contain:\n%s", got, want)
	}
	// Other control characters stay escaped — only newlines become line breaks.
	if !strings.Contains(got, `"a\tb"`) {
		t.Fatalf("tabs should remain escaped:\n%s", got)
	}
}

func TestRenderJSONPlainSortedAndIndented(t *testing.T) {
	v := map[string]any{
		"severity": "ERROR",
		"nested":   map[string]any{"b": float64(2), "a": float64(1)},
	}
	got := RenderJSON(v, true)

	// Sorted keys: "nested" before "severity"; "a" before "b".
	if strings.Index(got, "nested") > strings.Index(got, "severity") {
		t.Fatalf("keys not sorted:\n%s", got)
	}
	if strings.Index(got, `"a"`) > strings.Index(got, `"b"`) {
		t.Fatalf("nested keys not sorted:\n%s", got)
	}
	// Indentation present.
	if !strings.Contains(got, "\n  ") {
		t.Fatalf("expected 2-space indentation:\n%s", got)
	}
	// Plain mode contains no ANSI escape.
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("plain mode should have no ANSI escapes:\n%s", got)
	}
}
