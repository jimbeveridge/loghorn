// Package headless runs the loghorn engine as a stdin->stdout filter (no UI).
package headless

import (
	"io"

	"github.com/jimbeveridge/loghorn/internal/adapter"
	"github.com/jimbeveridge/loghorn/internal/engine"
	"github.com/jimbeveridge/loghorn/internal/ingest"
)

// Run reads records from r and writes the original bytes of each important one
// (plus a newline) to w. Output is byte-for-byte faithful to the input. If sink
// is non-nil it receives every record first, important or not — the log file
// keeps the whole stream. The slice passed to sink is only valid during the call.
func Run(r io.Reader, w io.Writer, sink func(rec []byte)) error {
	var writeErr error
	err := ingest.Records(r, func(line []byte) {
		if sink != nil {
			sink(line)
		}
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
