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
// rec comes back unchanged, aliasing the input, when it holds no newline;
// that path inherits Records' "valid only during the call" contract, so a
// caller that retains the result must copy it. A multi-line record that
// parses as JSON is compacted onto one line. Otherwise its newlines survive
// only if its first line begins "--" — precisely the condition ingest's own
// replay detection (framing, in reader.go) uses to re-group a YAML document
// on the way back in — and are collapsed to spaces everywhere else. That
// collapse is what a Ctrl-C'd tail's truncated final object needs: it is
// multi-line and invalid JSON, so without it the fragment would land in the
// log file spanning several lines and come back from -historical as several
// junk entries. Only the on-disk copy is affected either way: Entry.Raw keeps
// the original bytes, so the detail pane, find and yank still show what
// arrived on the wire.
func CompactRecord(rec []byte) []byte {
	if bytes.IndexByte(rec, '\n') < 0 {
		return rec
	}
	var out bytes.Buffer
	if err := json.Compact(&out, rec); err == nil {
		return out.Bytes()
	}
	// Still multi-line and not JSON. Only a document whose first line begins
	// "--" may keep its newlines, because that is exactly what ingest's replay
	// detection looks for: such a record is re-grouped on the way back in, and
	// nothing else can be. The fragment a Ctrl-C'd tail leaves behind is the
	// case that matters — written raw it would break the file's
	// one-record-per-line invariant and come back as several junk entries.
	first := rec
	if i := bytes.IndexByte(rec, '\n'); i >= 0 {
		first = rec[:i]
	}
	if bytes.HasPrefix(first, []byte("--")) {
		return rec
	}
	return collapseNewlines(rec)
}

// collapseNewlines puts a record on one line, replacing each line break with a
// space and keeping every other byte. A CR is dropped rather than turned into a
// second space, so a CRLF stream doesn't leave stray carriage returns mid-line.
func collapseNewlines(rec []byte) []byte {
	out := make([]byte, 0, len(rec))
	for _, c := range rec {
		switch c {
		case '\n':
			out = append(out, ' ')
		case '\r':
		default:
			out = append(out, c)
		}
	}
	return out
}
