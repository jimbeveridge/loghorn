// Package logfile echoes every record loghorn receives to disk. loghorn.log holds
// the current local day, each finished day is archived as loghorn-YYYY-MM-DD.log,
// and archives before the last three days are deleted.
//
// Expiry works on whole files because a log can only be appended to: dropping
// lines from the front would mean rewriting it. A day's file becomes deletable
// once its newest line is 72 hours old, which is the moment the fourth day
// starts — hence today plus three. Calendar dates rather than hour arithmetic
// keep DST days from mattering.
package logfile

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	currentName   = "loghorn.log"
	archivePrefix = "loghorn-"
	archiveSuffix = ".log"
	dateLayout    = "2006-01-02"

	// keepDays is how many finished days are kept before today.
	keepDays = 3
)

// Writer appends records to the log file. It is safe for concurrent use, so
// main can Close it while the ingest goroutine may still be writing.
type Writer struct {
	dir   string
	clock func() time.Time
	loc   *time.Location

	mu   sync.Mutex
	lock *os.File  // holds the flock for the Writer's lifetime
	f    *os.File  // loghorn.log; nil once closed or stopped
	day  time.Time // local midnight starting the day f covers
	next time.Time // the local midnight after day
	buf  []byte    // reused so a record and its newline go out in one write
}

// Open locks dir, archives a loghorn.log left from an earlier day, prunes old
// archives, and opens today's file. Another loghorn holding dir is reported as
// *LockedError. clock and loc decide every date, so tests need not wait for
// midnight.
func Open(dir string, clock func() time.Time, loc *time.Location) (*Writer, error) {
	lock, err := acquire(dir)
	if err != nil {
		return nil, err
	}
	w := &Writer{dir: dir, clock: clock, loc: loc, lock: lock}
	if err := w.start(w.midnight(clock())); err != nil {
		w.Close()
		return nil, err
	}
	return w, nil
}

// start readies today's file. A leftover's day is its mtime: while loghorn runs
// it archives the file itself at midnight, so a file found at startup holds only
// records from the day of its last write. Touching the file by hand is the only
// way to fool this, and the cost is an archive misdated by a day.
func (w *Writer) start(today time.Time) error {
	fi, err := os.Stat(w.path(currentName))
	switch {
	case err == nil && fi.ModTime().Before(today):
		if err := w.archive(w.midnight(fi.ModTime())); err != nil {
			return err
		}
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if err := w.prune(today); err != nil {
		return err
	}
	return w.open(today)
}

// Write appends rec and a newline in a single unbuffered write, so tail -F stays
// current and a crash loses nothing. If a local midnight has passed since the
// file was opened, it is archived first — before the write, so a record is never
// split across days and always lands on the side of midnight it arrived on.
//
// The first failure is returned and stops the Writer: every later Write does
// nothing and returns nil, so the caller reports the problem once. After Close,
// Write also does nothing.
func (w *Writer) Write(rec []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	if err := w.write(rec); err != nil {
		if w.f != nil {
			w.f.Close()
			w.f = nil
		}
		return err
	}
	return nil
}

func (w *Writer) write(rec []byte) error {
	if now := w.clock(); !now.Before(w.next) {
		if err := w.rollover(w.midnight(now)); err != nil {
			return err
		}
	}
	w.buf = append(append(w.buf[:0], rec...), '\n')
	_, err := w.f.Write(w.buf)
	return err
}

// rollover archives the current file under the day it covers, prunes, and opens
// today's. The archive is named by w.day rather than yesterday's date, so a file
// opened on the 14th that sees nothing until the 16th is still the 14th — which
// agrees with its mtime. The check happens only on a record; if nothing arrives
// after midnight, the next startup's mtime rule covers it.
func (w *Writer) rollover(today time.Time) error {
	f := w.f
	w.f = nil
	if err := f.Close(); err != nil {
		return err
	}
	if err := w.archive(w.day); err != nil {
		return err
	}
	if err := w.prune(today); err != nil {
		return err
	}
	return w.open(today)
}

// Close closes the file and releases the lock. It is safe to call twice.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var err error
	if w.f != nil {
		err = w.f.Close()
		w.f = nil
	}
	if w.lock != nil {
		w.lock.Close()
		w.lock = nil
	}
	return err
}

// open opens loghorn.log for appending as the file covering day.
func (w *Writer) open(day time.Time) error {
	f, err := os.OpenFile(w.path(currentName), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	y, m, d := day.Date()
	w.f, w.day, w.next = f, day, time.Date(y, m, d+1, 0, 0, 0, 0, w.loc)
	return nil
}

// archive moves loghorn.log to day's archive, appending to that archive if it
// already exists rather than replacing it.
func (w *Writer) archive(day time.Time) error {
	src, dst := w.path(currentName), w.path(archivePrefix+day.Format(dateLayout)+archiveSuffix)
	if _, err := os.Stat(dst); errors.Is(err, fs.ErrNotExist) {
		return os.Rename(src, dst)
	} else if err != nil {
		return err
	}
	if err := appendFile(dst, src); err != nil {
		return err
	}
	return os.Remove(src)
}

func appendFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// prune deletes archives dated before today − keepDays. Names that don't parse
// as an archive date are left alone.
func (w *Writer) prune(today time.Time) error {
	cutoff := today.AddDate(0, 0, -keepDays)
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		day, ok := w.archiveDay(e.Name())
		if !ok || !day.Before(cutoff) {
			continue
		}
		if err := os.Remove(w.path(e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// archiveDay parses the date out of an archive's name.
func (w *Writer) archiveDay(name string) (time.Time, bool) {
	s, ok := strings.CutPrefix(name, archivePrefix)
	if !ok {
		return time.Time{}, false
	}
	if s, ok = strings.CutSuffix(s, archiveSuffix); !ok {
		return time.Time{}, false
	}
	day, err := time.ParseInLocation(dateLayout, s, w.loc)
	if err != nil {
		return time.Time{}, false
	}
	return day, true
}

// midnight returns the local midnight starting t's day.
func (w *Writer) midnight(t time.Time) time.Time {
	y, m, d := t.In(w.loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, w.loc)
}

func (w *Writer) path(name string) string { return filepath.Join(w.dir, name) }
