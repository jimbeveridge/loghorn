package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/adapter"
	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/duckstore"
	"github.com/jimbeveridge/loghorn/internal/query"
	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
)

// TestSpeedAtScale times the web view's requests over a spike-2 sized day
// (about 770k records: 8 log streams in 3 services, and spans of 3 types)
// against the ~500 ms target (v2 spec): first in loghorn.duckdb,
// then after the next day's open has archived the day to
// Parquet. It also reports restart and archive times, disk sizes and peak
// memory. It is slow, so it runs only with LOGHORN_SPEED=1, and reports
// rather than fails.
func TestSpeedAtScale(t *testing.T) {
	if os.Getenv("LOGHORN_SPEED") == "" {
		t.Skip("set LOGHORN_SPEED=1 to time requests at scale")
	}
	n := 770_000
	if v, err := strconv.Atoi(os.Getenv("LOGHORN_SPEED_N")); err == nil {
		n = v
	}
	dir := t.TempDir()
	start := time.Date(2026, 11, 23, 0, 0, 0, 0, time.Local)
	s, err := Open(Options{Mode: ModeLive, Dir: dir, Input: config.Input{}, Now: func() time.Time { return start.Add(20 * time.Hour) }, Warn: func(m string) { t.Log(m) }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sevs := []string{"INFO", "INFO", "INFO", "WARNING", "ERROR", "DEBUG"}
	routes := []string{"/api/users", "/api/orders", "/api/cart", "/health", "/api/search"}
	t0 := time.Now()
	for i := 0; i < n; i++ {
		ts := start.Add(time.Duration(i) * 90 * time.Millisecond)
		var line string
		if i%5 == 4 { // spans: 20%
			typ := []string{`"kind":"client","attributes":{"db.system":"postgresql","db.statement":"select 1"}`, `"kind":"server","attributes":{"http.request.method":"GET","http.route":"/x"}`, `"kind":"internal","attributes":{"job":"j"}`}[i%3]
			line = fmt.Sprintf(`{"trace_id":"%032x","span_id":"%016x","parent_span_id":"%016x","name":"op%d","start":%q,"end":%q,"duration_ms":%v,"status":"ok",%s,"service.name":"svc%d"}`,
				i/20+1, i+1, (i/20)*20+1, i%7, ts.UTC().Format(time.RFC3339Nano), ts.Add(time.Millisecond).UTC().Format(time.RFC3339Nano), float64(i%997)*0.37+0.1, typ, i%3)
		} else {
			line = fmt.Sprintf(`{"timestamp":%q,"severity":%q,"message":"request %d handled for user %d","labels":{"schema":"stream%d"},"service":{"name":"svc%d"},"httpRequest":{"requestMethod":"GET","requestUrl":%q,"status":%d,"latency":"%0.3fs"},"userId":%d,"bytes":%d,"region":"r%d","cache":%v,"retries":%d,"tags":["a","b"]}`,
				ts.UTC().Format(time.RFC3339Nano), sevs[i%len(sevs)], i, i%5000, i%8, i%3, routes[i%len(routes)], []int{200, 200, 201, 404, 500}[i%5], float64(i%1000)/997, i%5000, i*13%65536, i%4, i%2 == 0, i%3)
		}
		var r record.Record
		var ok bool
		if strings.HasPrefix(line, `{"trace_id"`) {
			r, ok = record.NormalizeSpan([]byte(line))
		} else {
			r, ok = logRecordOK(line)
		}
		if !ok {
			t.Fatalf("bad line %s", line)
		}
		s.Add(r, ts)
		if i%50_000 == 0 {
			s.Flush() // a burst past MaxQueue would be skipped, by design
		}
	}
	s.Flush()
	t.Logf("wrote %d records in %v", n, time.Since(t0))
	if got := mustInfo(t, s).Count; got != n {
		t.Fatalf("stored %d, want %d", got, n)
	}
	now := start.Add(24 * time.Hour)
	t.Logf("machine: %s/%s, %d CPUs", runtime.GOOS, runtime.GOARCH, runtime.NumCPU())

	measure := func(name string, f func() error) {
		var ds []time.Duration
		for i := 0; i < 5; i++ {
			st := time.Now()
			if err := f(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			ds = append(ds, time.Since(st))
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		t.Logf("%-40s median %v", name, ds[len(ds)/2].Round(100*time.Microsecond))
	}
	queries := func(s *Store, label string) {
		run := func(name string, q query.Query) {
			src, _ := record.ParseSource(q.Source)
			sch, err := s.Schema(q.Service, src, q.Stream)
			if err != nil {
				t.Fatal(err)
			}
			sch = schema.Effective(sch, nil)
			measure(label+" "+name, func() error { _, err := s.Query(context.Background(), sch, q, now); return err })
		}
		run("samples stream", query.Query{Stream: "stream1", Service: "*"})
		run("samples filter+sort", query.Query{Stream: "stream1", Service: "*", Filters: []query.Filter{{Field: "httpRequest.status", Op: ">=", Values: []string{"400"}}}, Sort: query.Sort{Field: "bytes", Desc: true}})
		run("search", query.Query{Stream: "stream1", Service: "*", Search: "user 4242"})
		run("table p95 by status", query.Query{Stream: "stream1", Service: "*", View: "table", GroupBy: []string{"httpRequest.status"}, Aggregates: []query.Aggregate{{Op: "p95", Field: "httpRequest.latency"}, {Op: "error_rate"}}})
		run("graph auto by severity", query.Query{Stream: "stream1", Service: "*", View: "graph", GroupBy: []string{"severity"}})
		run("graph all kinds", query.Query{Stream: "*", Service: "*", View: "graph"})
		run("heatmap db spans", query.Query{Stream: "db", Source: "otel", Service: "*", View: "heatmap"})
		measure(label+" kinds", func() error { s.Kinds("log", "*", time.Time{}, time.Time{}); return nil })
		measure(label+" schema (cache cold)", func() error { s.cache = map[[3]string]cached{}; _, err := s.Schema("*", "log", "stream1"); return err })
		measure(label+" trace", func() error { s.TraceRecords(fmt.Sprintf("%032x", 1000)); return nil })
	}
	queries(s, "duckdb")
	sizes := func(when string) {
		var parts []string
		for _, pat := range []string{"*.duckdb", "*.duckdb.wal", duckstore.ArchiveDir + "/*.parquet"} {
			m, _ := filepath.Glob(filepath.Join(dir, pat))
			for _, f := range m {
				if fi, err := os.Stat(f); err == nil {
					parts = append(parts, fmt.Sprintf("%s %.1f MB", filepath.Base(f), float64(fi.Size())/(1<<20)))
				}
			}
		}
		t.Logf("disk %s: %s", when, strings.Join(parts, ", "))
	}
	sizes("after writing")

	s.Close()
	reopen := func(at time.Time) (*Store, time.Duration) {
		st := time.Now()
		s, err := Open(Options{Mode: ModeLive, Dir: dir, Input: config.Input{}, Now: func() time.Time { return at }, Warn: func(m string) { t.Log(m) }})
		if err != nil {
			t.Fatal(err)
		}
		return s, time.Since(st)
	}
	s, d := reopen(start.Add(20 * time.Hour))
	t.Logf("restart with %d records stored: %v", n, d.Round(time.Millisecond))
	s.Close()
	s, d = reopen(start.Add(30 * time.Hour)) // the next day: archives this one
	defer s.Close()
	t.Logf("next day's open, archiving the day to Parquet: %v", d.Round(time.Millisecond))
	sizes("after archiving")
	if got := mustInfo(t, s).Count; got != n {
		t.Fatalf("after archiving: %d records, want %d", got, n)
	}
	queries(s, "parquet")
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	rss := ru.Maxrss // bytes on macOS, KiB on Linux
	if runtime.GOOS == "linux" {
		rss <<= 10
	}
	t.Logf("peak RSS of the test process: %d MB", rss>>20)
}

func logRecordOK(line string) (record.Record, bool) {
	return record.Normalize(parser.ParseLine([]byte(line)), config.Input{})
}

var parser = adapter.New(config.Input{})
