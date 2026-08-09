package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"clog/internal/adapter"
	"clog/internal/entry"
)

// bodyLines splits a view into its body rows, dropping the status bar.
func bodyLines(view string) []string {
	lines := strings.Split(view, "\n")
	if len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// leftOf returns the first w display columns of a rendered line.
func leftOf(line string, w int) string { return clipTo(line, w) }

// The point of the overlay: opening the detail pane must not reformat the log
// view. Every visible column of every list line is identical before and after —
// the pane covers the right-hand side, it does not push the list into a narrower
// column and re-truncate it.
func TestOpeningDetailDoesNotReflowTheList(t *testing.T) {
	m := NewModel(nil, 200, 2)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 16})
	m = m2.(Model)
	for i := 0; i < 4; i++ {
		m = feed(m,
			entry.Entry{Message: "GET /api/v1/items/very/long/path/that/runs/past/the/pane/edge returned 200"},
			adapter.ParseLine([]byte(bigLogEntry)),
		)
		e := adapter.ParseLine([]byte(bigLogEntry))
		e.Important = true
		m = feed(m, e)
	}

	before := bodyLines(m.View())

	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)
	if !m.showDetail {
		t.Fatalf("precondition: enter should open the detail pane")
	}
	after := bodyLines(m.View())

	paneW, _ := m.detailDims()
	leftW := m.width - paneW - detailChrome
	if leftW < 10 {
		t.Fatalf("precondition: fixture leaves only %d columns of list to compare", leftW)
	}

	for i := range after {
		if i >= len(before) {
			break
		}
		want := leftOf(before[i], leftW)
		got := leftOf(after[i], leftW)
		if got != want {
			t.Fatalf("list line %d reflowed when the pane opened:\n before: %q\n  after: %q",
				i, want, got)
		}
	}
}

// Closing the pane restores the view exactly.
func TestClosingDetailRestoresTheList(t *testing.T) {
	m := NewModel(nil, 200, 0)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 14})
	m = m2.(Model)
	m = imps(m, 8)

	before := m.View()
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // enter closes it too
	m = m2.(Model)
	if m.showDetail {
		t.Fatalf("precondition: the pane should be closed")
	}
	if got := m.View(); got != before {
		t.Fatalf("closing the pane should restore the view exactly:\nbefore:\n%s\nafter:\n%s", before, got)
	}
}

// clipTo is what keeps the pane's left edge straight, so it must return exactly
// w columns for anything — most importantly for the empty line, which is what
// rows below the end of the list are made of.
func TestClipToAlwaysReturnsExactWidth(t *testing.T) {
	for _, in := range []string{"", " ", "short", strings.Repeat("wide ", 40), "▶ 00:00:00.000 x"} {
		for _, w := range []int{1, 5, 19, 40} {
			if got := lipgloss.Width(clipTo(in, w)); got != w {
				t.Fatalf("clipTo(%.12q…, %d) is %d columns, want exactly %d", in, w, got, w)
			}
		}
	}
}

// The pane starts in the same column on every row — including the rows below the
// end of the list, which is where a missing pad shows up as a ragged edge.
func TestPaneEdgeIsStraight(t *testing.T) {
	m := NewModel(nil, 200, 0)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 16})
	m = m2.(Model)
	// Two short rows against a tall entry, so most rows fall past the list's end.
	m = feed(m, entry.Entry{Message: "x", Important: true})
	e := adapter.ParseLine([]byte(bigLogEntry))
	e.Important = true
	m = feed(m, e)
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)

	paneW, _ := m.detailDims()
	leftW := m.width - paneW - detailChrome
	rows := bodyLines(m.View())
	if len(rows) <= len(m.listLines(m.width)) {
		t.Fatalf("precondition: need rows past the end of the list (%d rows, %d list lines)",
			len(rows), len(m.listLines(m.width)))
	}

	paneRows := strings.Split(m.detail.View(), "\n")
	for i, line := range rows {
		r := []rune(line)
		if len(r) < leftW+detailChrome {
			t.Fatalf("row %d is only %d columns, cannot reach the pane at %d: %q", i, len(r), leftW, line)
		}
		if got := string(r[leftW]); got != detailDivider {
			t.Fatalf("row %d: want the divider at column %d, got %q in %q", i, leftW, got, line)
		}
		if r[leftW+1] != ' ' {
			t.Fatalf("row %d: want a margin column after the divider, got %q in %q", i, r[leftW+1], line)
		}
		// Everything past the divider and margin is the pane's own line, unshifted.
		if i < len(paneRows) {
			if got, want := string(r[leftW+detailChrome:]), paneRows[i]; got != want {
				t.Fatalf("row %d: pane content is shifted\n got: %q\nwant: %q", i, got, want)
			}
		}
	}
}

// The divider is a continuous rule down the pane's full height, not just beside
// the rows the list happens to fill, and the pane's stated width is content —
// the chrome comes out of the reserve, not out of the pane.
func TestDividerRunsFullHeight(t *testing.T) {
	const h = 16
	m := NewModel(nil, 200, 0)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: h})
	m = m2.(Model)
	m = feed(m, entry.Entry{Message: "only row", Important: true}) // one list row, many pane rows
	e := adapter.ParseLine([]byte(bigLogEntry))
	e.Important = true
	m = feed(m, e)
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)

	rows := bodyLines(m.View())
	if len(rows) != h-1 {
		t.Fatalf("body should fill the terminal less the status bar: %d rows, want %d", len(rows), h-1)
	}
	paneW, _ := m.detailDims()
	leftW := m.width - paneW - detailChrome
	for i, line := range rows {
		if got := string([]rune(line)[leftW]); got != detailDivider {
			t.Fatalf("row %d has no divider at column %d (got %q) — the rule must be continuous",
				i, leftW, got)
		}
	}

	// The chrome is charged to the reserve: the pane still gets its full cap.
	if paneW > m.width-detailReserve {
		t.Fatalf("pane %d exceeds the cap %d", paneW, m.width-detailReserve)
	}
}

// The composite still fits the terminal, both ways.
func TestOverlayFitsTerminal(t *testing.T) {
	for _, dim := range []struct{ w, h int }{{80, 10}, {120, 24}, {200, 40}, {60, 8}} {
		m := NewModel(nil, 200, 1)
		m2, _ := m.Update(tea.WindowSizeMsg{Width: dim.w, Height: dim.h})
		m = m2.(Model)
		for i := 0; i < 20; i++ {
			e := adapter.ParseLine([]byte(bigLogEntry))
			e.Important = true
			m = feed(m, entry.Entry{Message: "routine"}, e)
		}
		m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = m2.(Model)

		if got := frameHeight(m.View()); got != dim.h {
			t.Fatalf("%dx%d: frame is %d lines, want %d", dim.w, dim.h, got, dim.h)
		}
		if got := bodyWidth(m.View()); got > dim.w {
			t.Fatalf("%dx%d: body is %d columns", dim.w, dim.h, got)
		}
	}
}

// Hit-testing has to agree with the overlay geometry: a click left of the pane
// selects the row drawn there, and one under the pane is ignored.
func TestClickGeometryMatchesOverlay(t *testing.T) {
	m := NewModel(nil, 200, 0)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 14})
	m = m2.(Model)
	m = imps(m, 8)
	m, _ = key(m, "k") // hold, so the selection is observable
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)

	paneW, _ := m.detailDims()
	leftW := m.width - paneW - detailChrome

	// Just inside the list: selects row 2.
	m = clickAt(m, leftW-1, 2)
	if m.selected != 2 {
		t.Fatalf("click at the right edge of the list should select row 2, got %d", m.selected)
	}
	// The divider, the margin, and everything right of them belong to the pane.
	for _, x := range []int{leftW, leftW + 1, m.width - 1} {
		before := m.selected
		m = clickAt(m, x, 4)
		if m.selected != before {
			t.Fatalf("click at x=%d (divider/margin/pane) moved the selection %d -> %d", x, before, m.selected)
		}
	}
}

// With the pane open the list still uses the whole width for its own layout, so
// a row shows as much text as it did before — only the covered part is hidden.
func TestListLayoutIgnoresThePane(t *testing.T) {
	long := entry.Entry{Message: strings.Repeat("abcdefghij", 30), Important: true}

	closed := NewModel(nil, 200, 0)
	m2, _ := closed.Update(tea.WindowSizeMsg{Width: 120, Height: 10})
	closed = m2.(Model)
	closed = feed(closed, long)

	open := closed
	m2, _ = open.Update(tea.KeyMsg{Type: tea.KeyEnter})
	open = m2.(Model)

	if len(open.listLines(open.width)) != len(closed.listLines(closed.width)) {
		t.Fatalf("the pane changed the list's line count")
	}
	if open.listLines(open.width)[0].text != closed.listLines(closed.width)[0].text {
		t.Fatalf("the pane changed how a list line is laid out")
	}
}
