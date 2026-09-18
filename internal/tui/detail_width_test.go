package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbeveridge/loghorn/internal/adapter"
	"github.com/jimbeveridge/loghorn/internal/entry"
)

// openOn feeds one entry and opens the detail pane on it.
func openOn(t *testing.T, w, h int, e entry.Entry) Model {
	t.Helper()
	m := NewModel(nil, 100)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = m2.(Model)
	e.Important = true
	m = feed(m, e)
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)
	if !m.showDetail {
		t.Fatalf("precondition: detail should be open")
	}
	return m
}

func rawEntry(lines ...string) entry.Entry {
	return entry.Entry{Message: "x", Raw: []byte(strings.Join(lines, "\n"))}
}

// bodyWidth measures the widest line of the split body, excluding the status
// bar. The bar is deliberately allowed to run long: Bubble Tea truncates each
// line to the terminal width rather than wrapping it, so an over-long bar loses
// its tail instead of costing a row. The body must fit, because a wrapped body
// line would push the frame past the terminal height.
func bodyWidth(view string) int {
	lines := strings.Split(view, "\n")
	if len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	return maxLineWidth(strings.Join(lines, "\n"))
}

// A pane holding narrow content is only as wide as its longest line — no more
// wasted half-screen.
func TestDetailPaneFitsNarrowContent(t *testing.T) {
	e := rawEntry("short", "a bit longer here", "mid")
	want := len("a bit longer here")

	m := openOn(t, 120, 20, e)
	if got := m.detail.Width; got != want {
		t.Fatalf("pane should be %d wide (the longest line), got %d", want, got)
	}
	// The old behaviour was half the terminal; make sure the test would catch a
	// regression to it.
	if want >= 120/2 {
		t.Fatalf("fixture too wide to prove anything")
	}
}

// Content that fits is untouched: the viewport holds exactly the source lines.
func TestFittingContentIsNotWrapped(t *testing.T) {
	e := rawEntry("alpha", "beta gamma delta", "epsilon")
	m := openOn(t, 120, 20, e)

	if got := m.detail.TotalLineCount(); got != 3 {
		t.Fatalf("content that fits should be left alone: %d lines held, want 3", got)
	}
}

// Wide content is capped so the list keeps its 20 columns.
func TestDetailPaneCapsAtTerminalWidthLess20(t *testing.T) {
	for _, w := range []int{80, 100, 140} {
		e := rawEntry(strings.Repeat("x", 500))
		m := openOn(t, w, 20, e)
		if got, want := m.detail.Width, w-detailReserve; got != want {
			t.Fatalf("at terminal width %d the pane should cap at %d, got %d", w, want, got)
		}
	}
}

// The cap leaves the list exactly the reserved columns, and the two panes plus
// their gap still fit the terminal.
func TestSplitFillsTerminalWidth(t *testing.T) {
	cases := []struct {
		name string
		e    entry.Entry
	}{
		{"narrow content", rawEntry("tiny")},
		{"capped content", rawEntry(strings.Repeat("y", 400))},
		{"real log entry", adapter.ParseLine([]byte(bigLogEntry))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const w = 120
			m := openOn(t, w, 20, tc.e)
			if got := bodyWidth(m.View()); got > w {
				t.Fatalf("body is %d columns wide, terminal is %d", got, w)
			}
			if m.detail.Width > w-detailReserve {
				t.Fatalf("pane %d exceeds the cap %d", m.detail.Width, w-detailReserve)
			}
		})
	}
}

// Resizing recomputes the cap, both ways.
func TestDetailWidthFollowsResize(t *testing.T) {
	e := rawEntry(strings.Repeat("z", 500)) // always wider than the cap
	m := openOn(t, 100, 20, e)
	if got, want := m.detail.Width, 100-detailReserve; got != want {
		t.Fatalf("initial width %d, want %d", got, want)
	}

	m2, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 20})
	m = m2.(Model)
	if got, want := m.detail.Width, 160-detailReserve; got != want {
		t.Fatalf("after growing: width %d, want %d", got, want)
	}

	m2, _ = m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m = m2.(Model)
	if got, want := m.detail.Width, 60-detailReserve; got != want {
		t.Fatalf("after shrinking: width %d, want %d", got, want)
	}
	if got := bodyWidth(m.View()); got > 60 {
		t.Fatalf("body is %d columns after shrinking, terminal is 60", got)
	}
}

// Each entry sizes the pane for itself, so a wide entry doesn't leave the pane
// stretched for the narrow one after it.
func TestPaneResizesPerEntry(t *testing.T) {
	m := NewModel(nil, 100)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	m = m2.(Model)
	m = feed(m,
		entry.Entry{Message: "narrow", Important: true, Raw: []byte("tiny")},
		entry.Entry{Message: "wide", Important: true, Raw: []byte(strings.Repeat("w", 400))},
	)

	// Open the wide one (newest), then the narrow one.
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)
	wide := m.detail.Width
	if wide != 120-detailReserve {
		t.Fatalf("wide entry should cap the pane at %d, got %d", 120-detailReserve, wide)
	}

	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // close
	m = m2.(Model)
	m, _ = key(m, "k") // move to the narrow row
	m, _ = key(m, "k")
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)
	if got := m.detail.Width; got != len("tiny") {
		t.Fatalf("narrow entry should shrink the pane to %d, got %d", len("tiny"), got)
	}
}

// Width is measured on display columns, not bytes: colour codes in the rendered
// JSON must not inflate it.
func TestWidthIgnoresANSI(t *testing.T) {
	e := adapter.ParseLine([]byte(`{"severity":"ERROR","message":"boom"}`))
	e.Important = true
	m := openOn(t, 120, 20, e)

	coloured := m.renderDetail()
	if !strings.Contains(coloured, "\x1b[") {
		t.Skip("lipgloss stripped colour in this environment; nothing to prove")
	}
	if m.detail.Width > maxLineWidth(coloured) {
		t.Fatalf("pane %d wider than the content's display width %d — ANSI counted as columns",
			m.detail.Width, maxLineWidth(coloured))
	}
}

// A terminal too narrow for the reserve must still produce a positive pane width
// rather than a negative one, at any size.
func TestNarrowTerminalDoesNotGoNegative(t *testing.T) {
	for _, w := range []int{1, 5, 20, 21, 22, 30} {
		e := rawEntry(strings.Repeat("q", 200))
		m := openOn(t, w, 10, e)
		if m.detail.Width < 1 {
			t.Fatalf("at terminal width %d the pane width is %d", w, m.detail.Width)
		}
	}
}

// From the narrowest width where a split is usable upward, the body fits and the
// frame keeps its height.
//
// Below ~16 columns neither holds, but that is not this code's doing: a list line
// spends a fixed 15 columns on the cursor and timestamp prefix before any message,
// so lipgloss wraps it and the frame grows. That predates sizing the pane to its
// content and is unreachable at any width where the split is legible — the pane
// itself never leaves the list fewer than 19 columns.
func TestSplitFitsFromUsableWidthsUp(t *testing.T) {
	for _, w := range []int{22, 30, 60, 100, 200} {
		e := rawEntry(strings.Repeat("q", 400))
		m := openOn(t, w, 10, e)
		if got := frameHeight(m.View()); got != 10 {
			t.Fatalf("at terminal width %d the frame is %d lines, want 10", w, got)
		}
		if got := bodyWidth(m.View()); got > w {
			t.Fatalf("at terminal width %d the body is %d columns", w, got)
		}
	}
}

// Sizing the pane must not break the frame-height invariant.
func TestDetailFrameStillFitsHeight(t *testing.T) {
	for _, h := range []int{6, 12, 24} {
		var sb strings.Builder
		for i := 0; i < 80; i++ {
			fmt.Fprintf(&sb, "trace line %d\n", i)
		}
		m := openOn(t, 100, h, rawEntry(sb.String()))
		if got := frameHeight(m.View()); got != h {
			t.Fatalf("detail frame is %d lines, terminal is %d", got, h)
		}
	}
}

// Capped content is wrapped to the pane, so nothing spills into the list.
func TestCappedContentIsWrappedToPane(t *testing.T) {
	e := adapter.ParseLine([]byte(bigLogEntry))
	e.Important = true
	m := openOn(t, 120, 30, e)

	if got := maxLineWidth(m.wrappedDetail()); got > m.detail.Width {
		t.Fatalf("wrapped content is %d columns, pane is %d", got, m.detail.Width)
	}
}

// One line far wider than the rest doesn't stretch the pane: the pane holds at
// the fold width, and the outlier folds into it.
func TestOutlierLineDoesNotStretchThePane(t *testing.T) {
	short := strings.Repeat("s", 50)
	e := rawEntry(short, short, short, short, short, short, strings.Repeat("L", 400))

	m := openOn(t, 120, 20, e)
	if got, want := m.detail.Width, 120/2; got != want {
		t.Fatalf("pane should hold at the fold width (%d columns), got %d", want, got)
	}
	if got := maxLineWidth(m.wrappedDetail()); got > m.detail.Width {
		t.Fatalf("the outlier should have folded: %d columns, pane is %d", got, m.detail.Width)
	}
	if squash(m.wrappedDetail()) != squash(m.renderDetail()) {
		t.Fatalf("folding the outlier changed the text")
	}
}

// Two wide lines are still outliers; by three they are the shape of the entry,
// and the pane sizes to them (up to the cap) rather than folding them all.
func TestOutlierRuleStopsAtTwoLines(t *testing.T) {
	short := strings.Repeat("s", 50)
	long := strings.Repeat("L", 400)
	six := []string{short, short, short, short, short, short}

	two := openOn(t, 120, 20, rawEntry(append(append([]string{}, six...), long, long)...))
	if got, want := two.detail.Width, 120/2; got != want {
		t.Fatalf("two outliers should still fold: pane %d, want %d", got, want)
	}

	three := openOn(t, 120, 20, rawEntry(append(append([]string{}, six...), long, long, long)...))
	if got, want := three.detail.Width, 120-detailReserve; got != want {
		t.Fatalf("three wide lines should size the pane: %d, want %d", got, want)
	}
}

// An outlier folds to half the terminal whatever the lines around it measure,
// so the fold follows the window rather than the shortest key in the entry.
func TestOutlierFoldsToHalfTheTerminal(t *testing.T) {
	e := rawEntry("a", "b", "c", "d", strings.Repeat("L", 400))

	for _, w := range []int{80, 120, 200} {
		if got, want := openOn(t, w, 20, e).detail.Width, w/2; got != want {
			t.Fatalf("on a %d-column terminal the fold should be %d columns, got %d", w, want, got)
		}
	}
}

// The pane is sized on the formatted statement, not the logged one. Formatting
// re-cuts a statement into many lines, which moves both the average and which
// lines stand out from it, so the measurement has to be redone when it lands.
func TestPaneSizedOnFormattedSQL(t *testing.T) {
	col := strings.Repeat("c", 58)
	long := "SELECT " + strings.Repeat(col+", ", 11) + col + " FROM t WHERE id = $1"

	m := NewModel(nil, 100)
	// Each select item on its own line, the way sqlfmt lays a statement out.
	m.formatSQL = func(s string) (string, error) {
		return strings.NewReplacer(", ", ",\n", " FROM ", "\nFROM ", " WHERE ", "\nWHERE ").Replace(s), nil
	}
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
	m = feed(m2.(Model), sqlEntry(long))
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)

	before := m.detail.Width
	if got := maxLineWidth(m.renderDetail()); got <= before {
		t.Fatalf("precondition: the logged statement should be one outlier line (%d columns), pane is %d", got, before)
	}

	m = deliver(t, m, cmd)
	if got, want := m.detail.Width, maxLineWidth(m.renderDetail()); got != want {
		t.Fatalf("no formatted line is an outlier, so the pane should fit them all: %d, widest line %d", got, want)
	}
	if m.detail.Width == before {
		t.Fatalf("the pane should have been re-measured on the formatted statement, but stayed %d", before)
	}
}
