package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbeveridge/loghorn/internal/entry"
)

// req builds an entry belonging to a request, optionally a failure.
func req(id, msg string, important bool) entry.Entry {
	return entry.Entry{Message: msg, CorrelationID: id, Important: important}
}

// A stream of two interleaved requests, each with routine lines and one failure,
// plus a line belonging to no request at all.
func filterModel() Model {
	m := NewModel(nil, 1000)
	m.width, m.height = 120, 24
	return feed(m,
		req("A", "A start", false),
		req("B", "B start", false),
		req("A", "A query", false),
		req("A", "A boom", true),
		req("B", "B query", false),
		req("B", "B boom", true),
		req("", "orphan line", false),
	)
}

func messages(m Model) []string {
	out := make([]string, 0, len(m.rows))
	for _, r := range m.rows {
		out = append(out, r.Entry.Message)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Default: failures only.
func TestDefaultShowsFailuresOnly(t *testing.T) {
	m := filterModel()
	if want := []string{"A boom", "B boom"}; !equal(messages(m), want) {
		t.Fatalf("default should show failures only, got %v want %v", messages(m), want)
	}
}

// 'a' shows everything, and toggles back.
func TestAllTogglesEveryLine(t *testing.T) {
	m := filterModel()
	m, _ = key(m, "a")
	if !m.showAll {
		t.Fatalf("a should turn the failures filter off")
	}
	want := []string{"A start", "B start", "A query", "A boom", "B query", "B boom", "orphan line"}
	if !equal(messages(m), want) {
		t.Fatalf("all mode should show every line, got %v", messages(m))
	}

	m, _ = key(m, "a")
	if m.showAll {
		t.Fatalf("a should toggle back")
	}
	if want := []string{"A boom", "B boom"}; !equal(messages(m), want) {
		t.Fatalf("back to failures only, got %v", messages(m))
	}
}

// Failures keep their styling in all mode — that is what makes it useful rather
// than a wall of undifferentiated text.
func TestAllModeKeepsFailuresHighlighted(t *testing.T) {
	m := filterModel()
	m, _ = key(m, "a")
	for _, r := range m.rows {
		want := RowContext
		if r.Entry.Important {
			want = RowImportant
		}
		if r.Kind != want {
			t.Fatalf("%q: kind %v, want %v", r.Entry.Message, r.Kind, want)
		}
	}
}

// 'c' pins to the selected line's request.
func TestCorrelatePinsToSelectedRequest(t *testing.T) {
	m := filterModel()
	m, _ = key(m, "k") // hold, cursor onto "B boom" (newest failure)
	m, _ = key(m, "k") // up to "A boom"
	if got := m.rows[m.selected].Entry.Message; got != "A boom" {
		t.Fatalf("precondition: cursor should be on \"A boom\", got %q", got)
	}

	m, _ = key(m, "c")
	if m.corrID != "A" {
		t.Fatalf("c should pin to request A, got %q", m.corrID)
	}
	// Still failures-only, now within A.
	if want := []string{"A boom"}; !equal(messages(m), want) {
		t.Fatalf("pinned failures should be A's only, got %v", messages(m))
	}
}

// The two filters stack: pinned + all shows the whole request.
func TestFiltersStack(t *testing.T) {
	m := filterModel()
	m, _ = key(m, "k")
	m, _ = key(m, "k") // on "A boom"
	m, _ = key(m, "c")
	m, _ = key(m, "a")

	want := []string{"A start", "A query", "A boom"}
	if !equal(messages(m), want) {
		t.Fatalf("pinned + all should show request A entire, got %v", messages(m))
	}
	if !m.showAll || !m.pinned || m.corrID != "A" {
		t.Fatalf("both filters should be active (all=%v id=%q)", m.showAll, m.corrID)
	}

	// Dropping the all filter leaves the pin: failures within A.
	m, _ = key(m, "a")
	if want := []string{"A boom"}; !equal(messages(m), want) {
		t.Fatalf("dropping all should leave the pin, got %v", messages(m))
	}
}

// 'c' again releases the pin.
func TestCorrelateTogglesOff(t *testing.T) {
	m := filterModel()
	m, _ = key(m, "k")
	m, _ = key(m, "c")
	if !m.pinned {
		t.Fatalf("precondition: c should pin")
	}
	m, _ = key(m, "c")
	if m.pinned {
		t.Fatalf("c should release the pin, still on %q", m.corrID)
	}
	if want := []string{"A boom", "B boom"}; !equal(messages(m), want) {
		t.Fatalf("releasing should restore both failures, got %v", messages(m))
	}
}

// A line with no correlation id pins to the uncorrelated set — everything logged
// outside a request. The empty id is a real target, not an error.
func TestCorrelateOnLineWithoutIDPinsUncorrelated(t *testing.T) {
	m := NewModel(nil, 1000)
	m.width, m.height = 120, 24
	m = feed(m,
		req("A", "in a request", true),
		req("", "startup failed", true),
		req("", "shutdown failed", true),
	)
	m, _ = key(m, "k") // hold on the newest, which has no id

	m, _ = key(m, "c")
	if !m.pinned || m.corrID != "" {
		t.Fatalf("c should pin to the uncorrelated set (pinned=%v id=%q)", m.pinned, m.corrID)
	}
	if want := []string{"startup failed", "shutdown failed"}; !equal(messages(m), want) {
		t.Fatalf("should show only lines with no request, got %v", messages(m))
	}
	if !strings.Contains(m.statusBar(), "uncorrelated") {
		t.Fatalf("the bar should name the uncorrelated pin:\n%s", m.statusBar())
	}

	m, _ = key(m, "c")
	if m.pinned {
		t.Fatalf("c should release the uncorrelated pin")
	}
	if len(m.rows) != 3 {
		t.Fatalf("releasing should restore all 3 failures, got %d", len(m.rows))
	}
}

// A notice lasts exactly until the next keystroke.
func TestNoticeClearsOnNextKey(t *testing.T) {
	m := NewModel(nil, 1000) // no rows at all: nothing to correlate
	m.width, m.height = 120, 24
	m, _ = key(m, "c")
	if m.notice == "" {
		t.Fatalf("precondition: expected a notice")
	}
	if !strings.Contains(m.statusBar(), "nothing to correlate") {
		t.Fatalf("the bar should carry the notice:\n%s", m.statusBar())
	}
	m, _ = key(m, "k")
	if m.notice != "" {
		t.Fatalf("the notice should clear on the next key, still %q", m.notice)
	}
}

// Active filters are named on the bar; the default says nothing.
func TestStatusBarNamesActiveFilters(t *testing.T) {
	m := filterModel()
	if bar := m.statusBar(); strings.Contains(bar, "ALL") || strings.Contains(bar, "id:") {
		t.Fatalf("the default view should not advertise filters:\n%s", bar)
	}

	m, _ = key(m, "a")
	if bar := m.statusBar(); !strings.Contains(bar, "ALL") {
		t.Fatalf("all mode should be named on the bar:\n%s", bar)
	}
	// In all mode the newest row is the orphan line, which carries no id — step
	// up to one that does.
	m, _ = key(m, "k")
	m, _ = key(m, "k")
	if got := m.rows[m.selected].Entry.CorrelationID; got == "" {
		t.Fatalf("precondition: cursor should be on a line with an id, got %q",
			m.rows[m.selected].Entry.Message)
	}
	m, _ = key(m, "c")
	if bar := m.statusBar(); !strings.Contains(bar, "id:") {
		t.Fatalf("the pin should be named on the bar:\n%s", bar)
	}
}

// Long trace ids are trimmed so they cannot swallow the bar.
func TestLongIDIsShortened(t *testing.T) {
	long := "a673e05f-9ce8-4762-8aae-2ff182b28efe"
	m := NewModel(nil, 1000)
	m.width, m.height = 120, 24
	m = feed(m, req(long, "boom", true))
	m, _ = key(m, "k")
	m, _ = key(m, "c")

	bar := m.statusBar()
	if strings.Contains(bar, long) {
		t.Fatalf("the full id should not reach the bar:\n%s", bar)
	}
	if !strings.Contains(bar, "id:a673e05f…") {
		t.Fatalf("want a trimmed id on the bar:\n%s", bar)
	}
}

// Filters change the row set wholesale, so the cursor and the shade have to be
// re-anchored rather than left pointing past the end.
func TestFiltersKeepTheCursorValid(t *testing.T) {
	m := filterModel()
	if !m.onShade() {
		t.Fatalf("precondition: should start live")
	}
	m, _ = key(m, "a") // many more rows
	if !m.onShade() || m.selected != len(m.rows) {
		t.Fatalf("a live view should stay on the handle across a filter change (selected=%d rows=%d)",
			m.selected, len(m.rows))
	}

	// Held, deep in the list, then narrow hard.
	for i := 0; i < 5; i++ {
		m, _ = key(m, "k")
	}
	m, _ = key(m, "c")
	if m.selected > len(m.rows) {
		t.Fatalf("cursor %d is past the end of %d rows", m.selected, len(m.rows))
	}
	if start, end := m.listWindow(); start < 0 || end > len(m.rows) {
		t.Fatalf("window [%d,%d) out of range for %d rows", start, end, len(m.rows))
	}
}

// The frame invariant survives every filter combination.
func TestFrameFitsUnderEveryFilter(t *testing.T) {
	const h = 12
	for _, keys := range [][]string{{}, {"a"}, {"k", "c"}, {"k", "c", "a"}, {"a", "/", "b", "o", "o", "m", "enter"}, {"/", "x"}} {
		m := NewModel(nil, 1000)
		m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: h})
		m = m2.(Model)
		for i := 0; i < 12; i++ {
			m = feed(m,
				req(fmt.Sprintf("R%d", i%3), fmt.Sprintf("routine %d", i), false),
				req(fmt.Sprintf("R%d", i%3), fmt.Sprintf("boom %d", i), true),
			)
		}
		for _, k := range keys {
			m, _ = key(m, k)
		}
		if got := frameHeight(m.View()); got > h {
			t.Fatalf("keys %v: frame is %d lines, terminal is %d", keys, got, h)
		}
	}
}

// Lines arriving while pinned are filtered too, not just the backlog.
func TestPinAppliesToNewLines(t *testing.T) {
	m := filterModel()
	m, _ = key(m, "k")
	m, _ = key(m, "k") // "A boom"
	m, _ = key(m, "c")
	before := len(m.rows)

	m = feed(m, req("B", "B boom later", true)) // other request, must not appear
	if len(m.rows) != before {
		t.Fatalf("a line from another request appeared while pinned: %v", messages(m))
	}
	m = feed(m, req("A", "A boom later", true)) // same request, must appear
	if len(m.rows) != before+1 {
		t.Fatalf("a line from the pinned request did not appear: %v", messages(m))
	}
}

// The stacking behaviour is the non-obvious part, so it stays written down even
// though the keys themselves are covered by TestHelpCoversEveryBinding.
func TestHelpExplainsThatFiltersStack(t *testing.T) {
	if got := filterModel().helpContent(); !strings.Contains(got, "with a:") {
		t.Fatalf("help should say what the two filters do together:\n%s", got)
	}
}
