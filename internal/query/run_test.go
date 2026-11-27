package query

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/adapter"
	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
)

var t0 = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func rec(seq int, at time.Time, raw string, kv ...any) record.Record {
	f := map[string]any{}
	for i := 0; i < len(kv); i += 2 {
		f[kv[i].(string)] = kv[i+1]
	}
	return record.Record{Seq: seq, Time: at, Stream: "a", Fields: f, Raw: []byte(raw)}
}

func sch(fs ...schema.Field) schema.Schema {
	cols := []string{}
	for _, f := range fs {
		cols = append(cols, f.Path)
	}
	return schema.Schema{Stream: "a", Columns: cols, Fields: fs}
}

func run(t *testing.T, recs []record.Record, s schema.Schema, q Query) Result {
	t.Helper()
	res, err := runSQL(t, recs, s, q, t0, t0.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func seqs(r Result) []int {
	var out []int
	for _, row := range r.Rows {
		out = append(out, row.Seq)
	}
	return out
}

func eq(t *testing.T, got, want []int) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestFilterIntEqExact(t *testing.T) {
	s := sch(schema.Field{Path: "id", Type: schema.TInt})
	recs := []record.Record{
		rec(1, t0, "", "id", json.Number("1763400000123456789")),
		rec(2, t0, "", "id", json.Number("1763400000123456788")),
	}
	q := Query{Filters: []Filter{{Field: "id", Op: "=", Values: []string{"1763400000123456789"}}}}
	eq(t, seqs(run(t, recs, s, q)), []int{1})
	q.Filters[0].Op = "!="
	eq(t, seqs(run(t, recs, s, q)), []int{2})
}

func TestBigIntCellIsExactString(t *testing.T) {
	s := sch(schema.Field{Path: "id", Type: schema.TInt}, schema.Field{Path: "small", Type: schema.TInt})
	recs := []record.Record{rec(1, t0, "", "id", json.Number("1763400000123456789"), "small", json.Number("42"))}
	res := run(t, recs, s, Query{})
	b, _ := json.Marshal(res.Rows[0].Cells)
	if string(b) != `[{"v":"1763400000123456789"},{"v":42}]` {
		t.Errorf("cells = %s", b)
	}
}

func TestFilterDurationGt(t *testing.T) {
	s := sch(schema.Field{Path: "latency", Type: schema.TDuration})
	recs := []record.Record{
		rec(1, t0, "", "latency", "0.061s"),
		rec(2, t0, "", "latency", "0.003s"),
	}
	q := Query{Filters: []Filter{{Field: "latency", Op: ">", Values: []string{"50ms"}}}}
	eq(t, seqs(run(t, recs, s, q)), []int{1})
}

func TestFilterIn(t *testing.T) {
	s := sch(schema.Field{Path: "op", Type: schema.TString})
	recs := []record.Record{rec(1, t0, "", "op", "SELECT"), rec(2, t0, "", "op", "UPDATE"), rec(3, t0, "", "op", "select")}
	q := Query{Sort: Sort{Field: "timestamp"}, Filters: []Filter{{Field: "op", Op: "in", Values: []string{"SELECT", "UPDATE"}}}}
	eq(t, seqs(run(t, recs, s, q)), []int{1, 2})
	q.Filters[0].Op = "not-in"
	eq(t, seqs(run(t, recs, s, q)), []int{3})
}

func TestFilterContainsCaseInsensitive(t *testing.T) {
	s := sch(schema.Field{Path: "q", Type: schema.TString})
	recs := []record.Record{rec(1, t0, "", "q", "SELECT * FROM Users"), rec(2, t0, "", "q", "insert")}
	q := Query{Filters: []Filter{{Field: "q", Op: "contains", Values: []string{"from users"}}}}
	eq(t, seqs(run(t, recs, s, q)), []int{1})
}

func TestEmptyNotEmpty(t *testing.T) {
	s := sch(schema.Field{Path: "x", Type: schema.TString})
	recs := []record.Record{
		rec(1, t0, ""),
		rec(2, t0, "", "x", nil),
		rec(3, t0, "", "x", ""),
		rec(4, t0, "", "x", "v"),
	}
	q := Query{Sort: Sort{Field: "timestamp"}, Filters: []Filter{{Field: "x", Op: "empty"}}}
	eq(t, seqs(run(t, recs, s, q)), []int{1, 2, 3})
	q.Filters[0].Op = "not-empty"
	eq(t, seqs(run(t, recs, s, q)), []int{4})
	q.Filters[0] = Filter{Field: "x", Op: "!=", Values: []string{"v"}}
	eq(t, seqs(run(t, recs, s, q)), []int{1, 2, 3})
}

func TestBadValueExcluded(t *testing.T) {
	s := sch(schema.Field{Path: "rows", Type: schema.TInt, Saved: true})
	recs := []record.Record{
		rec(1, t0, "", "rows", json.Number("3")),
		rec(2, t0, "", "rows", "n/a"),
	}
	for _, op := range []string{"<", "=", "!="} {
		q := Query{Filters: []Filter{{Field: "rows", Op: op, Values: []string{"5"}}}}
		for _, sq := range seqs(run(t, recs, s, q)) {
			if sq == 2 {
				t.Errorf("bad row matched %s", op)
			}
		}
	}
	q := Query{Filters: []Filter{{Field: "rows", Op: "not-empty"}}}
	if got := run(t, recs, s, q); got.Matched != 2 {
		t.Errorf("not-empty matched %d", got.Matched)
	}
	res := run(t, recs, s, Query{Sort: Sort{Field: "timestamp"}})
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %d", len(res.Rows))
	}
	c := res.Rows[1].Cells[0]
	if !c.Bad || c.V != "n/a" || res.Rows[0].Cells[0].Bad {
		t.Errorf("cells = %+v / %+v", res.Rows[0].Cells[0], c)
	}
}

func TestRangeThisRun(t *testing.T) {
	s := sch(schema.Field{Path: "x", Type: schema.TString})
	recs := []record.Record{rec(1, t0.Add(-2*time.Hour), ""), rec(2, t0.Add(-30*time.Minute), "")}
	eq(t, seqs(run(t, recs, s, Query{Range: Range{From: "this-run"}})), []int{2})
	eq(t, seqs(run(t, recs, s, Query{Range: Range{From: "all"}})), []int{2, 1})
	// zero runStart means unbounded
	res, err := runSQL(t, recs, s, Query{Range: Range{From: "this-run"}}, t0, time.Time{})
	if err != nil || res.Matched != 2 {
		t.Errorf("zero runStart: %v %d", err, res.Matched)
	}
}

func TestRangeRelative(t *testing.T) {
	s := sch(schema.Field{Path: "x", Type: schema.TString})
	recs := []record.Record{
		rec(1, t0.Add(-16*time.Minute), ""),
		rec(2, t0.Add(-15*time.Minute), ""), // boundary inclusive
		rec(3, t0.Add(-time.Minute), ""),
		rec(4, t0.Add(time.Minute), ""),
	}
	eq(t, seqs(run(t, recs, s, Query{Range: Range{From: "15m"}})), []int{4, 3, 2})
	to := t0.Format(time.RFC3339)
	eq(t, seqs(run(t, recs, s, Query{Range: Range{From: "15m", To: to}})), []int{3, 2})
}

func TestParseAgo(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"24m":   24 * time.Minute,
		"3.7h":  3*time.Hour + 42*time.Minute,
		"1d":    24 * time.Hour,
		"1.5d":  36 * time.Hour,
		"2w":    14 * 24 * time.Hour,
		"1d12h": 36 * time.Hour,
		" 90s ": 90 * time.Second,
		"250ms": 250 * time.Millisecond,
		"1h30m": 90 * time.Minute,
		".5h":   30 * time.Minute,
		"2D":    48 * time.Hour,
	} {
		got, ok := ParseAgo(in)
		if !ok || got != want {
			t.Errorf("ParseAgo(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "d", "1", "-1h", "1x", "1d-2h", "now", "1.2.3h", "0h"} {
		if got, ok := ParseAgo(in); ok {
			t.Errorf("ParseAgo(%q) = %v, want rejected", in, got)
		}
	}
	// Range uses it.
	from, _, err := Range{From: "1d"}.Resolve(t0, t0)
	if err != nil || !from.Equal(t0.Add(-24*time.Hour)) {
		t.Errorf("Resolve 1d = %v, %v", from, err)
	}
}

func TestRangeZeroTimeOnlyUnbounded(t *testing.T) {
	s := sch(schema.Field{Path: "x", Type: schema.TString})
	recs := []record.Record{rec(1, time.Time{}, ""), rec(2, t0, "")}
	eq(t, seqs(run(t, recs, s, Query{Range: Range{From: "all"}})), []int{2, 1})
	eq(t, seqs(run(t, recs, s, Query{Range: Range{From: "1h"}})), []int{2})
}

func TestSearchNonASCII(t *testing.T) {
	s := sch(schema.Field{Path: "x", Type: schema.TString})
	recs := []record.Record{rec(1, t0, `{"msg":"ÜBER alles"}`), rec(2, t0, `{"msg":"uber"}`)}
	eq(t, seqs(run(t, recs, s, Query{Search: "ÜBER"})), []int{1})
	eq(t, seqs(run(t, recs, s, Query{Search: "über"})), []int{1})
}

func TestJSONCellIsExactText(t *testing.T) {
	s := sch(schema.Field{Path: "ids", Type: schema.TJSON})
	recs := []record.Record{rec(1, t0, "", "ids", []any{json.Number("1763400000123456789"), "x"})}
	b, _ := json.Marshal(run(t, recs, s, Query{Columns: []string{"ids"}}).Rows[0].Cells)
	if string(b) != `[{"v":"[1763400000123456789,\"x\"]"}]` {
		t.Errorf("cells = %s", b)
	}
}

func TestSearchRaw(t *testing.T) {
	s := sch(schema.Field{Path: "x", Type: schema.TString})
	recs := []record.Record{rec(1, t0, `{"msg":"Hello World"}`), rec(2, t0, `{"msg":"bye"}`)}
	eq(t, seqs(run(t, recs, s, Query{Search: "hello w"})), []int{1})
}

func TestSortDefaultNewestFirst(t *testing.T) {
	s := sch(schema.Field{Path: "n", Type: schema.TInt})
	recs := []record.Record{
		rec(1, t0, "", "n", json.Number("2")),
		rec(2, t0.Add(time.Second), "", "n", json.Number("1")),
		rec(3, t0, "", "n", "zz"),
		rec(4, t0.Add(-time.Second), ""),
	}
	eq(t, seqs(run(t, recs, s, Query{})), []int{2, 3, 1, 4})
	eq(t, seqs(run(t, recs, s, Query{Sort: Sort{Field: "timestamp"}})), []int{4, 1, 3, 2})
	// by a typed column: bad/empty last in both directions
	eq(t, seqs(run(t, recs, s, Query{Sort: Sort{Field: "n"}})), []int{2, 1, 3, 4})
}

func TestLimitTruncated(t *testing.T) {
	s := sch(schema.Field{Path: "x", Type: schema.TString})
	var recs []record.Record
	for i := 1; i <= 5; i++ {
		recs = append(recs, rec(i, t0.Add(time.Duration(i)*time.Second), ""))
	}
	res := run(t, recs, s, Query{Limit: 2})
	if len(res.Rows) != 2 || !res.Truncated || res.Matched != 5 || res.StreamTotal != 5 {
		t.Errorf("%d rows trunc=%v matched=%d total=%d", len(res.Rows), res.Truncated, res.Matched, res.StreamTotal)
	}
	if res := run(t, recs, s, Query{}); res.Truncated || len(res.Rows) != 5 {
		t.Error("default limit truncated")
	}
}

func TestOffsetPages(t *testing.T) {
	s := sch(schema.Field{Path: "x", Type: schema.TString})
	var recs []record.Record
	for i := 1; i <= 5; i++ {
		recs = append(recs, rec(i, t0.Add(time.Duration(i)*time.Second), ""))
	}
	// Newest first: 5 4 | 3 2 | 1.
	for _, c := range []struct {
		offset int
		want   []int
		trunc  bool
	}{{0, []int{5, 4}, true}, {2, []int{3, 2}, true}, {4, []int{1}, false}, {5, []int{}, false}, {9, []int{}, false}} {
		res := run(t, recs, s, Query{Limit: 2, Offset: c.offset})
		eq(t, seqs(res), c.want)
		if res.Truncated != c.trunc || res.Matched != 5 {
			t.Errorf("offset %d: trunc=%v matched=%d", c.offset, res.Truncated, res.Matched)
		}
	}
}

func TestDefaultLimit(t *testing.T) {
	s := sch(schema.Field{Path: "x", Type: schema.TString})
	var recs []record.Record
	for i := 1; i <= DefaultLimit+1; i++ {
		recs = append(recs, rec(i, t0.Add(time.Duration(i)*time.Second), ""))
	}
	if res := run(t, recs, s, Query{}); len(res.Rows) != 100 || !res.Truncated {
		t.Errorf("default: %d rows trunc=%v, want 100 and truncated", len(res.Rows), res.Truncated)
	}
}

func TestOffsetNegative(t *testing.T) {
	if err := (Query{Offset: -1}).Validate(sch()); err == nil {
		t.Error("offset -1 accepted")
	}
}

func TestFiltersAnd(t *testing.T) {
	s := sch(schema.Field{Path: "a", Type: schema.TInt}, schema.Field{Path: "b", Type: schema.TString})
	recs := []record.Record{
		rec(1, t0, "", "a", json.Number("1"), "b", "x"),
		rec(2, t0, "", "a", json.Number("9"), "b", "x"),
		rec(3, t0, "", "a", json.Number("9"), "b", "y"),
	}
	q := Query{Filters: []Filter{{Field: "a", Op: ">", Values: []string{"5"}}, {Field: "b", Op: "=", Values: []string{"x"}}}}
	eq(t, seqs(run(t, recs, s, q)), []int{2})
}

func TestAggregateFieldsIgnoredBySamples(t *testing.T) {
	s := sch(schema.Field{Path: "x", Type: schema.TString})
	recs := []record.Record{rec(1, t0, ""), rec(2, t0.Add(time.Second), "")}
	base := run(t, recs, s, Query{})
	q := Query{GroupBy: []string{"x"}, Bucket: "1m", View: "samples"}
	got := run(t, recs, s, q)
	eq(t, seqs(got), seqs(base))
}

func TestColumnsAndCells(t *testing.T) {
	s := sch(
		schema.Field{Path: "d", Type: schema.TDuration, Label: "Latency"},
		schema.Field{Path: "at", Type: schema.TTimestamp, Unit: schema.UnitMs},
		schema.Field{Path: "ok", Type: schema.TBool},
		schema.Field{Path: "f", Type: schema.TFloat},
	)
	recs := []record.Record{rec(1, t0, "", "d", "50ms", "at", json.Number("1763400000123"), "ok", true, "f", json.Number("1.5"))}
	res := run(t, recs, s, Query{Columns: []string{"d", "at", "ok", "f", "ghost"}})
	if res.Columns[0].Label != "Latency" || res.Columns[4].Type != schema.TString || res.Columns[4].Label != "ghost" {
		t.Errorf("columns = %+v", res.Columns)
	}
	c := res.Rows[0].Cells
	if c[0].V != 0.05 || c[1].V != "2025-11-17T17:20:00.123Z" || c[2].V != true || c[3].V != 1.5 || c[4].V != nil {
		t.Errorf("cells = %+v", c)
	}
}

func TestEndToEndPino(t *testing.T) {
	in := config.Default().Input
	f, err := os.Open("../../testdata/web/pino.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var recs []record.Record
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if r, ok := record.Normalize(adapter.New(in).ParseLine(append([]byte(nil), sc.Bytes()...)), in); ok {
			r.Seq = len(recs) + 1
			recs = append(recs, r)
		}
	}
	s := schema.Infer("*", "", "db", recs)
	q := Query{Stream: "db", Range: Range{From: "all"}, Filters: []Filter{{Field: "latency", Op: ">", Values: []string{"50ms"}}}}
	res := run(t, recs, s, q)
	if res.Matched != 1 {
		t.Errorf("matched %d of %d", res.Matched, res.StreamTotal)
	}
}

func TestRun50kUnder500ms(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	s := sch(
		schema.Field{Path: "n", Type: schema.TInt}, schema.Field{Path: "op", Type: schema.TString},
		schema.Field{Path: "lat", Type: schema.TDuration},
	)
	ops := []string{"SELECT", "UPDATE", "INSERT", "DELETE"}
	recs := make([]record.Record, 50000)
	for i := range recs {
		recs[i] = rec(i+1, t0.Add(-time.Duration(i)*time.Second),
			fmt.Sprintf(`{"n":%d,"op":"%s","msg":"some log line number %d with text"}`, i, ops[i%4], i),
			"n", json.Number(fmt.Sprint(i)), "op", ops[i%4], "lat", fmt.Sprintf("%dms", i%100))
	}
	q := Query{
		Range:   Range{From: "all"},
		Search:  "line number",
		Filters: []Filter{{Field: "op", Op: "in", Values: []string{"SELECT", "UPDATE"}}, {Field: "lat", Op: ">", Values: []string{"10ms"}}, {Field: "n", Op: ">=", Values: []string{"5"}}},
		Sort:    Sort{Field: "lat", Desc: true},
	}
	// Time the query alone, not loading the records into DuckDB.
	b := openKeptBackend(t, recs)
	defer b.Close()
	start := time.Now()
	res, err := RunSQL(context.Background(), b, s, q, t0, t0.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 500*time.Millisecond && !raceEnabled {
		t.Errorf("took %v", d)
	}
	if res.Matched == 0 {
		t.Error("nothing matched")
	}
}

func TestRunFiltersBySource(t *testing.T) {
	recs := []record.Record{
		{Seq: 1, Time: t0, Stream: "db", Source: "log", Fields: map[string]any{"a": "x"}},
		{Seq: 2, Time: t0, Stream: "db", Source: "otel", Fields: map[string]any{"b": "y"}},
	}
	s := schema.Infer("*", "otel", "db", recs)
	res := run(t, recs, s, Query{Stream: "db", Source: "otel", Range: Range{From: "all"}})
	if res.StreamTotal != 1 || res.Matched != 1 || res.Rows[0].Seq != 2 {
		t.Fatalf("otel: %+v", res)
	}
	s = schema.Infer("*", "", "db", recs)
	res = run(t, recs, s, Query{Stream: "db", Range: Range{From: "all"}})
	if res.StreamTotal != 1 || res.Matched != 1 || res.Rows[0].Seq != 1 {
		t.Fatalf("log: %+v", res)
	}
	err := Query{Stream: "db", Source: "x", Range: Range{From: "all"}}.Validate(s)
	if err == nil || err.Error() != `unknown source "x"` {
		t.Fatalf("validate bad source: %v", err)
	}
}

func TestRunServiceFilter(t *testing.T) {
	a := rec(1, t0, "x")
	a.Service = "a"
	b := rec(2, t0, "x")
	b.Service = "b"
	recs := []record.Record{a, b}
	s := sch(schema.Field{Path: "x", Type: schema.TString})
	for svc, want := range map[string][]int{"a": {1}, "b": {2}, "*": {1, 2}, "": {1, 2}} {
		got := seqs(run(t, recs, s, Query{Service: svc}))
		sort.Ints(got)
		eq(t, got, want)
	}
}

func TestRunIntervalColumns(t *testing.T) {
	old := rec(1, t0.Add(-time.Hour), "x", "foo", "1")
	in := rec(2, t0.Add(-time.Minute), "x", "bar", "1")
	recs := []record.Record{old, in}
	s := schema.Infer("*", "", "a", recs)
	q := Query{Range: Range{From: "10m"}}
	res := run(t, recs, s, q)
	var got []string
	for _, c := range res.Columns {
		got = append(got, c.Path)
	}
	if !reflect.DeepEqual(got, []string{"bar"}) {
		t.Fatalf("interval columns = %v, want [bar]", got)
	}
	q.Columns = []string{"foo"}
	res = run(t, recs, s, q)
	if len(res.Columns) != 1 || res.Columns[0].Path != "foo" {
		t.Fatalf("explicit columns = %+v", res.Columns)
	}
}

// A string in a sql-visualized column carries its colour tokens; others don't.
func TestRunSQLTokens(t *testing.T) {
	recs := []record.Record{rec(1, t0.Add(-time.Minute), "x", "q", "select 1", "n", "select 2")}
	s := sch(schema.Field{Path: "q", Type: schema.TString, Visualizer: schema.VSQL}, schema.Field{Path: "n", Type: schema.TString, Visualizer: schema.VRaw})
	res := run(t, recs, s, Query{Stream: "a", Columns: []string{"q", "n"}})
	q, n := res.Rows[0].Cells[0], res.Rows[0].Cells[1]
	if len(q.SQL) == 0 || q.SQL[0].Class != "kw" || q.SQL[0].Text != "select" {
		t.Fatalf("sql cell tokens = %+v", q.SQL)
	}
	if n.SQL != nil {
		t.Fatalf("raw cell has tokens %+v", n.SQL)
	}
}

// A query that matches nothing still has the stream's default columns, so
// the table keeps its headings.
func TestRunNoMatchKeepsDefaultColumns(t *testing.T) {
	recs := []record.Record{rec(1, t0.Add(-time.Minute), "x", "op", "SELECT", "msg", "hi")}
	s := sch(schema.Field{Path: "op", Type: schema.TString}, schema.Field{Path: "msg", Type: schema.TString})
	res := run(t, recs, s, Query{Stream: "a", Filters: []Filter{{Field: "op", Op: "empty"}}})
	if len(res.Rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(res.Rows))
	}
	var got []string
	for _, c := range res.Columns {
		got = append(got, c.Path)
	}
	if fmt.Sprint(got) != fmt.Sprint(s.Columns) {
		t.Fatalf("columns = %v, want the stream's %v", got, s.Columns)
	}
}

func TestRunSpanRoot(t *testing.T) {
	s := sch()
	span := func(seq int, kv ...any) record.Record {
		r := rec(seq, t0, "", kv...)
		r.Source = record.SourceOtel
		return r
	}
	recs := []record.Record{
		span(1, "name", "GET /orders", "parent_span_id", "a400000000000001"),
		span(2, "name", "GET /orders"),
		span(3, "name", "GET /orders", "parent_span_id", ""),
	}
	logRes := run(t, []record.Record{rec(4, t0, "", "name", "GET /orders")}, s, Query{})
	if len(logRes.Rows) != 1 || logRes.Rows[0].SpanRoot {
		t.Errorf("log row SpanRoot = %+v", logRes.Rows)
	}
	res := run(t, recs, s, Query{Source: record.SourceOtel, Sort: Sort{Field: "timestamp"}})
	got := map[int]bool{}
	for _, r := range res.Rows {
		got[r.Seq] = r.SpanRoot
	}
	want := map[int]bool{1: false, 2: true, 3: true}
	for sq, w := range want {
		if got[sq] != w {
			t.Errorf("seq %d SpanRoot = %v, want %v", sq, got[sq], w)
		}
	}
}

func TestRunAllStreams(t *testing.T) {
	mk := func(seq int, src, stream string) record.Record {
		r := rec(seq, t0, "x")
		r.Source, r.Stream = src, stream
		return r
	}
	recs := []record.Record{
		mk(1, record.SourceOtel, "db"), mk(2, record.SourceOtel, "http"),
		mk(3, record.SourceOtel, "GET /x"), mk(4, record.SourceLog, "log"),
	}
	res := run(t, recs, sch(), Query{Source: "otel", Stream: "*"})
	if res.Matched != 3 || res.StreamTotal != 3 {
		t.Errorf("otel * matched %d of %d, want 3 of 3", res.Matched, res.StreamTotal)
	}
	res = run(t, recs, sch(), Query{Source: "log", Stream: "*"})
	if res.Matched != 1 {
		t.Errorf("log * matched %d, want 1", res.Matched)
	}
}

// An empty stream has no columns to show; the page maps over them, so they
// must encode as [], never null.
func TestRunEmptySamplesColumnsNotNull(t *testing.T) {
	res := run(t, nil, schema.Schema{}, Query{Stream: "*", Source: "otel", Range: Range{From: "all"}})
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"columns":[]`) {
		t.Fatalf("columns not []: %s", b)
	}
}
