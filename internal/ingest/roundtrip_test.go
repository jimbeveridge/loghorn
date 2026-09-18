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
			"multi-line non-json untouched",
			"Traceback:\n  at handler",
			"Traceback:\n  at handler",
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
