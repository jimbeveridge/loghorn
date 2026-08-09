package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeChild records what the TUI asks of the launched producer.
type fakeChild struct {
	sent      []byte
	terminate int
}

func (c *fakeChild) Send(p []byte) error { c.sent = append(c.sent, p...); return nil }
func (c *fakeChild) Terminate()          { c.terminate++ }
func (c *fakeChild) Name() string        { return "npm" }

func withChild() (Model, *fakeChild) {
	c := &fakeChild{}
	m := NewModel(nil, 100, 0)
	m.width, m.height = 100, 12
	m.SetChild(c)
	m = imps(m, 4)
	return m, c
}

// runCmd executes the tea.Cmd a key produced and returns its message, the way
// the runtime would.
func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// 'q' takes the producer down before leaving; that is the whole point of the
// launch mode.
func TestQuitTerminatesChild(t *testing.T) {
	m, c := withChild()

	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	_ = m2
	if cmd == nil {
		t.Fatalf("q should produce a command")
	}
	if c.terminate != 0 {
		t.Fatalf("termination must happen in the command, not inline — it blocks for the grace period")
	}
	if msg := runCmd(cmd); msg != (tea.QuitMsg{}) {
		t.Fatalf("the command should quit after terminating, got %#v", msg)
	}
	if c.terminate != 1 {
		t.Fatalf("child should have been terminated once, got %d", c.terminate)
	}
}

// ctrl+c behaves like q.
func TestCtrlCTerminatesChild(t *testing.T) {
	m, c := withChild()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	runCmd(cmd)
	if c.terminate != 1 {
		t.Fatalf("ctrl+c should terminate the child, got %d", c.terminate)
	}
}

// 'Q' detaches: clog leaves, the producer keeps running.
func TestShiftQuitDetaches(t *testing.T) {
	m, c := withChild()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Q'}})
	if msg := runCmd(cmd); msg != (tea.QuitMsg{}) {
		t.Fatalf("Q should quit, got %#v", msg)
	}
	if c.terminate != 0 {
		t.Fatalf("Q must leave the child running, terminate called %d times", c.terminate)
	}
}

// With no child there is nothing to stop, and q is just quit.
func TestQuitWithoutChildJustQuits(t *testing.T) {
	m := NewModel(nil, 100, 0)
	m.width, m.height = 80, 12
	m = imps(m, 3)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if msg := runCmd(cmd); msg != (tea.QuitMsg{}) {
		t.Fatalf("q should quit in pipe mode, got %#v", msg)
	}
}

// Once the child is gone, q must not try to signal it again.
func TestQuitAfterChildExitDoesNotTerminate(t *testing.T) {
	m, c := withChild()
	m2, _ := m.Update(childExitMsg{Code: 1})
	m = m2.(Model)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if msg := runCmd(cmd); msg != (tea.QuitMsg{}) {
		t.Fatalf("q should still quit, got %#v", msg)
	}
	if c.terminate != 0 {
		t.Fatalf("must not signal an already-dead child, terminate called %d times", c.terminate)
	}
}

// A dead producer is reported, and the TUI stays open so the crash is readable.
func TestChildExitIsReportedAndStaysOpen(t *testing.T) {
	m, _ := withChild()
	m2, cmd := m.Update(childExitMsg{Code: 137})
	m = m2.(Model)

	if cmd != nil {
		t.Fatalf("child exit must not quit the TUI — the scrollback is the point")
	}
	if got := m.statusBar(); !strings.Contains(got, "child exited (137)") {
		t.Fatalf("status bar should report the exit:\n%s", got)
	}
	if len(m.rows) != 4 {
		t.Fatalf("rows should survive the child's exit, got %d", len(m.rows))
	}
}

// Forward mode hands every key to the child — including 'q', which must not
// quit — and esc is the way out.
func TestForwardModeSendsKeysToChild(t *testing.T) {
	m, c := withChild()

	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m = m2.(Model)
	if !m.forwarding {
		t.Fatalf("f should enter forward mode")
	}

	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'r'}},
		{Type: tea.KeyRunes, Runes: []rune{'s'}},
		{Type: tea.KeyEnter},
	} {
		m2, cmd := m.Update(k)
		m = m2.(Model)
		if msg := runCmd(cmd); msg == (tea.QuitMsg{}) {
			t.Fatalf("no key should quit while forwarding")
		}
	}
	if string(c.sent) != "rs\r" {
		t.Fatalf("child should have received %q, got %q", "rs\r", string(c.sent))
	}

	// 'q' goes to the child too.
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = m2.(Model)
	if msg := runCmd(cmd); msg == (tea.QuitMsg{}) {
		t.Fatalf("q must reach the child while forwarding, not quit clog")
	}
	if string(c.sent) != "rs\rq" {
		t.Fatalf("q should have been forwarded, got %q", string(c.sent))
	}

	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = m2.(Model)
	if m.forwarding {
		t.Fatalf("esc should leave forward mode")
	}
	// And now q quits again.
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if msg := runCmd(cmd); msg != (tea.QuitMsg{}) {
		t.Fatalf("q should quit once forwarding has stopped, got %#v", msg)
	}
}

// Forward mode has to be unmistakable, since q no longer quits.
func TestForwardModeBannerIsLoud(t *testing.T) {
	m, _ := withChild()
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m = m2.(Model)

	bar := m.statusBar()
	for _, want := range []string{"FORWARDING", "npm", "esc"} {
		if !strings.Contains(bar, want) {
			t.Fatalf("forward banner should mention %q:\n%s", want, bar)
		}
	}
	if strings.Contains(bar, "q quit") {
		t.Fatalf("the banner must not still advertise q as quit:\n%s", bar)
	}
}

// Forward mode is meaningless without a child, and must not swallow the keyboard
// in pipe mode.
func TestForwardModeUnavailableWithoutChild(t *testing.T) {
	m := NewModel(nil, 100, 0)
	m.width, m.height = 80, 12
	m = imps(m, 3)

	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m = m2.(Model)
	if m.forwarding {
		t.Fatalf("f should do nothing in pipe mode")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if msg := runCmd(cmd); msg != (tea.QuitMsg{}) {
		t.Fatalf("q must still quit in pipe mode")
	}
}

// A child that dies while forwarding drops us out of the mode, rather than
// leaving the keyboard pointed at nothing.
func TestChildExitLeavesForwardMode(t *testing.T) {
	m, _ := withChild()
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m = m2.(Model)
	m2, _ = m.Update(childExitMsg{Code: 1})
	m = m2.(Model)
	if m.forwarding {
		t.Fatalf("child exit should leave forward mode")
	}
}

// The bar advertises the launch-mode keys only when there is a child.
func TestStatusBarHintsDependOnChild(t *testing.T) {
	m, _ := withChild()
	bar := m.statusBar()
	for _, want := range []string{"q quit+stop", "Q detach", "f keys→child"} {
		if !strings.Contains(bar, want) {
			t.Fatalf("launch-mode bar should mention %q:\n%s", want, bar)
		}
	}

	pipe := NewModel(nil, 100, 0)
	pipe.width, pipe.height = 100, 12
	pipe = imps(pipe, 3)
	bar = pipe.statusBar()
	if strings.Contains(bar, "detach") || strings.Contains(bar, "keys→child") {
		t.Fatalf("pipe-mode bar should not advertise child keys:\n%s", bar)
	}
	if !strings.Contains(bar, "q quit") {
		t.Fatalf("pipe-mode bar should still advertise q:\n%s", bar)
	}
}
