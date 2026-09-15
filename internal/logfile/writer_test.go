package logfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	_ "time/tzdata" // America/Los_Angeles without relying on the host's zoneinfo
)

var la = func() *time.Location {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		panic(err)
	}
	return loc
}()

func at(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, la)
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

func openAt(t *testing.T, dir string, now time.Time) (*Writer, *fakeClock) {
	t.Helper()
	c := &fakeClock{t: now}
	w, err := Open(dir, c.Now, la)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { w.Close() })
	return w, c
}

func write(t *testing.T, w *Writer, recs ...string) {
	t.Helper()
	for _, r := range recs {
		if err := w.Write([]byte(r)); err != nil {
			t.Fatalf("Write(%q): %v", r, err)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// putFile creates path with content and, unless mtime is zero, that mtime.
func putFile(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestOpenWritesRecordsWithNewlines(t *testing.T) {
	dir := t.TempDir()
	w, _ := openAt(t, dir, at(2026, 9, 15, 10, 0))
	write(t, w, "a", "---\nyaml: doc")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, filepath.Join(dir, "loghorn.log")), "a\n---\nyaml: doc\n"; got != want {
		t.Fatalf("loghorn.log = %q, want %q", got, want)
	}
}

// A leftover last written today is today's file; loghorn carries on appending.
func TestOpenAppendsToTodaysLeftover(t *testing.T) {
	dir := t.TempDir()
	putFile(t, filepath.Join(dir, "loghorn.log"), "old\n", at(2026, 9, 15, 8, 0))
	w, _ := openAt(t, dir, at(2026, 9, 15, 10, 0))
	write(t, w, "new")

	if got := readFile(t, filepath.Join(dir, "loghorn.log")); got != "old\nnew\n" {
		t.Fatalf("loghorn.log = %q", got)
	}
	if exists(filepath.Join(dir, "loghorn-2026-09-15.log")) {
		t.Fatalf("today's file must not be archived")
	}
}

// A leftover last written yesterday holds only yesterday's records: archive it
// under its mtime's date and start today fresh.
func TestOpenArchivesYesterdaysLeftover(t *testing.T) {
	dir := t.TempDir()
	putFile(t, filepath.Join(dir, "loghorn.log"), "old\n", at(2026, 9, 14, 23, 30))
	w, _ := openAt(t, dir, at(2026, 9, 15, 8, 0))
	write(t, w, "new")

	if got := readFile(t, filepath.Join(dir, "loghorn-2026-09-14.log")); got != "old\n" {
		t.Fatalf("archive = %q, want old", got)
	}
	if got := readFile(t, filepath.Join(dir, "loghorn.log")); got != "new\n" {
		t.Fatalf("loghorn.log = %q, want new", got)
	}
}

// If that day already has an archive, the leftover joins it rather than
// replacing it.
func TestOpenAppendsLeftoverOntoExistingArchive(t *testing.T) {
	dir := t.TempDir()
	putFile(t, filepath.Join(dir, "loghorn-2026-09-14.log"), "first\n", time.Time{})
	putFile(t, filepath.Join(dir, "loghorn.log"), "second\n", at(2026, 9, 14, 23, 0))
	openAt(t, dir, at(2026, 9, 15, 8, 0))

	if got := readFile(t, filepath.Join(dir, "loghorn-2026-09-14.log")); got != "first\nsecond\n" {
		t.Fatalf("archive = %q", got)
	}
}

func TestOpenPrunesBeforeTodayMinusThree(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"loghorn-2026-09-10.log", "loghorn-2026-09-11.log",
		"loghorn-2026-09-12.log", "loghorn-2026-09-13.log", "loghorn-2026-09-14.log",
		"loghorn-notadate.log", "other.log",
	} {
		putFile(t, filepath.Join(dir, name), "x\n", time.Time{})
	}
	openAt(t, dir, at(2026, 9, 15, 12, 0))

	for name, want := range map[string]bool{
		"loghorn-2026-09-10.log": false,
		"loghorn-2026-09-11.log": false,
		"loghorn-2026-09-12.log": true,
		"loghorn-2026-09-13.log": true,
		"loghorn-2026-09-14.log": true,
		"loghorn-notadate.log":   true,
		"other.log":              true,
	} {
		if got := exists(filepath.Join(dir, name)); got != want {
			t.Errorf("%s exists = %v, want %v", name, got, want)
		}
	}
}

func TestOpenReportsHeldLock(t *testing.T) {
	dir := t.TempDir()
	openAt(t, dir, at(2026, 9, 15, 10, 0))

	_, err := Open(dir, (&fakeClock{t: at(2026, 9, 15, 10, 0)}).Now, la)
	var locked *LockedError
	if !errors.As(err, &locked) || locked.PID != os.Getpid() {
		t.Fatalf("second Open: want *LockedError with our pid, got %v", err)
	}
}

func TestCloseReleasesLockAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	w, _ := openAt(t, dir, at(2026, 9, 15, 10, 0))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := w.Write([]byte("late")); err != nil {
		t.Fatalf("Write after Close must be a no-op, got %v", err)
	}
	openAt(t, dir, at(2026, 9, 15, 10, 0))
}
