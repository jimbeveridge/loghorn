// Package adapter turns raw log lines into normalized entry.Entry values.
package adapter

import (
	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/entry"
)

// Adapter detects and parses one log format. Detect must be cheap.
type Adapter interface {
	Detect(line []byte) bool
	Parse(line []byte) (entry.Entry, error)
}

// Parser normalizes records under one input configuration — which spelling of
// a LogEntry field name it accepts, and which encoding of a severity.
type Parser struct {
	logEntry  LogEntryAdapter
	yamlEntry YAMLAdapter
	rawText   RawTextAdapter
}

// New returns a Parser for in. A zero-value in behaves as if every setting
// were "auto".
func New(in config.Input) *Parser {
	return &Parser{
		logEntry:  LogEntryAdapter{In: in},
		yamlEntry: YAMLAdapter{In: in},
	}
}

// defaultParser serves ParseLine, for the callers and tests that configure
// nothing.
var defaultParser = New(config.Default().Input)

// ParseLine normalizes a single record with the default configuration.
func ParseLine(line []byte) entry.Entry { return defaultParser.ParseLine(line) }

// ParseLine normalizes a single record — usually one line, but one whole
// document when ingest.Records has split a gcloud `--format=yaml` stream on
// "---", or one whole object from a `--json` array. It never returns an error:
// a record that looks like JSON or YAML but fails to parse falls back to raw
// text, flagged Malformed.
func (p *Parser) ParseLine(line []byte) entry.Entry {
	switch {
	case p.logEntry.Detect(line):
		if e, err := p.logEntry.Parse(line); err == nil {
			return e
		}
	case p.yamlEntry.Detect(line):
		if e, err := p.yamlEntry.Parse(line); err == nil {
			return e
		}
	default:
		e, _ := p.rawText.Parse(line)
		return e
	}
	e, _ := p.rawText.Parse(line)
	e.Malformed = true
	return e
}
