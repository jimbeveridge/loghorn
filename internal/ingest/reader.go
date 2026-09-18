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
	if format == config.FormatAuto {
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
func detect(br *bufio.Reader) config.Format {
	head, err := br.Peek(detectPeek)
	if len(head) == 0 {
		return config.FormatText
	}
	end := bytes.IndexByte(head, '\n')
	if end < 0 {
		if !errors.Is(err, io.EOF) {
			// The first line runs past the peek, so it is no marker.
			return config.FormatText
		}
		end = len(head) // the whole stream is one unterminated line
	}
	switch first := bytes.TrimRight(head[:end], " \t\r"); {
	case bytes.Equal(first, []byte("[")):
		return config.FormatJSON
	case bytes.HasPrefix(first, []byte("--")):
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
