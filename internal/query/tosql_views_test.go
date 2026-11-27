package query

import (
	"strings"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/schema"
)

// viewSchema is sqlSchema with a string severity, which error_rate reads.
var viewSchema = func() schema.Schema {
	s := sqlSchema
	s.Fields = nil
	for _, f := range sqlSchema.Fields {
		if f.Path == "time" {
			s.Fields = append(s.Fields, schema.Field{Path: "severity", Type: schema.TString})
		}
		s.Fields = append(s.Fields, f)
	}
	return s
}()

// viewBody runs ToSQL on viewSchema and returns what follows the header and
// the notes, which the caller checks apart.
func viewBody(t *testing.T, q Query, r *Result) (notes, body string) {
	t.Helper()
	got, err := ToSQL(q, viewSchema, r)
	if err != nil {
		t.Fatalf("ToSQL: %v", err)
	}
	if !strings.HasPrefix(got, SQLHeader) {
		t.Fatalf("no header:\n%s", got)
	}
	got = strings.TrimPrefix(got, SQLHeader)
	i := 0
	for strings.HasPrefix(got[i:], "--") {
		i += strings.IndexByte(got[i:], '\n') + 1
	}
	return got[:i], got[i:]
}

func wantBody(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestToSQLTable(t *testing.T) {
	q := Query{Stream: "app", View: "table", GroupBy: []string{"message"},
		Aggregates: []Aggregate{{Op: "avg", Field: "duration_ms"}, {Op: "p95", Field: "duration_ms"}, {Op: "error_rate"}},
		Order:      &Order{Op: "count"}, Top: 10}
	notes, body := viewBody(t, q, &Result{View: "table"})
	for _, want := range []string{"nearest-rank", "quantile_cont", "ERROR, CRITICAL, ALERT, EMERGENCY or FATAL"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	wantBody(t, body, `SELECT
  "message",
  count(*) AS "count",
  CAST(printf('%.14e', avg("duration_ms")) AS DOUBLE) AS "avg(duration_ms)",
  list_sort(list("duration_ms") FILTER (WHERE "duration_ms" IS NOT NULL))[CAST(ceil(CAST(0.95 AS DOUBLE) * count("duration_ms")) AS BIGINT)] AS "p95(duration_ms)",
  CAST(printf('%.14e', CAST(count(*) FILTER (WHERE regexp_full_match(upper("severity"), '[\t-\r\x{85}\p{Z}]*(ERROR|CRITICAL|ALERT|EMERGENCY|FATAL)[\t-\r\x{85}\p{Z}]*')) AS DOUBLE) / nullif(count(*), 0)) AS DOUBLE) AS "error_rate"
FROM logs."app"
GROUP BY "message"
ORDER BY count(*) DESC NULLS LAST, "message" NULLS FIRST
LIMIT 10;
`)
}

func TestToSQLTableShapes(t *testing.T) {
	cases := []struct {
		name string
		q    Query
		want string
	}{
		{"no group-by is one row",
			Query{Stream: "app", View: "table", Range: Range{From: "1h"}, Aggregates: []Aggregate{{Op: "sum", Field: "code"}, {Op: "sum", Field: "latency"}}},
			`SELECT
  count(*) AS "count",
  CAST(sum("code") AS BIGINT) AS "sum(code)",
  CAST(printf('%.14e', sum("latency")) AS DOUBLE) AS "sum(latency)"
FROM logs."app"
WHERE "time" >= now() - INTERVAL '1 hours';
`},
		{"int and bool groups rank ties by text, metric order ascending",
			Query{Stream: "app", View: "table", GroupBy: []string{"code", "ok"}, Aggregates: []Aggregate{{Op: "p50", Field: "latency"}, {Op: "min", Field: "ratio"}},
				Order: &Order{Op: "p50", Field: "latency", Asc: true}, Limit: 3},
			`SELECT
  "code",
  "ok",
  count(*) AS "count",
  list_sort(list("latency") FILTER (WHERE "latency" IS NOT NULL))[CAST(ceil(CAST(0.5 AS DOUBLE) * count("latency")) AS BIGINT)] AS "p50(latency)",
  min("ratio") AS "min(ratio)"
FROM logs."app"
GROUP BY "code", "ok"
ORDER BY list_sort(list("latency") FILTER (WHERE "latency" IS NOT NULL))[CAST(ceil(CAST(0.5 AS DOUBLE) * count("latency")) AS BIGINT)] ASC NULLS LAST, count(*) DESC, CAST("code" AS VARCHAR) NULLS FIRST, CAST("ok" AS VARCHAR) NULLS FIRST
LIMIT 3;
`},
		{"quoted group path, default top",
			Query{Stream: "app", View: "table", GroupBy: []string{`a"b`}},
			`SELECT "a""b", count(*) AS "count"
FROM logs."app"
GROUP BY "a""b"
ORDER BY count(*) DESC NULLS LAST, "a""b" NULLS FIRST
LIMIT 10;
`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, body := viewBody(t, c.q, &Result{View: "table"})
			wantBody(t, body, c.want)
		})
	}
}

func TestToSQLErrorRateNoSeverity(t *testing.T) {
	// sqlSchema has no severity: no record counts as an error.
	got, err := ToSQL(Query{Stream: "app", View: "table", Aggregates: []Aggregate{{Op: "error_rate"}}}, sqlSchema, &Result{View: "table"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `count(*) FILTER (WHERE false)`) {
		t.Errorf("got\n%s", got)
	}
}

func TestToSQLGraph(t *testing.T) {
	start := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	q := Query{Stream: "app", View: "graph", Range: Range{From: "all"}, GroupBy: []string{"message"},
		Search: "50%", Aggregates: []Aggregate{{Op: "avg", Field: "latency"}}, Plot: &Aggregate{Op: "avg", Field: "latency"}, Top: 2}
	notes, body := viewBody(t, q, &Result{View: "graph", Graph: &Graph{Bucket: "5m", Starts: []time.Time{start}}})
	if !strings.Contains(notes, "long form") || !strings.Contains(notes, "pivots") {
		t.Errorf("notes:\n%s", notes)
	}
	wantBody(t, body, `WITH matched AS (
  SELECT *
  FROM logs."app"
  WHERE
    raw ILIKE '%50\%%' ESCAPE '\'
    AND "time" IS NOT NULL
),
ranked AS (
  SELECT
    "message",
    row_number() OVER (ORDER BY count(*) DESC NULLS LAST, "message" NULLS FIRST) AS series_rank,
    coalesce("message", '(empty)') AS series
  FROM matched
  GROUP BY "message"
),
tagged AS (
  SELECT
    time_bucket(INTERVAL '300 seconds', m."time") AS bucket,
    least(r.series_rank, 3) AS series_no,
    CASE WHEN r.series_rank <= 2 THEN r.series ELSE 'other' END AS series,
    m."latency" AS v
  FROM matched m
  JOIN ranked r ON m."message" IS NOT DISTINCT FROM r."message"
)
SELECT bucket, series, CAST(printf('%.14e', avg(v)) AS DOUBLE) AS value
FROM tagged
GROUP BY bucket, series_no, series
ORDER BY bucket, series_no;
`)
}

func TestToSQLGraphNoGroup(t *testing.T) {
	_, body := viewBody(t, Query{Stream: "app", View: "graph"}, &Result{View: "graph", Graph: &Graph{Bucket: "1h", Starts: []time.Time{}}})
	wantBody(t, body, `WITH matched AS (
  SELECT *
  FROM logs."app"
  WHERE "time" IS NOT NULL
),
tagged AS (
  SELECT
    time_bucket(INTERVAL '1 hours', m."time") AS bucket,
    0 AS series_no,
    'all' AS series
  FROM matched m
)
SELECT bucket, series, count(*) AS value
FROM tagged
GROUP BY bucket, series_no, series
ORDER BY bucket, series_no;
`)
}

func TestToSQLGraphLocalBuckets(t *testing.T) {
	old := sqlZone
	sqlZone = func() (string, error) { return "America/New_York", nil }
	t.Cleanup(func() { sqlZone = old })
	noon := time.Date(2026, 3, 7, 12, 0, 0, 0, time.Local)
	for _, c := range []struct{ bucket, want string }{
		{"1d", `timezone('America/New_York', date_trunc('day', timezone('America/New_York', m."time")))`},
		{"6h", `timezone('America/New_York', date_trunc('day', timezone('America/New_York', m."time"))) + to_microseconds(least((epoch_us(m."time") - epoch_us(timezone('America/New_York', date_trunc('day', timezone('America/New_York', m."time"))))) // 21600000000, 3) * 21600000000)`},
	} {
		notes, body := viewBody(t, Query{Stream: "app", View: "graph", Bucket: c.bucket}, &Result{View: "graph", Graph: &Graph{Bucket: c.bucket, Starts: []time.Time{noon}}})
		if !strings.Contains(body, c.want+" AS bucket") {
			t.Errorf("%s: body\n%s\nlacks\n%s", c.bucket, body, c.want)
		}
		if !strings.Contains(notes, "local midnight in America/New_York") {
			t.Errorf("%s: notes\n%s", c.bucket, notes)
		}
	}
}

func TestToSQLHeatmap(t *testing.T) {
	start := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	q := Query{Stream: "http.server", Source: "otel", View: "heatmap", Range: Range{From: "all"},
		Filters: []Filter{{Field: "message", Op: "=", Values: []string{"it's"}}}}
	r := &Result{View: "heatmap", Heatmap: &Heatmap{Bucket: "1m", Starts: []time.Time{start},
		Bands: []Band{{Lo: 1, Hi: 1.5848931924611136}, {Lo: 1.5848931924611136, Hi: 2.51188643150958}, {Lo: 2.51188643150958, Hi: 10}}}}
	notes, body := viewBody(t, q, r)
	if !strings.Contains(notes, "no lower bound") {
		t.Errorf("notes:\n%s", notes)
	}
	wantBody(t, body, `WITH bands(band, lo, hi) AS (
  VALUES
    (0, CAST(NULL AS DOUBLE), DOUBLE '1.5848931924611136'),
    (1, DOUBLE '1.5848931924611136', DOUBLE '2.51188643150958'),
    (2, DOUBLE '2.51188643150958', DOUBLE '10')
),
cells AS (
  SELECT
    time_bucket(INTERVAL '60 seconds', "time") AS bucket,
    CASE
      WHEN "duration_ms" < DOUBLE '1.5848931924611136' THEN 0
      WHEN "duration_ms" < DOUBLE '2.51188643150958' THEN 1
      WHEN "duration_ms" < DOUBLE '10' THEN 2
    END AS band
  FROM spans."http.server"
  WHERE
    "message" = 'it''s'
    AND "time" IS NOT NULL
    AND "duration_ms" IS NOT NULL
)
SELECT bucket, band, lo, hi, count(*) AS "count"
FROM cells
JOIN bands USING (band)
GROUP BY bucket, band, lo, hi
ORDER BY bucket, band;
`)
}

func TestToSQLViewErrors(t *testing.T) {
	durStr := viewSchema
	durStr.Fields = append([]schema.Field(nil), viewSchema.Fields...)
	for i := range durStr.Fields {
		if durStr.Fields[i].Path == "duration_ms" {
			durStr.Fields[i].Type = schema.TDuration
		}
	}
	hm := &Result{View: "heatmap", Heatmap: &Heatmap{Bucket: "1m", Bands: bands(1, 10)}}
	cases := []struct {
		name string
		q    Query
		s    schema.Schema
		r    *Result
	}{
		{"float group-by", Query{Stream: "app", View: "table", GroupBy: []string{"ratio"}}, viewSchema, &Result{View: "table"}},
		{"duration group-by", Query{Stream: "app", View: "graph", GroupBy: []string{"latency"}}, viewSchema, &Result{View: "graph", Graph: &Graph{Bucket: "1m"}}},
		{"time group-by", Query{Stream: "app", View: "table", GroupBy: []string{"at"}}, viewSchema, &Result{View: "table"}},
		{"json group-by", Query{Stream: "app", View: "table", GroupBy: []string{"body"}}, viewSchema, &Result{View: "table"}},
		{"model column group-by", Query{Stream: "app", View: "table", GroupBy: []string{"Raw"}}, viewSchema, &Result{View: "table"}},
		{"graph without result", Query{Stream: "app", View: "graph"}, viewSchema, &Result{View: "graph"}},
		{"graph nil result", Query{Stream: "app", View: "graph"}, viewSchema, nil},
		{"graph auto bucket", Query{Stream: "app", View: "graph"}, viewSchema, &Result{View: "graph", Graph: &Graph{Bucket: "auto"}}},
		{"graph group named series", Query{Stream: "app", View: "graph", GroupBy: []string{"Series"}}, func() schema.Schema {
			s := viewSchema
			s.Fields = append([]schema.Field{{Path: "Series", Type: schema.TString}}, viewSchema.Fields...)
			return s
		}(), &Result{View: "graph", Graph: &Graph{Bucket: "1m"}}},
		{"heatmap without result", Query{Stream: "x", Source: "otel", View: "heatmap"}, viewSchema, &Result{View: "heatmap"}},
		{"heatmap duration typed duration", Query{Stream: "x", Source: "otel", View: "heatmap"}, durStr, hm},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, err := ToSQL(c.q, c.s, c.r); err == nil {
				t.Errorf("want error, got\n%s", got)
			}
		})
	}
}

func TestToSQLRepeatedDSTHour(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	old := time.Local
	time.Local = ny
	t.Cleanup(func() { time.Local = old })
	// 2026-11-01 is 25 hours long in New York; a bucket starting 24h after
	// its midnight (23:00 local) is not one SQL can place.
	st := time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC)
	for _, b := range []string{"6h", "1d"} {
		_, err := ToSQL(Query{Stream: "app", View: "graph", Bucket: b}, viewSchema, &Result{View: "graph", Graph: &Graph{Bucket: b, Starts: []time.Time{st}}})
		if err == nil || !strings.Contains(err.Error(), "25-hour") {
			t.Errorf("%s: err = %v", b, err)
		}
	}
}

func TestZoneFromPath(t *testing.T) {
	for p, want := range map[string]string{
		"/var/db/timezone/zoneinfo/America/Los_Angeles": "America/Los_Angeles",
		"/usr/share/zoneinfo/UTC":                       "UTC",
	} {
		if got, err := zoneFromPath(p); err != nil || got != want {
			t.Errorf("%s: %q, %v", p, got, err)
		}
	}
	for _, p := range []string{"/etc/whatever", "/usr/share/zoneinfo/Not/AZone"} {
		if got, err := zoneFromPath(p); err == nil {
			t.Errorf("%s: %q, want error", p, got)
		}
	}
}
