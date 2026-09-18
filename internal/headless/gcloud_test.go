package headless

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimbeveridge/loghorn/internal/config"
)

// runFixture pipes a committed gcloud sample through the whole pipeline —
// framing, parsing, importance — and returns what the filter printed and every
// record the log-file sink saw.
func runFixture(t *testing.T, name string) (out string, records []string) {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	var buf strings.Builder
	err = Run(f, &buf, config.Default().Input, func(rec []byte) {
		records = append(records, string(rec))
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return buf.String(), records
}

// The JSON array fixture: four objects (the last truncated) plus one
// interleaved plain-text line.
func TestGcloudTailJSONFixture(t *testing.T) {
	out, records := runFixture(t, "gcloud-tail.json")

	if len(records) != 5 {
		t.Fatalf("got %d records, want 5 (4 objects + 1 plain-text line):\n%q", len(records), records)
	}
	if records[1] != "WARNING: gcloud notice interleaved on the same stream" {
		t.Errorf("record 1 = %q, want the plain-text line passed through", records[1])
	}
	if !strings.Contains(records[0], "not-a-brace") {
		t.Errorf("record 0 lost the braced string:\n%s", records[0])
	}

	// A numeric severity of 500 is an error, so those two print; the 200 does
	// not. This is the whole point of the change: importance works on gcloud
	// output.
	if !strings.Contains(out, "Database: QUERY failed") {
		t.Errorf("severity 500 record should be important:\n%s", out)
	}
	if strings.Contains(out, "run.googleapis.com%2Frequests") {
		t.Errorf("severity 200 record should not be important:\n%s", out)
	}
	// http_request.status 503 must count even with no message.
	if !strings.Contains(out, `"status": 503`) {
		t.Errorf("http_request.status 503 should be important:\n%s", out)
	}
}

func TestGcloudReadYAMLFixture(t *testing.T) {
	out, records := runFixture(t, "gcloud-read.yaml")

	if len(records) != 2 {
		t.Fatalf("got %d records, want 2 documents:\n%q", len(records), records)
	}
	for i, r := range records {
		if !strings.HasPrefix(r, "---") {
			t.Errorf("record %d should keep its leading separator: %q", i, r)
		}
	}
	if !strings.Contains(out, "upstream unavailable") {
		t.Errorf("severity 500 document should be important:\n%s", out)
	}
	if strings.Contains(out, "returned 1 row") {
		t.Errorf("severity 200 document should not be important:\n%s", out)
	}
}

// Forcing text framing turns grouping off, which is the escape hatch for a
// producer whose output only looks like one of the framed shapes.
func TestForcedTextFramingDisablesGrouping(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "gcloud-tail.json"))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	var n int
	in := config.Default().Input
	in.Format = config.FormatText
	if err := Run(f, io.Discard, in, func([]byte) { n++ }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n < 30 {
		t.Errorf("forced text framing emitted %d records, want one per line", n)
	}
}
