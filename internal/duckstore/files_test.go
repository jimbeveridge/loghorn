package duckstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/record"
)

func openFiles(t *testing.T, dir string, warn func(string)) *Engine {
	t.Helper()
	e, err := OpenFiles(config.Input{}, warn, FileOptions{Dir: dir, KeepDays: 3})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func addSync(t *testing.T, e *Engine, rs ...record.Record) {
	t.Helper()
	for _, r := range rs {
		e.Add(r)
	}
	if !e.Sync(5 * time.Second) {
		t.Fatal("sync timed out")
	}
}

func withSeq(r record.Record, seq int) record.Record { r.Seq = seq; return r }

func TestFilesPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	day := 20000
	e := openFiles(t, dir, nil)
	addSync(t, e,
		withSeq(logRec(t, "s", "api", `,"n":1`), day<<32+1),
		withSeq(spanRec(t, `{"trace_id":"0af7651916cd43dd8448eb211c80319c","span_id":"a000000000000001","name":"chat","kind":"client","start":"2026-11-17T17:01:01.000Z","end":"2026-11-17T17:01:02.500Z","duration_ms":1500,"service.name":"svc"}`), day<<32+2),
	)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "loghorn.duckdb")); err != nil {
		t.Fatal(err)
	}
	e = openFiles(t, dir, nil)
	defer e.Close()
	// One file, one records table for logs and spans (spec: Layout D).
	var tables []string
	rows, err := e.DB().Query("SELECT table_name FROM duckdb_tables() WHERE database_name = 'loghorn_db' ORDER BY 1")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	if strings.Join(tables, ",") != "_fields,records" {
		t.Errorf("tables %v, want _fields, records", tables)
	}
	var sources int
	if err := e.DB().QueryRow("SELECT count(DISTINCT _source) FROM loghorn_db.main.records").Scan(&sources); err != nil || sources != 2 {
		t.Errorf("records table holds %d sources (%v), want logs and spans", sources, err)
	}
	recs, err := e.Records(context.Background(), "true")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Seq != day<<32+1 || recs[1].Seq != day<<32+2 || recs[1].Source != record.SourceOtel {
		t.Fatalf("records after reopen: %+v", recs)
	}
	if got := maxSeq(t, e, day); got != 2 {
		t.Errorf("MaxSeq = %d, want 2", got)
	}
	tl, err := e.Tallies(record.SourceLog, "s", "api")
	if err != nil || tl["n"].Count != 1 {
		t.Errorf("tallies after reopen: %v, %v", tl, err)
	}
}

func TestWriteFailureFallsBackToMemory(t *testing.T) {
	var warnings []string
	e := openFiles(t, t.TempDir(), func(m string) { warnings = append(warnings, m) })
	defer e.Close()
	addSync(t, e, withSeq(logRec(t, "s", "api", ``), 1))
	e.wmu.Lock()
	e.failWrites = true
	e.wmu.Unlock()
	addSync(t, e, withSeq(logRec(t, "s", "api", ``), 2))
	addSync(t, e, withSeq(logRec(t, "s", "api", ``), 3))
	recs, err := e.Records(context.Background(), "true")
	if err != nil || len(recs) != 3 {
		t.Fatalf("records: %d, %v", len(recs), err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "DuckDB write failed") {
		t.Errorf("warnings %q", warnings)
	}
	tl, _ := e.Tallies(record.SourceLog, "s", "api")
	if tl["labels.schema"].Count != 3 {
		t.Errorf("tallies count %d, want 3", tl["labels.schema"].Count)
	}
}

func TestFilesAreOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	e := openFiles(t, dir, nil)
	addSync(t, e, withSeq(logRec(t, "s", "api", ``), 1))
	defer e.Close()
	matches, _ := filepath.Glob(filepath.Join(dir, "*.duckdb*"))
	if len(matches) < 2 {
		t.Fatalf("files %v", matches)
	}
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", filepath.Base(m), fi.Mode().Perm())
		}
	}
}

// TestOutOfRangeTimeKeepsTheBatch: a time DuckDB can't hold (TIMESTAMP_NS
// covers 1677 to 2262) is stored as NULL, so neither the record nor the rest
// of its batch is lost and the files stay in use (review §1.1).
func TestOutOfRangeTimeKeepsTheBatch(t *testing.T) {
	var warns []string
	e := openFiles(t, t.TempDir(), func(m string) { warns = append(warns, m) })
	defer e.Close()
	far := logRec(t, "api", "s", ``)
	far.Time = time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
	old := logRec(t, "api", "s", ``)
	old.Time = time.Date(1600, 1, 1, 0, 0, 0, 0, time.UTC)
	addSync(t, e, withSeq(logRec(t, "api", "s", ``), 1), withSeq(far, 2), withSeq(old, 3))
	if e.failed || len(warns) != 0 {
		t.Fatalf("files failed %v, warnings %q", e.failed, warns)
	}
	var n, nulls int
	if err := e.DB().QueryRow("SELECT count(*), count(*) FILTER (WHERE _time IS NULL) FROM loghorn_db.main.records").Scan(&n, &nulls); err != nil || n != 3 || nulls != 2 {
		t.Fatalf("file holds %d records, %d without a time (%v); want 3 and 2", n, nulls, err)
	}
}

// TestBadRowSkipsOnlyItself: a row DuckDB refuses for its data, in memory
// as well as in the file, is skipped with a warning; the rest of the batch
// is stored and the files stay in use (review §1.1).
func TestBadRowSkipsOnlyItself(t *testing.T) {
	var warns []string
	e := openFiles(t, t.TempDir(), func(m string) { warns = append(warns, m) })
	defer e.Close()
	e.wmu.Lock()
	e.rejectRow = func(r record.Record) bool { return r.Seq == 2 }
	e.wmu.Unlock()
	addSync(t, e, withSeq(logRec(t, "api", "s", ``), 1), withSeq(logRec(t, "api", "s", ``), 2), withSeq(logRec(t, "api", "s", ``), 3))
	if e.failed {
		t.Fatal("a bad row sent the session to memory")
	}
	var n int
	if err := e.DB().QueryRow("SELECT count(*) FROM loghorn_db.main.records").Scan(&n); err != nil || n != 2 {
		t.Fatalf("file holds %d records (%v), want 2", n, err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "skipped") {
		t.Errorf("warnings %q", warns)
	}
}

func marked(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ImportedFile))
	return err == nil
}

// TestImportedMark: a clean Close of complete files marks them, beside the
// database file; records kept out of the file this run (a full queue, a
// failed write) leave them unmarked, and TakeImported clears the mark
// (review §1.3).
func TestImportedMark(t *testing.T) {
	dir := t.TempDir()
	e := openFiles(t, dir, nil)
	e.Close()
	if marked(dir) {
		t.Error("files never marked complete were marked at Close")
	}
	e = openFiles(t, dir, nil)
	e.MarkComplete()
	e.Close()
	if !marked(dir) {
		t.Fatal("a clean Close of complete files left no mark")
	}
	if !TakeImported(dir) || marked(dir) {
		t.Fatal("TakeImported didn't report and clear the mark")
	}
	if TakeImported(dir) {
		t.Error("TakeImported reported a mark it had cleared")
	}

	var warns []string
	e = openFiles(t, dir, func(m string) { warns = append(warns, m) })
	e.MarkComplete()
	e.SetMaxQueue(1)
	e.wmu.Lock() // the writer stalls, as during a long archive
	for i := 1; i <= 3; i++ {
		e.Add(withSeq(logRec(t, "api", "s", ``), i))
	}
	e.wmu.Unlock()
	e.Close()
	if marked(dir) {
		t.Error("files with skipped records were marked complete")
	}
	if !strings.Contains(strings.Join(warns, "\n"), "skipped") {
		t.Errorf("warnings %q", warns)
	}

	e = openFiles(t, dir, func(string) {})
	e.MarkComplete()
	e.wmu.Lock()
	e.failWrites = true
	e.wmu.Unlock()
	addSync(t, e, withSeq(logRec(t, "api", "s", ``), 9))
	e.Close()
	if marked(dir) {
		t.Error("files whose write failed were marked complete")
	}
}

func count(t *testing.T, e *Engine) int {
	t.Helper()
	n, err := e.Count()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func maxSeq(t *testing.T, e *Engine, day int) int {
	t.Helper()
	n, err := e.MaxSeq(day)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
