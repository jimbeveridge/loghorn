package duckstore

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
)

var bg = context.Background()

// Relation is a SELECT over the records of one stream (record.AllStreams:
// every stream of source) and service ("" or "*": every service). Its
// columns are _id, _time, _time_inherited, _service, _source, _stream, _raw
// (the bytes), _text (_raw as text for searching; escaped where it is not
// valid UTF-8), then for each field i: vi, the value converted to cols[i].Type (NULL when
// missing, empty or not convertible), and xi, true when the value is present
// but not convertible ("bad"). Values are read from the attributes JSON and
// converted per record as schema.Convert converts them.
func (e *Engine) Relation(source, service, stream string, cols []schema.Field) (string, []any) {
	var args []any
	q := relation(source, service, stream, cols, func(v string) string {
		args = append(args, v)
		return "?"
	})
	return q, args
}

// relation is Relation with each compared value written by bind: a
// parameter, or a literal for a view.
func relation(source, service, stream string, cols []schema.Field, bind func(string) string) string {
	base := "_id, _time, _time_inherited, _service, _source, _stream, _raw"
	text := base + ", coalesce(try(decode(_raw)), CAST(_raw AS VARCHAR)) AS _text"
	where := " WHERE _source = " + bind(source)
	if service != "" && service != "*" {
		where += " AND _service = " + bind(service)
	}
	if stream != record.AllStreams {
		where += " AND _stream = " + bind(stream)
	}
	if len(cols) == 0 {
		return "SELECT " + text + " FROM " + sessionSchema + "all_records" + where
	}
	base += ", _text"
	// One parse of attributes per record extracts every field.
	ptrs := make([]string, len(cols))
	js := make([]string, len(cols))
	out := make([]string, len(cols))
	for i, f := range cols {
		ptrs[i] = lit(jsonPointer(f.Path))
		js[i] = "js[" + itoa(i+1) + "] AS j" + itoa(i)
		j := "j" + itoa(i)
		v := jsonConv(j, f)
		out[i] = v + " AS v" + itoa(i) + ", (" + present(j) + " AND " + v + " IS NULL) AS x" + itoa(i)
	}
	inner := "SELECT " + text + ", json_extract(attributes, [" + strings.Join(ptrs, ", ") + "]) AS js FROM " + sessionSchema + "all_records" + where
	mid := "SELECT " + base + ", " + strings.Join(js, ", ") + " FROM (" + inner + ")"
	return "SELECT " + base + ", " + strings.Join(out, ", ") + " FROM (" + mid + ")"
}

func itoa(i int) string { return strconv.Itoa(i) }

// present is true when JSON value j is there and not empty: neither
// missing, null nor "" (schema.Convert's Empty).
func present(j string) string {
	return "coalesce(json_type(" + j + ") NOT IN ('NULL') AND NOT (json_type(" + j + ") = 'VARCHAR' AND json_extract_string(" + j + ", '$') = ''), false)"
}

// jsonConv is the SQL giving schema.Convert's value for JSON value j read as
// field f, from the kind of JSON value it is: a string as text, a number as
// json.Number, true and false as bool, an array as []any. NULL means
// missing, empty or not convertible.
func jsonConv(j string, f schema.Field) string {
	t := norm(f.Type)
	text := "json_extract_string(" + j + ", '$')"
	str := "(CASE WHEN " + text + " <> '' THEN " + conv(text, schema.TString, f) + " END)"
	jsonText := "CAST(" + j + " AS VARCHAR)"
	var integer, float, boolean, array string
	switch t {
	case schema.TString, schema.TJSON:
		integer, float, array = jsonText, jsonText, jsonText
		boolean = "CAST(CAST(" + j + " AS BOOLEAN) AS VARCHAR)"
	case schema.TInt:
		// A number that is not an integer literal fails strconv.ParseInt.
		integer, float, boolean, array = "CAST("+j+" AS BIGINT)", nullOf(t), nullOf(t), nullOf(t)
	default:
		integer = conv("CAST("+j+" AS BIGINT)", schema.TInt, f)
		float = conv("CAST("+j+" AS DOUBLE)", schema.TFloat, f)
		boolean = conv("CAST("+j+" AS BOOLEAN)", schema.TBool, f)
		array = nullOf(t)
	}
	return "(CASE json_type(" + j + ")" +
		" WHEN 'VARCHAR' THEN " + str +
		" WHEN 'BIGINT' THEN " + integer +
		// A non-negative integer is UBIGINT; past BIGINT it is read as a
		// float, as strconv.ParseInt would reject it.
		" WHEN 'UBIGINT' THEN (CASE WHEN TRY_CAST(" + j + " AS BIGINT) IS NOT NULL THEN " + integer + " ELSE " + float + " END)" +
		" WHEN 'DOUBLE' THEN " + float +
		" WHEN 'BOOLEAN' THEN " + boolean +
		" WHEN 'ARRAY' THEN " + array +
		" WHEN 'OBJECT' THEN " + array +
		" END)"
}

// Records returns the records of all_records matching where (SQL over its
// columns), oldest _id first, rebuilt from _raw as they were first built.
func (e *Engine) Records(ctx context.Context, where string, args ...any) ([]record.Record, error) {
	return e.rowsToRecords(ctx, "SELECT _id, _time, _time_inherited, _service, _source, _stream, _raw FROM "+sessionSchema+"all_records WHERE "+where+" ORDER BY _id", args...)
}

// rowsToRecords rebuilds the records of a query returning _id, _time,
// _time_inherited, _service, _source, _stream and _raw.
func (e *Engine) rowsToRecords(ctx context.Context, q string, args ...any) ([]record.Record, error) {
	rows, err := e.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []record.Record
	for rows.Next() {
		var id int64
		var tm sql.NullTime
		var inh sql.NullBool
		var svc, src, stream string
		var raw []byte
		if err := rows.Scan(&id, &tm, &inh, &svc, &src, &stream, &raw); err != nil {
			return nil, err
		}
		out = append(out, e.Rebuild(id, tm.Time, inh.Bool, svc, src, stream, string(raw)))
	}
	return out, rows.Err()
}

// Rebuild is the record a stored row stands for: its line normalized again,
// with the row's id, time and service. A zero t means no time. A line that
// no longer normalizes (the input config changed, or a record made by hand)
// keeps the row's stream and source with no fields.
func (e *Engine) Rebuild(id int64, t time.Time, inherited bool, service, source, stream, raw string) record.Record {
	r, ok := e.rebuild(source, []byte(raw))
	if !ok {
		r = record.Record{Stream: stream, Source: source, Fields: map[string]any{}, Raw: []byte(raw)}
	}
	r.Seq, r.Service, r.TimeInherited, r.Time = int(id), service, inherited, t
	return r
}

// Tallies merges the per-path tallies of one stream (record.AllStreams:
// every stream) of source, for one service ("" or "*": every service).
func (e *Engine) Tallies(source, service, kind string) (map[string]schema.Tally, error) {
	every := service == "" || service == "*"
	rows, err := e.db.Query("SELECT path, CAST(sum(n) AS BIGINT), bit_or(kinds) FROM "+sessionSchema+"all_fields WHERE source = ? AND (? OR service = ?) AND (? OR kind = ?) GROUP BY path",
		source, every, service, kind == record.AllStreams, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]schema.Tally{}
	for rows.Next() {
		var p string
		var n int64
		var k int32
		if err := rows.Scan(&p, &n, &k); err != nil {
			return nil, err
		}
		out[p] = schema.Tally{Count: int(n), Kinds: schema.KindSet(k)}
	}
	return out, rows.Err()
}

// MaxSeq is the highest per-day sequence stored for day, or 0 when it
// holds none.
func (e *Engine) MaxSeq(day int) (int, error) {
	var id *int64
	lo := int64(day) << 32
	if err := e.db.QueryRow("SELECT max(_id) FROM "+sessionSchema+"all_records WHERE _id >= ? AND _id < ?", lo, lo+1<<32).Scan(&id); err != nil {
		return 0, err
	}
	if id == nil {
		return 0, nil
	}
	return int(*id - lo), nil
}

// Count is how many records are stored.
func (e *Engine) Count() (int, error) {
	var n int
	if err := e.db.QueryRow("SELECT count(*) FROM " + sessionSchema + "all_records").Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// Warn reports msg to the user through the engine's warn function.
func (e *Engine) Warn(msg string) { e.warn(msg) }

// Failed reports whether a file write failed this session, sending later
// records to memory.
func (e *Engine) Failed() bool {
	e.wmu.Lock()
	defer e.wmu.Unlock()
	return e.failed
}
