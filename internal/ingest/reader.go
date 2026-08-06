// Package ingest reads a byte stream into individual log lines.
package ingest

import (
	"bufio"
	"errors"
	"io"
)

// Lines reads r to EOF, invoking emit once per line without the trailing '\n'.
// It is safe for arbitrarily long lines. The slice passed to emit is only valid
// during the call; emit must copy it to retain it.
func Lines(r io.Reader, emit func(line []byte)) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			// Trim a single trailing \n and optional \r.
			end := len(line)
			if end > 0 && line[end-1] == '\n' {
				end--
				if end > 0 && line[end-1] == '\r' {
					end--
				}
			}
			emit(line[:end])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
