package tui

import (
	"strings"
	"testing"
)

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
