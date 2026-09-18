package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/logfile"
)

func TestCompactRecord(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"single line untouched", `{"a":1}`, `{"a":1}`},
		{"plain text untouched", "just a log line", "just a log line"},
		{
			"multi-line json compacted",
			"{\n  \"a\": 1,\n  \"b\": {\n    \"c\": 2\n  }\n}",
			`{"a":1,"b":{"c":2}}`,
		},
		{
			// A YAML document needs no help: it keeps its leading "---", so a
			// replay re-detects YAML framing and re-groups it.
			"yaml untouched",
			"---\nseverity: 500\nmessage: boom",
			"---\nseverity: 500\nmessage: boom",
		},
		{
			// Superseded by the "--" rule below: without that lead, replay can't
			// re-group this, so it must collapse rather than stay multi-line.
			// See TestCompactRecordCollapsesUnregroupableNewlines for the case
			// this is standing in for.
			"multi-line non-json without a --- lead is collapsed",
			"Traceback:\n  at handler",
			"Traceback:   at handler",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(CompactRecord([]byte(tc.in))); got != tc.want {
				t.Errorf("CompactRecord:\ngot:  %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// The log file holds one record per line, so a multi-line JSON record must be
// compacted before it is written. Without that, -historical replay reads the
// stored file — which starts with '{', not '[' — as line-framed and shreds
// each record back into fragments.
func TestMultiLineRecordSurvivesTheLogFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	w, err := logfile.Open(dir, time.Now, time.Local)
	if err != nil {
		t.Fatalf("logfile.Open: %v", err)
	}
	// Close is documented safe to call twice, so this still guards the flock
	// if a Fatalf inside the emit callback below Goexits through Records
	// before the success-path Close runs.
	defer w.Close()

	stream := "[\n  {\n    \"insert_id\": \"a1\",\n    \"severity\": 500\n  },\n" +
		"  {\n    \"insert_id\": \"a2\",\n    \"severity\": 200\n  }\n]\n"

	var ingested []string
	err = Records(strings.NewReader(stream), config.FormatAuto, func(rec []byte) {
		ingested = append(ingested, string(rec))
		if err := w.Write(CompactRecord(rec)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(ingested) != 2 {
		t.Fatalf("ingested %d records, want 2: %q", len(ingested), ingested)
	}
	if !strings.Contains(ingested[0], "\n") {
		t.Fatalf("premise failed: the ingested record should be multi-line, got %q", ingested[0])
	}

	f, err := os.Open(filepath.Join(dir, "loghorn.log"))
	if err != nil {
		t.Fatalf("open stored log: %v", err)
	}
	defer f.Close()

	var replayed []string
	if err := Records(f, config.FormatAuto, func(rec []byte) {
		replayed = append(replayed, string(rec))
	}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	want := []string{`{"insert_id":"a1","severity":500}`, `{"insert_id":"a2","severity":200}`}
	if strings.Join(replayed, "|") != strings.Join(want, "|") {
		t.Fatalf("replay:\ngot:  %q\nwant: %q", replayed, want)
	}
}

// A record that cannot be re-grouped on replay must not reach the file
// multi-line. The fragment a Ctrl-C'd tail leaves behind is invalid JSON, so
// compaction can't help it and only newline collapsing keeps the file's
// one-record-per-line invariant.
func TestCompactRecordCollapsesUnregroupableNewlines(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{
			"truncated json fragment",
			"{\n  \"insert_id\": \"a2\",\n  \"http_request\": {\n    \"referer\": \"",
			`{   "insert_id": "a2",   "http_request": {     "referer": "`,
		},
		{
			// A YAML document is the one thing that may keep its newlines.
			"yaml document keeps its newlines",
			"---\nseverity: 500\nmessage: boom",
			"---\nseverity: 500\nmessage: boom",
		},
		{
			"multi-line non-json without a --- lead is collapsed",
			"Traceback:\n  at handler",
			"Traceback:   at handler",
		},
		{
			"crlf leaves no stray carriage return",
			"{\r\n  \"a\": \"",
			`{   "a": "`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(CompactRecord([]byte(tc.in))); got != tc.want {
				t.Errorf("CompactRecord:\ngot:  %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// End to end: a tail cut off mid-object must leave the stored file replayable,
// one record per line, with the fragment as a single entry rather than several.
func TestTruncatedTailSurvivesTheLogFileAsOneRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	w, err := logfile.Open(dir, time.Now, time.Local)
	if err != nil {
		t.Fatalf("logfile.Open: %v", err)
	}
	defer w.Close()

	// The shape a Ctrl-C'd `gcloud beta logging tail --json` leaves: no
	// closing bracket, last object cut off mid-key.
	stream := "[\n  {\n    \"insert_id\": \"a1\",\n    \"severity\": 500\n  },\n" +
		"  {\n    \"insert_id\": \"a2\",\n    \"http_request\": {\n      \"referer\": \""

	if err := Records(strings.NewReader(stream), config.FormatAuto, func(rec []byte) {
		if err := w.Write(CompactRecord(rec)); err != nil {
			t.Errorf("Write: %v", err)
		}
	}); err != nil {
		t.Fatalf("Records: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.Open(filepath.Join(dir, "loghorn.log"))
	if err != nil {
		t.Fatalf("open stored log: %v", err)
	}
	defer f.Close()

	var replayed []string
	if err := Records(f, config.FormatAuto, func(rec []byte) {
		replayed = append(replayed, string(rec))
	}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(replayed) != 2 {
		t.Fatalf("replayed %d records, want 2 — the truncated fragment must stay one record:\n%q", len(replayed), replayed)
	}
	if replayed[0] != `{"insert_id":"a1","severity":500}` {
		t.Errorf("record 0 = %q", replayed[0])
	}
	if !strings.Contains(replayed[1], "a2") || strings.Contains(replayed[1], "\n") {
		t.Errorf("record 1 = %q, want the fragment on one line", replayed[1])
	}
}
