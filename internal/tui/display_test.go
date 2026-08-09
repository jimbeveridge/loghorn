package tui

import (
	"fmt"
	"testing"

	"loghorn/internal/entry"
)

func lines(n int, important map[int]bool) []entry.Entry {
	es := make([]entry.Entry, n)
	for i := range es {
		es[i] = entry.Entry{Message: fmt.Sprintf("line %d", i), Important: important[i]}
	}
	return es
}

// BuildFailures keeps only what the engine flagged. There is no leading-context
// window any more: 'a' shows the whole stream when the lines around a failure
// are what you want.
func TestBuildFailuresKeepsOnlyImportant(t *testing.T) {
	rows := BuildFailures(lines(10, map[int]bool{3: true, 7: true}))
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Kind != RowImportant {
			t.Fatalf("%q should be important, got kind %v", r.Entry.Message, r.Kind)
		}
	}
	if rows[0].Entry.Message != "line 3" || rows[1].Entry.Message != "line 7" {
		t.Fatalf("wrong rows: %q, %q", rows[0].Entry.Message, rows[1].Entry.Message)
	}
}

// No failures, no rows. This is why the bar carries an ingest count: on a
// healthy run there is genuinely nothing to show.
func TestBuildFailuresEmptyWhenNothingFails(t *testing.T) {
	if rows := BuildFailures(lines(50, nil)); len(rows) != 0 {
		t.Fatalf("expected no rows, got %d", len(rows))
	}
}

// BuildAll keeps everything, and keeps failures distinguishable among it.
func TestBuildAllKeepsEverythingAndItsStyling(t *testing.T) {
	es := lines(6, map[int]bool{2: true, 5: true})
	rows := BuildAll(es)
	if len(rows) != len(es) {
		t.Fatalf("expected %d rows, got %d", len(es), len(rows))
	}
	for i, r := range rows {
		want := RowContext
		if es[i].Important {
			want = RowImportant
		}
		if r.Kind != want {
			t.Fatalf("row %d kind %v, want %v", i, r.Kind, want)
		}
		if r.Entry.Message != es[i].Message {
			t.Fatalf("row %d is %q, want %q", i, r.Entry.Message, es[i].Message)
		}
	}
}

func TestBuildersHandleNoEntries(t *testing.T) {
	if rows := BuildFailures(nil); len(rows) != 0 {
		t.Fatalf("BuildFailures(nil) returned %d rows", len(rows))
	}
	if rows := BuildAll(nil); len(rows) != 0 {
		t.Fatalf("BuildAll(nil) returned %d rows", len(rows))
	}
}
