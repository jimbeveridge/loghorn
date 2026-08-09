package tui

import (
	"fmt"
	"strings"
	"testing"

	"clog/internal/entry"
)

// The status bar reports how many lines have been ingested, so an idle-looking
// screen can be told apart from a stalled pipe. It counts every line read —
// including routine ones the display filters out — which is the whole point:
// "shown" can sit still for minutes while lines are streaming in fine.
func TestStatusBarCountsIngestedLines(t *testing.T) {
	m := NewModel(nil, 1000)
	m.width, m.height = 100, 20

	if got := m.statusBar(); !strings.Contains(got, "0 lines") {
		t.Fatalf("status bar should start at 0 lines:\n%s", got)
	}

	// 7 routine lines and 2 important ones: 9 ingested, 2 shown.
	for i := 0; i < 7; i++ {
		m2, _ := m.Update(entryMsg(entry.Entry{Message: fmt.Sprintf("routine %d", i)}))
		m = m2.(Model)
	}
	m = imps(m, 2)

	got := m.statusBar()
	if !strings.Contains(got, "9 lines") {
		t.Fatalf("status bar should count all 9 ingested lines:\n%s", got)
	}
	if !strings.Contains(got, "2 shown") {
		t.Fatalf("status bar should still report 2 shown rows:\n%s", got)
	}
}

// The counter keeps climbing past the scrollback capacity — it is a tally of
// what has been read, not of what is still in the ring.
func TestIngestCounterSurvivesRingEviction(t *testing.T) {
	m := NewModel(nil, 4) // ring holds 4 entries
	m.width, m.height = 100, 20
	m = imps(m, 10)

	if len(m.rows) > 4 {
		t.Fatalf("precondition: ring should have evicted down to 4, got %d rows", len(m.rows))
	}
	if got := m.statusBar(); !strings.Contains(got, "10 lines") {
		t.Fatalf("counter should tally all 10 ingested lines, not the 4 retained:\n%s", got)
	}
}
