package tui

import (
	"testing"

	"clog/internal/entry"
)

func imp(m string) entry.Entry { return entry.Entry{Message: m, Important: true} }
func rou(m string) entry.Entry { return entry.Entry{Message: m} }

func TestBuildDisplayContextAndHiding(t *testing.T) {
	entries := []entry.Entry{
		rou("r1"), rou("r2"), rou("r3"), imp("E1"), rou("r4"), rou("r5"),
	}
	rows := BuildDisplay(entries, 2)

	// Expect r2, r3 (context), E1 (important). r1, r4, r5 hidden.
	var got []string
	for _, row := range rows {
		got = append(got, row.Entry.Message)
	}
	want := []string{"r2", "r3", "E1"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
	if rows[2].Kind != RowImportant {
		t.Fatalf("E1 should be RowImportant")
	}
	if rows[0].Kind != RowContext {
		t.Fatalf("r2 should be RowContext")
	}
}

func TestBuildDisplayOverlapNoDuplicate(t *testing.T) {
	entries := []entry.Entry{rou("r1"), imp("E1"), imp("E2")}
	rows := BuildDisplay(entries, 2)
	// r1 context for E1; E1 also context for E2 but already shown as important.
	var got []string
	for _, row := range rows {
		got = append(got, row.Entry.Message)
	}
	want := []string{"r1", "E1", "E2"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildDisplayGapMarker(t *testing.T) {
	entries := []entry.Entry{imp("E1"), rou("r1"), rou("r2"), rou("r3"), imp("E2")}
	rows := BuildDisplay(entries, 1)
	// E1 (index0), then context r3 (index3), E2 (index4). r3 is not contiguous with E1.
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d (%v)", len(rows), rows)
	}
	if rows[1].Entry.Message != "r3" || !rows[1].GapBefore {
		t.Fatalf("r3 should start a new group with GapBefore=true: %+v", rows[1])
	}
}
