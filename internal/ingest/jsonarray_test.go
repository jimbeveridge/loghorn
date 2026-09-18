package ingest

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/config"
)

// collectAs is collectRecords with an explicit framing, for the cases that
// test an override rather than detection.
func collectAs(t *testing.T, format config.Format, input string) []string {
	t.Helper()
	var got []string
	err := Records(strings.NewReader(input), format, func(rec []byte) {
		got = append(got, string(rec))
	})
	if err != nil {
		t.Fatalf("Records error: %v", err)
	}
	return got
}

// The shape gcloud beta logging tail --json produces: a bare "[" opens the
// stream and each entry is pretty-printed across many lines.
const tailStream = `[
  {
    "insert_id": "a1",
    "severity": 200
  },
  {
    "insert_id": "a2",
    "severity": 500
  }
]
`

func TestDetectsJSONArrayFromLeadingBracket(t *testing.T) {
	got := collectRecords(t, tailStream)
	want := []string{
		"{\n    \"insert_id\": \"a1\",\n    \"severity\": 200\n  }",
		"{\n    \"insert_id\": \"a2\",\n    \"severity\": 500\n  }",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d:\n%q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d:\ngot:  %q\nwant: %q", i, got[i], want[i])
		}
	}
}

// Detection picks each framing from the stream's first line.
func TestDetectionByFirstLine(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		wantFirst   string
		wantCount   int
	}{
		{"json array", "[\n  {\"a\":1}\n]\n", `{"a":1}`, 1},
		{"yaml documents", "---\na: 1\n---\na: 2\n", "---\na: 1", 2},
		{"one json object per line", "{\"a\":1}\n{\"a\":2}\n", `{"a":1}`, 2},
		{"plain text", "hello\nworld\n", "hello", 2},
		// A first line longer than the peek can't be a framing marker.
		{"very long first line", strings.Repeat("Z", 1000) + "\nx\n", strings.Repeat("Z", 1000), 2},
		// A stream that is nothing but "---", with no newline at all.
		{"bare yaml separator", "---", "---", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := collectRecords(t, tc.input)
			if len(got) != tc.wantCount {
				t.Fatalf("got %d records, want %d:\n%q", len(got), tc.wantCount, got)
			}
			if got[0] != tc.wantFirst {
				t.Errorf("first record:\ngot:  %q\nwant: %q", got[0], tc.wantFirst)
			}
		})
	}
}

// An explicit format skips detection, which is what a tail attached mid-stream
// needs: it missed the opening "[".
func TestExplicitFormatSkipsDetection(t *testing.T) {
	midStream := "  {\n    \"insert_id\": \"a1\"\n  },\n"
	got := collectAs(t, config.FormatJSON, midStream)
	if len(got) != 1 || got[0] != "{\n    \"insert_id\": \"a1\"\n  }" {
		t.Fatalf("forced json framing should group the object: %q", got)
	}

	// Forced text framing disables all grouping, even for a "[" stream.
	got = collectAs(t, config.FormatText, tailStream)
	if len(got) != 10 {
		t.Fatalf("forced text framing should emit every line: got %d records\n%q", len(got), got)
	}

	// Forced yaml framing groups on "---" even without a leading separator.
	got = collectAs(t, config.FormatYAML, "a: 1\n---\na: 2\n")
	if len(got) != 2 {
		t.Fatalf("forced yaml framing should split on ---: %q", got)
	}
}

// A brace inside a string must not move the depth, or a user_agent or a SQL
// statement would swallow the rest of the stream.
func TestBracesInsideStringsDoNotAffectDepth(t *testing.T) {
	input := `[
  {
    "user_agent": "Mozilla/5.0 {not-a-brace} [nor-this]",
    "quoted": "he said \"hi {x}\" and left",
    "backslash": "ends with a backslash \\",
    "severity": 200
  }
]
`
	got := collectRecords(t, input)
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1:\n%q", len(got), got)
	}
	for _, want := range []string{"not-a-brace", `\"hi {x}\"`, "severity"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("record lost %q:\n%s", want, got[0])
		}
	}
}

// Nested objects and arrays close in the right order.
func TestNestedObjectsGroupAsOneRecord(t *testing.T) {
	input := `[
  {
    "resource": {
      "labels": {
        "service_name": "taa-backend"
      },
      "type": "cloud_run_revision"
    },
    "error_groups": [],
    "severity": 200
  }
]
`
	got := collectRecords(t, input)
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1:\n%q", len(got), got)
	}
	if !strings.Contains(got[0], "cloud_run_revision") || !strings.Contains(got[0], `"severity": 200`) {
		t.Errorf("record truncated at a nested close:\n%s", got[0])
	}
}

// A line that isn't JSON stays a plain-text log line, even inside a JSON
// array stream — gcloud's own warnings arrive that way when stderr is merged.
func TestPlainTextLinesPassThrough(t *testing.T) {
	input := `[
  {
    "insert_id": "a1"
  },
WARNING: some gcloud notice
  {
    "insert_id": "a2"
  }
`
	got := collectRecords(t, input)
	if len(got) != 3 {
		t.Fatalf("got %d records, want 3:\n%q", len(got), got)
	}
	if got[1] != "WARNING: some gcloud notice" {
		t.Errorf("plain-text record = %q, want it passed through unchanged", got[1])
	}
}

// A tail stopped with Ctrl-C leaves the array unclosed and the last object cut
// off mid-key. Emit it rather than drop it; the adapter flags it Malformed.
func TestTruncatedFinalObjectIsEmitted(t *testing.T) {
	input := `[
  {
    "insert_id": "a1"
  },
  {
    "insert_id": "a2",
    "http_request": {
      "referer": "`
	got := collectRecords(t, input)
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2:\n%q", len(got), got)
	}
	if !strings.Contains(got[1], "a2") || strings.HasSuffix(got[1], "\n") {
		t.Errorf("truncated record = %q, want the partial object with no trailing newline", got[1])
	}
}

// An object sharing its line with array punctuation still becomes its own
// record, and a second object on one line is not lost.
func TestObjectsSharingALine(t *testing.T) {
	got := collectRecords(t, "[\n  {\"a\":1}, {\"a\":2}\n]\n")
	want := []string{`{"a":1}`, `{"a":2}`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBlankAndPunctuationLinesAreDropped(t *testing.T) {
	got := collectRecords(t, "[\n\n  {\"a\":1}\n  ,\n\n]\n")
	if len(got) != 1 || got[0] != `{"a":1}` {
		t.Fatalf("got %q, want one record", got)
	}
}

// A live tail must not buffer without limit because one object never closed.
func TestOversizeRecordIsEmittedAndScannerResets(t *testing.T) {
	old := maxRecord
	maxRecord = 64
	t.Cleanup(func() { maxRecord = old })

	input := "[\n  {\n" + strings.Repeat(`    "k": "vvvvvvvvvv",`+"\n", 20) + "  {\"a\":1}\n"
	got := collectRecords(t, input)
	if len(got) < 2 {
		t.Fatalf("got %d records, want the oversize buffer plus a later one:\n%q", len(got), got)
	}
	if got[len(got)-1] != `{"a":1}` {
		t.Errorf("last record = %q, want the scanner to have reset and found the next object", got[len(got)-1])
	}
}

// A zero-value Format must behave as auto, since Task 5 wires a struct field
// straight into this parameter and the plan requires that tolerance.
func TestZeroValueFormatDetects(t *testing.T) {
	got := collectAs(t, config.Format(""), tailStream)
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2: a zero-value Format must auto-detect, not line-split:\n%q", len(got), got)
	}
}

// Detection must not wait for detectPeek bytes. A reader that yields a short
// first line and then blocks forever would hang the old Peek(512).
func TestDetectionDoesNotWaitForAFullPeek(t *testing.T) {
	done := make(chan []string, 1)
	go func() {
		var got []string
		// blockAfter yields "[\n" then blocks, so detection must decide from
		// the first newline alone.
		_ = Records(blockAfter("[\n  {\"a\":1}\n"), config.FormatAuto, func(rec []byte) {
			got = append(got, string(rec))
			if len(got) == 1 {
				done <- got
			}
		})
	}()
	select {
	case got := <-done:
		if got[0] != `{"a":1}` {
			t.Fatalf("first record = %q, want the object", got[0])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no record emitted: detection blocked waiting for more input than the first line")
	}
}

// blockAfter returns a reader that serves s and then blocks forever, standing
// in for a live producer that has gone quiet.
func blockAfter(s string) io.Reader {
	return io.MultiReader(strings.NewReader(s), blockingReader{})
}

type blockingReader struct{}

func (blockingReader) Read([]byte) (int, error) {
	select {} // a live source with nothing to say yet
}
