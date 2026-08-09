package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"clog/internal/entry"
)

// clickAt presses the left mouse button at a screen cell.
func clickAt(m Model, x, y int) Model {
	m2, _ := m.Update(tea.MouseMsg{
		X: x, Y: y,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	})
	return m2.(Model)
}

func wheel(m Model, btn tea.MouseButton) Model {
	m2, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: btn})
	return m2.(Model)
}

// A model with a controllable clock, so double-click timing is testable without
// sleeping.
func mouseModel(contextN int) (Model, *time.Time) {
	clock := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	m := NewModel(nil, 1000, contextN)
	m.now = func() time.Time { return clock }
	m.width, m.height = 100, 12
	return m, &clock
}

// Clicking a line selects the row drawn on it.
func TestClickSelectsRow(t *testing.T) {
	m, _ := mouseModel(0)
	m = imps(m, 8) // 8 rows, all visible in an 11-line budget

	m = clickAt(m, 10, 2)
	if m.selected != 2 {
		t.Fatalf("click on line 2 should select row 2, got %d", m.selected)
	}
	m = clickAt(m, 10, 5)
	if m.selected != 5 {
		t.Fatalf("click on line 5 should select row 5, got %d", m.selected)
	}
}

// Clicking away from the newest row pauses; clicking the newest row resumes
// follow — the same rule j/k use.
func TestClickDrivesFollowLikeKeys(t *testing.T) {
	m, _ := mouseModel(0)
	m = imps(m, 8)
	if !m.follow {
		t.Fatalf("precondition: should start following")
	}

	m = clickAt(m, 10, 3)
	if m.follow {
		t.Fatalf("clicking an older row should pause")
	}
	m = clickAt(m, 10, 7) // the last of 8 rows
	if !m.follow {
		t.Fatalf("clicking the newest row should resume follow (selected=%d of %d)", m.selected, len(m.rows))
	}
}

// A "⋯" gap marker is drawn but is not a row: clicking it must do nothing.
// Its line index also proves screen lines and row indices are not the same.
func TestClickOnGapMarkerIsNoop(t *testing.T) {
	m, _ := mouseModel(1)
	// Two groups of (routine, important) separated by a hidden routine line, so
	// the second group is preceded by a gap marker.
	for g := 0; g < 3; g++ {
		m = feed(m,
			entry.Entry{Message: fmt.Sprintf("skipped %d", g)},
			entry.Entry{Message: fmt.Sprintf("context %d", g)},
			entry.Entry{Message: fmt.Sprintf("boom %d", g), Important: true},
		)
	}

	lines := m.listLines(m.width)
	gapLine := -1
	for i, l := range lines {
		if l.row < 0 {
			gapLine = i
			break
		}
	}
	if gapLine < 0 {
		t.Fatalf("precondition: expected a gap marker line, got %d lines", len(lines))
	}

	m = clickAt(m, 10, 0) // land somewhere known first
	before := m.selected
	m = clickAt(m, 10, gapLine)
	if m.selected != before {
		t.Fatalf("clicking the gap marker moved the selection %d -> %d", before, m.selected)
	}
}

// Clicking past the end of the list, or on the status bar, is a no-op.
func TestClickBelowListIsNoop(t *testing.T) {
	m, _ := mouseModel(0)
	m = imps(m, 3)
	m = clickAt(m, 10, 1)
	before := m.selected

	for _, y := range []int{len(m.listLines(m.width)), m.height - 1, m.height + 5} {
		m = clickAt(m, 10, y)
		if m.selected != before {
			t.Fatalf("click at y=%d should be a no-op, selection moved to %d", y, m.selected)
		}
	}
}

// Two clicks on the same row inside the double-click window open the detail
// pane; the same two clicks spread further apart do not.
func TestDoubleClickOpensDetail(t *testing.T) {
	m, clock := mouseModel(0)
	m = imps(m, 8)

	m = clickAt(m, 10, 2)
	if m.showDetail {
		t.Fatalf("a single click must not open the detail pane")
	}
	*clock = clock.Add(100 * time.Millisecond)
	m = clickAt(m, 10, 2)
	if !m.showDetail {
		t.Fatalf("a double click should open the detail pane")
	}
	if got := m.detailEntry.Message; got != m.rows[2].Entry.Message {
		t.Fatalf("detail shows %q, want the clicked row %q", got, m.rows[2].Entry.Message)
	}

	// Slow clicks are two single clicks.
	m2, _ := mouseModel(0)
	m2 = imps(m2, 8)
	m2 = clickAt(m2, 10, 2)
	*clock = clock.Add(2 * time.Second)
	m2.now = func() time.Time { return *clock }
	m2 = clickAt(m2, 10, 2)
	if m2.showDetail {
		t.Fatalf("clicks 2s apart must not count as a double click")
	}
}

// Clicks on two different rows in quick succession are not a double click.
func TestTwoDifferentRowsIsNotDoubleClick(t *testing.T) {
	m, _ := mouseModel(0)
	m = imps(m, 8)
	m = clickAt(m, 10, 2)
	m = clickAt(m, 10, 3)
	if m.showDetail {
		t.Fatalf("clicking two different rows must not open the detail pane")
	}
}

// With the detail pane open, a click on the list re-targets the pane.
func TestClickRetargetsOpenDetailPane(t *testing.T) {
	m, _ := mouseModel(0)
	m = imps(m, 8)
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)
	if !m.showDetail {
		t.Fatalf("precondition: detail should be open")
	}

	m = clickAt(m, 5, 1) // left half = the list
	if m.selected != 1 {
		t.Fatalf("click should select row 1, got %d", m.selected)
	}
	if got := m.detailEntry.Message; got != m.rows[1].Entry.Message {
		t.Fatalf("detail pane shows %q, want the clicked row %q", got, m.rows[1].Entry.Message)
	}
}

// Clicks landing in the detail pane's half of the screen do not move the list.
func TestClickInDetailPaneIgnored(t *testing.T) {
	m, _ := mouseModel(0)
	m = imps(m, 8)
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)
	before := m.selected

	m = clickAt(m, m.width-2, 1) // right half = the detail pane
	if m.selected != before {
		t.Fatalf("click in the detail pane moved the list selection %d -> %d", before, m.selected)
	}
}

// The wheel moves the selection one row at a time and follows the same pause
// rule as j/k.
func TestWheelScrollsList(t *testing.T) {
	m, _ := mouseModel(0)
	m = imps(m, 8)
	last := len(m.rows) - 1

	m = wheel(m, tea.MouseButtonWheelUp)
	if m.selected != last-1 {
		t.Fatalf("wheel up should move one row up, got %d want %d", m.selected, last-1)
	}
	if m.follow {
		t.Fatalf("wheel up away from the tail should pause")
	}
	m = wheel(m, tea.MouseButtonWheelDown)
	if m.selected != last || !m.follow {
		t.Fatalf("wheel down to the tail should resume follow (selected=%d follow=%v)", m.selected, m.follow)
	}

	// Wheeling past the ends clamps rather than wrapping.
	for i := 0; i < 50; i++ {
		m = wheel(m, tea.MouseButtonWheelUp)
	}
	if m.selected != 0 {
		t.Fatalf("wheel up should clamp at the top, got %d", m.selected)
	}
	for i := 0; i < 50; i++ {
		m = wheel(m, tea.MouseButtonWheelDown)
	}
	if m.selected != last {
		t.Fatalf("wheel down should clamp at the bottom, got %d", m.selected)
	}
}

// With the detail pane open the wheel scrolls the pane, not the list.
func TestWheelScrollsDetailPane(t *testing.T) {
	m, _ := mouseModel(0)
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&sb, "trace line %d\n", i)
	}
	m = feed(m, entry.Entry{Message: "boom", Important: true, Raw: []byte(sb.String())})
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)
	beforeSel := m.selected

	m = wheel(m, tea.MouseButtonWheelDown)
	if m.detail.YOffset <= 0 {
		t.Fatalf("wheel down should scroll the detail pane, YOffset=%d", m.detail.YOffset)
	}
	if m.selected != beforeSel {
		t.Fatalf("scrolling the detail pane must not move the list selection (%d -> %d)", beforeSel, m.selected)
	}
}

// 'm' toggles mouse capture and emits the command that switches terminal
// tracking, so text selection can be handed back to the terminal for copying.
func TestMouseToggle(t *testing.T) {
	m, _ := mouseModel(0)
	m = imps(m, 3)
	if !m.mouse {
		t.Fatalf("mouse capture should start enabled")
	}
	if !strings.Contains(m.statusBar(), "mouse:on") {
		t.Fatalf("status bar should show mouse:on:\n%s", m.statusBar())
	}

	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = m2.(Model)
	if m.mouse {
		t.Fatalf("'m' should disable mouse capture")
	}
	if cmd == nil {
		t.Fatalf("'m' should emit a command to change terminal mouse tracking")
	}
	if !strings.Contains(m.statusBar(), "mouse:off") {
		t.Fatalf("status bar should show mouse:off:\n%s", m.statusBar())
	}

	m2, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = m2.(Model)
	if !m.mouse || cmd == nil {
		t.Fatalf("'m' should re-enable capture and emit a command (mouse=%v cmd=%v)", m.mouse, cmd != nil)
	}
}

// The toggle works while the detail pane is open, where the viewport otherwise
// owns the keyboard.
func TestMouseToggleWorksInDetail(t *testing.T) {
	m, _ := mouseModel(0)
	m = imps(m, 3)
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)

	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = m2.(Model)
	if m.mouse {
		t.Fatalf("'m' should toggle capture from the detail pane too")
	}
	if !m.showDetail {
		t.Fatalf("'m' must not close the detail pane")
	}
}

// Mouse handling must not break the frame-height invariant.
func TestFrameStillFitsAfterMouseSelection(t *testing.T) {
	m, _ := mouseModel(2)
	for g := 0; g < 10; g++ {
		m = feed(m,
			entry.Entry{Message: fmt.Sprintf("routine %d", g)},
			entry.Entry{Message: fmt.Sprintf("boom %d", g), Important: true},
		)
	}
	for y := 0; y < m.height+2; y++ {
		m = clickAt(m, 10, y)
		if got := frameHeight(m.View()); got > m.height {
			t.Fatalf("after clicking y=%d the frame is %d lines, terminal is %d", y, got, m.height)
		}
	}
}
