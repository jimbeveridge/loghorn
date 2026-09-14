// Package adapter turns raw log lines into normalized entry.Entry values.
package adapter

import "github.com/jimbeveridge/loghorn/internal/entry"

// Adapter detects and parses one log format. Detect must be cheap.
type Adapter interface {
	Detect(line []byte) bool
	Parse(line []byte) (entry.Entry, error)
}

var (
	logEntry  LogEntryAdapter
	yamlEntry YAMLAdapter
	rawText   RawTextAdapter
)

// ParseLine normalizes a single record — usually one line, but one whole
// document when ingest.Records has split a gcloud `--format=yaml` stream on
// "---". It never returns an error: a record that looks like JSON or YAML but
// fails to parse falls back to raw text, flagged Malformed.
func ParseLine(line []byte) entry.Entry {
	switch {
	case logEntry.Detect(line):
		if e, err := logEntry.Parse(line); err == nil {
			return e
		}
	case yamlEntry.Detect(line):
		if e, err := yamlEntry.Parse(line); err == nil {
			return e
		}
	default:
		e, _ := rawText.Parse(line)
		return e
	}
	e, _ := rawText.Parse(line)
	e.Malformed = true
	return e
}
