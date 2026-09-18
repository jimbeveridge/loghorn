// Package ingest reads a byte stream into individual log lines or records.
package ingest

import (
	"bufio"
	"bytes"
	"errors"
	"io"

	"github.com/jimbeveridge/loghorn/internal/config"
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

// Records reads r to EOF, invoking emit once per log record.
//
// Producers frame records three ways, and format decides which applies.
// config.FormatAuto detects it from the stream's first line:
//
//	"["  a JSON array — gcloud beta logging tail --json, where each record is
//	     a pretty-printed object spanning many lines
//	"--" YAML documents — gcloud logging read --format=yaml, one record per
//	     document with "---" between them
//	else one record per line: plain text, or one JSON object per line
//
// An explicit format skips detection, which is what a tail attached
// mid-stream needs: it missed the opening "[".
//
// The slice passed to emit is only valid during the call; emit must copy it
// to retain it.
func Records(r io.Reader, format config.Format, emit func(rec []byte)) error {
	br := bufio.NewReader(r)
	switch format {
	case config.FormatJSON, config.FormatYAML, config.FormatText:
		// An explicit framing, honoured as given.
	default:
		// FormatAuto, and any zero value, which must behave as auto.
		format = detect(br)
	}
	switch format {
	case config.FormatJSON:
		return jsonArray(br, emit)
	case config.FormatYAML:
		return yamlDocuments(br, emit)
	default:
		return lines(br, emit)
	}
}

// detectPeek bounds how much of the stream detection examines. Every framing
// marker is a line of one to three bytes, so a longer first line can't be
// one, and a peek must stay well inside bufio's buffer anyway.
const detectPeek = 512

// detect reports the framing the stream's first line indicates, without
// consuming any of it.
//
// The peek grows to the first newline rather than asking for detectPeek up
// front: bufio.Reader.Peek fills in a loop until it has the bytes asked for,
// so a single Peek(detectPeek) would stall a quiet producer until 512 bytes
// existed and no record would be emitted before then. Waiting for the newline
// instead costs nothing, because whichever framing wins, its next act is to
// read a whole line — which blocks for that same newline. Deciding from a
// partial first line is the alternative and is worse: a wrong guess misframes
// every record in the stream, not just the first.
func detect(br *bufio.Reader) config.Format {
	for n := 1; ; {
		head, err := br.Peek(n)
		if i := bytes.IndexByte(head, '\n'); i >= 0 {
			return framing(head[:i])
		}
		if err != nil {
			// The stream ended, so this prefix is the whole first line.
			return framing(head)
		}
		if n >= detectPeek {
			// A first line this long is no framing marker.
			return config.FormatText
		}
		// Bytes that have already arrived are free; past them, ask for one
		// more, which is the smallest wait that can still make progress.
		if buffered := br.Buffered(); buffered > n {
			n = buffered
		} else {
			n++
		}
		if n > detectPeek {
			n = detectPeek
		}
	}
}

// framing maps a stream's first line to its record framing.
func framing(first []byte) config.Format {
	switch t := bytes.TrimRight(first, " \t\r"); {
	case bytes.Equal(t, []byte("[")):
		return config.FormatJSON
	case bytes.HasPrefix(t, []byte("--")):
		return config.FormatYAML
	}
	return config.FormatText
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
