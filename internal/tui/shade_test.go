package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func press(m Model, r rune) Model {
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return m2.(Model)
}

// The cursor starts on the handle, so clog opens live.
func TestOpensLive(t *testing.T) {
	m := NewModel(nil, 100, 0)
	m.width, m.height = 40, 10
	if !m.onShade() {
		t.Fatalf("a fresh model should start on the handle")
	}
	m = imps(m, 5)
	if !m.onShade() || m.selected != len(m.rows) {
		t.Fatalf("should still be on the handle after rows arrive (selected=%d rows=%d)", m.selected, len(m.rows))
	}
}

// Down while already on the handle has nowhere to go and must not let go of it.
func TestDownOnHandleStaysLive(t *testing.T) {
	m := NewModel(nil, 100, 0)
	m.width, m.height = 40, 10
	m = imps(m, 5)

	m = press(m, 'j')
	if !m.onShade() {
		t.Fatalf("down on the handle must stay LIVE, got HELD")
	}
	if m.selected != len(m.rows) {
		t.Fatalf("cursor should stay on the handle, got %d of %d rows", m.selected, len(m.rows))
	}
}

// Up off the handle lands on the newest row and pulls the shade down.
func TestUpOffHandleHolds(t *testing.T) {
	m := NewModel(nil, 100, 0)
	m.width, m.height = 40, 10
	m = imps(m, 5)

	m = press(m, 'k')
	if m.onShade() {
		t.Fatalf("up off the handle should hold the shade")
	}
	if m.selected != len(m.rows)-1 {
		t.Fatalf("up off the handle should land on the newest row %d, got %d", len(m.rows)-1, m.selected)
	}
}

// Walking back down to the handle goes live again.
func TestDownToHandleGoesLive(t *testing.T) {
	m := NewModel(nil, 100, 0)
	m.width, m.height = 40, 10
	m = imps(m, 5)
	m = press(m, 'k')
	if m.onShade() {
		t.Fatalf("k should hold the shade")
	}
	for !m.onShade() {
		m = press(m, 'j')
	}
	if m.selected != len(m.rows) {
		t.Fatalf("reaching the handle should park on it, got %d", m.selected)
	}
}

// Up at the very top is a no-op and stays held.
func TestUpAtTopIsNoop(t *testing.T) {
	m := NewModel(nil, 100, 0)
	m.width, m.height = 40, 10
	m = imps(m, 5)
	for m.selected > 0 {
		m = press(m, 'k')
	}
	m = press(m, 'k')
	if m.selected != 0 || m.onShade() {
		t.Fatalf("up at the top should be a no-op and stay held (selected=%d live=%v)", m.selected, m.onShade())
	}
}

// G grabs the handle from anywhere in the scrollback.
func TestGGrabsHandle(t *testing.T) {
	m := NewModel(nil, 100, 0)
	m.width, m.height = 40, 10
	m = imps(m, 5)
	m = press(m, 'k')
	m = press(m, 'k')
	if m.onShade() {
		t.Fatalf("k should hold the shade")
	}
	m = press(m, 'G')
	if !m.onShade() || m.selected != len(m.rows) {
		t.Fatalf("G should grab the handle (live=%v selected=%d)", m.onShade(), m.selected)
	}
}

// g jumps to the oldest row and holds.
func TestGoTopHolds(t *testing.T) {
	m := NewModel(nil, 100, 0)
	m.width, m.height = 40, 10
	m = imps(m, 5)
	m = press(m, 'g')
	if m.selected != 0 || m.onShade() {
		t.Fatalf("g should select the oldest row and hold (selected=%d live=%v)", m.selected, m.onShade())
	}
}

// Space toggles: off the handle to the newest row, and back onto the handle.
func TestSpaceToggles(t *testing.T) {
	m := NewModel(nil, 100, 0)
	m.width, m.height = 40, 10
	m = imps(m, 5)

	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m = m2.(Model)
	if m.onShade() {
		t.Fatalf("space on the handle should hold the shade")
	}
	if m.selected != len(m.rows)-1 {
		t.Fatalf("space should hold at the newest row %d, got %d", len(m.rows)-1, m.selected)
	}

	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m = m2.(Model)
	if !m.onShade() {
		t.Fatalf("space while held should grab the handle")
	}
}
