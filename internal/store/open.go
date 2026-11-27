package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jimbeveridge/loghorn/internal/adapter"
	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/duckstore"
	"github.com/jimbeveridge/loghorn/internal/ingest"
	"github.com/jimbeveridge/loghorn/internal/logfile"
	"github.com/jimbeveridge/loghorn/internal/record"
)

// KeepDays is how many days before today are kept, as for the text files.
const KeepDays = 3

// Options opens the store over a data folder.
type Options struct {
	Mode     Mode
	RunStart time.Time
	Input    config.Input
	// Dir is the data folder: the text logs, loghorn.duckdb and its
	// Parquet archive. The store owns the database file: it is opened by
	// the one process holding the folder's lock (loghorn serve).
	Dir  string
	Now  func() time.Time // nil: time.Now
	Warn func(string)     // nil: print to stderr
}

// Open opens the database file in o.Dir for writing, creating it if
// missing, over the retained days (today and KeepDays before). Files not
// yet filled from text (new or cut short) are started again from the
// retained days' text, once; after that the files hold the records.
// Closed days then move to Parquet archives, and expired ones go.
func Open(o Options) (*Store, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Warn == nil {
		o.Warn = func(m string) { fmt.Fprintln(os.Stderr, m) }
	}
	return openWriter(o, DayNumber(o.Now()))
}

func fileOptions(o Options) duckstore.FileOptions {
	return duckstore.FileOptions{Dir: o.Dir, KeepDays: KeepDays}
}

func newStore(e *duckstore.Engine, o Options) *Store {
	s := wrap(e, o.Mode, o.RunStart)
	s.now = o.Now
	return s
}

// openWriter opens the files for writing, importing the text first if they
// aren't marked as holding it (see duckstore.TakeImported). The mark is
// cleared before the files are opened, so an open that fails leaves it
// cleared too.
func openWriter(o Options, today int) (*Store, error) {
	imported := duckstore.TakeImported(o.Dir)
	e, err := duckstore.OpenFiles(o.Input, o.Warn, fileOptions(o))
	if err != nil {
		return nil, err
	}
	if !imported {
		n, err := e.Count()
		if err != nil {
			// Never import on top of records it couldn't count.
			e.Close()
			return nil, fmt.Errorf("counting the stored records: %w", err)
		}
		if n > 0 || archived(o.Dir) {
			// Whatever is there came from text, which still has it.
			e.Close()
			clearFiles(o.Dir)
			if e, err = duckstore.OpenFiles(o.Input, o.Warn, fileOptions(o)); err != nil {
				return nil, err
			}
		}
		s := newStore(e, o)
		n, complete := s.importText(o, today)
		if complete && !e.Failed() {
			e.MarkComplete()
		}
		if n > 0 {
			o.Warn(fmt.Sprintf("loghorn: imported %d records from the text logs into DuckDB", n))
		}
		e.Archive(today)
		return s.ready(today)
	}
	e.MarkComplete()
	e.Archive(today) // failures are warned about and retried next time
	return newStore(e, o).ready(today)
}

func archived(dir string) bool {
	entries, _ := os.ReadDir(filepath.Join(dir, duckstore.ArchiveDir))
	return len(entries) > 0
}

// clearFiles removes the database file and the archive.
func clearFiles(dir string) {
	for _, sfx := range []string{"", ".wal"} {
		os.Remove(filepath.Join(dir, duckstore.FileName+sfx))
	}
	os.RemoveAll(filepath.Join(dir, duckstore.ArchiveDir))
}

// ready continues today's ids after the stored ones. A failed read fails
// the open (closing the store): ids are never given out on top of a day it
// couldn't read.
func (s *Store) ready(today int) (*Store, error) {
	m, err := s.e.MaxSeq(today)
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("reading the stored record ids: %w", err)
	}
	s.mu.Lock()
	s.seq[today] = max(s.seq[today], m)
	s.seqRead[today] = true
	s.mu.Unlock()
	return s, nil
}

// importText adds the retained days' text, each day with its own ids. It
// returns how many records it added and whether every file was read; an
// incomplete import leaves the files unmarked, to be done again.
func (s *Store) importText(o Options, today int) (n int, complete bool) {
	before, errBefore := s.e.Count()
	complete = true
	for d := today - KeepDays; d <= today; d++ {
		if files := dayText(o.Dir, d); textBytes(files) > 0 {
			if err := s.load(files, d, o.Input); err != nil {
				o.Warn("loghorn: reading the text logs: " + firstLine(err) + "; the next start imports them again")
				complete = false
			}
		}
	}
	after, err := s.e.Count()
	if err = errors.Join(errBefore, err); err != nil {
		// Without both counts it can't tell what the files hold.
		o.Warn("loghorn: counting the stored records: " + firstLine(err) + "; the next start imports the text logs again")
		return 0, false
	}
	return after - before, complete
}

// load reads text files of day into s, ids carrying that day. A day is
// read on its own, so its untimed records take times from that day only:
// leading ones the day's first time, and with none they stay untimed.
func (s *Store) load(paths []string, day int, in config.Input) error {
	s.mu.Lock()
	s.last, s.pending = time.Time{}, nil
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.last, s.pending = time.Time{}, nil
		s.mu.Unlock()
	}()
	p := adapter.New(in)
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		err = ingest.Stored(f, func(rec []byte) {
			l := bytes.Clone(rec)
			if r, ok := record.NormalizeSpan(l); ok {
				s.add(r, time.Time{}, day, true)
			} else if r, ok := record.Normalize(p.ParseLine(l), in); ok {
				s.add(r, time.Time{}, day, true)
			}
		})
		f.Close()
		if err != nil {
			return err
		}
	}
	s.Flush()
	return nil
}

// dayText lists a day's text files: logs, then spans; each the archive
// loghorn-YYYY-MM-DD<suffix>, then the current file when it was last
// written that day.
func dayText(dir string, day int) []string {
	var out []string
	name := dayName(day)
	for _, f := range []struct{ base, suffix string }{{logfile.LogBase, logfile.LogSuffix}, {logfile.SpansBase, logfile.SpansSuffix}} {
		arch := filepath.Join(dir, f.base+"-"+name+f.suffix)
		if fi, err := os.Stat(arch); err == nil && fi.Mode().IsRegular() {
			out = append(out, arch)
		}
		cur := filepath.Join(dir, f.base+f.suffix)
		if fi, err := os.Stat(cur); err == nil && fi.Mode().IsRegular() && DayNumber(fi.ModTime()) == day {
			out = append(out, cur)
		}
	}
	return out
}

func textBytes(paths []string) int64 {
	var n int64
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			n += fi.Size()
		}
	}
	return n
}

func firstLine(err error) string {
	m := err.Error()
	if i := strings.IndexByte(m, '\n'); i >= 0 {
		m = m[:i]
	}
	return m
}

// dayName is a day number's date, YYYY-MM-DD, as the text archives name it.
func dayName(day int) string { return time.Unix(int64(day)*86400, 0).UTC().Format("2006-01-02") }
