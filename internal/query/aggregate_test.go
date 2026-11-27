package query

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
)

var (
	routeF = schema.Field{Path: "route", Type: schema.TString, Visualizer: "text"}
	latF   = schema.Field{Path: "lat", Type: schema.TFloat, Visualizer: "number"}
)

func TestValidateExploreFields(t *testing.T) {
	s := sch(routeF, latF)
	bad := map[string]Query{
		"view":     {View: "pie"},
		"op":       {Aggregates: []Aggregate{{Op: "median", Field: "lat"}}},
		"nofield":  {Aggregates: []Aggregate{{Op: "sum"}}},
		"string":   {Aggregates: []Aggregate{{Op: "sum", Field: "route"}}},
		"bucket":   {Bucket: "7m"},
		"chart":    {Chart: "pie"},
		"plot":     {Plot: &Aggregate{Op: "sum", Field: "lat"}},
		"emptygrp": {GroupBy: []string{""}},
	}
	for name, q := range bad {
		if err := q.Validate(s); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	ok := Query{
		View: "graph", GroupBy: []string{"route"}, Bucket: "5m", Chart: "stacked",
		Aggregates: []Aggregate{{Op: "p95", Field: "lat"}},
		Plot:       &Aggregate{Op: "p95", Field: "lat"},
	}
	if err := ok.Validate(s); err != nil {
		t.Fatal(err)
	}
	ok.Plot = &Aggregate{Op: "count"}
	if err := ok.Validate(s); err != nil {
		t.Fatal(err)
	}
}

func cellsOf(r Result, i int) []any {
	var out []any
	for _, c := range r.Rows[i].Cells {
		out = append(out, c.V)
	}
	return out
}

func TestTableGroupsByCountDesc(t *testing.T) {
	s := sch(routeF)
	recs := []record.Record{rec(1, t0, "x", "route", "a"), rec(2, t0, "x", "route", "b"), rec(3, t0, "x", "route", "a"), rec(4, t0, "x", "route", "a")}
	res := run(t, recs, s, Query{View: "table", GroupBy: []string{"route"}})
	if res.View != "table" || res.Groups != 2 || len(res.Rows) != 2 {
		t.Fatalf("res = %+v", res)
	}
	if fmt.Sprint(cellsOf(res, 0)) != "[a 3]" || fmt.Sprint(cellsOf(res, 1)) != "[b 1]" {
		t.Errorf("rows = %v %v", cellsOf(res, 0), cellsOf(res, 1))
	}
	if !res.Columns[0].Group || res.Columns[0].Path != "route" || res.Columns[1].Path != "count" || res.Columns[1].Group {
		t.Errorf("columns = %+v", res.Columns)
	}
}

func TestTableNoGroupByIsTotals(t *testing.T) {
	s := sch(routeF)
	recs := []record.Record{rec(1, t0, "x", "route", "a"), rec(2, t0, "x", "route", "b")}
	res := run(t, recs, s, Query{View: "table"})
	if len(res.Rows) != 1 || res.Rows[0].Cells[0].V != int64(2) {
		t.Fatalf("rows = %+v", res.Rows)
	}
}

func TestTableEmptyGroupValue(t *testing.T) {
	s := sch(routeF)
	recs := []record.Record{rec(1, t0, "x", "route", "a"), rec(2, t0, "x")}
	res := run(t, recs, s, Query{View: "table", GroupBy: []string{"route"}})
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	var found bool
	for _, row := range res.Rows {
		if row.Cells[0].V == nil && row.Cells[1].V == int64(1) {
			found = true
		}
	}
	if !found {
		t.Errorf("empty group row missing: %+v", res.Rows)
	}
}

func TestTableRealEmptyLabelStaysSeparate(t *testing.T) {
	s := sch(routeF)
	recs := []record.Record{rec(1, t0, "x", "route", "(empty)"), rec(2, t0, "x")}
	res := run(t, recs, s, Query{View: "table", GroupBy: []string{"route"}})
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	var real, missing bool
	for _, row := range res.Rows {
		if row.Cells[0].V == "(empty)" {
			real = true
		}
		if row.Cells[0].V == nil {
			missing = true
		}
	}
	if !real || !missing {
		t.Errorf("real=%v missing=%v rows=%+v", real, missing, res.Rows)
	}
}

func latRecs() []record.Record {
	var recs []record.Record
	for i := 1; i <= 10; i++ {
		recs = append(recs, rec(i, t0, "x", "lat", json.Number(fmt.Sprint(i))))
	}
	return recs
}

func TestTablePercentileNearestRank(t *testing.T) {
	s := sch(latF)
	var aggs []Aggregate
	for _, op := range AggregateOps {
		if op != "error_rate" {
			aggs = append(aggs, Aggregate{Op: op, Field: "lat"})
		}
	}
	res := run(t, latRecs(), s, Query{View: "table", Aggregates: aggs})
	want := map[string]float64{"sum(lat)": 55, "avg(lat)": 5.5, "min(lat)": 1, "max(lat)": 10, "p50(lat)": 5, "p90(lat)": 9, "p95(lat)": 10, "p99(lat)": 10}
	for i, c := range res.Columns {
		if w, ok := want[c.Path]; ok {
			if got := res.Rows[0].Cells[i].V; got != w {
				t.Errorf("%s = %v want %v", c.Path, got, w)
			}
		}
	}
}

func TestTableDurationMetricKeepsType(t *testing.T) {
	d := schema.Field{Path: "dur", Type: schema.TDuration, Unit: "ms", Visualizer: "duration"}
	res := run(t, []record.Record{rec(1, t0, "x", "dur", "2s")}, sch(d), Query{View: "table", Aggregates: []Aggregate{{Op: "p95", Field: "dur"}}})
	c := res.Columns[1]
	if c.Type != schema.TDuration || c.Unit != "ms" {
		t.Errorf("column = %+v", c)
	}
}

func TestTableAvgOfIntIsFloat(t *testing.T) {
	n := schema.Field{Path: "n", Type: schema.TInt, Visualizer: "number"}
	res := run(t, []record.Record{rec(1, t0, "x", "n", json.Number("1")), rec(2, t0, "x", "n", json.Number("2"))}, sch(n),
		Query{View: "table", Aggregates: []Aggregate{{Op: "avg", Field: "n"}, {Op: "sum", Field: "n"}}})
	if res.Columns[1].Type != schema.TFloat || res.Rows[0].Cells[1].V != 1.5 {
		t.Errorf("avg = %+v %v", res.Columns[1], res.Rows[0].Cells[1])
	}
	if res.Columns[2].Type != schema.TInt || res.Rows[0].Cells[2].V != int64(3) {
		t.Errorf("sum = %+v %v", res.Columns[2], res.Rows[0].Cells[2])
	}
}

func TestTableMetricAllEmptyIsNull(t *testing.T) {
	s := sch(routeF, latF)
	recs := []record.Record{rec(1, t0, "x", "route", "a", "lat", json.Number("1")), rec(2, t0, "x", "route", "b"), rec(3, t0, "x", "route", "b", "lat", "oops")}
	res := run(t, recs, s, Query{View: "table", GroupBy: []string{"route"}, Aggregates: []Aggregate{{Op: "avg", Field: "lat"}, {Op: "p95", Field: "lat"}}})
	b := res.Rows[0] // b has count 2
	if b.Cells[0].V != "b" || b.Cells[2].V != nil || b.Cells[3].V != nil {
		t.Errorf("row b = %+v", b)
	}
	if _, err := json.Marshal(res); err != nil {
		t.Fatal(err)
	}
}

func TestTableLimitTruncatesGroups(t *testing.T) {
	s := sch(routeF)
	var recs []record.Record
	for i := 0; i < 5; i++ {
		recs = append(recs, rec(i, t0, "x", "route", fmt.Sprint("r", i)))
	}
	res := run(t, recs, s, Query{View: "table", GroupBy: []string{"route"}, Limit: 2})
	if len(res.Rows) != 2 || res.Groups != 5 || !res.Truncated {
		t.Errorf("rows=%d groups=%d truncated=%v", len(res.Rows), res.Groups, res.Truncated)
	}
}

func TestSamplesUnchangedWhenViewEmpty(t *testing.T) {
	res := run(t, []record.Record{rec(1, t0, "x", "route", "a")}, sch(routeF), Query{})
	if res.View != "samples" || len(res.Rows) != 1 || res.Rows[0].Seq != 1 {
		t.Errorf("res = %+v", res)
	}
}

// TestErrorRate: error_rate is the share of records whose severity is an
// error, in any case; a record with no severity counts as not an error.
func TestErrorRate(t *testing.T) {
	var recs []record.Record
	for i, sev := range []any{"ERROR", "error", "INFO", nil} {
		r := rec(i+1, t0, "x")
		if sev != nil {
			r.Fields["severity"] = sev
		}
		recs = append(recs, r)
	}
	res := run(t, recs, sch(), Query{View: "table", Aggregates: []Aggregate{{Op: "error_rate"}}})
	if len(res.Rows) != 1 || fmt.Sprint(cellsOf(res, 0)) != "[4 0.5]" {
		t.Errorf("error_rate rows = %+v, want one row [4 0.5]", res.Rows)
	}
	c := metricColumn(Aggregate{Op: "error_rate"}, sch())
	if c.Path != "error_rate" || c.Type != schema.TFloat {
		t.Errorf("column = %+v", c)
	}
}

func TestValidateErrorRate(t *testing.T) {
	s := sch(routeF, latF)
	er := Aggregate{Op: "error_rate"}
	if err := (Query{Aggregates: []Aggregate{{Op: "error_rate", Field: "lat"}}}).Validate(s); err == nil {
		t.Error("error_rate with a field: want error")
	}
	ok := Query{View: "table", Aggregates: []Aggregate{er}, Order: &Order{Op: "error_rate"}, Plot: &er}
	if err := ok.Validate(s); err != nil {
		t.Errorf("error_rate: %v", err)
	}
	if err := (Query{Aggregates: []Aggregate{{Op: "p90", Field: "route"}}}).Validate(s); err == nil {
		t.Error("p90 on a string field: want error")
	}
	if err := (Query{Order: &Order{Op: "error_rate"}}).Validate(s); err == nil {
		t.Error("order by error_rate without the aggregate: want error")
	}
}
