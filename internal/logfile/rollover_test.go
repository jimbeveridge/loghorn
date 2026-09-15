package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Rolling over before the write puts each record on its own side of midnight.
func TestMidnightRolloverSplitsAtTheRecord(t *testing.T) {
	dir := t.TempDir()
	w, c := openAt(t, dir, at(2026, 9, 14, 23, 59))
	write(t, w, "before")
	c.t = at(2026, 9, 15, 0, 0)
	write(t, w, "after")

	if got := readFile(t, filepath.Join(dir, "loghorn-2026-09-14.log")); got != "before\n" {
		t.Fatalf("archive = %q, want before", got)
	}
	if got := readFile(t, filepath.Join(dir, "loghorn.log")); got != "after\n" {
		t.Fatalf("loghorn.log = %q, want after", got)
	}
}

// With nothing arriving on the 15th, the file is still the 14th's — named by the
// day it covers, which agrees with its mtime, not by "yesterday".
func TestRolloverAfterAGapNamesTheFilesOwnDay(t *testing.T) {
	dir := t.TempDir()
	w, c := openAt(t, dir, at(2026, 9, 14, 10, 0))
	write(t, w, "a")
	c.t = at(2026, 9, 16, 2, 0)
	write(t, w, "b")

	if got := readFile(t, filepath.Join(dir, "loghorn-2026-09-14.log")); got != "a\n" {
		t.Fatalf("archive = %q", got)
	}
	if exists(filepath.Join(dir, "loghorn-2026-09-15.log")) {
		t.Fatalf("no archive should exist for a day nothing arrived on")
	}
	if got := readFile(t, filepath.Join(dir, "loghorn.log")); got != "b\n" {
		t.Fatalf("loghorn.log = %q", got)
	}
}

// Crossing two midnights in one run guards against rollover failing to
// advance day/next after the first crossing — a bug that would leave the
// second midnight undetected and both the 15th's and the 16th's records
// landing in the same file.
func TestRolloverCrossesTwoMidnights(t *testing.T) {
	dir := t.TempDir()
	w, c := openAt(t, dir, at(2026, 9, 14, 23, 0))
	write(t, w, "a")
	c.t = at(2026, 9, 15, 0, 30)
	write(t, w, "b")
	c.t = at(2026, 9, 15, 12, 0)
	write(t, w, "c")
	c.t = at(2026, 9, 16, 0, 10)
	write(t, w, "d")

	if got := readFile(t, filepath.Join(dir, "loghorn-2026-09-14.log")); got != "a\n" {
		t.Fatalf("2026-09-14 archive = %q, want a", got)
	}
	if got := readFile(t, filepath.Join(dir, "loghorn-2026-09-15.log")); got != "b\nc\n" {
		t.Fatalf("2026-09-15 archive = %q, want b and c", got)
	}
	if got := readFile(t, filepath.Join(dir, "loghorn.log")); got != "d\n" {
		t.Fatalf("loghorn.log = %q, want d", got)
	}
}

func TestRolloverPrunes(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "loghorn-2026-09-11.log")
	putFile(t, old, "x\n", time.Time{})
	w, c := openAt(t, dir, at(2026, 9, 14, 23, 0))
	if !exists(old) {
		t.Fatalf("the 11th is within today − 3 on the 14th and must survive Open")
	}
	c.t = at(2026, 9, 15, 0, 30)
	write(t, w, "x")
	if exists(old) {
		t.Fatalf("the 11th is before today − 3 on the 15th and must be pruned at rollover")
	}
}

// 2026-11-01 is 25 hours long in Los Angeles (clocks fall back). Rolling over
// 24 hours after midnight would split the day an hour early.
func TestRolloverOnDSTDayWaitsForLocalMidnight(t *testing.T) {
	dir := t.TempDir()
	start := at(2026, 11, 1, 0, 30)
	w, c := openAt(t, dir, start)
	write(t, w, "a")

	c.t = start.Add(24 * time.Hour) // 23:30 local, still Nov 1
	write(t, w, "b")
	if exists(filepath.Join(dir, "loghorn-2026-11-01.log")) {
		t.Fatalf("rolled over at %s, before local midnight", c.t.In(la))
	}

	c.t = at(2026, 11, 2, 0, 0)
	write(t, w, "c")
	if got := readFile(t, filepath.Join(dir, "loghorn-2026-11-01.log")); got != "a\nb\n" {
		t.Fatalf("archive = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "loghorn.log")); got != "c\n" {
		t.Fatalf("loghorn.log = %q", got)
	}
}

// A failed write is reported once; the Writer stops rather than erroring on
// every record.
func TestWriteFailureStopsTheWriter(t *testing.T) {
	dir := t.TempDir()
	w, _ := openAt(t, dir, at(2026, 9, 15, 10, 0))
	w.f.Close() // the next write hits a closed descriptor

	if err := w.Write([]byte("a")); err == nil {
		t.Fatalf("first Write after the failure should return it")
	}
	if err := w.Write([]byte("b")); err != nil {
		t.Fatalf("later Writes must be silent no-ops, got %v", err)
	}
}

// A rollover that cannot archive stops the Writer the same way. A directory
// squatting on the archive's name makes the append fail.
func TestRolloverFailureStopsTheWriter(t *testing.T) {
	dir := t.TempDir()
	w, c := openAt(t, dir, at(2026, 9, 14, 22, 0))
	if err := os.Mkdir(filepath.Join(dir, "loghorn-2026-09-14.log"), 0o755); err != nil {
		t.Fatal(err)
	}
	c.t = at(2026, 9, 15, 1, 0)

	if err := w.Write([]byte("a")); err == nil {
		t.Fatalf("a failed rollover should be returned")
	}
	if err := w.Write([]byte("b")); err != nil {
		t.Fatalf("later Writes must be silent no-ops, got %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "loghorn.log")); strings.Contains(got, "a") || strings.Contains(got, "b") {
		t.Fatalf("a stopped writer must have written nothing, got %q", got)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close after a stop: %v", err)
	}

	// loghorn.log's mtime is whatever the real clock was when Open created it
	// above, not the fake clock the test runs on; reopening must not depend on
	// the real clock having reached 2026-09-15.
	fakeNow := at(2026, 9, 15, 1, 0)
	if err := os.Chtimes(filepath.Join(dir, "loghorn.log"), fakeNow, fakeNow); err != nil {
		t.Fatal(err)
	}
	openAt(t, dir, fakeNow) // the lock was released
}
