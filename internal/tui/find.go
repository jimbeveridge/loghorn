package tui

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbeveridge/loghorn/internal/entry"
)

// Find is the third view filter: '/', some text, enter, and the view keeps only
// lines containing that text, ignoring case, until '/' and a bare enter clears
// it. It stacks with the other two — within a pinned request, among failures or
// all lines — and applies to lines as they arrive, the same as they do.

// setFind replaces the active find, "" turning it off, and re-filters.
func (m Model) setFind(s string) Model {
	m.find = s
	m.findHits = nil // verdicts were for the old text
	return m.rebuild()
}

// findMatches keeps the entries that match the active find.
//
// rebuild re-filters the whole ring on every arriving line, and searching each
// entry's text every time would make a burst of input quadratic. Entries never
// change once read, so each verdict is cached by Seq for as long as the find text
// stands. The cache is dropped and refilled once it holds far more entries than
// the ring does — the ring has evicted the rest — so it stays bounded.
func (m *Model) findMatches(entries []entry.Entry) []entry.Entry {
	if m.findHits == nil || len(m.findHits) > 2*len(entries)+64 {
		m.findHits = make(map[int]bool, len(entries))
	}
	needle := strings.ToLower(m.find)
	kept := make([]entry.Entry, 0, len(entries))
	for _, e := range entries {
		hit, ok := m.findHits[e.Seq]
		if !ok {
			hit = entryMatches(e, needle)
			m.findHits[e.Seq] = hit
		}
		if hit {
			kept = append(kept, e)
		}
	}
	return kept
}

// entryMatches reports whether e contains needle, which is already lower case.
// The raw line carries every field, including ones the list doesn't show; the
// message is checked too, since it is decoded — a JSON-escaped character matches
// as it reads on screen.
func entryMatches(e entry.Entry, needle string) bool {
	return strings.Contains(strings.ToLower(string(e.Raw)), needle) ||
		strings.Contains(strings.ToLower(e.Message), needle)
}

// openFind opens the prompt. It starts empty even while a find is active, which
// is what makes '/' then enter the way to clear one.
func (m Model) openFind() Model {
	m.finding, m.findInput = true, ""
	return m
}

// handleFindKey edits the prompt. Every printable key is text — 'q' included —
// so the only ways out are enter, esc, backspace past the start, and ctrl+c.
func (m Model) handleFindKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m.quit()
	case tea.KeyEnter:
		m.finding = false
		return m.setFind(m.findInput), nil
	case tea.KeyEsc:
		m.finding = false // whatever find was active stays active
		return m, nil
	case tea.KeyBackspace:
		if m.findInput == "" {
			m.finding = false
			return m, nil
		}
		r := []rune(m.findInput)
		m.findInput = string(r[:len(r)-1])
		return m, nil
	case tea.KeyRunes, tea.KeySpace:
		// A paste arrives as runes too; a pasted newline can't be part of a
		// single-line match.
		m.findInput += strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, string(msg.Runes))
	}
	return m, nil
}

// findPrompt is the status bar while the prompt is open. With nothing typed it
// says what enter will do, since that is clearing an active find.
func (m Model) findPrompt() string {
	hint := " · enter find · esc cancel"
	if m.findInput == "" {
		hint = " · type to find · esc cancel"
		if m.find != "" {
			hint = " · enter clears /" + shortFind(m.find) + " · esc keeps it"
		}
	}
	return "  " + statusStyle.Render("loghorn "+m.spinner()+" · /"+m.findInput+"█") + moreStyle.Render(hint)
}

// shortFind trims a long find so it can't swallow the bar.
func shortFind(s string) string {
	const n = 20
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
