package tui

import (
	"strings"
	"testing"
	"time"

	"loghorn/internal/entry"
)

// Each list line is prefaced with the wall-clock ingest time as HH:MM:SS.mmm,
// with no date.
func TestListPrefixesIngestTime(t *testing.T) {
	m := NewModel(nil, 100)
	m.width, m.height = 80, 10
	when := time.Date(2026, 8, 6, 15, 4, 5, int(123*time.Millisecond), time.UTC)

	m2, _ := m.Update(entryMsg(entry.Entry{Message: "boom", Important: true, Received: when}))
	m = m2.(Model)

	view := m.View()
	if !strings.Contains(view, "15:04:05.123") {
		t.Fatalf("list should prefix the ingest time HH:MM:SS.mmm:\n%s", view)
	}
	if strings.Contains(view, "2026") {
		t.Fatalf("time prefix must not include the date:\n%s", view)
	}
}
