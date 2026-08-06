// Package adapter turns raw log lines into normalized entry.Entry values.
package adapter

import "clog/internal/entry"

// Adapter detects and parses one log format. Detect must be cheap.
type Adapter interface {
	Detect(line []byte) bool
	Parse(line []byte) (entry.Entry, error)
}
