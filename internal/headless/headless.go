// Package headless runs the clog engine as a stdin->stdout filter (no UI).
package headless

import (
	"io"

	"clog/internal/adapter"
	"clog/internal/engine"
	"clog/internal/ingest"
)

// Run reads lines from r and writes the original bytes of each important line
// (plus a newline) to w. Output is byte-for-byte faithful to the input line.
func Run(r io.Reader, w io.Writer) error {
	var writeErr error
	err := ingest.Lines(r, func(line []byte) {
		if writeErr != nil {
			return
		}
		e := adapter.ParseLine(line)
		if !engine.IsImportant(e) {
			return
		}
		if _, err := w.Write(e.Raw); err != nil {
			writeErr = err
			return
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			writeErr = err
		}
	})
	if err != nil {
		return err
	}
	return writeErr
}
