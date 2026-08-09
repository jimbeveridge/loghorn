package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"loghorn/internal/entry"
)

func imps(m Model, n int) Model {
	for i := 0; i < n; i++ {
		m2, _ := m.Update(entryMsg(entry.Entry{Message: fmt.Sprintf("E%d", i), Important: true}))
		m = m2.(Model)
	}
	return m
}

// While live nothing is behind the shade, so the bar shows no ▼; once it is down
// and rows arrive, the bar reports the backlog. ▲ flags older content scrolled
// above the window.
func TestBacklogIndicators(t *testing.T) {
	m := NewModel(nil, 1000)  // contextN 0 → one display row per important entry
	m.width, m.height = 60, 5 // visible = 4 rows
	m = imps(m, 20)

	if strings.Contains(m.statusBar(), "▼") {
		t.Fatalf("no ▼ expected while live:\n%s", m.statusBar())
	}
	if !strings.Contains(m.statusBar(), "▲") {
		t.Fatalf("expected ▲ for the rows scrolled above the window:\n%s", m.statusBar())
	}

	// Pull the shade down and scroll up.
	for i := 0; i < 10; i++ {
		m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
		m = m2.(Model)
	}
	if m.onShade() {
		t.Fatalf("k should hold the shade")
	}

	m = imps(m, 6) // arrives behind the shade
	if !strings.Contains(m.statusBar(), "▼6 of 6 waiting") {
		t.Fatalf("status bar should report the backlog:\n%s", m.statusBar())
	}
}

// The model must reflow to WindowSizeMsg: a taller terminal shows more list
// rows, and an open detail pane adopts the new height.
func TestResizeReflowsListAndDetail(t *testing.T) {
	m := NewModel(nil, 1000)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	m = m2.(Model)
	if m.width != 80 || m.height != 10 {
		t.Fatalf("resize should update dims, got %dx%d", m.width, m.height)
	}
	m = imps(m, 50)

	start1, end1 := m.listWindow()
	shown1 := end1 - start1

	m2, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m = m2.(Model)
	start2, end2 := m.listWindow()
	shown2 := end2 - start2
	if shown2 <= shown1 {
		t.Fatalf("growing the terminal should show more rows: %d -> %d", shown1, shown2)
	}

	// Detail pane adopts the new height on resize.
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)
	h1 := m.detail.Height
	m2, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 50})
	m = m2.(Model)
	if m.detail.Height <= h1 {
		t.Fatalf("detail viewport should grow on resize: %d -> %d", h1, m.detail.Height)
	}
}
