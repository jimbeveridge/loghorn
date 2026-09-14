// Package ingest reads a byte stream into individual log lines or records.
package ingest

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// Lines reads r to EOF, invoking emit once per line without the trailing '\n'.
// It is safe for arbitrarily long lines. The slice passed to emit is only valid
// during the call; emit must copy it to retain it.
func Lines(r io.Reader, emit func(line []byte)) error {
	return lines(bufio.NewReader(r), emit)
}

func lines(br *bufio.Reader, emit func(line []byte)) error {
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			emit(trimEOL(line))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// Records reads r to EOF, invoking emit once per log record. Most producers
// emit one record per line — JSON objects, plain text — and Records is
// equivalent to Lines. gcloud's `--format=yaml` output is different: each
// record is a multi-line document, with a line of "---" marking where one
// ends and the next begins. Records detects that shape by peeking whether
// the stream's first line starts with "--" and, if so, buffers each document
// whole (keeping its leading "---", so a record adapter's own format check
// stays a simple prefix test) instead of splitting on '\n'.
func Records(r io.Reader, emit func(rec []byte)) error {
	br := bufio.NewReader(r)
	first, _ := br.Peek(2)
	if len(first) == 2 && first[0] == '-' && first[1] == '-' {
		return yamlDocuments(br, emit)
	}
	return lines(br, emit)
}

func yamlDocuments(br *bufio.Reader, emit func(rec []byte)) error {
	var doc [][]byte
	flush := func() {
		if len(doc) > 0 {
			emit(bytes.Join(doc, []byte("\n")))
			doc = nil
		}
	}
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := trimEOL(line)
			if isDocSeparator(trimmed) {
				flush()
			}
			doc = append(doc, trimmed)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				flush()
				return nil
			}
			return err
		}
	}
}

func isDocSeparator(line []byte) bool {
	t := bytes.TrimSpace(line)
	return len(t) >= 2 && t[0] == '-' && t[1] == '-'
}

// trimEOL trims a single trailing '\n' and optional '\r' from a line read by
// bufio.Reader.ReadBytes('\n').
func trimEOL(line []byte) []byte {
	end := len(line)
	if end > 0 && line[end-1] == '\n' {
		end--
		if end > 0 && line[end-1] == '\r' {
			end--
		}
	}
	return line[:end]
}
