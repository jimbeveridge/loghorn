package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"clog/internal/entry"
)

func imps(m Model, n int) Model {
	for i := 0; i < n; i++ {
		m2, _ := m.Update(entryMsg(entry.Entry{Message: fmt.Sprintf("E%d", i), Important: true}))
		m = m2.(Model)
	}
	return m
}

// While paused with newer rows below the visible window, the status bar must
// surface a ▼ indicator; while following it must not (the window is pinned to
// the bottom, so nothing newer is unseen).
func TestMoreContentIndicatorWhenPaused(t *testing.T) {
	m := NewModel(nil, 1000, 0) // contextN 0 → one display row per important entry
	m.width, m.height = 40, 5   // visible = 4 rows
	m = imps(m, 20)

	// Following: pinned to the bottom, nothing unseen below.
	if _, end := m.listWindow(); end != len(m.rows) {
		t.Fatalf("following should reach the last row, end=%d len=%d", end, len(m.rows))
	}
	if strings.Contains(m.statusBar(), "▼") {
		t.Fatalf("no ▼ expected while following:\n%s", m.statusBar())
	}

	// Pause and scroll up.
	for i := 0; i < 10; i++ {
		m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
		m = m2.(Model)
	}
	if m.follow {
		t.Fatalf("k should pause (follow=false)")
	}
	_, end := m.listWindow()
	if below := len(m.rows) - end; below <= 0 {
		t.Fatalf("expected unseen rows below while paused, below=%d", below)
	}
	if !strings.Contains(m.statusBar(), "▼") {
		t.Fatalf("status bar should show ▼ for unseen newer content:\n%s", m.statusBar())
	}
}

// The model must reflow to WindowSizeMsg: a taller terminal shows more list
// rows, and an open detail pane adopts the new height.
func TestResizeReflowsListAndDetail(t *testing.T) {
	m := NewModel(nil, 1000, 0)
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
