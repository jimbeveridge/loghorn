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

func collectRecords(t *testing.T, input string) []string {
	t.Helper()
	var got []string
	err := Records(strings.NewReader(input), func(rec []byte) {
		got = append(got, string(rec))
	})
	if err != nil {
		t.Fatalf("Records error: %v", err)
	}
	return got
}

// A stream that doesn't start with "--" is unaffected: each line is still its
// own record, same as Lines.
func TestRecordsFallsBackToLinesWithoutYAMLSeparator(t *testing.T) {
	got := collectRecords(t, "{\"a\":1}\n{\"a\":2}\nplain text\n")
	want := []string{`{"a":1}`, `{"a":2}`, "plain text"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// gcloud's `--format=yaml` output: a "---" line opens each document. Records
// groups the lines between separators into one record per document, keeping
// the leading "---".
func TestRecordsGroupsYAMLDocuments(t *testing.T) {
	input := "---\nseverity: INFO\nmessage: one\n---\nseverity: ERROR\nmessage: two\n"
	got := collectRecords(t, input)
	want := []string{
		"---\nseverity: INFO\nmessage: one",
		"---\nseverity: ERROR\nmessage: two",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d:\n%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("record %d:\ngot:  %q\nwant: %q", i, got[i], want[i])
		}
	}
}

// A YAML document with no trailing "---" after it (stream ends mid-document)
// still gets flushed as a record.
func TestRecordsFlushesFinalYAMLDocument(t *testing.T) {
	got := collectRecords(t, "---\nseverity: INFO\nmessage: only\n")
	if len(got) != 1 || got[0] != "---\nseverity: INFO\nmessage: only" {
		t.Fatalf("got %v", got)
	}
}

func TestRecordsEmptyInput(t *testing.T) {
	got := collectRecords(t, "")
	if len(got) != 0 {
		t.Fatalf("expected no records from empty input, got %v", got)
	}
}
