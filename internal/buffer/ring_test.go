package buffer

import (
	"testing"

	"clog/internal/entry"
)

func msg(m string) entry.Entry { return entry.Entry{Message: m} }

func TestRingUnderCapacity(t *testing.T) {
	r := New(5)
	r.Append(msg("a"))
	r.Append(msg("b"))
	got := r.Snapshot()
	if len(got) != 2 || got[0].Message != "a" || got[1].Message != "b" {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
}

func TestRingEvictsOldest(t *testing.T) {
	r := New(2)
	r.Append(msg("a"))
	r.Append(msg("b"))
	r.Append(msg("c"))
	got := r.Snapshot()
	if len(got) != 2 || got[0].Message != "b" || got[1].Message != "c" {
		t.Fatalf("expected [b c], got %+v", got)
	}
}
