package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jimbeveridge/loghorn/internal/adapter"
)

// A real, large backend.log-style LogEntry line (the 401 API request) with a
// userAgent value far wider than a narrow detail pane.
const bigLogEntry = `{"severity":"INFO","time":"2026-07-15T23:58:30.636Z","pid":4481,"hostname":"Jims-MacBook-Air.local","message":"API request: POST /api/v1/auth/login returned 401 in 0.055s","serviceName":"theauctionapp.googleapis.com","methodName":"POST /api/v1/auth/login","latency":"0.055s","requestId":"a673e05f-9ce8-4762-8aae-2ff182b28efe","httpRequest":{"requestMethod":"POST","requestUrl":"http://localhost:8787/api/v1/auth/login","requestSize":55,"status":401,"responseSize":0,"userAgent":"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36","remoteIp":"","latency":"0.055s"},"apiMetadata":{"queryParams":{}}}`

// squash drops styling and every space, so wrapped and unwrapped renders of the
// same entry compare equal exactly when no text was lost or invented.
func squash(s string) string {
	return strings.Join(strings.Fields(ansi.Strip(s)), "")
}

func openBig(t *testing.T, w int) Model {
	t.Helper()
	return openOn(t, w, 40, adapter.ParseLine([]byte(bigLogEntry)))
}

// Content too wide for the pane folds onto continuation lines: every line fits,
// and none of the text is lost off the right edge.
func TestDetailWrapsLongLines(t *testing.T) {
	m := openBig(t, 120)
	raw := renderDetail(m.detailEntry)

	// Precondition: the content really is wider than the pane, or the test
	// proves nothing.
	if maxLineWidth(raw) <= m.detail.Width {
		t.Fatalf("precondition failed: raw max width %d not wider than pane %d", maxLineWidth(raw), m.detail.Width)
	}

	wrapped := m.wrappedDetail()
	if got, src := m.detail.TotalLineCount(), strings.Count(raw, "\n")+1; got <= src {
		t.Fatalf("a too-wide line should fold: %d lines held, %d in the source", got, src)
	}
	if got := maxLineWidth(wrapped); got > m.detail.Width {
		t.Fatalf("wrapped content is %d columns, pane is %d", got, m.detail.Width)
	}
	if squash(wrapped) != squash(raw) {
		t.Fatalf("wrapping changed the text:\n%s", ansi.Strip(wrapped))
	}
}

// A folded value keeps its line's indentation, so it doesn't spill back to the
// left edge and read as a top-level key.
func TestWrapKeepsIndentation(t *testing.T) {
	m := openBig(t, 60)
	lines := strings.Split(ansi.Strip(m.wrappedDetail()), "\n")

	for i, ln := range lines {
		if !strings.Contains(ln, "userAgent:") {
			continue
		}
		if i+1 >= len(lines) {
			t.Fatalf("userAgent is the last line; it should have folded")
		}
		want := len(ln) - len(strings.TrimLeft(ln, " "))
		next := lines[i+1]
		if got := len(next) - len(strings.TrimLeft(next, " ")); got != want || want == 0 {
			t.Fatalf("continuation indented %d, the userAgent line %d:\n%s\n%s", got, want, ln, next)
		}
		return
	}
	t.Fatalf("no userAgent line in:\n%s", strings.Join(lines, "\n"))
}

// An indent too deep to leave room for text is dropped rather than squeezing the
// continuation into a sliver.
func TestWrapDropsIndentWhenTooDeep(t *testing.T) {
	ln := strings.Repeat(" ", 30) + strings.Repeat("v", 40)
	for _, part := range strings.Split(wrapLine(ln, 40), "\n") {
		if w := len(part); w > 40 {
			t.Fatalf("line is %d columns, pane is 40: %q", w, part)
		}
	}
}

// The fold is redone on every resize, both ways: narrowing folds more, and
// widening past the content unfolds it back to the source lines.
func TestWrapFollowsResize(t *testing.T) {
	m := openBig(t, 120)
	src := strings.Count(renderDetail(m.detailEntry), "\n") + 1
	at120 := m.detail.TotalLineCount()

	m2, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 40})
	m = m2.(Model)
	if got := m.detail.TotalLineCount(); got <= at120 {
		t.Fatalf("narrowing to 60 should fold more: %d lines, was %d at 120", got, at120)
	}
	if got := maxLineWidth(m.detail.View()); got > m.detail.Width {
		t.Fatalf("after narrowing the pane shows %d columns, width is %d", got, m.detail.Width)
	}
	if got := bodyWidth(m.View()); got > 60 {
		t.Fatalf("body is %d columns after narrowing, terminal is 60", got)
	}

	m2, _ = m.Update(tea.WindowSizeMsg{Width: 400, Height: 40})
	m = m2.(Model)
	if got := m.detail.TotalLineCount(); got != src {
		t.Fatalf("widening past the content should unfold it: %d lines, source has %d", got, src)
	}
}

// Colour carries onto continuation lines. The overlay composites the pane line
// by line, so a style opened on the first line and not re-opened on the next
// would leave the rest of the value uncoloured.
//
// Built by hand rather than through lipgloss, which drops colour when the tests
// have no terminal.
func TestWrapKeepsColourOnContinuations(t *testing.T) {
	ln := "  userAgent: \x1b[32m" + strings.Repeat("word ", 20) + "\x1b[0m"
	parts := strings.Split(wrapLine(ln, 30), "\n")
	if len(parts) < 2 {
		t.Fatalf("fixture should fold at 30 columns: %q", parts)
	}
	for _, p := range parts[1:] {
		if !strings.Contains(p, "\x1b[32m") {
			t.Fatalf("continuation lost its colour: %q", p)
		}
	}
}
