package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"clog/internal/entry"
)

// pagingModel: 60 failures in a 21-line terminal, so a page is 20 rows and a
// half page is 10.
func pagingModel(t *testing.T) Model {
	t.Helper()
	m := NewModel(nil, 1000)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 21})
	m = m2.(Model)
	m = imps(m, 60)
	if got := m.windowBudget(); got != 20 {
		t.Fatalf("precondition: page should be 20 rows, got %d", got)
	}
	if got := m.halfPage(); got != 10 {
		t.Fatalf("precondition: half page should be 10 rows, got %d", got)
	}
	return m
}

// pgup/pgdn move a screenful in the list.
func TestListPagesByAScreenful(t *testing.T) {
	m := pagingModel(t)
	handle := len(m.rows) // 60; the cursor starts here

	m, _ = key(m, "pgup")
	if want := handle - 20; m.selected != want {
		t.Fatalf("pgup should move a page up to %d, got %d", want, m.selected)
	}
	if m.onShade() {
		t.Fatalf("paging up off the handle should hold the shade")
	}

	m, _ = key(m, "pgup")
	if want := handle - 40; m.selected != want {
		t.Fatalf("second pgup should reach %d, got %d", want, m.selected)
	}

	m, _ = key(m, "pgdown")
	if want := handle - 20; m.selected != want {
		t.Fatalf("pgdown should come back to %d, got %d", want, m.selected)
	}
}

// ctrl+d/ctrl+u move half that.
func TestListPagesByHalfAScreenful(t *testing.T) {
	m := pagingModel(t)
	handle := len(m.rows)

	m, _ = key(m, "ctrl+u")
	if want := handle - 10; m.selected != want {
		t.Fatalf("ctrl+u should move a half page up to %d, got %d", want, m.selected)
	}
	m, _ = key(m, "ctrl+u")
	if want := handle - 20; m.selected != want {
		t.Fatalf("second ctrl+u should reach %d, got %d", want, m.selected)
	}
	m, _ = key(m, "ctrl+d")
	if want := handle - 10; m.selected != want {
		t.Fatalf("ctrl+d should come back to %d, got %d", want, m.selected)
	}
}

// Paging obeys the shade rules: down onto the handle goes live, and paging past
// either end clamps rather than wrapping.
func TestPagingObeysTheShade(t *testing.T) {
	m := pagingModel(t)

	m, _ = key(m, "pgup")
	if m.onShade() {
		t.Fatalf("precondition: should be held after paging up")
	}
	m, _ = key(m, "pgdown") // overshoots the tail, lands on the handle
	if !m.onShade() {
		t.Fatalf("paging down past the newest row should grab the handle and go live")
	}

	for i := 0; i < 20; i++ {
		m, _ = key(m, "pgup")
	}
	if m.selected != 0 {
		t.Fatalf("paging up should clamp at the oldest row, got %d", m.selected)
	}
	for i := 0; i < 20; i++ {
		m, _ = key(m, "pgdown")
	}
	if !m.onShade() || m.selected != len(m.rows) {
		t.Fatalf("paging down should clamp on the handle, got %d of %d", m.selected, len(m.rows))
	}
}

// A page moves the visible window too, not just the cursor.
func TestPagingMovesTheWindow(t *testing.T) {
	m := pagingModel(t)
	m, _ = key(m, "pgup")
	start1, _ := m.listWindow()
	m, _ = key(m, "pgup")
	start2, _ := m.listWindow()

	if start2 >= start1 {
		t.Fatalf("a second pgup should scroll the window further up (%d -> %d)", start1, start2)
	}
	if got := start1 - start2; got != 20 {
		t.Fatalf("the window should move a full page, moved %d", got)
	}
}

// Page size follows the terminal.
func TestPageSizeFollowsTerminalHeight(t *testing.T) {
	m := NewModel(nil, 1000)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 11})
	m = m2.(Model)
	m = imps(m, 60)

	m, _ = key(m, "pgup")
	if want := 60 - 10; m.selected != want {
		t.Fatalf("in an 11-line terminal a page is 10 rows: want %d, got %d", want, m.selected)
	}

	m2, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 41})
	m = m2.(Model)
	before := m.selected
	m, _ = key(m, "pgup")
	if want := before - 40; m.selected != want {
		t.Fatalf("in a 41-line terminal a page is 40 rows: want %d, got %d", want, m.selected)
	}
}

// A terminal too short to halve still moves at least a row, rather than sticking.
func TestPagingAlwaysMovesInATinyTerminal(t *testing.T) {
	m := NewModel(nil, 1000)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 2}) // budget 1, half page 0 without a floor
	m = m2.(Model)
	m = imps(m, 10)

	before := m.selected
	m, _ = key(m, "ctrl+u")
	if m.selected == before {
		t.Fatalf("ctrl+u should still move one row when half a page rounds to zero")
	}
}

// The frame invariant holds across paging.
func TestFrameFitsWhilePaging(t *testing.T) {
	const h = 14
	m := NewModel(nil, 1000)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: h})
	m = m2.(Model)
	m = imps(m, 80)

	for _, k := range []string{"pgup", "pgup", "ctrl+u", "pgdown", "ctrl+d", "pgup", "pgdown"} {
		m, _ = key(m, k)
		if got := frameHeight(m.View()); got != h {
			t.Fatalf("after %q the frame is %d lines, terminal is %d", k, got, h)
		}
	}
}

// The detail pane pages too — it inherits the viewport keymap, so this is a
// regression guard against a global key shadowing it later.
func TestDetailPanePages(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&sb, "trace line %d\n", i)
	}
	m := NewModel(nil, 100)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = m2.(Model)
	m = feed(m, entry.Entry{Message: "boom", Important: true, Raw: []byte(sb.String())})
	m, _ = key(m, "enter")

	for _, k := range []string{"pgdown", "ctrl+d"} {
		m, _ = key(m, "enter") // close
		m, _ = key(m, "enter") // reopen at the top
		if m.detail.YOffset != 0 {
			t.Fatalf("precondition: pane should open at the top")
		}
		m, _ = key(m, k)
		if m.detail.YOffset == 0 {
			t.Fatalf("%q should scroll the detail pane", k)
		}
		down := m.detail.YOffset

		up := map[string]string{"pgdown": "pgup", "ctrl+d": "ctrl+u"}[k]
		m, _ = key(m, up)
		if m.detail.YOffset >= down {
			t.Fatalf("%q should scroll back up (%d -> %d)", up, down, m.detail.YOffset)
		}
	}
}

// Paging the detail pane must not move the list underneath it.
func TestDetailPagingLeavesTheListAlone(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	m := NewModel(nil, 100)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = m2.(Model)
	m = imps(m, 40)
	m = feed(m, entry.Entry{Message: "boom", Important: true, Raw: []byte(sb.String())})
	m, _ = key(m, "enter")
	before := m.selected

	m, _ = key(m, "pgdown")
	m, _ = key(m, "ctrl+d")
	if m.selected != before {
		t.Fatalf("paging the pane moved the list selection %d -> %d", before, m.selected)
	}
}

// The help overlay pages as well, for the same reason.
func TestHelpPages(t *testing.T) {
	m := NewModel(nil, 100)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 8}) // shorter than the reference
	m = m2.(Model)
	m = imps(m, 3)
	m, _ = key(m, "?")
	if m.help.AtBottom() {
		t.Fatalf("precondition: help should overflow this terminal")
	}

	m, _ = key(m, "pgdown")
	if m.help.YOffset == 0 {
		t.Fatalf("pgdown should page the help overlay")
	}
	if !m.showHelp {
		t.Fatalf("paging must not close help")
	}
}

// The keys are documented, including how to reach them on a Mac laptop.
func TestHelpDocumentsPaging(t *testing.T) {
	m := pagingModel(t)
	help := m.helpContent()
	for _, want := range []string{"pgdn / pgup", "ctrl+d / ctrl+u", "fn+"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help should document %q:\n%s", want, help)
		}
	}
}
