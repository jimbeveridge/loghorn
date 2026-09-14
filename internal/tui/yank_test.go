package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbeveridge/loghorn/internal/adapter"
	"github.com/jimbeveridge/loghorn/internal/entry"
)

// yankModel opens the detail pane on one entry with a captured clipboard.
func yankModel(t *testing.T, e entry.Entry) (Model, *[]string) {
	t.Helper()
	var copied []string
	m := NewModel(nil, 100)
	m.copy = func(s string) error { copied = append(copied, s); return nil }
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	m = m2.(Model)
	e.Important = true
	m = feed(m, e)
	m, _ = key(m, "enter")
	if !m.showDetail {
		t.Fatalf("precondition: detail should be open")
	}
	return m, &copied
}

// 'y' copies the inspected entry.
func TestYankCopiesTheEntry(t *testing.T) {
	m, copied := yankModel(t, adapter.ParseLine([]byte(`{"severity":"ERROR","message":"boom"}`)))

	m, _ = key(m, "y")
	if len(*copied) != 1 {
		t.Fatalf("y should copy once, got %d copies", len(*copied))
	}
	got := (*copied)[0]
	for _, want := range []string{"severity: ERROR", "message: boom"} {
		if !strings.Contains(got, want) {
			t.Fatalf("copied text should contain %s:\n%s", want, got)
		}
	}
}

// What lands on the clipboard has no colour in it — it is going somewhere that
// is not a terminal.
func TestYankCopiesPlainText(t *testing.T) {
	m, copied := yankModel(t, adapter.ParseLine([]byte(bigLogEntry)))
	m, _ = key(m, "y")

	if got := (*copied)[0]; strings.Contains(got, "\x1b[") {
		t.Fatalf("copied text should carry no ANSI escapes:\n%q", got)
	}
}

// The pane's width is a display choice, so the whole entry is copied rather than
// the wrapped view.
func TestYankCopiesTheWholeEntryNotTheWrappedView(t *testing.T) {
	m, copied := yankModel(t, adapter.ParseLine([]byte(bigLogEntry)))
	m, _ = key(m, "y")
	got := (*copied)[0]

	if maxLineWidth(got) <= m.detail.Width {
		t.Fatalf("fixture does not exceed the pane, nothing proved")
	}
	// The long userAgent is folded on screen but must be copied as one line.
	if !strings.Contains(got, "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36") {
		t.Fatalf("copied text should hold the userAgent unfolded:\n%s", got)
	}
}

// A raw (non-JSON) entry copies its original line.
func TestYankCopiesRawEntries(t *testing.T) {
	raw := "panic: runtime error\n\tgoroutine 1 [running]"
	m, copied := yankModel(t, entry.Entry{Message: "panic", Raw: []byte(raw)})
	m, _ = key(m, "y")

	if got := (*copied)[0]; got != raw {
		t.Fatalf("raw entry should copy verbatim:\ngot:  %q\nwant: %q", got, raw)
	}
}

// The copy is confirmed on the bar — a silent clipboard write is indistinguishable
// from a broken one.
func TestYankConfirmsOnTheBar(t *testing.T) {
	m, _ := yankModel(t, entry.Entry{Message: "x", Raw: []byte("one\ntwo\nthree")})
	m, _ = key(m, "y")

	if bar := m.statusBar(); !strings.Contains(bar, "copied 3 lines") {
		t.Fatalf("the bar should confirm the copy:\n%s", bar)
	}
}

func TestYankSingularLine(t *testing.T) {
	m, _ := yankModel(t, entry.Entry{Message: "x", Raw: []byte("just one")})
	m, _ = key(m, "y")
	if bar := m.statusBar(); !strings.Contains(bar, "copied 1 line") {
		t.Fatalf("want a singular confirmation:\n%s", bar)
	}
}

// A clipboard that is not there is reported, not swallowed — otherwise you paste
// stale content and never know why.
func TestYankReportsFailure(t *testing.T) {
	m, _ := yankModel(t, entry.Entry{Message: "x", Raw: []byte("data")})
	m.copy = func(string) error { return errors.New("none of pbcopy found on PATH") }

	m, _ = key(m, "y")
	bar := m.statusBar()
	if !strings.Contains(bar, "clipboard:") || !strings.Contains(bar, "pbcopy") {
		t.Fatalf("a failed copy should say so on the bar:\n%s", bar)
	}
}

// The confirmation clears on the next keystroke, like any notice.
func TestYankNoticeClears(t *testing.T) {
	m, _ := yankModel(t, entry.Entry{Message: "x", Raw: []byte("data")})
	m, _ = key(m, "y")
	if m.notice == "" {
		t.Fatalf("precondition: expected a confirmation")
	}
	m, _ = key(m, "j")
	if m.notice != "" {
		t.Fatalf("the confirmation should clear on the next key, still %q", m.notice)
	}
}

// 'y' belongs to the detail pane. In the list it must not copy — it is free for
// a future line-level yank, and silently copying the wrong thing would be worse
// than doing nothing.
func TestYankOnlyInsideTheDetailPane(t *testing.T) {
	var copied []string
	m := NewModel(nil, 100)
	m.copy = func(s string) error { copied = append(copied, s); return nil }
	m.width, m.height = 120, 20
	m = imps(m, 4)

	m, _ = key(m, "y")
	if len(copied) != 0 {
		t.Fatalf("y in the list should not copy, got %d copies", len(copied))
	}
}

// 'y' must not disturb the pane it is copying from.
func TestYankLeavesThePaneAlone(t *testing.T) {
	m, _ := yankModel(t, adapter.ParseLine([]byte(bigLogEntry)))
	before := m.detail.YOffset
	m, _ = key(m, "y")
	if !m.showDetail {
		t.Fatalf("y should not close the pane")
	}
	if m.detail.YOffset != before {
		t.Fatalf("y should not scroll the pane (%d -> %d)", before, m.detail.YOffset)
	}
}

// The detail bar advertises it, and the help page documents it.
func TestYankIsDiscoverable(t *testing.T) {
	m, _ := yankModel(t, entry.Entry{Message: "x", Raw: []byte("data")})
	if bar := m.statusBar(); !strings.Contains(bar, "y yank") {
		t.Fatalf("the detail bar should advertise y:\n%s", bar)
	}
	if help := m.helpContent(); !strings.Contains(help, "clipboard") {
		t.Fatalf("help should document the yank:\n%s", help)
	}
}
