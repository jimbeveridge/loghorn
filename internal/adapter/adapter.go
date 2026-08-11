// Package adapter turns raw log lines into normalized entry.Entry values.
package adapter

import "github.com/jimbeveridge/loghorn/internal/entry"

// Adapter detects and parses one log format. Detect must be cheap.
type Adapter interface {
	Detect(line []byte) bool
	Parse(line []byte) (entry.Entry, error)
}

var (
	logEntry LogEntryAdapter
	rawText  RawTextAdapter
)

// ParseLine normalizes a single line. It never returns an error: a line that
// looks like JSON but fails to parse falls back to raw text, flagged Malformed.
func ParseLine(line []byte) entry.Entry {
	if logEntry.Detect(line) {
		if e, err := logEntry.Parse(line); err == nil {
			return e
		}
		e, _ := rawText.Parse(line)
		e.Malformed = true
		return e
	}
	e, _ := rawText.Parse(line)
	return e
}
