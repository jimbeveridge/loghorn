package query

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/duckstore"
	"github.com/jimbeveridge/loghorn/internal/record"
)

// TestToSQLOverModel runs the equivalence cases against the table model as
// serve builds it (duckstore views over the stored records) instead of
// tables loaded by hand: ToSQL's SQL must give RunSQL's answer there too.
func TestToSQLOverModel(t *testing.T) {
	recs := duckRecords()
	e, err := duckstore.OpenMemory(config.Input{}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	for _, r := range recs {
		e.Add(r)
	}
	if !e.Sync(10 * time.Second) {
		t.Fatal("sync timed out")
	}
	ddl := duckstore.ModelDDL([]duckstore.ModelTable{
		{Source: record.SourceLog, Stream: "app", Fields: duckLogSchema.Fields},
		{Source: record.SourceOtel, Stream: "http.server", Fields: duckSpanSchema.Fields},
	}, time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC), "run-1")
	for _, c := range append(duckCases(), duckCase{"samples by latency", Query{Stream: "app", Range: Range{From: "all"},
		Filters: []Filter{{Field: "code", Op: ">=", Values: []string{"404"}}}, Sort: Sort{Field: "latency", Desc: true}, Limit: 20}}) {
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
			run := sql
			if c.q.View == "" || c.q.View == "samples" {
				// Samples select only fields; seq is added to compare the order.
				run = strings.Replace(sql, "\nSELECT ", "\nSELECT seq, ", 1)
			}
			var rows []map[string]any
			err = e.WithModel(context.Background(), ddl, func() error {
				rows = modelRun(t, e, run)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if c.q.View == "" || c.q.View == "samples" {
				var got, want []string
				for _, r := range rows {
					n, _ := r["seq"].(json.Number)
					if n == "" {
						t.Fatalf("no seq in %v\nSQL:\n%s", r, sql)
					}
					got = append(got, n.String())
				}
				for _, r := range res.Rows {
					want = append(want, strconv.Itoa(r.Seq))
				}
				if strings.Join(got, ",") != strings.Join(want, ",") {
					t.Fatalf("seqs\n got %v\nwant %v\nSQL:\n%s", got, want, sql)
				}
				return
			}
			duckCheck(t, c.q, res, rows, sql)
		})
	}
}

// modelRun runs sql on one connection in UTC, with now() fixed at duckNow,
// and gives its rows as DuckDB's JSON output would: numbers as json.Number,
// times as "2006-01-02 15:04:05-07" in UTC.
func modelRun(t *testing.T, e *duckstore.Engine, sql string) []map[string]any {
	t.Helper()
	ctx := context.Background()
	conn, err := e.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET TimeZone='UTC'"); err != nil {
		t.Fatal(err)
	}
	now, _ := timestampLit(duckNow)
	rs, err := conn.QueryContext(ctx, strings.ReplaceAll(sql, "now()", now))
	if err != nil {
		t.Fatalf("%v\nSQL:\n%s", err, sql)
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	var out []map[string]any
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := map[string]any{}
		for i, c := range cols {
			switch v := vals[i].(type) {
			case int64:
				row[c] = json.Number(strconv.FormatInt(v, 10))
			case int32:
				row[c] = json.Number(strconv.FormatInt(int64(v), 10))
			case float64:
				row[c] = json.Number(strconv.FormatFloat(v, 'g', -1, 64))
			case time.Time:
				row[c] = v.UTC().Format("2006-01-02 15:04:05-07")
			default:
				row[c] = v
			}
		}
		out = append(out, row)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
