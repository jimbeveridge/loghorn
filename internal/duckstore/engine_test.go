package duckstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/adapter"
	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
)

// logRec normalizes one log line of stream with extra JSON members, the way
// the recorder does.
func logRec(t *testing.T, svc, stream, extra string) record.Record {
	t.Helper()
	line := fmt.Sprintf(`{"timestamp":"2026-11-23T10:00:00.123456789Z","severity":"INFO","message":"m","labels":{"schema":%q},"service":{"name":%q}%s}`, stream, svc, extra)
	r, ok := record.Normalize(adapter.New(config.Input{}).ParseLine([]byte(line)), config.Input{})
	if !ok {
		t.Fatalf("not a record: %s", line)
	}
	if r.Service == "" {
		r.Service = svc
	}
	return r
}

type rig struct {
	t   *testing.T
	e   *Engine
	seq int
}

func newRig(t *testing.T) *rig {
	t.Helper()
	e, err := OpenMemory(config.Input{}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return &rig{t: t, e: e}
}

func (g *rig) add(rs ...record.Record) []record.Record {
	for i := range rs {
		g.seq++
		rs[i].Seq = g.seq
		g.e.Add(rs[i])
	}
	if !g.e.Sync(5 * time.Second) {
		g.t.Fatal("sync timed out")
	}
	return rs
}

func (g *rig) one(q string, args ...any) any {
	g.t.Helper()
	var v any
	if err := g.e.DB().QueryRow(q, args...).Scan(&v); err != nil {
		g.t.Fatalf("%s: %v", q, err)
	}
	return v
}

// relation runs Relation for one field and returns (value, bad) per row in
// _id order.
func (g *rig) relation(src, svc, stream string, f schema.Field) (vals []any, bad []bool) {
	g.t.Helper()
	q, args := g.e.Relation(src, svc, stream, []schema.Field{f})
	rows, err := g.e.DB().Query("SELECT v0, x0 FROM ("+q+") ORDER BY _id", args...)
	if err != nil {
		g.t.Fatalf("%v\n%s", err, q)
	}
	defer rows.Close()
	for rows.Next() {
		var v any
		var x bool
		if err := rows.Scan(&v, &x); err != nil {
			g.t.Fatal(err)
		}
		vals, bad = append(vals, v), append(bad, x)
	}
	return vals, bad
}

func TestRecordsRoundTrip(t *testing.T) {
	g := newRig(t)
	in := g.add(
		logRec(t, "s1", "api", `,"requestId":7,"tags":["a","b"]`),
		logRec(t, "s1", "worker", `,"latency":"250ms"`),
		logRec(t, "s2", "api", `,"requestId":8.5`),
	)
	got, err := g.e.Records(context.Background(), "true")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(in) {
		t.Fatalf("got %d records, want %d", len(got), len(in))
	}
	for i := range in {
		w := in[i]
		if !reflect.DeepEqual(got[i], w) {
			t.Errorf("record %d:\n got %+v\nwant %+v", i, got[i], w)
		}
	}
	if n := g.one(`SELECT count(*) FROM all_records`); n != int64(3) {
		t.Errorf("all_records = %v", n)
	}
}

// TestTallies: a path's tally counts the records of the source, service
// and stream that hold it (nulls included), and its kinds give the type:
// int with float is float, a number with a string is a string.
func TestTallies(t *testing.T) {
	g := newRig(t)
	g.add(
		logRec(t, "s1", "api", `,"n":7`),
		logRec(t, "s1", "api", `,"n":7.5,"x":null`),
		logRec(t, "s2", "api", `,"n":"seven"`),
		logRec(t, "s1", "web", `,"w":1`),
		spanRec(t, `{"trace_id":"0af7651916cd43dd8448eb211c80319c","span_id":"a000000000000002","name":"chat","start":"2026-11-17T17:01:01.000Z","end":"2026-11-17T17:01:02.500Z","duration_ms":1500,"service.name":"s1"}`),
	)
	type tally struct {
		count int
		typ   schema.Type
	}
	none := tally{}
	for _, c := range []struct {
		src, svc, stream string
		want             map[string]tally
	}{
		{record.SourceLog, "*", "api", map[string]tally{"n": {3, schema.TString}, "x": {1, schema.TString}, "severity": {3, schema.TString}, "w": none, "duration_ms": none}},
		{record.SourceLog, "", "api", map[string]tally{"n": {3, schema.TString}}},
		{record.SourceLog, "s1", "api", map[string]tally{"n": {2, schema.TFloat}, "x": {1, schema.TString}}},
		{record.SourceLog, "s2", "api", map[string]tally{"n": {1, schema.TString}, "x": none}},
		{record.SourceLog, "*", record.AllStreams, map[string]tally{"n": {3, schema.TString}, "w": {1, schema.TInt}, "severity": {4, schema.TString}, "duration_ms": none}},
		{record.SourceOtel, "*", record.AllStreams, map[string]tally{"duration_ms": {1, schema.TInt}, "n": none, "w": none}},
	} {
		tl, err := g.e.Tallies(c.src, c.svc, c.stream)
		if err != nil {
			t.Fatal(err)
		}
		sch := schema.FromTallies(c.src, c.stream, tl)
		for path, w := range c.want {
			f, ok := sch.Field(path)
			got := tally{}
			if ok {
				got = tally{f.Count, f.Type}
			}
			if got != w {
				t.Errorf("%s %s %s: %s = %+v, want %+v", c.src, c.svc, c.stream, path, got, w)
			}
		}
	}
}

// TestNumbersAsTextAreCanonical: read as text, a number gives its JSON
// form, which can differ from the line's spelling (1.50 reads "1.5").
func TestNumbersAsTextAreCanonical(t *testing.T) {
	g := newRig(t)
	g.add(logRec(t, "s", "api", `,"v":5`), logRec(t, "s", "api", `,"v":1.50`), logRec(t, "s", "api", `,"v":"x"`))
	vals, _ := g.relation(record.SourceLog, "s", "api", schema.Field{Path: "v", Type: schema.TString})
	want := []any{"5", "1.5", "x"}
	if !reflect.DeepEqual(vals, want) {
		t.Errorf("values %v, want %v", vals, want)
	}
}

// TestRawIsLossless: a line that is not valid UTF-8 is kept byte for byte.
func TestRawIsLossless(t *testing.T) {
	g := newRig(t)
	r := logRec(t, "s", "api", ``)
	r.Raw = append([]byte(`{"labels":{"schema":"api"},"m":"`), 0xff, 0x00, '"', '}')
	g.add(r)
	var raw []byte
	if err := g.e.DB().QueryRow(`SELECT _raw FROM all_records`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, r.Raw) {
		t.Errorf("_raw = %q, want %q", raw, r.Raw)
	}
}

// TestUntimedRecordIsStored: a record with no time keeps its day in _id.
func TestUntimedRecordIsStored(t *testing.T) {
	g := newRig(t)
	r := logRec(t, "s", "api", ``)
	r.Time, r.Seq = time.Time{}, 5<<32+1
	g.e.Add(r)
	g.e.Sync(5 * time.Second)
	if v := g.one(`SELECT _time IS NULL FROM all_records`); v != true {
		t.Errorf("_time IS NULL = %v", v)
	}
	recs, err := g.e.Records(context.Background(), "true")
	if err != nil || len(recs) != 1 {
		t.Fatalf("records %v, %v", recs, err)
	}
	if recs[0].Seq != 5<<32+1 || !recs[0].Time.IsZero() {
		t.Errorf("got seq %d time %v", recs[0].Seq, recs[0].Time)
	}
}

func spanRec(t *testing.T, line string) record.Record {
	t.Helper()
	r, ok := record.NormalizeSpan([]byte(line))
	if !ok {
		t.Fatalf("not a span: %s", line)
	}
	return r
}

// TestCommonColumns: the fixed columns are filled from the fields.
func TestCommonColumns(t *testing.T) {
	g := newRig(t)
	g.add(
		spanRec(t, `{"trace_id":"0AF7651916CD43DD8448EB211C80319C","span_id":"A000000000000002","parent_span_id":"a000000000000001","name":"chat","kind":"client","start":"2026-11-17T17:01:01.000Z","end":"2026-11-17T17:01:02.500Z","duration_ms":1500,"status":"error","service.name":"svc"}`),
		logRec(t, "s", "api", `,"trace_id":"0af7651916cd43dd8448eb211c80319c","span_id":"a000000000000002"`),
	)
	type row struct {
		trace, span, parent, sev, msg sql.NullString
		dur                           sql.NullFloat64
	}
	read := func(src string) row {
		var r row
		if err := g.e.DB().QueryRow(`SELECT trace_id, span_id, parent_span_id, severity, message, duration_ms FROM all_records WHERE _source = ?`, src).
			Scan(&r.trace, &r.span, &r.parent, &r.sev, &r.msg, &r.dur); err != nil {
			t.Fatal(err)
		}
		return r
	}
	sp := read(record.SourceOtel)
	if sp.trace.String != "0af7651916cd43dd8448eb211c80319c" || sp.span.String != "a000000000000002" || sp.parent.String != "a000000000000001" ||
		sp.sev.String != "ERROR" || sp.msg.String != "chat" || sp.dur.Float64 != 1500 {
		t.Errorf("span row %+v", sp)
	}
	lg := read(record.SourceLog)
	if lg.trace.String != "0af7651916cd43dd8448eb211c80319c" || lg.span.String != "a000000000000002" || lg.parent.Valid ||
		lg.sev.String != "INFO" || lg.msg.String != "m" || lg.dur.Valid {
		t.Errorf("log row %+v", lg)
	}
}

func TestBadValueIsNullAndFlagged(t *testing.T) {
	g := newRig(t)
	g.add(logRec(t, "s", "api", `,"receiveTimestamp":"soon"`), logRec(t, "s", "api", ``))
	vals, bad := g.relation(record.SourceLog, "s", "api", schema.Field{Path: "receiveTimestamp", Type: schema.TTimestamp})
	if vals[0] != nil || !bad[0] {
		t.Errorf("bad value: got (%v, %v), want (nil, true)", vals[0], bad[0])
	}
	if vals[1] != nil || bad[1] {
		t.Errorf("missing value: got (%v, %v), want (nil, false)", vals[1], bad[1])
	}
	recs, err := g.e.Records(context.Background(), "true")
	if err != nil {
		t.Fatal(err)
	}
	if raw := string(recs[0].Raw); !strings.Contains(raw, `"soon"`) {
		t.Errorf("_raw lost the value: %s", raw)
	}
}

func TestNamesDifferingInCaseStaySeparate(t *testing.T) {
	g := newRig(t)
	g.add(
		logRec(t, "s", "api", `,"userId":1,"a.b":"dot"`),
		logRec(t, "s", "api", `,"userID":2`),
		logRec(t, "S", "API", `,"userId":3`),
	)
	for _, c := range []struct {
		svc, stream, path string
		want              []any
	}{
		{"s", "api", "userId", []any{int64(1), nil}},
		{"s", "api", "userID", []any{nil, int64(2)}},
		{"s", "api", "a.b", []any{"dot", nil}},
		{"S", "API", "userId", []any{int64(3)}},
	} {
		vals, _ := g.relation(record.SourceLog, c.svc, c.stream, schema.Field{Path: c.path, Type: schema.TInt})
		if c.path == "a.b" {
			vals, _ = g.relation(record.SourceLog, c.svc, c.stream, schema.Field{Path: c.path, Type: schema.TString})
		}
		if !reflect.DeepEqual(vals, c.want) {
			t.Errorf("%s/%s %s = %v, want %v", c.svc, c.stream, c.path, vals, c.want)
		}
	}
}

// TestRelationConvertsLikeConvert checks every stored type against every
// effective type: the value and the bad flag must be what schema.Convert gives
// the value as received, except the known differences (D-011 #1).
func TestRelationConvertsLikeConvert(t *testing.T) {
	g := newRig(t)
	stored := map[string]string{ // service -> JSON value of f
		"int": `42`, "neg": `-7`, "float": `1.5`, "intfloat": `3.0`, "str": `"12"`, "strf": `" 2.5 "`,
		"word": `"hello"`, "bool": `true`, "strbool": `"T"`, "ts": `"2026-11-23T10:00:00.5Z"`,
		"dur": `"1m30s"`, "json": `[1,"a"]`, "epoch": `1700000000123`,
		// Go's TrimSpace also trims tabs, newlines and Unicode spaces.
		"tabint": `"\t12\n"`, "tabfloat": `"\r\n2.5\t"`, "tabbool": `"\ttrue\n"`, "tabdur": `"\t1m30s\n"`,
		"tabts": `"\n2026-11-23T10:00:00.5Z\t"`, "nbspint": `" 12　"`,
		// Past BIGINT, spelled unusually, empty, nested, and false.
		"tsns": `"2026-11-23T10:00:00.123456789Z"`, "tsoff": `"2026-11-23T12:00:00.123456789+02:00"`,
		"big": `18446744073709551615`, "exp": `1e3`, "zeros": `1.50`, "null": `null`, "empty": `""`,
		"obj": `[{"a":"<b>"}]`, "false": `false`,
		// Too large for a float: Go marks it bad, DuckDB reads infinity.
		"huge": `1e400`, "nhuge": `-1e400`, "hugestr": `"1e400"`,
	}
	for svc, v := range stored {
		g.add(logRec(t, svc, "conv", `,"f":`+v))
	}
	types := []schema.Type{schema.TInt, schema.TFloat, schema.TBool, schema.TString, schema.TDuration, schema.TTimestamp, schema.TJSON}
	for svc, v := range stored {
		var raw any
		dec := json.NewDecoder(strings.NewReader(v))
		dec.UseNumber()
		if err := dec.Decode(&raw); err != nil {
			t.Fatal(err)
		}
		stype := schema.TallyType("f", schema.Classify(raw))
		for _, et := range types {
			if knownDifference(stype, et, raw) {
				continue
			}
			f := schema.Field{Path: "f", Type: et}
			want := schema.Convert(raw, f)
			vals, bad := g.relation(record.SourceLog, svc, "conv", f)
			if len(vals) != 1 {
				t.Fatalf("%s: %d rows", svc, len(vals))
			}
			if bad[0] != want.Bad {
				t.Errorf("%s (%s) as %s: bad = %v, want %v", svc, stype, et, bad[0], want.Bad)
				continue
			}
			if want.Bad || want.Empty {
				continue
			}
			if got, w := vals[0], expect(want, et); !reflect.DeepEqual(got, w) {
				t.Errorf("%s (%s) as %s: got %#v, want %#v", svc, stype, et, got, w)
			}
		}
	}
}

// knownDifference lists the values whose text DuckDB's JSON re-spells when
// read as text: a number with an exponent or trailing zeros gives its
// canonical form (TestNumbersAsTextAreCanonical), and an array keeps <, >
// and & where Go's encoder escapes them. The JSON means the same.
func knownDifference(stored, eff schema.Type, raw any) bool {
	if eff != schema.TString && eff != schema.TJSON {
		return false
	}
	switch x := raw.(type) {
	case json.Number:
		s := x.String()
		return strings.ContainsAny(s, "eE") || (strings.Contains(s, ".") && strings.HasSuffix(s, "0") && !strings.HasSuffix(s, ".0"))
	case []any:
		b, _ := json.Marshal(x)
		return strings.Contains(string(b), `\u00`)
	}
	return false
}

func expect(v schema.Value, t schema.Type) any {
	switch t {
	case schema.TInt:
		return v.Int
	case schema.TFloat, schema.TDuration:
		return v.Num
	case schema.TBool:
		return v.Bool
	case schema.TTimestamp:
		return v.Time.UTC()
	}
	return v.Str
}

// TestMemoryEngineSpillsToItsOwnTempDir: an in-memory engine (loghorn
// open, serve's fallback) spills large sorts to a folder of its own under
// the system temp folder, not to .tmp in whatever directory the process
// started in; Close removes it (review §4).
func TestMemoryEngineSpillsToItsOwnTempDir(t *testing.T) {
	e, err := OpenMemory(config.Input{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var dir string
	if err := e.DB().QueryRow("SELECT current_setting('temp_directory')").Scan(&dir); err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(dir) || !strings.HasPrefix(dir, filepath.Clean(os.TempDir())) {
		t.Fatalf("temp_directory %q, want a folder under %s", dir, os.TempDir())
	}
	e.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temp folder left after Close: %v", err)
	}
}
