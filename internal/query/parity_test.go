package query

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/adapter"
	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/duckstore"
	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
)

func BenchmarkRunSQLSmall(b *testing.B) {
	t := &testing.T{}
	recs := parityRecords(t)
	e, err := duckstore.OpenMemory(config.Input{}, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer e.Close()
	for _, r := range recs {
		e.Add(r)
	}
	e.Sync(time.Minute)
	be := backend{e}
	tl, _ := e.Tallies("log", "*", "api")
	sch := schema.FromTallies("log", "api", tl)
	now := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	for _, q := range []Query{
		{Stream: "api", Source: "log"},
		{Stream: "api", Source: "log", View: "table", GroupBy: []string{"severity"}},
	} {
		b.Run(q.View, func(b *testing.B) {
			for b.Loop() {
				if _, err := RunSQL(context.Background(), be, sch, q, now, time.Time{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	b.Run("count", func(b *testing.B) {
		for b.Loop() {
			var n int
			e.DB().QueryRow("SELECT count(*) FROM all_records").Scan(&n)
		}
	})
}

// TestRunSQLTimestampUnitOverride: a saved epoch unit on a numeric
// timestamp field changes how its numbers are read, as in schema.Convert:
// seconds, microseconds, nanoseconds, or milliseconds when none is saved.
func TestRunSQLTimestampUnitOverride(t *testing.T) {
	in := config.Input{}
	p := adapter.New(in)
	var recs []record.Record
	var epochs []int64
	for i := 0; i < 6; i++ {
		n := int64(1700000000 + i*86400*10)
		l := fmt.Sprintf(`{"timestamp":"2026-11-23T10:00:0%dZ","severity":"INFO","message":"m","labels":{"schema":"api"},"receiveTimestamp":%d}`, i, n)
		r, ok := record.Normalize(p.ParseLine([]byte(l)), in)
		if !ok {
			t.Fatal(l)
		}
		r.Seq, r.Service = i+1, "s"
		recs = append(recs, r)
		epochs = append(epochs, n)
	}
	b := newBackend(t, recs)
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	cut := time.Date(2023, 12, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		unit string
		at   func(n int64) time.Time
	}{
		{"s", func(n int64) time.Time { return time.Unix(n, 0) }},
		{"us", time.UnixMicro},
		{"ns", func(n int64) time.Time { return time.Unix(0, n) }},
		{"ms", time.UnixMilli},
		{"", time.UnixMilli},
	} {
		// The written answers: seq i+1 holds epochs[i], read in c.unit.
		var after []int // records at or after the cut, newest record first
		for i := len(epochs) - 1; i >= 0; i-- {
			if !c.at(epochs[i]).Before(cut) {
				after = append(after, i+1)
			}
		}
		// One group per value, each of count 1, so ordered by the value's
		// text: the tie after count is the group key.
		var keys []string
		for _, n := range epochs {
			keys = append(keys, c.at(n).UTC().Format(time.RFC3339Nano))
		}
		sort.Strings(keys)
		var groups []string
		for _, k := range keys {
			groups = append(groups, fmt.Sprint([]any{k, 1}))
		}
		sch := schema.Effective(b.schema(t, "*", "log", "api"), []schema.Field{{Path: "receiveTimestamp", Type: schema.TTimestamp, Unit: c.unit}})
		ask := func(q Query) Result {
			t.Helper()
			res, err := RunSQL(context.Background(), b, sch, q, now, time.Time{})
			if err != nil {
				t.Fatalf("unit %q: %v", c.unit, err)
			}
			return res
		}
		res := ask(Query{Stream: "api", Filters: []Filter{{Field: "receiveTimestamp", Op: ">=", Values: []string{"2023-12-01T00:00:00Z"}}}})
		if fmt.Sprint(seqs(res)) != fmt.Sprint(after) && !(len(after) == 0 && len(res.Rows) == 0) {
			t.Errorf("unit %q filter: %v, want %v", c.unit, seqs(res), after)
		}
		res = ask(Query{Stream: "api", Sort: Sort{Field: "receiveTimestamp", Desc: true}})
		if got := fmt.Sprint(seqs(res)); got != "[6 5 4 3 2 1]" {
			t.Errorf("unit %q sort: %v", c.unit, got)
		}
		res = ask(Query{Stream: "api", View: "table", GroupBy: []string{"receiveTimestamp"}})
		var got []string
		for i := range res.Rows {
			got = append(got, fmt.Sprint(cellsOf(res, i)))
		}
		if fmt.Sprint(got) != fmt.Sprint(groups) {
			t.Errorf("unit %q groups:\n got %v\nwant %v", c.unit, got, groups)
		}
	}
}

// TestErrorRateTrimsLikeGo: error_rate counts a severity with tabs,
// newlines or Unicode spaces around it (strings.TrimSpace), not only
// spaces (review §2.4). Ingest normalizes severity, so the records here
// carry the text directly: three of the four are errors.
func TestErrorRateTrimsLikeGo(t *testing.T) {
	var recs []record.Record
	for _, r := range parityRecords(t) {
		if r.Key().Source == record.SourceLog && r.Stream == "api" && len(recs) < 4 {
			r.Fields["severity"] = []string{"ERROR\n", "\tfatal", "ERROR\u00a0", "INFO"}[len(recs)]
			recs = append(recs, r)
		}
	}
	b := newBackend(t, recs)
	sch := b.schema(t, "*", "log", "api")
	q := Query{Stream: "api", Source: "log", View: "table", Aggregates: []Aggregate{{Op: "error_rate"}}}
	now := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	res, err := RunSQL(context.Background(), b, sch, q, now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || fmt.Sprint(cellsOf(res, 0)) != "[4 0.75]" {
		t.Fatalf("error_rate rows = %+v, want one row [4 0.75]", res.Rows)
	}
}

// TestSearchFoldsUnicodeCase: search matches case-insensitively by Unicode
// rules (DuckDB's lower()), not only ASCII: "istanbul" finds "İSTANBUL" and
// "ÜNÏCODE" finds "ünïcode". Run folds only ASCII for an ASCII term, so
// these are written expected values, not Run's answers (D-032, review §2.3).
func TestSearchFoldsUnicodeCase(t *testing.T) {
	var recs []record.Record
	for _, r := range parityRecords(t) {
		if r.Key().Source == record.SourceLog && r.Stream == "api" {
			recs = append(recs, r)
		}
	}
	b := newBackend(t, recs)
	sch := b.schema(t, "*", "log", "api")
	now := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	count := func(search, text string) (got, want int) {
		for _, r := range recs {
			if strings.Contains(string(r.Raw), text) {
				want++
			}
		}
		res, err := RunSQL(context.Background(), b, sch, Query{Stream: "api", Source: "log", Search: search, Limit: 1000}, now, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		return len(res.Rows), want
	}
	for _, c := range []struct{ search, text string }{{"istanbul", "İSTANBUL"}, {"ÜNÏCODE", "ünïcode"}, {"ERROR HERE", "Error here"}} {
		if got, want := count(c.search, c.text); got != want || want == 0 {
			t.Errorf("search %q: %d records, want the %d holding %q", c.search, got, want, c.text)
		}
	}
}
