package query

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
)

// duckNow is the fixed now of the equivalence test; ToSQL's now() is
// replaced with it before the SQL runs.
var duckNow = time.Date(2026, 3, 9, 18, 0, 0, 0, time.UTC)

var duckLogSchema = schema.Schema{Stream: "app", Fields: []schema.Field{
	{Path: "code", Type: schema.TInt},
	{Path: "duration_ms", Type: schema.TFloat},
	{Path: "latency", Type: schema.TDuration},
	{Path: "method", Type: schema.TString},
	{Path: "ok", Type: schema.TBool},
	{Path: "severity", Type: schema.TString},
}}

var duckSpanSchema = schema.Schema{Stream: "http.server", Source: record.SourceOtel, Fields: []schema.Field{
	{Path: "duration_ms", Type: schema.TFloat},
	{Path: "name", Type: schema.TString},
}}

// duckRecords is a fixed record set with no bad values: logs every 53
// minutes from 2026-03-07 12:00 UTC (across the US daylight-saving change
// of March 8), two with no time, and spans of stream http.server.
func duckRecords() []record.Record {
	methods := []any{"GET", "POST", "PUT", nil, "get", "DELETE"}
	sevs := []any{"INFO", "error", "WARN", " Fatal\t", "DEBUG", nil, "critical"}
	codes := []any{json.Number("200"), json.Number("404"), json.Number("500"), json.Number("201")}
	base := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	var recs []record.Record
	seq := 0
	for i := 0; i < 64; i++ {
		seq++
		f := map[string]any{
			"method":   methods[i%len(methods)],
			"severity": sevs[i%len(sevs)],
			"code":     codes[(i/2)%len(codes)],
			"ok":       i%3 == 0,
			"latency":  fmt.Sprintf("%dms", (i*17)%40+1),
		}
		if i%11 != 0 {
			f["duration_ms"] = json.Number(strconv.FormatFloat(float64((i*37)%100)+0.25, 'f', -1, 64))
		}
		at := base.Add(time.Duration(i) * 53 * time.Minute).Add(time.Duration(i) * 1234 * time.Microsecond)
		if i == 5 || i == 40 {
			at = time.Time{}
		}
		recs = append(recs, record.Record{Seq: seq, Time: at, Stream: "app", Fields: f,
			Raw: []byte(fmt.Sprintf(`{"line":%d,"method":%q}`, i, methods[i%len(methods)]))})
	}
	names := []string{"GET /a", "GET /b", "POST /c"}
	for i := 0; i < 50; i++ {
		seq++
		d := math.Pow(10, float64((i*7)%30)/6-1) // 0.1ms to about 10s
		if i%9 == 0 {
			d = 0
		}
		f := map[string]any{
			"name":        names[i%len(names)],
			"duration_ms": json.Number(strconv.FormatFloat(d, 'g', -1, 64)),
		}
		at := base.Add(time.Duration(i) * 7 * time.Minute)
		recs = append(recs, record.Record{Seq: seq, Time: at, Stream: "http.server", Source: record.SourceOtel, Fields: f,
			Raw: []byte(fmt.Sprintf(`{"span":%d}`, i))})
	}
	return recs
}

// duckLoad writes the table model of recs: schema logs table "app" and
// schema spans table "http.server".
func duckLoad(t *testing.T, recs []record.Record) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("SET TimeZone='UTC';\nCREATE SCHEMA logs;\nCREATE SCHEMA spans;\nCREATE SCHEMA loghorn;\n")
	b.WriteString("CREATE MACRO loghorn.this_run_start() AS TIMESTAMPTZ '2026-03-07 12:00:00+00';\n")
	b.WriteString("CREATE MACRO loghorn.this_run_id() AS 'run-1';\n")
	for _, tbl := range []struct {
		sch, stream, src string
		s                schema.Schema
	}{
		{"logs", "app", record.SourceLog, duckLogSchema},
		{"spans", "http.server", record.SourceOtel, duckSpanSchema},
	} {
		id, _ := quoteIdent(tbl.stream)
		name := tbl.sch + "." + id
		cols := []string{"seq BIGINT", `"time" TIMESTAMPTZ`, "service VARCHAR", "raw VARCHAR"}
		for _, f := range tbl.s.Fields {
			typ, _ := schema.SQLType(f.Type)
			fid, _ := quoteIdent(f.Path)
			cols = append(cols, fid+" "+typ)
		}
		b.WriteString("CREATE TABLE " + name + " (" + strings.Join(cols, ", ") + ");\n")
		var rows []string
		for _, r := range recs {
			if r.Stream != tbl.stream || r.Key().Source != tbl.src {
				continue
			}
			tm := "NULL"
			if !r.Time.IsZero() {
				tm, _ = timestampLit(r.Time)
			}
			raw, _ := quoteString(string(r.Raw))
			vals := []string{strconv.Itoa(r.Seq), tm, "NULL", raw}
			for _, f := range tbl.s.Fields {
				vals = append(vals, duckValue(t, r.Fields[f.Path], f))
			}
			rows = append(rows, "("+strings.Join(vals, ", ")+")")
		}
		b.WriteString("INSERT INTO " + name + " VALUES\n" + strings.Join(rows, ",\n") + ";\n")
	}
	p := filepath.Join(t.TempDir(), "load.sql")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// duckValue is the table-model literal of one field value.
func duckValue(t *testing.T, raw any, f schema.Field) string {
	t.Helper()
	v := schema.Convert(raw, f)
	if v.Bad {
		t.Fatalf("bad value %v for %s", raw, f.Path)
	}
	if v.Empty {
		return "NULL"
	}
	var s string
	var err error
	switch f.Type {
	case schema.TInt:
		s = strconv.FormatInt(v.Int, 10)
	case schema.TFloat:
		s, err = doubleLit(v.Num)
	case schema.TDuration:
		s, err = doubleLit(Round15(v.Num * 1000))
	case schema.TBool:
		s = strconv.FormatBool(v.Bool)
	default:
		s, err = quoteString(v.Str)
	}
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// duckRun runs sql over the loaded model and returns its rows.
func duckRun(t *testing.T, duck, load, sql string) []map[string]any {
	t.Helper()
	now, _ := timestampLit(duckNow)
	sql = strings.ReplaceAll(sql, "now()", now)
	cmd := exec.Command(duck, "-json", "-c", ".read "+load, "-c", sql)
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "AI_AGENT=") { // DuckDB's agent mode changes the output
			env = append(env, e)
		}
	}
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || stderr.Len() > 0 {
		t.Fatalf("duckdb: %v\n%s\nSQL:\n%s", err, stderr.String(), sql)
	}
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	var rows []map[string]any
	if err := dec.Decode(&rows); err != nil {
		t.Fatalf("duckdb output: %v\n%s", err, out)
	}
	return rows
}

// duckNum is a JSON number of DuckDB's output.
func duckNum(t *testing.T, v any) float64 {
	t.Helper()
	n, ok := v.(json.Number)
	if !ok {
		t.Fatalf("%v (%T) is not a number", v, v)
	}
	f, err := n.Float64()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func duckNear(a, b float64) bool {
	return a == b || math.Abs(a-b) <= 1e-9*math.Max(math.Abs(a), math.Abs(b))
}

// duckBucket parses a TIMESTAMPTZ of DuckDB's output (time zone UTC).
func duckBucket(t *testing.T, v any) time.Time {
	t.Helper()
	s, _ := v.(string)
	tm, err := time.Parse("2006-01-02 15:04:05-07", s)
	if err != nil {
		t.Fatalf("bucket %v: %v", v, err)
	}
	return tm
}

func TestToSQLDuckDB(t *testing.T) {
	duck, err := exec.LookPath("duckdb")
	if err != nil {
		t.Skip("duckdb not on PATH")
	}
	recs := duckRecords()
	load := duckLoad(t, recs)
	for _, c := range duckCases() {
		t.Run(c.name, func(t *testing.T) {
			s := duckLogSchema
			if c.q.Source == record.SourceOtel {
				s = duckSpanSchema
			}
			res, err := runSQL(t, recs, s, c.q, duckNow, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			sql, err := ToSQL(c.q, s, &res)
			if err != nil {
				t.Fatal(err)
			}
			duckCheck(t, c.q, res, duckRun(t, duck, load, sql), sql)
		})
	}
}

// duckCheck compares the rows of a query's SQL with its RunSQL result.
func duckCheck(t *testing.T, q Query, res Result, rows []map[string]any, sql string) {
	t.Helper()
	switch q.View {
	case "table":
		duckCheckTable(t, res, rows, sql)
	case "graph":
		duckCheckGraph(t, res, rows, sql)
	case "heatmap":
		duckCheckHeatmap(t, res, rows, sql)
	}
}

type duckCase struct {
	name string
	q    Query
}

// duckCases are the queries the equivalence tests run.
func duckCases() []duckCase {
	all := Range{From: "all"}
	return []duckCase{
		{"table by method", Query{Stream: "app", Range: all, View: "table", GroupBy: []string{"method"},
			Aggregates: []Aggregate{{Op: "avg", Field: "duration_ms"}, {Op: "p95", Field: "duration_ms"}, {Op: "error_rate"}, {Op: "sum", Field: "latency"}, {Op: "sum", Field: "code"}},
			Top:        10}},
		{"table by code and ok, p50 ascending", Query{Stream: "app", Range: Range{From: "2026-03-07T20:00:00Z", To: "2026-03-09T06:00:00Z"}, View: "table",
			GroupBy: []string{"code", "ok"}, Search: "line", Filters: []Filter{{Field: "method", Op: "!=", Values: []string{"PUT"}}},
			Aggregates: []Aggregate{{Op: "p50", Field: "latency"}, {Op: "max", Field: "code"}, {Op: "min", Field: "duration_ms"}, {Op: "p99", Field: "duration_ms"}},
			Order:      &Order{Op: "p50", Field: "latency", Asc: true}, Top: 3}},
		{"table by error rate", Query{Stream: "app", Range: all, View: "table", GroupBy: []string{"method"},
			Aggregates: []Aggregate{{Op: "error_rate"}, {Op: "p90", Field: "code"}}, Order: &Order{Op: "error_rate"}, Limit: 4}},
		{"table no group-by", Query{Stream: "app", Range: Range{From: "30h"}, View: "table",
			Aggregates: []Aggregate{{Op: "avg", Field: "duration_ms"}, {Op: "error_rate"}, {Op: "avg", Field: "latency"}}}},
		{"table no match", Query{Stream: "app", Range: all, View: "table", Search: "nothing matches this",
			Aggregates: []Aggregate{{Op: "avg", Field: "duration_ms"}, {Op: "error_rate"}}}},
		{"graph top 2 avg", Query{Stream: "app", Range: all, View: "graph", GroupBy: []string{"method"}, Bucket: "15m",
			Aggregates: []Aggregate{{Op: "avg", Field: "duration_ms"}}, Plot: &Aggregate{Op: "avg", Field: "duration_ms"}, Top: 2}},
		{"graph auto count", Query{Stream: "app", Range: all, View: "graph"}},
		{"graph 6h by code", Query{Stream: "app", Range: all, View: "graph", GroupBy: []string{"code"}, Bucket: "6h",
			Aggregates: []Aggregate{{Op: "p90", Field: "latency"}}, Plot: &Aggregate{Op: "p90", Field: "latency"}, Order: &Order{Op: "p90", Field: "latency"}, Top: 3}},
		{"graph 1d error rate", Query{Stream: "app", Range: all, View: "graph", GroupBy: []string{"ok", "method"}, Bucket: "1d",
			Aggregates: []Aggregate{{Op: "error_rate"}}, Plot: &Aggregate{Op: "error_rate"}, Top: 4}},
		{"graph 1h sum", Query{Stream: "app", Range: Range{From: "30h"}, View: "graph", Bucket: "1h",
			Aggregates: []Aggregate{{Op: "sum", Field: "duration_ms"}}, Plot: &Aggregate{Op: "sum", Field: "duration_ms"}}},
		{"heatmap auto", Query{Stream: "http.server", Source: record.SourceOtel, Range: all, View: "heatmap"}},
		{"heatmap 1h filtered", Query{Stream: "http.server", Source: record.SourceOtel, Range: all, View: "heatmap", Bucket: "1h",
			Filters: []Filter{{Field: "name", Op: "contains", Values: []string{"get"}}}}},
	}
}

func duckCheckTable(t *testing.T, res Result, rows []map[string]any, sql string) {
	t.Helper()
	if len(rows) != len(res.Rows) {
		t.Fatalf("%d rows, RunSQL has %d\nSQL:\n%s\nrows: %v", len(rows), len(res.Rows), sql, rows)
	}
	for i, r := range res.Rows {
		for j, col := range res.Columns {
			got, ok := rows[i][col.Path]
			if !ok {
				t.Fatalf("row %d lacks column %s: %v", i, col.Path, rows[i])
			}
			want := r.Cells[j].V
			switch w := want.(type) {
			case nil:
				if got != nil {
					t.Errorf("row %d %s = %v, want NULL", i, col.Path, got)
				}
			case string:
				if got != w {
					t.Errorf("row %d %s = %v, want %q", i, col.Path, got, w)
				}
			case bool:
				if got != w {
					t.Errorf("row %d %s = %v, want %v", i, col.Path, got, w)
				}
			case int64:
				if g := duckNum(t, got); g != float64(w) {
					t.Errorf("row %d %s = %v, want %d", i, col.Path, got, w)
				}
			case float64:
				if col.Type == schema.TDuration {
					w = Round15(w * 1000) // the model's ms
				}
				if g := duckNum(t, got); !duckNear(g, w) {
					t.Errorf("row %d %s = %v, want %v", i, col.Path, g, w)
				}
			default:
				t.Fatalf("row %d %s: RunSQL cell %T", i, col.Path, want)
			}
		}
	}
	if t.Failed() {
		t.Logf("SQL:\n%s\nrows: %v", sql, rows)
	}
}

func duckCheckGraph(t *testing.T, res Result, rows []map[string]any, sql string) {
	t.Helper()
	g := res.Graph
	// Pivot the long output: series -> bucket -> value.
	wide := map[string]map[int64]any{}
	var order []string
	for _, r := range rows {
		s, _ := r["series"].(string)
		if wide[s] == nil {
			wide[s] = map[int64]any{}
			order = append(order, s)
		}
		wide[s][duckBucket(t, r["bucket"]).UnixMicro()] = r["value"]
	}
	if len(g.Series) == 0 && len(rows) > 0 {
		t.Fatalf("RunSQL has no series; SQL rows %v", rows)
	}
	seen := 0
	for _, se := range g.Series {
		col := wide[se.Label]
		for i, st := range g.Starts {
			got, ok := col[st.UnixMicro()]
			w := se.Values[i]
			switch {
			case w == nil:
				// No value: no row, or a row whose records have none.
				if ok {
					seen++
					if got != nil {
						t.Errorf("series %s bucket %s = %v, want none", se.Label, st, got)
					}
				}
			case !ok:
				if g.Metric.Path != "count" || *w != 0 {
					t.Errorf("series %s bucket %s missing, want %v", se.Label, st, *w)
				}
			default:
				seen++
				want := *w
				if g.Metric.Type == schema.TDuration {
					want = Round15(want * 1000)
				}
				if gv := duckNum(t, got); !duckNear(gv, want) {
					t.Errorf("series %s bucket %s = %v, want %v", se.Label, st, gv, want)
				}
			}
		}
	}
	if seen != len(rows) {
		t.Errorf("SQL has %d rows, RunSQL's series match %d of them", len(rows), seen)
	}
	if len(order) != len(g.Series) {
		t.Errorf("SQL series %v, RunSQL has %d", order, len(g.Series))
	}
	if t.Failed() {
		t.Logf("SQL:\n%s\nrows: %v", sql, rows)
	}
}

func duckCheckHeatmap(t *testing.T, res Result, rows []map[string]any, sql string) {
	t.Helper()
	h := res.Heatmap
	idx := map[int64]int{}
	for i, st := range h.Starts {
		idx[st.UnixMicro()] = i
	}
	total := 0
	for _, r := range rows {
		ti, ok := idx[duckBucket(t, r["bucket"]).UnixMicro()]
		if !ok {
			t.Fatalf("bucket %v is not one of RunSQL's", r["bucket"])
		}
		bi := int(duckNum(t, r["band"]))
		n := int(duckNum(t, r["count"]))
		total += n
		if n != h.Counts[ti][bi] {
			t.Errorf("cell %v band %d = %d, RunSQL has %d", r["bucket"], bi, n, h.Counts[ti][bi])
		}
		if bi == 0 {
			if r["lo"] != nil {
				t.Errorf("band 0 lo = %v, want NULL", r["lo"])
			}
		} else if lo := duckNum(t, r["lo"]); lo != h.Bands[bi].Lo {
			t.Errorf("band %d lo = %v, want %v", bi, lo, h.Bands[bi].Lo)
		}
		if hi := duckNum(t, r["hi"]); hi != h.Bands[bi].Hi {
			t.Errorf("band %d hi = %v, want %v", bi, hi, h.Bands[bi].Hi)
		}
	}
	if total != h.Total {
		t.Errorf("SQL counts %d spans, RunSQL %d", total, h.Total)
	}
	if t.Failed() {
		t.Logf("SQL:\n%s\nrows: %v", sql, rows)
	}
}
