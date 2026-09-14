package tui

import (
	"strings"
	"testing"
)

// A string carrying a stack trace must print as a stack trace: real line
// breaks, each continuation line indented one level past its key, in a
// literal block scalar that needs no escaping at all.
func TestRenderYAMLBreaksMultilineStrings(t *testing.T) {
	v := map[string]any{
		"message": "Error: boom\n    at handler (/app/x.js:10:5)\n    at next (/app/y.js:3:1)",
		"tab":     "a\tb",
	}
	got := RenderYAML(v, true)

	if strings.Contains(got, `\n`) {
		t.Fatalf("newlines should be real line breaks, not escaped:\n%s", got)
	}
	want := "message: |-\n  Error: boom\n      at handler (/app/x.js:10:5)\n      at next (/app/y.js:3:1)"
	if !strings.Contains(got, want) {
		t.Fatalf("multi-line string not laid out as expected:\ngot:\n%s\nwant to contain:\n%s", got, want)
	}
	// A tab is a control character, so it stays escaped inside a quoted
	// scalar — only newlines get the block-scalar treatment.
	if !strings.Contains(got, `tab: "a\tb"`) {
		t.Fatalf("tab should remain escaped:\n%s", got)
	}
}

func TestRenderYAMLPlainSortedAndIndented(t *testing.T) {
	v := map[string]any{
		"severity": "ERROR",
		"nested":   map[string]any{"b": float64(2), "a": float64(1)},
	}
	got := RenderYAML(v, true)

	// Sorted keys: "nested" before "severity"; "a" before "b".
	if strings.Index(got, "nested") > strings.Index(got, "severity") {
		t.Fatalf("keys not sorted:\n%s", got)
	}
	if strings.Index(got, "a:") > strings.Index(got, "b:") {
		t.Fatalf("nested keys not sorted:\n%s", got)
	}
	// Indentation present.
	if !strings.Contains(got, "\n  ") {
		t.Fatalf("expected 2-space indentation:\n%s", got)
	}
	// A plain identifier value needs no quoting.
	if !strings.Contains(got, "severity: ERROR") {
		t.Fatalf("plain scalars should be unquoted:\n%s", got)
	}
	// Plain mode contains no ANSI escape.
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("plain mode should have no ANSI escapes:\n%s", got)
	}
}

// A string that looks like a number, a bool, or is empty must stay quoted —
// otherwise it reads back as a different type.
func TestRenderYAMLQuotesAmbiguousStrings(t *testing.T) {
	v := map[string]any{
		"requestSize": "16",
		"flag":        "true",
		"remoteIp":    "",
	}
	got := RenderYAML(v, true)

	for _, want := range []string{`requestSize: "16"`, `flag: "true"`, `remoteIp: ""`} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q to stay quoted:\n%s", want, got)
		}
	}
	// A real number and a real bool, by contrast, render unquoted.
	v2 := map[string]any{"count": float64(16), "ok": true}
	got2 := RenderYAML(v2, true)
	if !strings.Contains(got2, "count: 16") || !strings.Contains(got2, "ok: true") {
		t.Fatalf("real numbers/bools should render unquoted:\n%s", got2)
	}
}

func TestRenderYAMLEmptyCollections(t *testing.T) {
	v := map[string]any{"queryParams": map[string]any{}, "tags": []any{}}
	got := RenderYAML(v, true)
	if !strings.Contains(got, "queryParams: {}") {
		t.Fatalf("empty map should render as {}:\n%s", got)
	}
	if !strings.Contains(got, "tags: []") {
		t.Fatalf("empty array should render as []:\n%s", got)
	}
}

func TestRenderYAMLArray(t *testing.T) {
	v := map[string]any{"items": []any{"a", "b"}}
	got := RenderYAML(v, true)
	want := "items:\n  - a\n  - b"
	if !strings.Contains(got, want) {
		t.Fatalf("array should render as a block sequence:\ngot:\n%s\nwant to contain:\n%s", got, want)
	}
}
