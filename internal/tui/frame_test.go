package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"loghorn/internal/adapter"
	"loghorn/internal/entry"
)

func frameHeight(s string) int { return strings.Count(s, "\n") + 1 }

// A GCP LogEntry whose message carries a stack trace. JSON decoding turns the
// \n escapes into real newlines, so entry.Message is genuinely multi-line.
const stackTraceEntry = `{"severity":"ERROR","message":"Error: boom\n    at handler (/app/routes/x.js:10:5)\n    at next (/app/mw/y.js:3:1)\n    at run (/app/server/z.js:99:2)\n    at loop (/app/core/w.js:1:1)"}`

func importantLine(t *testing.T, line string) entry.Entry {
	t.Helper()
	e := adapter.ParseLine([]byte(line))
	e.Important = true
	return e
}

func feed(m Model, entries ...entry.Entry) Model {
	for _, e := range entries {
		m2, _ := m.Update(entryMsg(e))
		m = m2.(Model)
	}
	return m
}

// Every list row occupies exactly one terminal line. Messages decoded from JSON
// carry real newlines (stack traces above all); rendering them verbatim makes a
// single row span many lines, which blows the frame past the terminal height.
func TestListRowIsOneLinePerRow(t *testing.T) {
	m := NewModel(nil, 100)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = m2.(Model)
	m = feed(m, importantLine(t, stackTraceEntry))

	if !strings.Contains(m.rows[0].Entry.Message, "\n") {
		t.Fatalf("precondition: fixture message should contain a real newline")
	}
	list := m.listView(m.width)
	if got := frameHeight(list); got != 1 {
		t.Fatalf("one row should render as 1 line, got %d:\n%s", got, list)
	}
}

// The rendered frame must never exceed the terminal height. Bubble Tea's
// renderer drops the *top* lines of an oversized frame (standard_renderer.go
// keeps only the last r.height lines), so an overlong frame wipes the list and
// leaves only the tail of the newest entry on screen.
func TestFrameNeverExceedsTerminalHeight(t *testing.T) {
	const height = 12

	cases := []struct {
		name  string
		build func(Model) Model
	}{
		{
			// Plain rows filling the screen: catches the trailing blank line
			// listView leaves behind.
			name: "rows fill the screen",
			build: func(m Model) Model {
				var es []entry.Entry
				for i := 0; i < 40; i++ {
					es = append(es, entry.Entry{Message: fmt.Sprintf("E%d", i), Important: true})
				}
				return feed(m, es...)
			},
		},
		{
			// Multi-line stack traces.
			name: "multi-line messages",
			build: func(m Model) Model {
				for i := 0; i < 10; i++ {
					m = feed(m, importantLine(t, stackTraceEntry))
				}
				return m
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel(nil, 1000)
			m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: height})
			m = m2.(Model)
			m = tc.build(m)

			if got := frameHeight(m.View()); got > height {
				t.Fatalf("frame is %d lines, terminal is %d — Bubble Tea will drop the top %d:\n%s",
					got, height, got-height, m.View())
			}
		})
	}
}

// The list should use the whole terminal, not just part of it: with plenty of
// rows the frame fills every available line.
func TestFrameFillsTerminalHeight(t *testing.T) {
	const height = 12
	m := NewModel(nil, 1000)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: height})
	m = m2.(Model)
	m = imps(m, 50)

	if got := frameHeight(m.View()); got != height {
		t.Fatalf("frame is %d lines, want the full %d:\n%s", got, height, m.View())
	}
}
