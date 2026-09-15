package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/jimbeveridge/loghorn/internal/entry"
)

// sgrSeq matches one CSI SGR escape, e.g. "\x1b[48;2;48;48;48m" or "\x1b[0m".
var sgrSeq = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// sgrSetsBackground folds one escape's parameters into the running background
// state: "0" (or the two ways a terminal spells "no parameters", which mean the
// same thing) turns every attribute off, including a background set earlier in
// the very same escape, and "48" turns it on. "38" (foreground) and "48"
// (background) both take a colour-space selector and its own operands — "5;n"
// for a 256-colour index or "2;r;g;b" for truecolor — which must be skipped
// rather than scanned as codes of their own: an RGB component can itself equal
// 0 or 48 (e.g. amber #ffaf00 is rgb(255,175,0)), and reading that as a reset or
// a second background code would misjudge the very cases this helper exists to
// catch.
func sgrSetsBackground(params string, bg bool) bool {
	if params == "" {
		return false
	}
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "0":
			bg = false
		case "48":
			bg = true
			i += operandWidth(fields, i)
		case "38":
			i += operandWidth(fields, i)
		}
	}
	return bg
}

// operandWidth is how many fields after a "38" or "48" selector its colour
// operands occupy: 2 more for "5;n", 4 more for "2;r;g;b", 0 for anything else
// (malformed input the terminal would ignore too).
func operandWidth(fields []string, i int) int {
	if i+1 >= len(fields) {
		return 0
	}
	switch fields[i+1] {
	case "5":
		return 2
	case "2":
		return 4
	}
	return 0
}

// assertBackgroundThroughout fails t unless every visible character in s is
// drawn with a background SGR in effect — the continuous-highlight guarantee a
// selected row or bar must keep across every segment it's built from, not just
// the first.
func assertBackgroundThroughout(t *testing.T, label, s string) {
	t.Helper()
	bg := false
	last := 0
	for _, loc := range sgrSeq.FindAllStringIndex(s, -1) {
		if visible := s[last:loc[0]]; visible != "" && !bg {
			t.Fatalf("%s: no background active before %q in %q", label, visible, s)
		}
		params := s[loc[0]+2 : loc[1]-1] // strip the leading "\x1b[" and trailing "m"
		bg = sgrSetsBackground(params, bg)
		last = loc[1]
	}
	if tail := s[last:]; tail != "" && !bg {
		t.Fatalf("%s: no background active for trailing %q in %q", label, tail, s)
	}
}

// afterHandle strips the shade handle's leading marker, unstyled by design (see
// listLines and statusBar): only the text after it needs a continuous
// background.
func afterHandle(t *testing.T, s string) string {
	t.Helper()
	rest, ok := strings.CutPrefix(s, "▶ ")
	if !ok {
		t.Fatalf("expected %q to start with the shade handle %q", s, "▶ ")
	}
	return rest
}

// selectRow builds a model with one context row and one important row, sizes it
// tall enough that both are on screen, and points the cursor at row idx without
// disturbing the rest of the layout.
func selectRow(t *testing.T, idx int) Model {
	t.Helper()
	m := NewModel(nil, 100)
	m.showAll = true // BuildAll keeps RowContext rows, not just failures
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	m = m2.(Model)
	m = feed(m,
		entry.Entry{Message: "context line", Important: false},
		entry.Entry{Message: "important line", Important: true},
	)
	if len(m.rows) != 2 {
		t.Fatalf("precondition: want 2 rows, got %d", len(m.rows))
	}
	m.selected = idx
	return m
}

// lineFor is the rendered text of display row idx from listLines.
func lineFor(t *testing.T, m Model, idx int) string {
	t.Helper()
	for _, l := range m.listLines(m.width) {
		if l.row == idx {
			return l.text
		}
	}
	t.Fatalf("row %d not in the visible window", idx)
	return ""
}

// TestSelectedRowHighlightIsContinuous covers a selected context row and a
// selected important row: today's rendering nests already-styled segments
// (tsStyle, then impStyle/dimStyle) inside selStyle.Render, and lipgloss closes
// every Render with a full SGR reset, so the inner segments' own resets cancel
// the outer background partway through — only the timestamp stays highlighted.
func TestSelectedRowHighlightIsContinuous(t *testing.T) {
	useProfile(t, termenv.TrueColor)

	for _, tc := range []struct {
		name string
		idx  int
	}{
		{"context row", 0},
		{"important row", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := selectRow(t, tc.idx)
			line := lineFor(t, m, tc.idx)
			assertBackgroundThroughout(t, tc.name, afterHandle(t, line))
		})
	}
}

// TestNonSelectedRowUnchanged pins the non-selected rendering to exactly what it
// was before this fix: tsStyle.Render(ts), a plain space, then impStyle or
// dimStyle.Render(msg), with the "  " prefix — nothing about the fix may touch
// this path. Covers both row kinds: selecting the other row each time so a
// context row and an important row are each checked while themselves
// non-selected.
func TestNonSelectedRowUnchanged(t *testing.T) {
	useProfile(t, termenv.TrueColor)

	for _, tc := range []struct {
		name        string
		selectedIdx int
		checkIdx    int
		style       lipgloss.Style
	}{
		{"context row", 1, 0, dimStyle},
		{"important row", 0, 1, impStyle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := selectRow(t, tc.selectedIdx)
			got := lineFor(t, m, tc.checkIdx)

			e := m.rows[tc.checkIdx].Entry
			ts := e.Received.Format(tsLayout)
			msg := truncate(flatten(e.Message), m.width-len(ts)-3)
			want := "  " + tsStyle.Render(ts) + " " + tc.style.Render(msg)
			if got != want {
				t.Errorf("non-selected row changed:\ngot  %q\nwant %q", got, want)
			}
		})
	}
}

// TestHeldStatusBarUnchanged pins the held (non-live) bar's rendering to exactly
// what it was before this fix: statusStyle.Render and moreStyle.Render calls
// concatenated directly, with the "  " prefix. selBg only enters the picture
// when live (see statusBar), so this path must be untouched.
func TestHeldStatusBarUnchanged(t *testing.T) {
	useProfile(t, termenv.TrueColor)

	m := NewModel(nil, 100)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	m = m2.(Model)
	m = feed(m, entry.Entry{Message: "line", Important: true})
	m = m.setSelected(0) // pull the shade down
	if m.onShade() {
		t.Fatalf("precondition: model should be held")
	}

	got := m.statusBar()

	base := fmt.Sprintf("loghorn %s · %s · %s lines · %d shown",
		m.spinner(), "HELD", comma(m.ingested), len(m.rows))
	var more string
	if start, _ := m.listWindow(); start > 0 {
		more += moreStyle.Render(fmt.Sprintf(" · ▲%d", start))
	}
	more += moreStyle.Render(fmt.Sprintf(" · ▼%d of %s waiting",
		len(m.rows)-m.heldRows, comma(m.ingested-m.heldLines)))
	tail := fmt.Sprintf(" · j/k · spc %s · enter open · ? help · q quit", toggleWord(false, m.historical))
	want := "  " + statusStyle.Render(base) + more + statusStyle.Render(tail)
	if got != want {
		t.Errorf("held status bar changed:\ngot  %q\nwant %q", got, want)
	}
}

// TestLiveStatusBarHighlightIsContinuous covers the ordinary live bar, which
// today is "▶ " + selStyle.Render(base + more + tail) — several already-styled
// segments concatenated and then wrapped, so only the first (base) stays
// highlighted once its own reset fires.
func TestLiveStatusBarHighlightIsContinuous(t *testing.T) {
	useProfile(t, termenv.TrueColor)

	m := NewModel(nil, 100)
	m.showAll = true // adds a " · ALL" segment, so more than one boundary is exercised
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	m = m2.(Model)
	m = feed(m, entry.Entry{Message: "line", Important: true})
	if !m.onShade() {
		t.Fatalf("precondition: model should be live")
	}

	bar := m.statusBar()
	assertBackgroundThroughout(t, "live status bar", afterHandle(t, bar))
}

// TestForwardingBarHighlightIsContinuous covers the forwarding bar, reachable by
// setting the model's forwarding flag directly.
func TestForwardingBarHighlightIsContinuous(t *testing.T) {
	useProfile(t, termenv.TrueColor)

	m := NewModel(nil, 100)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	m = m2.(Model)
	m.forwarding = true

	bar := m.statusBar()
	assertBackgroundThroughout(t, "forwarding bar", afterHandle(t, bar))
}
