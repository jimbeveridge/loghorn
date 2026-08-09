package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func key(m Model, s string) (Model, tea.Cmd) {
	var msg tea.KeyMsg
	switch s {
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case " ":
		msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	m2, cmd := m.Update(msg)
	return m2.(Model), cmd
}

func helpModel() Model {
	m := NewModel(nil, 100)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = m2.(Model)
	return imps(m, 6)
}

// '?' opens the reference and esc closes it.
func TestHelpOpensAndCloses(t *testing.T) {
	m := helpModel()
	m, _ = key(m, "?")
	if !m.showHelp {
		t.Fatalf("? should open help")
	}
	if !strings.Contains(m.View(), "clog — keys") {
		t.Fatalf("help view should render the reference:\n%s", m.View())
	}
	m, _ = key(m, "esc")
	if m.showHelp {
		t.Fatalf("esc should close help")
	}
}

// Every key that reads as "done" closes it — you should not have to remember
// which one.
func TestHelpClosesOnAnyDoneKey(t *testing.T) {
	for _, k := range []string{"esc", "enter", "?", "q", " "} {
		m := helpModel()
		m, cmd := key(m, "?")
		if !m.showHelp {
			t.Fatalf("precondition: ? should open help")
		}
		m, cmd = key(m, k)
		if m.showHelp {
			t.Fatalf("%q should close help", k)
		}
		if cmd != nil && cmd() == (tea.QuitMsg{}) {
			t.Fatalf("%q should close help, not quit clog", k)
		}
	}
}

// ctrl+c still quits from help, as it does everywhere.
func TestHelpCtrlCStillQuits(t *testing.T) {
	m := helpModel()
	m, _ = key(m, "?")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil || cmd() != (tea.QuitMsg{}) {
		t.Fatalf("ctrl+c should quit from the help overlay")
	}
}

// Scroll keys scroll the reference rather than closing it, so a short terminal
// can still reach the bottom.
func TestHelpScrolls(t *testing.T) {
	m := NewModel(nil, 100)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 8}) // too short for the whole page
	m = m2.(Model)
	m = imps(m, 3)
	m, _ = key(m, "?")

	if m.help.AtBottom() {
		t.Fatalf("precondition: help should overflow an 8-line terminal")
	}
	m, _ = key(m, "j")
	if m.help.YOffset == 0 {
		t.Fatalf("j should scroll the help page, YOffset=%d", m.help.YOffset)
	}
	if !m.showHelp {
		t.Fatalf("scrolling must not close help")
	}
}

// The overlay obeys the frame-height invariant like every other view.
func TestHelpFrameFitsTerminal(t *testing.T) {
	for _, h := range []int{6, 12, 24, 40} {
		m := NewModel(nil, 100)
		m2, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: h})
		m = m2.(Model)
		m = imps(m, 30)
		m, _ = key(m, "?")
		if got := frameHeight(m.View()); got != h {
			t.Fatalf("help frame is %d lines, terminal is %d", got, h)
		}
	}
}

// Resizing while help is open keeps it sized to the terminal.
func TestHelpReflowsOnResize(t *testing.T) {
	m := helpModel()
	m, _ = key(m, "?")
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	m = m2.(Model)
	if m.help.Height != 29 {
		t.Fatalf("help should adopt the new height, got %d", m.help.Height)
	}
	if got := frameHeight(m.View()); got != 30 {
		t.Fatalf("frame is %d lines after resize, want 30", got)
	}
}

// The reference must actually document the keys taken off the bar, or moving
// them there loses them.
func TestHelpDocumentsTheKeysTakenOffTheBar(t *testing.T) {
	m := helpModel()
	help := m.helpContent()
	for _, want := range []string{"mouse capture", "this help", "oldest row", "grab the shade"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help should document %q:\n%s", want, help)
		}
	}
}

// The bar carries only the constantly-used keys, plus the pointer to the rest.
func TestStatusBarCarriesOnlyCommonKeys(t *testing.T) {
	m := helpModel()
	bar := m.statusBar()
	for _, want := range []string{"j/k", "spc", "enter open", "? help", "q quit"} {
		if !strings.Contains(bar, want) {
			t.Fatalf("bar should keep the common key %q:\n%s", want, bar)
		}
	}
	for _, unwanted := range []string{"mouse:on", "m mouse", "keys→child"} {
		if strings.Contains(bar, unwanted) {
			t.Fatalf("bar should not carry %q any more:\n%s", unwanted, bar)
		}
	}
}

// Opening help from the detail pane returns to it, rather than dumping you back
// in the list.
func TestHelpOverlaysDetailPane(t *testing.T) {
	m := helpModel()
	m, _ = key(m, "k") // hold, select a row
	m, _ = key(m, "enter")
	if !m.showDetail {
		t.Fatalf("precondition: detail should be open")
	}
	m, _ = key(m, "?")
	if !m.showHelp || !m.showDetail {
		t.Fatalf("help should overlay the detail pane, not replace it (help=%v detail=%v)",
			m.showHelp, m.showDetail)
	}
	m, _ = key(m, "esc")
	if m.showHelp || !m.showDetail {
		t.Fatalf("closing help should return to the detail pane (help=%v detail=%v)",
			m.showHelp, m.showDetail)
	}
}

// Enter opened the pane, so enter closes it — no reaching across the keyboard.
func TestEnterClosesDetailPane(t *testing.T) {
	m := helpModel()
	m, _ = key(m, "k")
	m, _ = key(m, "enter")
	if !m.showDetail {
		t.Fatalf("enter should open the detail pane")
	}
	m, _ = key(m, "enter")
	if m.showDetail {
		t.Fatalf("enter should close the detail pane too")
	}

	// esc still works.
	m, _ = key(m, "enter")
	m, _ = key(m, "esc")
	if m.showDetail {
		t.Fatalf("esc should still close the detail pane")
	}
}

// Closing with enter must not immediately reopen it on the same keystroke.
func TestEnterCloseDoesNotReopen(t *testing.T) {
	m := helpModel()
	m, _ = key(m, "k")
	for i := 0; i < 4; i++ {
		want := i%2 == 0
		m, _ = key(m, "enter")
		if m.showDetail != want {
			t.Fatalf("enter %d: detail=%v, want %v — enter should toggle cleanly", i+1, m.showDetail, want)
		}
	}
}

// The detail bar advertises both closing keys.
func TestDetailBarShowsBothCloseKeys(t *testing.T) {
	m := helpModel()
	m, _ = key(m, "k")
	m, _ = key(m, "enter")
	if bar := m.statusBar(); !strings.Contains(bar, "enter/esc close") {
		t.Fatalf("detail bar should advertise enter and esc:\n%s", bar)
	}
}

// Forward mode still owns the keyboard: '?' goes to the child, not to help.
func TestForwardModeSwallowsHelpKey(t *testing.T) {
	m, c := withChild()
	m, _ = key(m, "f")
	if !m.forwarding {
		t.Fatalf("precondition: f should enter forward mode")
	}
	m, _ = key(m, "?")
	if m.showHelp {
		t.Fatalf("? must reach the child while forwarding, not open help")
	}
	if string(c.sent) != "?" {
		t.Fatalf("child should have received %q, got %q", "?", string(c.sent))
	}
}
