package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"loghorn/internal/entry"
)

func shadeModel(height int) Model {
	m := NewModel(nil, 1000)
	m.width, m.height = 100, height
	return m
}

// The point of the shade: once it is down, arriving rows must not scroll the
// view. The old follow/pause model kept the window tail-anchored, so paused rows
// still slid past.
func TestHeldWindowDoesNotMove(t *testing.T) {
	m := shadeModel(8)
	m = imps(m, 20)

	m = press(m, 'k') // pull the shade down
	if m.onShade() {
		t.Fatalf("k should hold the shade")
	}
	before := m.listView(m.width)
	beforeStart, beforeEnd := m.listWindow()

	m = imps(m, 30) // a burst arrives behind the shade

	if start, end := m.listWindow(); start != beforeStart || end != beforeEnd {
		t.Fatalf("held window moved from [%d,%d) to [%d,%d)", beforeStart, beforeEnd, start, end)
	}
	if got := m.listView(m.width); got != before {
		t.Fatalf("held view changed as rows arrived:\nbefore:\n%s\nafter:\n%s", before, got)
	}
}

// Live is the opposite: the window rides the newest row.
func TestLiveWindowRidesTail(t *testing.T) {
	m := shadeModel(8)
	m = imps(m, 20)
	if _, end := m.listWindow(); end != len(m.rows) {
		t.Fatalf("live window should reach the last row, end=%d rows=%d", end, len(m.rows))
	}
	m = imps(m, 10)
	if _, end := m.listWindow(); end != len(m.rows) {
		t.Fatalf("live window should still reach the last row, end=%d rows=%d", end, len(m.rows))
	}
}

// Letting go of the shade snaps the view back to the newest rows.
func TestGrabbingSnapsBackToTail(t *testing.T) {
	m := shadeModel(8)
	m = imps(m, 20)
	m = press(m, 'k')
	m = imps(m, 30)

	m = press(m, 'G')
	if !m.onShade() {
		t.Fatalf("G should grab the handle")
	}
	if _, end := m.listWindow(); end != len(m.rows) {
		t.Fatalf("after grabbing, the window should reach the newest row (end=%d rows=%d)", end, len(m.rows))
	}
}

// Both waiting figures measure from the moment the shade came down, and both
// reset when it is released.
func TestWaitingCountersMeasureFromTheFreeze(t *testing.T) {
	m := shadeModel(10)
	m = imps(m, 5) // 5 rows before freezing

	m = press(m, 'k')
	if got := m.statusBar(); !strings.Contains(got, "▼0 of 0 waiting") {
		t.Fatalf("nothing should be waiting the instant the shade comes down:\n%s", got)
	}

	// 7 routine lines (filtered out) and 3 important ones behind the shade.
	for i := 0; i < 7; i++ {
		m = feed(m, entry.Entry{Message: fmt.Sprintf("routine %d", i)})
	}
	m = imps(m, 3)

	got := m.statusBar()
	if !strings.Contains(got, "▼3 of 10 waiting") {
		t.Fatalf("want 3 rows of 10 raw lines waiting:\n%s", got)
	}
	if !strings.Contains(got, "HELD") {
		t.Fatalf("bar should read HELD:\n%s", got)
	}

	// Releasing clears the backlog.
	m = press(m, 'G')
	got = m.statusBar()
	if strings.Contains(got, "waiting") {
		t.Fatalf("nothing should be waiting once the shade is up:\n%s", got)
	}
	if !strings.Contains(got, "LIVE") {
		t.Fatalf("bar should read LIVE:\n%s", got)
	}
}

// The total counter climbs even when no line survives the display filter — that
// is the whole point of having it alongside the waiting figure.
func TestTotalClimbsWhileNothingIsDisplayed(t *testing.T) {
	m := shadeModel(10)
	for i := 0; i < 400; i++ {
		m = feed(m, entry.Entry{Message: fmt.Sprintf("routine %d", i)})
	}
	if len(m.rows) != 0 {
		t.Fatalf("precondition: routine lines should produce no rows, got %d", len(m.rows))
	}
	if got := m.statusBar(); !strings.Contains(got, "400 lines") {
		t.Fatalf("total should count filtered-out lines:\n%s", got)
	}
}

// Six figures stay readable.
func TestLineCountIsGrouped(t *testing.T) {
	m := shadeModel(10)
	m.ingested = 1234567
	if got := m.statusBar(); !strings.Contains(got, "1,234,567 lines") {
		t.Fatalf("thousands should be grouped:\n%s", got)
	}
	m.ingested = 42
	if got := m.statusBar(); !strings.Contains(got, "42 lines") {
		t.Fatalf("small counts should be plain:\n%s", got)
	}
}

// The handle carries the cursor and the row highlight while it holds it.
func TestHandleShowsCursorWhenLive(t *testing.T) {
	m := shadeModel(10)
	m = imps(m, 5)

	// The highlight itself is a styling concern lipgloss strips outside a TTY, so
	// the cursor glyph is what is asserted here; the highlight is checked in the
	// pty run.
	if bar := m.statusBar(); !strings.HasPrefix(bar, "▶ ") {
		t.Fatalf("the live handle should carry the ▶ cursor:\n%s", bar)
	}

	m = press(m, 'k')
	if bar := m.statusBar(); strings.HasPrefix(bar, "▶ ") {
		t.Fatalf("a held handle should not carry the cursor:\n%s", bar)
	}
	// With the cursor off the handle, exactly one row wears it.
	var cursors int
	for _, l := range m.listLines(m.width) {
		if strings.HasPrefix(l.text, "▶ ") {
			cursors++
		}
	}
	if cursors != 1 {
		t.Fatalf("expected exactly one row cursor while held, got %d", cursors)
	}
}

// While live, no row wears the cursor — it is on the handle.
func TestNoRowCursorWhileLive(t *testing.T) {
	m := shadeModel(10)
	m = imps(m, 5)
	for _, l := range m.listLines(m.width) {
		if strings.HasPrefix(l.text, "▶ ") {
			t.Fatalf("no row should wear the cursor while live:\n%s", l.text)
		}
	}
}

// The spinner advances per ingested line and holds still otherwise, so it reads
// as liveness rather than decoration.
func TestSpinnerAdvancesPerLine(t *testing.T) {
	m := shadeModel(10)
	seen := map[string]bool{}
	prev := m.spinner()
	for i := 0; i < len(spinnerFrames); i++ {
		m = feed(m, entry.Entry{Message: "routine"})
		if got := m.spinner(); got == prev {
			t.Fatalf("spinner did not advance on line %d (stuck on %q)", i, got)
		} else {
			prev = got
			seen[got] = true
		}
	}
	if len(seen) != len(spinnerFrames) {
		t.Fatalf("spinner should cycle all %d frames, saw %d", len(spinnerFrames), len(seen))
	}

	// No ingest, no movement — a dead pipe visibly stops.
	still := m.spinner()
	m = press(m, 'k')
	m = press(m, 'j')
	if m.spinner() != still {
		t.Fatalf("spinner moved without any line arriving")
	}
}

// Clicking the bar pulls the handle; clicking a row lets go of it.
func TestClickingBarGrabsHandle(t *testing.T) {
	m := shadeModel(10)
	m = imps(m, 5)

	m = clickAt(m, 10, 1) // a row
	if m.onShade() {
		t.Fatalf("clicking a row should hold the shade")
	}
	m = clickAt(m, 10, m.height-1) // the bar
	if !m.onShade() {
		t.Fatalf("clicking the bar should grab the handle")
	}
	if m.selected != len(m.rows) {
		t.Fatalf("grabbing should park the cursor on the handle, got %d", m.selected)
	}
}

// The detail pane's bar is a different bar: clicking it must not grab anything.
func TestClickingDetailBarDoesNotGrab(t *testing.T) {
	m := shadeModel(10)
	m = imps(m, 5)
	m = press(m, 'k') // hold, so a grab would be observable
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)

	m = clickAt(m, 10, m.height-1)
	if m.onShade() {
		t.Fatalf("the detail pane's bar is not the shade handle")
	}
}

// A held view still has to fit the terminal.
func TestHeldFrameFitsTerminal(t *testing.T) {
	m := shadeModel(10)
	for g := 0; g < 15; g++ {
		m = feed(m,
			entry.Entry{Message: fmt.Sprintf("routine %d", g)},
			entry.Entry{Message: fmt.Sprintf("boom %d", g), Important: true},
		)
	}
	m = press(m, 'k')
	for i := 0; i < 20; i++ {
		m = press(m, 'k')
		m = imps(m, 2)
		if got := frameHeight(m.View()); got > m.height {
			t.Fatalf("held frame is %d lines, terminal is %d:\n%s", got, m.height, m.View())
		}
	}
}
