package ingest

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// maxRecord bounds one in-progress JSON record. A live tail must not be able
// to buffer without limit because a single object never closed, so at this
// size the buffer is emitted as-is — parsing will flag it Malformed — and the
// scanner resets to look for the next object. A var, not a const, so a test
// can provoke the limit without building a 16MB stream.
var maxRecord = 16 << 20

// jsonArray frames a JSON array stream: array punctuation between objects is
// dropped, each object becomes one record however many lines it spans, and any
// other line is emitted as its own record so a plain-text line interleaved
// into the stream stays a plain-text log line.
func jsonArray(br *bufio.Reader, emit func(rec []byte)) error {
	var s objScanner
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			s.feed(trimEOL(line), emit)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				// An object still open at EOF is the normal ending for a tail
				// stopped with Ctrl-C, not an edge case: emit the partial
				// record rather than silently dropping data.
				s.reset(emit)
				return nil
			}
			return err
		}
	}
}

// objScanner accumulates one JSON object at a time. Depth, string and escape
// state persist across lines, since a record spans many of them.
type objScanner struct {
	buf     []byte
	depth   int
	inStr   bool
	escaped bool
}

// feed processes one input line, which may finish the open object, start a new
// one, or both — so whatever follows a closing brace is fed back through the
// same states rather than discarded.
func (s *objScanner) feed(line []byte, emit func(rec []byte)) {
	for {
		if s.depth == 0 {
			rest, ok := s.start(line, emit)
			if !ok {
				return
			}
			line = rest
		}
		n := s.scan(line)
		s.buf = append(s.buf, line[:n]...)
		line = line[n:]
		switch {
		case s.depth == 0:
			emit(s.buf)
			s.buf = s.buf[:0]
		case len(s.buf) >= maxRecord:
			s.reset(emit)
			return
		default:
			// The object continues on the next line, and its newline is part
			// of the record.
			s.buf = append(s.buf, '\n')
			return
		}
	}
}

// start positions line at the next object's opening brace. When the line holds
// no object it handles the line itself — dropping array punctuation, emitting
// anything else as a plain-text record — and reports that no object began.
func (s *objScanner) start(line []byte, emit func(rec []byte)) ([]byte, bool) {
	i := bytes.IndexByte(line, '{')
	// Everything before the brace must be punctuation too, or this is a text
	// line that merely happens to contain one.
	if i < 0 || !isArrayPunct(line[:i]) {
		if t := bytes.TrimSpace(line); len(t) > 0 && !isArrayPunct(t) {
			emit(line)
		}
		return nil, false
	}
	return line[i:], true
}

// isArrayPunct reports whether b holds only the brackets, commas and space
// that frame an array — the bytes between records, which are not records.
func isArrayPunct(b []byte) bool {
	for _, c := range b {
		switch c {
		case '[', ']', ',', ' ', '\t', '\r':
		default:
			return false
		}
	}
	return true
}

// scan advances the object state across line and returns how many bytes belong
// to the current object: all of line, or up to and including the brace that
// closes it. Quotes and backslash escapes are honoured, so a brace inside a
// user_agent or a SQL statement cannot move the depth.
func (s *objScanner) scan(line []byte) int {
	for i, c := range line {
		switch {
		case s.escaped:
			s.escaped = false
		case s.inStr:
			switch c {
			case '\\':
				s.escaped = true
			case '"':
				s.inStr = false
			}
		default:
			switch c {
			case '"':
				s.inStr = true
			case '{':
				s.depth++
			case '}':
				if s.depth--; s.depth == 0 {
					return i + 1
				}
			}
		}
	}
	return len(line)
}

// reset emits whatever has accumulated and clears the scanner, for EOF with an
// object still open and for one that outgrew maxRecord. The trailing newline
// goes: a partial object's last line break carries no meaning.
func (s *objScanner) reset(emit func(rec []byte)) {
	if buf := bytes.TrimRight(s.buf, "\n"); len(buf) > 0 {
		emit(buf)
	}
	s.buf, s.depth, s.inStr, s.escaped = s.buf[:0], 0, false, false
}
