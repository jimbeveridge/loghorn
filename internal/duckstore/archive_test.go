package duckstore

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
)

const archDay = 20415 // 2025-11-23

func archivePath(dir string, day int) string {
	return filepath.Join(dir, ArchiveDir, "records-"+dayName(day)+".parquet")
}

func hotCount(t *testing.T, e *Engine) int {
	t.Helper()
	var n int
	q := "SELECT count(*) FROM loghorn_db.main.records"
	if err := e.DB().QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func twoDays(t *testing.T, e *Engine) []record.Record {
	recs := []record.Record{
		withSeq(logRec(t, "s", "api", `,"n":1`), (archDay-1)<<32+1),
		withSeq(spanRec(t, `{"trace_id":"0af7651916cd43dd8448eb211c80319c","span_id":"a000000000000001","name":"chat","kind":"client","start":"2026-11-17T17:01:01.000Z","end":"2026-11-17T17:01:02.500Z","duration_ms":1500,"service.name":"svc"}`), (archDay-1)<<32+2),
		withSeq(logRec(t, "s", "api", `,"n":3`), archDay<<32+1),
	}
	addSync(t, e, recs...)
	return recs
}

func TestArchiveMovesClosedDays(t *testing.T) {
	dir := t.TempDir()
	e := openFiles(t, dir, nil)
	defer e.Close()
	in := twoDays(t, e)
	if err := e.Archive(archDay); err != nil {
		t.Fatal(err)
	}
	// One archive per day holds both signals.
	if files, _ := filepath.Glob(filepath.Join(dir, ArchiveDir, "*")); len(files) != 1 || files[0] != archivePath(dir, archDay-1) {
		t.Errorf("archives %v, want only %s", files, archivePath(dir, archDay-1))
	}
	if n := hotCount(t, e); n != 1 {
		t.Errorf("hot rows = %d, want today's 1", n)
	}
	got, err := e.Records(context.Background(), "true")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("records = %d, want 3", len(got))
	}
	for i := range in {
		if got[i].Seq != in[i].Seq || !reflect.DeepEqual(got[i].Raw, in[i].Raw) || !reflect.DeepEqual(got[i].Fields, in[i].Fields) {
			t.Errorf("record %d: got %+v, want %+v", i, got[i], in[i])
		}
	}
	if maxSeq(t, e, archDay-1) != 2 {
		t.Errorf("MaxSeq of the archived day = %d", maxSeq(t, e, archDay-1))
	}
	// Reopening reads the archives again.
	e.Close()
	e = openFiles(t, dir, nil)
	if n := count(t, e); n != 3 {
		t.Errorf("after reopen: %d records", n)
	}
}

func TestFailedArchiveKeepsHotRows(t *testing.T) {
	dir := t.TempDir()
	var warns []string
	e := openFiles(t, dir, func(m string) { warns = append(warns, m) })
	defer e.Close()
	twoDays(t, e)
	e.wmu.Lock()
	e.failArchive = true
	e.wmu.Unlock()
	e.Archive(archDay)
	matches, _ := filepath.Glob(filepath.Join(dir, ArchiveDir, "*"))
	if len(matches) != 0 {
		t.Errorf("files left after a failed archive: %v", matches)
	}
	if n := hotCount(t, e); n != 3 {
		t.Errorf("hot rows = %d, want 3", n)
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "archive") {
		t.Errorf("warnings %q", warns)
	}
	e.wmu.Lock()
	e.failArchive = false
	e.wmu.Unlock()
	if err := e.Archive(archDay); err != nil {
		t.Fatal(err)
	}
	if n := hotCount(t, e); n != 1 || count(t, e) != 3 {
		t.Errorf("retry: hot %d, total %d", n, count(t, e))
	}
}

func TestArchiveExpiresOldDays(t *testing.T) {
	dir := t.TempDir()
	e := openFiles(t, dir, nil)
	defer e.Close()
	twoDays(t, e)
	e.Archive(archDay)
	if err := e.Archive(archDay + 3); err != nil { // keeps archDay..archDay+3: archDay−1 goes
		t.Fatal(err)
	}
	if _, err := os.Stat(archivePath(dir, archDay-1)); !os.IsNotExist(err) {
		t.Errorf("old archive still there: %v", err)
	}
	if _, err := os.Stat(archivePath(dir, archDay)); err != nil {
		t.Errorf("kept day's archive: %v", err)
	}
	if n := count(t, e); n != 1 {
		t.Errorf("records = %d, want 1", n)
	}
	var n int
	e.DB().QueryRow("SELECT count(*) FROM all_fields WHERE day < ?", archDay).Scan(&n)
	if n != 0 {
		t.Errorf("%d tallies of expired days", n)
	}
}

func TestRelationReadsArchives(t *testing.T) {
	e := openFiles(t, t.TempDir(), nil)
	defer e.Close()
	twoDays(t, e)
	e.Archive(archDay)
	q, args := e.Relation(record.SourceLog, "s", "api", []schema.Field{{Path: "n", Type: schema.TInt}})
	var sum int64
	if err := e.DB().QueryRow("SELECT sum(v0) FROM ("+q+")", args...).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if sum != 4 {
		t.Errorf("sum over both days = %d, want 4", sum)
	}
	tl, _ := e.Tallies(record.SourceLog, "s", "api")
	if tl["n"].Count != 2 {
		t.Errorf("tally of n = %d, want 2", tl["n"].Count)
	}
}

func TestUntimedRecordArchived(t *testing.T) {
	e := openFiles(t, t.TempDir(), nil)
	defer e.Close()
	r := logRec(t, "s", "api", ``)
	r.Time, r.Seq = time.Time{}, (archDay-1)<<32+1
	addSync(t, e, r)
	e.Archive(archDay)
	recs, err := e.Records(context.Background(), "true")
	if err != nil || len(recs) != 1 || !recs[0].Time.IsZero() || hotCount(t, e) != 0 {
		t.Errorf("records %+v, %v; hot %d", recs, err, hotCount(t, e))
	}
}

// TestArchiveAtMidnight: the first batch of a new day archives the day
// before.
func TestArchiveAtMidnight(t *testing.T) {
	dir := t.TempDir()
	e := openFiles(t, dir, nil)
	defer e.Close()
	e.Archive(archDay)
	addSync(t, e, withSeq(logRec(t, "s", "api", ``), archDay<<32+1))
	addSync(t, e, withSeq(logRec(t, "s", "api", ``), (archDay+1)<<32+1))
	if _, err := os.Stat(archivePath(dir, archDay)); err != nil {
		t.Errorf("no archive after midnight: %v", err)
	}
	if n := hotCount(t, e); n != 1 || count(t, e) != 2 {
		t.Errorf("hot %d, total %d", n, count(t, e))
	}
}

// TestArchiveMergesRowsLeftHot: rows still hot for an archived day (a crash
// between publishing and deleting, or a late row) are merged into the
// archive, each once.
func TestArchiveMergesRowsLeftHot(t *testing.T) {
	e := openFiles(t, t.TempDir(), nil)
	defer e.Close()
	twoDays(t, e)
	e.Archive(archDay)
	addSync(t, e, withSeq(logRec(t, "s", "api", ``), (archDay-1)<<32+3)) // late
	// Before archiving again, the view must not double count anything.
	if n := count(t, e); n != 4 {
		t.Errorf("before: %d records, want 4", n)
	}
	if err := e.Archive(archDay); err != nil {
		t.Fatal(err)
	}
	if n := count(t, e); n != 4 || hotCount(t, e) != 1 {
		t.Errorf("after: %d records (want 4), hot %d", n, hotCount(t, e))
	}
}

// TestArchiveBumpsVersion: archiving deletes rows and expires tallies, so
// it counts as a change: a schema cached between a write and the midnight
// archive is not kept past it (review §4).
func TestArchiveBumpsVersion(t *testing.T) {
	e := openFiles(t, t.TempDir(), nil)
	defer e.Close()
	twoDays(t, e)
	v := e.Version()
	if err := e.Archive(archDay + 3); err != nil {
		t.Fatal(err)
	}
	if e.Version() == v {
		t.Error("archiving left the version unchanged")
	}
}

// TestUnknownHotDaysCountNothingTwice: if the hot days can't be read,
// every archive is read as possibly overlapping the hot rows, so a row
// left hot after a crash is not counted twice (review §4).
func TestUnknownHotDaysCountNothingTwice(t *testing.T) {
	e := openFiles(t, t.TempDir(), nil)
	defer e.Close()
	twoDays(t, e)
	e.Archive(archDay)
	addSync(t, e, withSeq(logRec(t, "s", "api", ``), (archDay-1)<<32+1)) // a row the archive also has
	e.wmu.Lock()
	e.failHotDays = true
	e.schemaMu.Lock()
	err := e.rebuildViews()
	e.schemaMu.Unlock()
	e.wmu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, e); n != 3 {
		t.Errorf("%d records, want 3", n)
	}
}
