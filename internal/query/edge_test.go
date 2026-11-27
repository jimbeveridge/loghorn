package query

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/adapter"
	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/ingest"
	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
)

// parityDurs are the string durations of parityLines' field d, by i%9.
var parityDurs = []string{`"12ms"`, `"1.5s"`, `"250us"`, `"2m"`, `"5us"`, `"3ns"`, `"9ms"`, `"0.1s0.2s"`, `"1h2m3.5s"`}

// parityLines is the generated data set: two services, two log streams,
// mixed and clashing types, untimed records, case-clashing and dotted
// names, and spans with parents, events and every duration shape. Times
// cross the 2026-03-08 US daylight-saving change.
func parityLines() []string {
	base := time.Date(2026, 3, 7, 20, 0, 0, 0, time.UTC)
	var out []string
	sevs := []string{"INFO", "WARNING", "ERROR", "error ", "DEBUG", "CRITICAL", "ERROR\n", "\tfatal", "WARNING\u00a0"}
	// Durations whose float64 seconds a per-unit factor rounds differently
	// from Go's whole nanoseconds (review §2.5).
	durs := parityDurs
	for i := 0; i < 160; i++ {
		svc := []string{"alpha", "beta"}[i%2]
		stream := "api"
		if i%3 == 0 {
			stream = "worker"
		}
		var b strings.Builder
		fmt.Fprintf(&b, `{"severity":%q,"message":"msg %d %s","labels":{"schema":%q},"service":{"name":%q}`, sevs[i%len(sevs)], i%9, []string{"Alpha", "ünïcode", "Error here", "İSTANBUL"}[i%4], stream, svc)
		if i%17 != 5 && i != 0 {
			fmt.Fprintf(&b, `,"timestamp":%q`, base.Add(time.Duration(i)*23*time.Minute+time.Duration(i%7)*time.Millisecond).Format(time.RFC3339Nano))
		}
		if i%11 != 0 {
			fmt.Fprintf(&b, `,"n":%d`, i%7)
		}
		if svc == "alpha" && i%37 == 2 {
			fmt.Fprintf(&b, `,"f":1e400,"mixed":%d`, i%5) // not a float64: bad, as in Go (review §2.2)
		} else if svc == "alpha" {
			fmt.Fprintf(&b, `,"f":%v,"mixed":%d`, float64(i)*0.25+0.5, i%5)
		} else {
			fmt.Fprintf(&b, `,"f":%d,"mixed":"m%d"`, i%4, i%3)
		}
		fmt.Fprintf(&b, `,"b":%v,"d":%s,"latency":%d`, i%2 == 0, durs[i%len(durs)], i*3%50)
		if i%4 == 0 {
			fmt.Fprintf(&b, `,"tags":["t%d","x"]`, i%3)
		}
		if i%5 == 0 {
			fmt.Fprintf(&b, `,"userId":%d`, i)
		} else if i%5 == 1 {
			fmt.Fprintf(&b, `,"userID":"u%d"`, i)
		}
		if i%6 == 0 {
			fmt.Fprintf(&b, `,"a.b":"dot%d","empty":"","nul":null`, i%2)
		}
		if i%13 == 0 {
			b.WriteString(`,"receiveTimestamp":"not a time"`)
		} else if i%13 == 1 {
			fmt.Fprintf(&b, `,"receiveTimestamp":%q`, base.Add(time.Duration(i)*time.Hour).Format(time.RFC3339))
		}
		if i%10 == 3 {
			fmt.Fprintf(&b, `,"trace":"projects/p/traces/%032x","spanId":"%016x"`, i%4+1, i)
		}
		b.WriteString("}")
		out = append(out, b.String())
	}
	durations := []string{`0.5`, `12`, `0`, `-1`, `4800.25`, `33`, `1`, `100`, ``, `"9ms"`}
	for i := 0; i < 80; i++ {
		trace := i%10 + 1
		start := base.Add(time.Duration(i) * 41 * time.Minute)
		name, kind, attrs := "GET /a", "server", `{"http.request.method":"GET","http.route":"/a"}`
		switch i % 4 {
		case 1:
			name, kind, attrs = "db query", "client", fmt.Sprintf(`{"db.system":"postgresql","db.statement":"select %d"}`, i%3)
		case 2:
			name, kind, attrs = "work item", "internal", `{"job":"x"}`
		case 3:
			name, kind, attrs = "GET /a", "internal", `{}`
		}
		parent := ""
		if i%4 != 0 {
			parent = fmt.Sprintf(`,"parent_span_id":"%016x"`, (i/4)*4+1)
		}
		dur := ""
		if d := durations[i%len(durations)]; d != "" {
			dur = `,"duration_ms":` + d
		}
		svc := []string{"shop", "pay"}[i%3%2]
		events := ""
		if i%7 == 0 {
			events = `,"events":[{"name":"retry"}]`
		}
		out = append(out, fmt.Sprintf(`{"trace_id":"%032x","span_id":"%016x","name":%q,"kind":%q,"start":%q,"end":%q%s%s,"status":%q,"attributes":%s,"service.name":%q%s}`,
			trace, i+1, name, kind, start.Format(time.RFC3339Nano), start.Add(time.Second).Format(time.RFC3339Nano), parent, dur,
			[]string{"ok", "error"}[i%5/4], attrs, svc, events))
	}
	return out
}

// parityRecords normalizes the fixtures and the generated lines and
// numbers them the way the store does: Seq from 1, the default service,
// and inherited times (an untimed leading record takes the next time).
func parityRecords(t *testing.T) []record.Record {
	t.Helper()
	var raw [][]byte
	paths, _ := filepath.Glob("../../testdata/web/*")
	sort.Strings(paths)
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		ingest.Stored(f, func(l []byte) { raw = append(raw, bytes.Clone(l)) })
		f.Close()
	}
	for _, l := range parityLines() {
		raw = append(raw, []byte(l))
	}
	return normalizeLines(t, raw)
}

// edgeLines are three records of service gamma, appended to parityLines:
// a time past DuckDB's TIMESTAMP_NS range (stored as NULL), numbers whose
// sum overflows float64, an overflowing and a negative duration, and an
// untimed record after them.
var edgeLines = []string{
	`{"timestamp":"3000-01-01T00:00:00Z","severity":"INFO","message":"far future","labels":{"schema":"api"},"service":{"name":"gamma"},"f":1.5e308,"d":"9999999h","n":1}`,
	`{"timestamp":"2026-03-09T00:00:00Z","severity":"INFO","message":"huge","labels":{"schema":"api"},"service":{"name":"gamma"},"f":1.5e308,"d":"-2.5s","n":2}`,
	`{"severity":"INFO","message":"after","labels":{"schema":"api"},"service":{"name":"gamma"},"f":-1e308,"d":"0","n":3}`,
}

// edgeRecords is parityLines, then edgeLines, without the fixtures. Log
// line i is seq i+1, span j is seq 161+j, and the gamma records are seqs
// 241 to 243.
func edgeRecords(t *testing.T) []record.Record {
	t.Helper()
	var raw [][]byte
	for _, l := range append(parityLines(), edgeLines...) {
		raw = append(raw, []byte(l))
	}
	recs := normalizeLines(t, raw)
	if len(recs) != 243 {
		t.Fatalf("%d records, want 243", len(recs))
	}
	for i, r := range recs {
		if r.Seq != i+1 || (i < 160) != (r.Key().Source == record.SourceLog && r.Service != "gamma") {
			t.Fatalf("record %d: seq %d source %s service %s", i, r.Seq, r.Key().Source, r.Service)
		}
	}
	return recs
}

func normalizeLines(t *testing.T, raw [][]byte) []record.Record {
	t.Helper()
	in := config.Input{}
	p := adapter.New(in)
	var recs []record.Record
	var pending []int
	for _, l := range raw {
		r, ok := record.Normalize(p.ParseLine(l), in)
		if !ok {
			continue
		}
		r.Seq = len(recs) + 1
		if r.Service == "" {
			r.Service = record.DefaultService
		}
		if r.Time.IsZero() {
			r.TimeInherited = true
			if len(recs) > 0 {
				r.Time = recs[len(recs)-1].Time
			}
		}
		if r.Time.IsZero() {
			pending = append(pending, len(recs))
		} else {
			for _, i := range pending {
				recs[i].Time = r.Time
			}
			pending = nil
		}
		recs = append(recs, r)
	}
	return recs
}

// The edge tests below hold written expected values for the edge classes
// the parity data covers. Each is worked out from the generator in
// parityLines (log line i is seq i+1: service alpha when i is even, else
// beta; stream worker when i%3 == 0, else api; span j is seq 161+j) and
// the Go standard library, never from another query engine.

var edgeNow = time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC)

type edgeEnv struct {
	t    *testing.T
	recs []record.Record
	b    backend
}

func newEdgeEnv(t *testing.T) *edgeEnv {
	recs := edgeRecords(t)
	return &edgeEnv{t, recs, newBackend(t, recs)}
}

func (e *edgeEnv) ask(s schema.Schema, q Query) Result {
	e.t.Helper()
	if q.Limit == 0 {
		q.Limit = MaxLimit
	}
	res, err := RunSQL(context.Background(), e.b, s, q, edgeNow, time.Time{})
	if err != nil {
		e.t.Fatalf("%+v: %v", q, err)
	}
	return res
}

// logSeqs is the seqs of log lines i (0 to 159) that keep holds for, then
// the gamma seqs given.
func logSeqs(keep func(i int) bool, gamma ...int) []int {
	var out []int
	for i := 0; i < 160; i++ {
		if keep(i) {
			out = append(out, i+1)
		}
	}
	return append(out, gamma...)
}

// newestFirst orders seqs as the samples view does by default: by time,
// newest first, then by seq, highest first; seq 241's time is stored as
// NULL, so it comes last.
func (e *edgeEnv) newestFirst(seqs []int) []int {
	out := append([]int(nil), seqs...)
	sort.SliceStable(out, func(a, b int) bool {
		x, y := out[a], out[b]
		if (x == 241) != (y == 241) {
			return y == 241
		}
		tx, ty := e.recs[x-1].Time, e.recs[y-1].Time
		if !tx.Equal(ty) {
			return tx.After(ty)
		}
		return x > y
	})
	return out
}

func alpha(i int) bool { return i%2 == 0 }
func api(i int) bool   { return i%3 != 0 }

// round15 is v to 15 significant digits, as aggregates are shown.
func round15(v float64) float64 {
	r, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'g', 15, 64), 64)
	return r
}

func with(s schema.Schema, fs ...schema.Field) schema.Schema {
	return schema.Effective(s, fs)
}

// (a) A time outside TIMESTAMP_NS is stored as NULL: the record is still
// returned, with a zero time, its text intact, last when newest come first
// and first when oldest do.
func TestEdgeOutOfRangeTime(t *testing.T) {
	e := newEdgeEnv(t)
	g := e.b.schema(t, "gamma", "log", "api")
	res := e.ask(g, Query{Source: "log", Service: "gamma", Stream: "api"})
	if got := fmt.Sprint(seqs(res)); got != "[243 242 241]" || res.Matched != 3 || res.StreamTotal != 3 {
		t.Fatalf("newest first: %v matched %d of %d", got, res.Matched, res.StreamTotal)
	}
	if !res.Rows[2].Time.IsZero() || res.Rows[2].TimeInherited {
		t.Errorf("seq 241 time %v inherited %v, want zero", res.Rows[2].Time, res.Rows[2].TimeInherited)
	}
	if !res.Rows[0].TimeInherited || !res.Rows[0].Time.Equal(time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("seq 243 time %v inherited %v, want seq 242's", res.Rows[0].Time, res.Rows[0].TimeInherited)
	}
	if got := fmt.Sprint(res.Rows[2].Cells[0].V); res.Columns[0].Path != "timestamp" || got != "3000-01-01T00:00:00Z" {
		t.Errorf("seq 241 %s cell %q", res.Columns[0].Path, got)
	}
	res = e.ask(g, Query{Source: "log", Service: "gamma", Stream: "api", Sort: Sort{Field: "timestamp"}})
	if got := fmt.Sprint(seqs(res)); got != "[241 242 243]" {
		t.Errorf("oldest first: %v", got)
	}
}

// (b) A number past float64 (1e400) is bad and left out of every metric;
// a sum that overflows float64 shows no value, as does an average built on
// it, while min and max still show the extremes.
func TestEdgeHugeNumbers(t *testing.T) {
	e := newEdgeEnv(t)
	aggs := []Aggregate{{Op: "sum", Field: "f"}, {Op: "avg", Field: "f"}, {Op: "min", Field: "f"}, {Op: "max", Field: "f"}}

	// alpha's api lines hold f = i*0.25+0.5, except 1e400 at i = 2 and 76.
	var sum, lo, hi float64
	n, count := 0, 0
	lo, hi = math.Inf(1), math.Inf(-1)
	for i := 0; i < 160; i++ {
		if !alpha(i) || !api(i) {
			continue
		}
		count++
		if i%37 == 2 {
			continue
		}
		v := float64(i)*0.25 + 0.5
		sum += v
		n++
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if count != 53 || n != 51 || sum != 1059.5 || lo != 1.5 || hi != 40 {
		t.Fatalf("worked values: count %d n %d sum %v min %v max %v", count, n, sum, lo, hi)
	}
	res := e.ask(e.b.schema(t, "alpha", "log", "api"), Query{Source: "log", Service: "alpha", Stream: "api", View: "table", Aggregates: aggs})
	want := fmt.Sprint([]any{count, sum, round15(sum / float64(n)), lo, hi})
	if len(res.Rows) != 1 || fmt.Sprint(cellsOf(res, 0)) != want {
		t.Errorf("alpha: %+v, want one row %s", res.Rows, want)
	}

	// gamma: 1.5e308 + 1.5e308 - 1e308 overflows in any order.
	res = e.ask(e.b.schema(t, "gamma", "log", "api"), Query{Source: "log", Service: "gamma", Stream: "api", View: "table", Aggregates: aggs})
	if len(res.Rows) != 1 || fmt.Sprint(cellsOf(res, 0)) != "[3 <nil> <nil> -1e+308 1.5e+308]" {
		t.Errorf("gamma: %+v", res.Rows)
	}
}

// (c) String durations read as a saved duration: fractional ("1.5s"),
// multi-part ("0.1s0.2s" is exactly 300ms, "1h2m3.5s"), negative, a bare
// "0", and one that overflows time.Duration ("9999999h", bad).
func TestEdgeStringDurations(t *testing.T) {
	e := newEdgeEnv(t)
	s := with(e.b.schema(t, "*", "log", "api"), schema.Field{Path: "d", Type: schema.TDuration})
	secs := map[int]float64{} // seq -> d in seconds, as time.ParseDuration reads it
	for i := 0; i < 160; i++ {
		if api(i) {
			d, err := time.ParseDuration(strings.Trim(parityDurs[i%9], `"`))
			if err != nil {
				t.Fatal(err)
			}
			secs[i+1] = d.Seconds()
		}
	}
	secs[242], secs[243] = -2.5, 0
	if _, err := time.ParseDuration("9999999h"); err == nil {
		t.Fatal("9999999h should overflow")
	}
	where := func(keep func(v float64) bool) []int {
		var out []int
		for seq, v := range secs {
			if keep(v) {
				out = append(out, seq)
			}
		}
		return e.newestFirst(out)
	}
	for _, c := range []struct {
		op   string
		vals []string
		want []int
	}{
		{">", []string{"1s"}, where(func(v float64) bool { return v > 1 })},
		{"=", []string{"300ms"}, where(func(v float64) bool { return v == 0.3 })},
		{"<=", []string{"0s"}, []int{243, 242}},
		{"<", []string{"10us"}, where(func(v float64) bool { return v < 10e-6 })},
		{"not-empty", nil, e.newestFirst(append(where(func(float64) bool { return true }), 241))},
		{"empty", nil, nil},
	} {
		res := e.ask(s, Query{Source: "log", Service: "*", Stream: "api", Filters: []Filter{{Field: "d", Op: c.op, Values: c.vals}}})
		if fmt.Sprint(seqs(res)) != fmt.Sprint(c.want) {
			t.Errorf("d %s %v:\n got %v\nwant %v", c.op, c.vals, seqs(res), c.want)
		}
	}
	if n := len(where(func(v float64) bool { return v == 0.3 })); n != 17 {
		t.Errorf("%d records of 0.1s0.2s, want 17", n)
	}

	// Sorted by value, then seq in the same direction; the bad one last.
	var asc []int
	for seq := range secs {
		asc = append(asc, seq)
	}
	sort.Slice(asc, func(a, b int) bool {
		if secs[asc[a]] != secs[asc[b]] {
			return secs[asc[a]] < secs[asc[b]]
		}
		return asc[a] < asc[b]
	})
	desc := make([]int, len(asc))
	for i, seq := range asc {
		desc[len(asc)-1-i] = seq
	}
	for _, c := range []struct {
		desc bool
		want []int
	}{{false, append(asc, 241)}, {true, append(desc, 241)}} {
		res := e.ask(s, Query{Source: "log", Service: "*", Stream: "api", Sort: Sort{Field: "d", Desc: c.desc}})
		if fmt.Sprint(seqs(res)) != fmt.Sprint(c.want) {
			t.Errorf("sort desc=%v:\n got %v\nwant %v", c.desc, seqs(res), c.want)
		}
	}
}

// (d) Buckets of 6h and 1d count from local midnight, and a bucket ends
// where the next starts: across the America/New_York change on 2026-03-08
// the 6h bucket from 23:00Z runs 5h to the next local midnight (04:00Z),
// and the day of 2026-03-08 is 23h long.
func TestEdgeGraphAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	old := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = old })
	e := newEdgeEnv(t)
	s := e.b.schema(t, "*", "log", record.AllStreams)
	at := func(day, hour int) time.Time { return time.Date(2026, 3, day, hour, 0, 0, 0, time.UTC) }
	for _, c := range []struct {
		bucket, from, to string
		starts           []time.Time
	}{
		{"6h", "2026-03-08T00:00:00Z", "2026-03-09T12:00:00Z", []time.Time{at(7, 23), at(8, 5), at(8, 11), at(8, 17), at(8, 23), at(9, 4), at(9, 10)}},
		{"1d", "2026-03-07T12:00:00Z", "2026-03-10T00:00:00Z", []time.Time{at(7, 5), at(8, 5), at(9, 4)}},
	} {
		from, _ := time.Parse(time.RFC3339, c.from)
		to, _ := time.Parse(time.RFC3339, c.to)
		counts := make([]int, len(c.starts))
		total := 0
		for _, r := range e.recs {
			if r.Key().Source != record.SourceLog || r.Seq == 241 || r.Time.Before(from) || r.Time.After(to) {
				continue
			}
			total++
			for k := len(c.starts) - 1; k >= 0; k-- {
				if !r.Time.Before(c.starts[k]) {
					counts[k]++
					break
				}
			}
		}
		res := e.ask(s, Query{Source: "log", Service: "*", Stream: record.AllStreams, View: "graph", Bucket: c.bucket, Range: Range{From: c.from, To: c.to}})
		g := res.Graph
		if g == nil || len(g.Starts) != len(c.starts) || len(g.Series) != 1 {
			t.Fatalf("%s: graph %+v", c.bucket, g)
		}
		for k := range c.starts {
			if !g.Starts[k].Equal(c.starts[k]) {
				t.Errorf("%s: start %d is %v, want %v", c.bucket, k, g.Starts[k].UTC(), c.starts[k])
			}
		}
		var got []string
		for _, v := range g.Series[0].Values {
			if v == nil {
				got = append(got, "nil")
			} else {
				got = append(got, fmt.Sprint(*v))
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(counts) || g.Series[0].Count != total || res.Matched != total {
			t.Errorf("%s: values %v count %d matched %d, want %v and %d", c.bucket, got, g.Series[0].Count, res.Matched, counts, total)
		}
	}
}

// (e) mixed holds numbers in alpha (i%5) and strings in beta ("m" and
// i%3), and is missing in gamma. Read as a string the numbers are their
// text; read as a number the strings are bad, which only not-empty
// matches; a missing value matches empty, != and not-in.
func TestEdgeMixedTypes(t *testing.T) {
	e := newEdgeEnv(t)
	base := e.b.schema(t, "*", "log", "api")
	if f, _ := base.Field("mixed"); f.Type != schema.TString {
		t.Fatalf("mixed inferred as %s, want string", f.Type)
	}
	gamma := []int{241, 242, 243}
	for _, c := range []struct {
		typ   schema.Type
		op    string
		vals  []string
		keep  func(i int) bool
		gamma bool
	}{
		{schema.TString, "=", []string{"3"}, func(i int) bool { return alpha(i) && i%5 == 3 }, false},
		{schema.TString, "=", []string{"m1"}, func(i int) bool { return !alpha(i) && i%3 == 1 }, false},
		{schema.TString, "!=", []string{"m1"}, func(i int) bool { return !(!alpha(i) && i%3 == 1) }, true},
		{schema.TString, "in", []string{"m2", "4"}, func(i int) bool { return (!alpha(i) && i%3 == 2) || (alpha(i) && i%5 == 4) }, false},
		{schema.TInt, "=", []string{"3"}, func(i int) bool { return alpha(i) && i%5 == 3 }, false},
		{schema.TInt, ">=", []string{"3"}, func(i int) bool { return alpha(i) && i%5 >= 3 }, false},
		{schema.TInt, "!=", []string{"3"}, func(i int) bool { return alpha(i) && i%5 != 3 }, true},
		{schema.TInt, "not-empty", nil, func(i int) bool { return true }, false},
		{schema.TInt, "empty", nil, func(i int) bool { return false }, true},
		{schema.TFloat, "<", []string{"1.5"}, func(i int) bool { return alpha(i) && i%5 <= 1 }, false},
	} {
		s := with(base, schema.Field{Path: "mixed", Type: c.typ})
		var g []int
		if c.gamma {
			g = gamma
		}
		want := e.newestFirst(logSeqs(func(i int) bool { return api(i) && c.keep(i) }, g...))
		res := e.ask(s, Query{Source: "log", Service: "*", Stream: "api", Filters: []Filter{{Field: "mixed", Op: c.op, Values: c.vals}}})
		if fmt.Sprint(seqs(res)) != fmt.Sprint(want) {
			t.Errorf("mixed as %s %s %v:\n got %v\nwant %v", c.typ, c.op, c.vals, seqs(res), want)
		}
	}
}

// (f) The two sets of saved types the parity test ran: a saved type wins
// over the inferred one for filters and metrics.
func TestEdgeSavedTypes(t *testing.T) {
	e := newEdgeEnv(t)
	logs := e.b.schema(t, "*", "log", "api")
	q := Query{Source: "log", Service: "*", Stream: "api"}
	filter := func(s schema.Schema, f Filter, want []int) {
		t.Helper()
		q := q
		q.Filters = []Filter{f}
		if res := e.ask(s, q); fmt.Sprint(seqs(res)) != fmt.Sprint(want) {
			t.Errorf("%+v:\n got %v\nwant %v", f, seqs(res), want)
		}
	}

	// Set 1: n as float, mixed as int (see TestEdgeMixedTypes), b as
	// string, latency (a whole number) as a duration in seconds.
	s1 := with(logs, schema.Field{Path: "n", Type: schema.TFloat}, schema.Field{Path: "mixed", Type: schema.TInt},
		schema.Field{Path: "b", Type: schema.TString}, schema.Field{Path: "latency", Type: schema.TDuration})
	sum, n := 1.0+2+3, 3 // gamma's n
	for i := 0; i < 160; i++ {
		if api(i) && i%11 != 0 {
			sum += float64(i % 7)
			n++
		}
	}
	agg := q
	agg.View, agg.Aggregates = "table", []Aggregate{{Op: "avg", Field: "n"}}
	res := e.ask(s1, agg)
	if want := fmt.Sprint([]any{109, round15(sum / float64(n))}); len(res.Rows) != 1 || fmt.Sprint(cellsOf(res, 0)) != want {
		t.Errorf("avg n: %+v, want %s", res.Rows, want)
	}
	filter(s1, Filter{Field: "latency", Op: ">", Values: []string{"30s"}}, e.newestFirst(logSeqs(func(i int) bool { return api(i) && i*3%50 > 30 })))
	filter(s1, Filter{Field: "b", Op: "=", Values: []string{"true"}}, e.newestFirst(logSeqs(func(i int) bool { return api(i) && i%2 == 0 })))

	// Set 2: f as string, message as JSON, duration_ms as int.
	s2 := with(logs, schema.Field{Path: "f", Type: schema.TString}, schema.Field{Path: "message", Type: schema.TJSON})
	filter(s2, Filter{Field: "message", Op: "contains", Values: []string{"error here"}}, e.newestFirst(logSeqs(func(i int) bool { return api(i) && i%4 == 2 })))
	// beta's f is i%4; alpha's f is 3 only at i = 10.
	filter(s2, Filter{Field: "f", Op: "=", Values: []string{"3"}}, e.newestFirst(logSeqs(func(i int) bool { return api(i) && (!alpha(i) && i%4 == 3 || i == 10) })))

	// duration_ms by j%10: 0.5, 12, 0, -1, 4800.25, 33, 1, 100, missing,
	// "9ms". As int, 0.5, 4800.25 and "9ms" are bad.
	spans := with(e.b.schema(t, "*", "otel", record.AllStreams), schema.Field{Path: "duration_ms", Type: schema.TInt})
	sq := Query{Source: "otel", Service: "*", Stream: record.AllStreams}
	var ge1 []int
	for j := 0; j < 80; j++ {
		if m := j % 10; m == 1 || m == 5 || m == 6 || m == 7 {
			ge1 = append(ge1, 161+j)
		}
	}
	sq.Filters = []Filter{{Field: "duration_ms", Op: ">=", Values: []string{"1"}}}
	if res := e.ask(spans, sq); fmt.Sprint(seqs(res)) != fmt.Sprint(e.newestFirst(ge1)) {
		t.Errorf("duration_ms >= 1:\n got %v\nwant %v", seqs(res), e.newestFirst(ge1))
	}
	// Ascending: the eight -1s (j%10 == 3), then the 0s (j%10 == 2).
	sq.Filters, sq.Sort, sq.Limit = nil, Sort{Field: "duration_ms"}, 12
	want := []int{164, 174, 184, 194, 204, 214, 224, 234, 163, 173, 183, 193}
	if res := e.ask(spans, sq); fmt.Sprint(seqs(res)) != fmt.Sprint(want) {
		t.Errorf("duration_ms ascending:\n got %v\nwant %v", seqs(res), want)
	}
}

// (g) A field missing from a record, null or "" is empty; a present one is
// not. a.b, empty and nul appear only on worker lines with i%6 == 0.
func TestEdgeMissingField(t *testing.T) {
	e := newEdgeEnv(t)
	s := e.b.schema(t, "*", "log", "worker")
	worker := func(keep func(i int) bool) []int {
		return e.newestFirst(logSeqs(func(i int) bool { return i%3 == 0 && keep(i) }))
	}
	all := func(int) bool { return true }
	none := func(int) bool { return false }
	for _, c := range []struct {
		field, op string
		vals      []string
		keep      func(i int) bool
	}{
		{"a.b", "not-empty", nil, func(i int) bool { return i%6 == 0 }},
		{"a.b", "empty", nil, func(i int) bool { return i%6 != 0 }},
		{"a.b", "=", []string{"dot0"}, func(i int) bool { return i%6 == 0 }},
		{"a.b", "!=", []string{"dot0"}, func(i int) bool { return i%6 != 0 }},
		{"empty", "empty", nil, all},
		{"empty", "not-empty", nil, none},
		{"nul", "empty", nil, all},
		{"nul", "not-empty", nil, none},
		{"userId", "not-empty", nil, func(i int) bool { return i%5 == 0 }},
		{"userId", "empty", nil, func(i int) bool { return i%5 != 0 }},
	} {
		res := e.ask(s, Query{Source: "log", Service: "*", Stream: "worker", Filters: []Filter{{Field: c.field, Op: c.op, Values: c.vals}}})
		if want := worker(c.keep); fmt.Sprint(seqs(res)) != fmt.Sprint(want) {
			t.Errorf("%s %s %v:\n got %v\nwant %v", c.field, c.op, c.vals, seqs(res), want)
		}
	}
}

// (h) The schemas DuckDB infers for two parity streams. Counts are how
// many records hold the path; types come from the kinds seen.
func TestEdgeInferredSchemas(t *testing.T) {
	e := newEdgeEnv(t)
	fld := func(path string, typ schema.Type, vis string, count int) schema.Field {
		return schema.Field{Path: path, Type: typ, Visualizer: vis, Inferred: typ, Count: count}
	}
	str, num, ts := schema.TString, schema.TInt, schema.TTimestamp
	for _, c := range []struct {
		svc, src, stream string
		want             schema.Schema
	}{
		// alpha's api lines: i even and i%3 != 0, 53 of them.
		{"alpha", "log", "api", schema.Schema{Stream: "api", Source: "log",
			Columns: []string{"timestamp", "severity", "message", "latency", "b", "d", "f", "mixed"},
			Fields: []schema.Field{
				fld("b", schema.TBool, "raw", 53), fld("d", schema.TDuration, "duration", 53), fld("f", schema.TFloat, "number", 53),
				fld("labels.schema", str, "raw", 53), fld("latency", num, "number", 53), fld("message", str, "raw", 53),
				fld("mixed", num, "number", 53), fld("n", num, "number", 48), fld("receiveTimestamp", ts, "local-time", 8),
				fld("service.name", str, "raw", 53), fld("severity", str, "severity", 53), fld("tags", schema.TJSON, "json", 26),
				fld("timestamp", ts, "local-time", 49), fld("userID", str, "raw", 10), fld("userId", num, "number", 10),
			}}},
		// shop's server spans: j%4 == 0 and j%3 != 1, 13 of them; 3 lack
		// duration_ms (j%10 == 8) and 2 have events (j%7 == 0).
		{"shop", "otel", "http.server", schema.Schema{Stream: "http.server", Source: "otel",
			Columns: []string{"timestamp", "name", "duration_ms", "severity", "span_id", "kind", "http.request.method", "http.route"},
			Fields: []schema.Field{
				fld("duration_ms", schema.TFloat, "number", 10), fld("end", ts, "local-time", 13), fld("events", schema.TJSON, "json", 2),
				fld("http.request.method", str, "raw", 13), fld("http.route", str, "raw", 13), fld("kind", str, "raw", 13),
				fld("name", str, "raw", 13), fld("service.name", str, "raw", 13), fld("severity", str, "severity", 13),
				fld("span.event_count", num, "number", 13), fld("span.type", str, "raw", 13), fld("span_id", str, "raw", 13),
				fld("start", ts, "local-time", 13), fld("status", str, "raw", 13), fld("timestamp", ts, "local-time", 13),
				fld("trace_id", str, "raw", 13),
			}}},
	} {
		got := e.b.schema(t, c.svc, c.src, c.stream)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %s %s:\n got %+v\nwant %+v", c.svc, c.src, c.stream, got, c.want)
		}
	}
}
