package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func press(m Model, r rune) Model {
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return m2.(Model)
}

// Pressing down while already on the newest (last) row must not switch to
// PAUSED — there is no row past the bottom to select.
func TestDownAtBottomStaysFollowing(t *testing.T) {
	m := NewModel(nil, 100, 0)
	m.width, m.height = 40, 10
	m = imps(m, 5)
	if !m.follow || m.selected != len(m.rows)-1 {
		t.Fatalf("precondition: should start following at the last row (follow=%v selected=%d)", m.follow, m.selected)
	}
	m = press(m, 'j')
	if !m.follow {
		t.Fatalf("down at the bottom must stay in FOLLOW, got PAUSED")
	}
	if m.selected != len(m.rows)-1 {
		t.Fatalf("selection should stay on the last row, got %d", m.selected)
	}
}

// Walking back down to the newest row resumes follow.
func TestDownToTailResumesFollow(t *testing.T) {
	m := NewModel(nil, 100, 0)
	m.width, m.height = 40, 10
	m = imps(m, 5)
	m = press(m, 'k') // pause, move up one
	if m.follow {
		t.Fatalf("k should pause")
	}
	for m.selected < len(m.rows)-1 {
		m = press(m, 'j')
	}
	if !m.follow {
		t.Fatalf("reaching the newest row again should resume follow")
	}
}

// Up moves the selection and pauses; at the top it is a no-op.
func TestUpPausesAndTopIsNoop(t *testing.T) {
	m := NewModel(nil, 100, 0)
	m.width, m.height = 40, 10
	m = imps(m, 5)
	m = press(m, 'k')
	if m.follow || m.selected != len(m.rows)-2 {
		t.Fatalf("k should pause and move up one (follow=%v selected=%d)", m.follow, m.selected)
	}
	// Walk to the top; further up is a no-op and stays paused.
	for m.selected > 0 {
		m = press(m, 'k')
	}
	m = press(m, 'k')
	if m.selected != 0 || m.follow {
		t.Fatalf("up at the top should be a no-op and remain paused (selected=%d follow=%v)", m.selected, m.follow)
	}
}

// G jumps to the newest row and resumes follow.
func TestGResumesFollow(t *testing.T) {
	m := NewModel(nil, 100, 0)
	m.width, m.height = 40, 10
	m = imps(m, 5)
	m = press(m, 'k')
	m = press(m, 'k')
	if m.follow {
		t.Fatalf("k should pause")
	}
	m = press(m, 'G')
	if !m.follow || m.selected != len(m.rows)-1 {
		t.Fatalf("G should jump to newest and resume follow (follow=%v selected=%d)", m.follow, m.selected)
	}
}
