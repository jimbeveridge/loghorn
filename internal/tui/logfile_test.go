package tui

import (
	"strings"
	"testing"
)

// The normal state says nothing; the bar only names the surprising one.
func TestLogFileMarkerAbsentByDefault(t *testing.T) {
	m := NewModel(nil, 100)
	m.width, m.height = 200, 12
	m = imps(m, 2)
	if got := m.statusBar(); strings.Contains(got, "no log file") {
		t.Fatalf("bar should not mention the log file while it works:\n%s", got)
	}
}

// Another loghorn held the lock at startup.
func TestSetLogFileOffShowsOnBar(t *testing.T) {
	m := NewModel(nil, 100)
	m.width, m.height = 200, 12
	m.SetLogFileOff("pid 48213 has it")
	m = imps(m, 2)
	if got := m.statusBar(); !strings.Contains(got, "no log file (pid 48213 has it)") {
		t.Fatalf("bar should name the lock holder:\n%s", got)
	}
}

// A mid-run failure arrives as a message and, unlike a notice, survives the
// next keystroke.
func TestLogFileOffMessagePersists(t *testing.T) {
	m := NewModel(nil, 100)
	m.width, m.height = 200, 12
	m = imps(m, 2)

	m2, cmd := m.Update(LogFileOff("write failed"))
	if cmd != nil {
		t.Fatalf("the marker must not trigger a command")
	}
	m = press(m2.(Model), 'j')

	if got := m.statusBar(); !strings.Contains(got, "no log file (write failed)") {
		t.Fatalf("marker should persist past a keystroke:\n%s", got)
	}
}
