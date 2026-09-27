package tui

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbeveridge/loghorn/internal/entry"
)

// An arriving entry takes everything already queued behind it into the same
// update, so a replay rebuilds the rows once per batch rather than once per
// line — per line is quadratic in the ring size.
func TestEntryMsgDrainsQueuedEntries(t *testing.T) {
	ch := make(chan entry.Entry, 10)
	for i := 2; i <= 5; i++ {
		ch <- entry.Entry{Message: fmt.Sprintf("E%d", i), Important: true}
	}
	m := NewModel(ch, 100)

	m2, cmd := m.Update(entryMsg(entry.Entry{Message: "E1", Important: true}))
	got := m2.(Model)

	if got.ingested != 5 || got.ring.Len() != 5 || len(got.rows) != 5 {
		t.Fatalf("ingested=%d ring=%d rows=%d, want 5 of each", got.ingested, got.ring.Len(), len(got.rows))
	}
	for i, r := range got.rows {
		if r.Entry.Seq != i+1 {
			t.Errorf("row %d has Seq %d, want %d", i, r.Entry.Seq, i+1)
		}
	}
	if cmd == nil {
		t.Error("no command to wait for the next entry")
	}
}

// A closed channel ends the drain without losing the entries queued before it.
func TestEntryMsgDrainStopsAtClosedChannel(t *testing.T) {
	ch := make(chan entry.Entry, 4)
	ch <- entry.Entry{Message: "E2", Important: true}
	close(ch)
	m := NewModel(ch, 100)

	m2, _ := m.Update(entryMsg(entry.Entry{Message: "E1", Important: true}))
	if got := m2.(Model); got.ingested != 2 {
		t.Fatalf("ingested=%d, want 2", got.ingested)
	}
}

// Replaying a large stream must not be quadratic in the number of lines.
func TestReplayIsLinear(t *testing.T) {
	const n = 50000
	ch := make(chan entry.Entry, 1024)
	go func() {
		for i := 0; i < n; i++ {
			ch <- entry.Entry{Message: "hello", Important: i%10 == 0}
		}
		close(ch)
	}()
	var m tea.Model = NewModel(ch, 100000)
	start := time.Now()
	for {
		msg := waitForEntry(ch)()
		if _, done := msg.(doneMsg); done {
			break
		}
		m, _ = m.Update(msg)
	}
	if got := m.(Model).ingested; got != n {
		t.Fatalf("ingested %d, want %d", got, n)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("replaying %d entries took %v", n, d)
	}
}
