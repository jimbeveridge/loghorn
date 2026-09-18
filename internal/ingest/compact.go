package ingest

import (
	"bytes"
	"encoding/json"
)

// CompactRecord returns rec as a single line, for the log file.
//
// logfile's Writer appends a record and a newline, so the stored file holds
// one record per line. A multi-line JSON record written verbatim would break
// that: the stored file starts with '{' rather than '[', so -historical reads
// it back as line-framed and shreds every record into fragments. YAML needs no
// such help — its records keep their leading "---", so a replay re-detects
// YAML framing and re-groups them.
//
// rec comes back unchanged when it holds no newline, and when it isn't valid
// JSON, so YAML documents and multi-line plain text pass through untouched.
// Only the on-disk copy is compacted: Entry.Raw keeps the original bytes, so
// the detail pane, find and yank still show what arrived on the wire.
func CompactRecord(rec []byte) []byte {
	if bytes.IndexByte(rec, '\n') < 0 {
		return rec
	}
	var out bytes.Buffer
	if err := json.Compact(&out, rec); err != nil {
		return rec
	}
	return out.Bytes()
}
