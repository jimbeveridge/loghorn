package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jimbeveridge/loghorn/internal/duckstore"
	"github.com/jimbeveridge/loghorn/internal/query"
	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
)

// SQLResult is the answer to a SQL query: its column names and at most the
// asked number of rows, as export values. Truncated is true when the query
// had more rows.
type SQLResult struct {
	Columns   []string
	Rows      [][]any
	Truncated bool
}

// SQLTimeout bounds one SQL query. The query holds the store's read lock,
// so this is also how long it can keep an archive, and every reader queued
// behind that archive, waiting.
const SQLTimeout = 10 * time.Second

// errTrailingText is the answer to a query the row cap cannot wrap.
var errTrailingText = errors.New("the query must be one SELECT; remove trailing text after it")

// SQL runs a read-only query over the table model of query.SQLHeader:
// logs."<stream>", spans."<stream>" and "*" of each, built from the stored
// records and the schema inferred now, and the loghorn.this_run_* macros
// for the latest run. Anything but one query over those tables is refused.
// At most max rows are read: the row cap is part of the query, and a query
// it cannot wrap is refused.
func (s *Store) SQL(ctx context.Context, q string, max int) (SQLResult, error) {
	q = strings.TrimRight(q, "; \t\r\n\f\v")
	tables, err := s.modelTables()
	if err != nil {
		return SQLResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, SQLTimeout)
	defer cancel()
	defer s.read()()
	start, runID := s.runStart, ""
	if id, t, ok := s.e.LatestRun(ctx); ok {
		start, runID = t, id
	}
	var res SQLResult
	err = s.e.WithModel(ctx, duckstore.ModelDDL(tables, start, runID), func() error {
		return s.e.ReadOnly(ctx, func(conn *sql.Conn) error {
			if err := duckstore.CheckReadOnly(ctx, conn, q); err != nil {
				return err
			}
			// DuckDB builds the whole answer before the first row is read,
			// so the row cap goes into the query. Text the wrapper cannot
			// hold (such as a comment after a ;) is refused, never run
			// without the cap.
			run := "SELECT * FROM (\n" + q + "\n) LIMIT " + strconv.Itoa(max+1)
			if duckstore.CheckReadOnly(ctx, conn, run) != nil {
				return errTrailingText
			}
			rows, err := conn.QueryContext(ctx, run)
			if err != nil {
				return err
			}
			defer rows.Close()
			if res.Columns, err = rows.Columns(); err != nil {
				return err
			}
			for rows.Next() {
				if len(res.Rows) == max {
					res.Truncated = true
					break
				}
				vals := make([]any, len(res.Columns))
				ptrs := make([]any, len(vals))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					return err
				}
				for i, v := range vals {
					vals[i] = duckstore.SQLValue(v)
					if f, ok := vals[i].(float64); ok {
						vals[i] = query.Round15(f) // as exports round computed numbers
					}
				}
				res.Rows = append(res.Rows, vals)
			}
			return rows.Err()
		})
	})
	return res, err
}

// modelTables is a table per stored stream and a "*" table per source, each
// with its stream's schema over every service.
func (s *Store) modelTables() ([]duckstore.ModelTable, error) {
	var out []duckstore.ModelTable
	add := func(src, stream string) error {
		inferred, err := s.Schema("*", src, stream)
		if err != nil {
			return err
		}
		out = append(out, duckstore.ModelTable{Source: src, Stream: stream, Fields: schema.Effective(inferred, nil).Fields})
		return nil
	}
	for _, src := range []string{record.SourceLog, record.SourceOtel} {
		if err := add(src, record.AllStreams); err != nil {
			return nil, err
		}
	}
	// Larger streams first: of streams whose names differ only in case, the
	// model keeps the first.
	streams, err := s.Streams()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(streams, func(i, j int) bool { return streams[i].Count > streams[j].Count })
	for _, c := range streams {
		if c.Name == record.AllStreams {
			continue // the "*" table is every stream
		}
		if err := add(c.Source, c.Name); err != nil {
			return nil, err
		}
	}
	return out, nil
}
