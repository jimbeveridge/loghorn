package ingest

import (
	"strings"
	"testing"
)

func collect(t *testing.T, input string) []string {
	t.Helper()
	var got []string
	err := Lines(strings.NewReader(input), func(line []byte) {
		got = append(got, string(line))
	})
	if err != nil {
		t.Fatalf("Lines error: %v", err)
	}
	return got
}

func TestLinesBasic(t *testing.T) {
	got := collect(t, "a\nb\nc\n")
	want := []string{"a", "b", "c"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLinesNoTrailingNewline(t *testing.T) {
	got := collect(t, "x\ny")
	if len(got) != 2 || got[1] != "y" {
		t.Fatalf("expected final unterminated line emitted, got %v", got)
	}
}

func TestLinesVeryLong(t *testing.T) {
	long := strings.Repeat("Z", 200000)
	got := collect(t, long+"\n")
	if len(got) != 1 || len(got[0]) != 200000 {
		t.Fatalf("expected one 200000-byte line, got %d lines", len(got))
	}
}
