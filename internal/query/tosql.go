package query

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
)

// SQLHeader is the comment block that opens every ToSQL answer: the DuckDB
// table model the statement targets, so a reader (or an LLM editing it)
// knows what the names mean.
const SQLHeader = `-- loghorn query as DuckDB SQL. Table model:
--   Schema logs holds log records and schema spans holds span records, with
--   one table per stream named exactly as the stream: logs."app",
--   spans."http.server". The table "*" (spans."*") is every stream of its
--   schema at once.
--   Columns: seq BIGINT (store order; every sort breaks ties on it),
--   time TIMESTAMPTZ (the effective time, inherited times included; NULL
--   when the record has none), service VARCHAR, raw VARCHAR (the line that
--   search matches), then one column per field path, quoted with its dotted
--   name ("http.status").
--   Field types: string VARCHAR, int BIGINT, float DOUBLE, duration DOUBLE
--   (ms), bool BOOLEAN, time TIMESTAMPTZ, json JSON.
--   A missing value, null or empty string is NULL. A value that does not fit
--   its field's type (a bad value) also loads as NULL, so SQL treats it as
--   empty. In loghorn a bad value matches only not-empty; here it fails
--   not-empty and matches empty, != and not-in, like any NULL.
--   Durations load as milliseconds rounded to 15 significant digits, and a
--   duration filter compares against its value in ms rounded the same way,
--   so a value within rounding of a filter edge can fall on the other side.
--   A field named seq, time, service or raw (in any case) has no column.
--   Macros: loghorn.this_run_start() is the start of the run that is current
--   when the SQL executes, and loghorn.this_run_id() is its run ID.
`

// thisRunNote explains the run-ID condition of a this-run range.
const thisRunNote = `-- this-run: a wrapped command's own output lines carry no run ID, so once
-- later runs are stored this also matches their untagged lines.
`

// percentileNote explains the pNN expression.
const percentileNote = `-- pNN is loghorn's nearest-rank percentile: the value at rank ceil(p * n)
-- of the sorted non-NULL values. quantile_cont would interpolate between
-- values, and quantile_disc ranks differently, so neither matches.
`

// errorRateNote spells out the severity set of error_rate.
const errorRateNote = `-- error_rate is the fraction of rows whose severity, trimmed of white space
-- and upper-cased, is ERROR, CRITICAL, ALERT, EMERGENCY or FATAL.
`

// errorSeverityRE matches what isErrorSeverity accepts once upper-cased:
// the set, trimmed of Unicode white space (\t-\r, U+0085 and \p{Z} are
// exactly unicode.IsSpace).
const errorSeverityRE = `'[\t-\r\x{85}\p{Z}]*(ERROR|CRITICAL|ALERT|EMERGENCY|FATAL)[\t-\r\x{85}\p{Z}]*'`

// modelColumns are the table model's own columns; a field path equal to one
// of them (DuckDB identifiers are case-insensitive) has no column.
var modelColumns = []string{"seq", "time", "service", "raw"}

// sqlZone names the time zone RunSQL counts 6h and 1d buckets in (time.Local)
// as an IANA name DuckDB knows. Tests replace it.
var sqlZone = localZoneName

// ToSQL translates q into a DuckDB statement over the table model in
// SQLHeader that selects the same rows RunSQL does. r is the result of running
// q; it supplies what exists only after a run (the samples column list when
// q names none, the graph and heatmap bucket, the heatmap bands). Anything
// that cannot be expressed exactly is an error. The layout is written here by
// hand, not by internal/sqlfmt, which drops quotes around names.
func ToSQL(q Query, s schema.Schema, r *Result) (string, error) {
	if err := q.Validate(s); err != nil {
		return "", err
	}
	var b sqlBuilder
	from, err := b.source(q, s)
	if err != nil {
		return "", err
	}
	where, err := b.where(q, s)
	if err != nil {
		return "", err
	}
	var body string
	switch q.view() {
	case "table":
		body, err = b.tableSQL(q, s, from, where)
	case "graph":
		body, err = b.graphSQL(q, s, r, from, where)
	case "heatmap":
		body, err = b.heatmapSQL(q, s, r, from, where)
	default:
		body, err = samplesSQL(q, s, r, from, where)
	}
	if err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString(SQLHeader)
	for _, n := range b.notes {
		out.WriteString(n)
	}
	out.WriteString(body)
	return out.String(), nil
}

// samplesSQL selects the sorted, limited rows of the samples view.
func samplesSQL(q Query, s schema.Schema, r *Result, from string, where []string) (string, error) {
	var err error
	paths := q.Columns
	if r != nil {
		paths = make([]string, len(r.Columns))
		for i, c := range r.Columns {
			paths[i] = c.Path
		}
	}
	cols := make([]string, len(paths))
	for i, p := range paths {
		if cols[i], err = fieldIdent(p); err != nil {
			return "", err
		}
	}
	if len(cols) == 0 {
		cols = []string{"*"}
	}
	order, err := sqlOrder(q.Sort, s)
	if err != nil {
		return "", err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}

	var out strings.Builder
	out.WriteString(selectList(cols))
	out.WriteString("FROM " + from + "\n")
	out.WriteString(whereClause(where))
	out.WriteString("ORDER BY " + order + "\n")
	out.WriteString("LIMIT " + strconv.Itoa(limit))
	if q.Offset > 0 {
		out.WriteString("\nOFFSET " + strconv.Itoa(q.Offset))
	}
	out.WriteString(";\n")
	return out.String(), nil
}

// sqlBuilder collects the comment lines a translation adds.
type sqlBuilder struct {
	notes []string
}

// note adds comment lines once.
func (b *sqlBuilder) note(n string) {
	for _, x := range b.notes {
		if x == n {
			return
		}
	}
	b.notes = append(b.notes, n)
}

// sqlGroup is one group-by column: its identifier, and the text Go ranks
// ties on and names series by (groupKey's label).
type sqlGroup struct {
	id, text string
}

// groupCols translates the group-by paths. groupKey labels a value with its
// Go text, which DuckDB writes the same way only for strings, ints and
// bools; other types are an error, since the tie order and series names
// would differ.
func groupCols(paths []string, s schema.Schema) ([]sqlGroup, error) {
	out := make([]sqlGroup, len(paths))
	for i, p := range paths {
		id, err := fieldIdent(p)
		if err != nil {
			return nil, err
		}
		switch t := fieldType(s, p); t {
		case schema.TString:
			out[i] = sqlGroup{id: id, text: id}
		case schema.TInt, schema.TBool:
			out[i] = sqlGroup{id: id, text: "CAST(" + id + " AS VARCHAR)"}
		default:
			return nil, fmt.Errorf("sql: group-by field %q is %s: loghorn ranks ties and names series by the value's text, which DuckDB writes differently for that type", p, t)
		}
	}
	return out, nil
}

// aggInput is the column aggregate a reads and its type: none for count;
// for error_rate the severity column, or "" when the schema has no string
// severity (isErrorSeverity then never matches).
func aggInput(a Aggregate, s schema.Schema) (string, schema.Type, error) {
	switch a.Op {
	case "count":
		return "", "", nil
	case "error_rate":
		if f, ok := s.Field("severity"); !ok || (f.Type != "" && f.Type != schema.TString) {
			return "", schema.TString, nil
		}
		id, err := fieldIdent("severity")
		return id, schema.TString, err
	}
	id, err := fieldIdent(a.Field)
	return id, fieldType(s, a.Field), err
}

// aggSQL is aggregate a over column id of type t, as metricValue computes
// it: avg, a float or duration sum and error_rate rounded to 15 significant
// digits (round15SQL), pNN by nearest rank.
func (b *sqlBuilder) aggSQL(a Aggregate, id string, t schema.Type) string {
	switch a.Op {
	case "count":
		return "count(*)"
	case "error_rate":
		b.note(errorRateNote)
		match := "false"
		if id != "" {
			match = "regexp_full_match(upper(" + id + "), " + errorSeverityRE + ")"
		}
		return round15SQL("CAST(count(*) FILTER (WHERE " + match + ") AS DOUBLE) / nullif(count(*), 0)")
	case "sum":
		if t == schema.TInt {
			// DuckDB's sum(BIGINT) is HUGEINT, which -json prints as a string.
			return "CAST(sum(" + id + ") AS BIGINT)"
		}
		return round15SQL("sum(" + id + ")")
	case "avg":
		return round15SQL("avg(" + id + ")")
	case "min", "max":
		return a.Op + "(" + id + ")"
	}
	// p50, p90, p95, p99 (Validate checked the operator).
	b.note(percentileNote)
	p, _ := strconv.ParseFloat(a.Op[1:], 64)
	frac := strconv.FormatFloat(p/100, 'g', -1, 64)
	return "list_sort(list(" + id + ") FILTER (WHERE " + id + " IS NOT NULL))" +
		"[CAST(ceil(CAST(" + frac + " AS DOUBLE) * count(" + id + ")) AS BIGINT)]"
}

// round15SQL rounds x to 15 significant digits as Round15 does: printf
// rounds the decimal digits exactly, as strconv does.
func round15SQL(x string) string {
	return "CAST(printf('%.14e', " + x + ") AS DOUBLE)"
}

// rankOrder is the ORDER BY list of rankGroups: the order metric (count
// descending by default) with no value last, then count descending, then
// the group text ascending (an empty value first, as its \x01 key sorts).
func (b *sqlBuilder) rankOrder(o *Order, s schema.Schema, keys []sqlGroup) (string, error) {
	by := Order{Op: "count"}
	if o != nil {
		by = *o
	}
	a := Aggregate{Op: by.Op, Field: by.Field}
	id, t, err := aggInput(a, s)
	if err != nil {
		return "", err
	}
	dir := " DESC"
	if by.Asc {
		dir = " ASC"
	}
	parts := []string{b.aggSQL(a, id, t) + dir + " NULLS LAST"}
	if by.Op != "count" {
		parts = append(parts, "count(*) DESC")
	}
	for _, k := range keys {
		parts = append(parts, k.text+" NULLS FIRST")
	}
	return strings.Join(parts, ", "), nil
}

// tableSQL groups and ranks as runTable does.
func (b *sqlBuilder) tableSQL(q Query, s schema.Schema, from string, where []string) (string, error) {
	keys, err := groupCols(q.GroupBy, s)
	if err != nil {
		return "", err
	}
	var cols, ids []string
	for _, k := range keys {
		cols = append(cols, k.id)
		ids = append(ids, k.id)
	}
	cols = append(cols, `count(*) AS "count"`)
	for _, a := range q.Aggregates {
		id, t, err := aggInput(a, s)
		if err != nil {
			return "", err
		}
		name, err := quoteIdent(metricColumn(a, s).Path)
		if err != nil {
			return "", err
		}
		cols = append(cols, b.aggSQL(a, id, t)+" AS "+name)
	}
	var out strings.Builder
	out.WriteString(selectList(cols))
	out.WriteString("FROM " + from + "\n")
	out.WriteString(whereClause(where))
	if len(keys) == 0 {
		// One row over every match, as runTable's single group.
		return strings.TrimSuffix(out.String(), "\n") + ";\n", nil
	}
	order, err := b.rankOrder(q.Order, s, keys)
	if err != nil {
		return "", err
	}
	limit := q.Top
	if limit <= 0 {
		limit = q.Limit
	}
	if limit <= 0 {
		limit = DefaultTop
	}
	limit = min(limit, MaxLimit)
	out.WriteString("GROUP BY " + strings.Join(ids, ", ") + "\n")
	out.WriteString("ORDER BY " + order + "\n")
	out.WriteString("LIMIT " + strconv.Itoa(limit) + ";\n")
	return out.String(), nil
}

// graphNote explains the graph's long output.
const graphNote = `-- Graph, long form: one row per bucket and series that has rows (bucket,
-- series, value). The CLI's wide form pivots it, one column per series; a
-- bucket a series has no row in counts 0. Series are ranked over the whole
-- range; those past the top fold into other.
`

// graphSQL buckets and ranks as runGraph does, with the bucket r resolved.
func (b *sqlBuilder) graphSQL(q Query, s schema.Schema, r *Result, from string, where []string) (string, error) {
	if r == nil || r.Graph == nil {
		return "", errors.New("sql: a graph needs its run result")
	}
	bucket, err := b.bucketExpr(r.Graph.Bucket, r.Graph.Starts, `m."time"`)
	if err != nil {
		return "", err
	}
	keys, err := groupCols(q.GroupBy, s)
	if err != nil {
		return "", err
	}
	for _, p := range q.GroupBy {
		if strings.EqualFold(p, "series") || strings.EqualFold(p, "series_rank") {
			return "", fmt.Errorf("sql: group-by field %q takes the name of the graph's own column", p)
		}
	}
	plot := Aggregate{Op: "count"}
	if q.Plot != nil {
		plot = *q.Plot
	}
	pid, pt, err := aggInput(plot, s)
	if err != nil {
		return "", err
	}
	b.note(graphNote)

	var out strings.Builder
	out.WriteString("WITH matched AS (\n")
	out.WriteString(indent("SELECT *\nFROM " + from + "\n" + whereClause(append(where, `"time" IS NOT NULL`))))
	out.WriteString("),\n")
	tagged := []string{bucket + " AS bucket"}
	join := ""
	if len(keys) == 0 {
		tagged = append(tagged, "0 AS series_no", "'all' AS series")
	} else {
		top := q.Top
		if top <= 0 {
			top = DefaultTop
		}
		top = min(top, MaxGraphTop)
		order, err := b.rankOrder(q.Order, s, keys)
		if err != nil {
			return "", err
		}
		var ids, labels, on []string
		for _, k := range keys {
			ids = append(ids, k.id)
			labels = append(labels, "coalesce("+k.text+", '"+emptyLabel+"')")
			on = append(on, "m."+k.id+" IS NOT DISTINCT FROM r."+k.id)
		}
		label := labels[0]
		if len(labels) > 1 {
			label = "concat_ws(' · ', " + strings.Join(labels, ", ") + ")"
		}
		rcols := append(append([]string{}, ids...),
			"row_number() OVER (ORDER BY "+order+") AS series_rank",
			label+" AS series")
		out.WriteString("ranked AS (\n")
		out.WriteString(indent(selectList(rcols) + "FROM matched\nGROUP BY " + strings.Join(ids, ", ") + "\n"))
		out.WriteString("),\n")
		n := strconv.Itoa(top)
		tagged = append(tagged,
			"least(r.series_rank, "+strconv.Itoa(top+1)+") AS series_no",
			"CASE WHEN r.series_rank <= "+n+" THEN r.series ELSE 'other' END AS series")
		join = "JOIN ranked r ON " + strings.Join(on, " AND ") + "\n"
	}
	vid := ""
	if pid != "" {
		tagged = append(tagged, "m."+pid+" AS v")
		vid = "v"
	}
	out.WriteString("tagged AS (\n")
	out.WriteString(indent(selectList(tagged) + "FROM matched m\n" + join))
	out.WriteString(")\n")
	out.WriteString("SELECT bucket, series, " + b.aggSQL(plot, vid, pt) + " AS value\n")
	out.WriteString("FROM tagged\n")
	out.WriteString("GROUP BY bucket, series_no, series\n")
	out.WriteString("ORDER BY bucket, series_no;\n")
	return out.String(), nil
}

// heatmapSQL counts spans by bucket and band as runHeatmap does, with the
// bucket and band edges r resolved.
func (b *sqlBuilder) heatmapSQL(q Query, s schema.Schema, r *Result, from string, where []string) (string, error) {
	if r == nil || r.Heatmap == nil || len(r.Heatmap.Bands) == 0 {
		return "", errors.New("sql: a heatmap needs its run result")
	}
	switch fieldType(s, durationField.Path) {
	case schema.TInt, schema.TFloat:
	default:
		return "", fmt.Errorf("sql: a heatmap needs %s typed int or float", durationField.Path)
	}
	bucket, err := b.bucketExpr(r.Heatmap.Bucket, r.Heatmap.Starts, `"time"`)
	if err != nil {
		return "", err
	}
	d, _ := fieldIdent(durationField.Path)
	hs := r.Heatmap.Bands
	rows := make([]string, len(hs))
	whens := make([]string, len(hs))
	for i, band := range hs {
		lo := "CAST(NULL AS DOUBLE)"
		if i > 0 {
			if lo, err = doubleLit(band.Lo); err != nil {
				return "", err
			}
		}
		hi, err := doubleLit(band.Hi)
		if err != nil {
			return "", err
		}
		rows[i] = "(" + strconv.Itoa(i) + ", " + lo + ", " + hi + ")"
		whens[i] = "WHEN " + d + " < " + hi + " THEN " + strconv.Itoa(i)
	}
	b.note(`-- Heatmap: span count per bucket and duration band (ms, lo inclusive, hi
-- exclusive). The lowest band has no lower bound; a duration at or past the
-- top edge is skipped, as are spans with no time or duration.
`)
	conds := append(where, `"time" IS NOT NULL`, d+" IS NOT NULL")
	var out strings.Builder
	out.WriteString("WITH bands(band, lo, hi) AS (\n  VALUES\n    ")
	out.WriteString(strings.Join(rows, ",\n    "))
	out.WriteString("\n),\ncells AS (\n")
	out.WriteString(indent("SELECT\n  " + bucket + " AS bucket,\n  CASE\n    " +
		strings.Join(whens, "\n    ") + "\n  END AS band\nFROM " + from + "\n" + whereClause(conds)))
	out.WriteString(")\n")
	out.WriteString(`SELECT bucket, band, lo, hi, count(*) AS "count"` + "\n")
	out.WriteString("FROM cells\nJOIN bands USING (band)\n")
	out.WriteString("GROUP BY bucket, band, lo, hi\n")
	out.WriteString("ORDER BY bucket, band;\n")
	return out.String(), nil
}

// bucketExpr is the start of the bucket holding time column t, as xAxis
// and bucketIndex place it. Sizes up to 1h truncate the absolute time, as
// time_bucket does (both align to UTC midnight). 6h and 1d count from local
// midnight in time.Local, the last bucket of a day running to the next
// midnight. starts are the run's bucket starts.
func (b *sqlBuilder) bucketExpr(name string, starts []time.Time, t string) (string, error) {
	size, ok := bucketSizes[name]
	if !ok {
		return "", fmt.Errorf("sql: bucket %q is not resolved", name)
	}
	if size <= time.Hour {
		iv, err := sqlInterval(size)
		if err != nil {
			return "", err
		}
		return "time_bucket(" + iv + ", " + t + ")", nil
	}
	// The run's first bucket starts where the data does: data that starts in
	// the last hour of a 25-hour day (a daylight-saving change) gets a first
	// bucket 24h after midnight, and the buckets after it are shifted.
	for _, st := range starts {
		lt := st.In(time.Local)
		mid := time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, time.Local)
		if st.Sub(mid) >= 24*time.Hour {
			return "", fmt.Errorf("sql: the %s bucket starting %s is in the last hour of a 25-hour day, which SQL cannot place; pick another bucket or range", name, st.Format(time.RFC3339))
		}
	}
	zone, err := sqlZone()
	if err != nil {
		return "", fmt.Errorf("sql: %s buckets count from local midnight: %w", name, err)
	}
	z, err := quoteString(zone)
	if err != nil {
		return "", err
	}
	b.note("-- " + name + " buckets start at local midnight in " + zone + " (the server's time zone),\n" +
		"-- then every " + name + "; the last bucket of a day runs to the next midnight.\n")
	mid := "timezone(" + z + ", date_trunc('day', timezone(" + z + ", " + t + ")))"
	if size == 24*time.Hour {
		return mid, nil
	}
	us := strconv.FormatInt(size.Microseconds(), 10)
	last := strconv.FormatInt(int64(24*time.Hour/size)-1, 10)
	return mid + " + to_microseconds(least((epoch_us(" + t + ") - epoch_us(" + mid + ")) // " + us + ", " + last + ") * " + us + ")", nil
}

// localZoneName is the IANA name of time.Local, found the way Go loads it:
// from TZ, else the /etc/localtime link.
func localZoneName() (string, error) {
	if tz, ok := os.LookupEnv("TZ"); ok {
		tz = strings.TrimPrefix(tz, ":")
		switch {
		case tz == "":
			return "UTC", nil
		case strings.HasPrefix(tz, "/"):
			return zoneFromPath(tz)
		}
		if _, err := time.LoadLocation(tz); err == nil {
			return tz, nil
		}
		return "UTC", nil // Go falls back to UTC for an unknown TZ
	}
	p, err := os.Readlink("/etc/localtime")
	if err != nil {
		return "", fmt.Errorf("cannot name the local time zone: %w", err)
	}
	return zoneFromPath(p)
}

// zoneFromPath is the zone name in a zoneinfo file path.
func zoneFromPath(p string) (string, error) {
	i := strings.LastIndex(p, "zoneinfo/")
	if i < 0 {
		return "", fmt.Errorf("cannot name the local time zone from %s", p)
	}
	name := p[i+len("zoneinfo/"):]
	if _, err := time.LoadLocation(name); err != nil {
		return "", fmt.Errorf("cannot name the local time zone: %w", err)
	}
	return name, nil
}

// doubleLit is a DOUBLE literal that DuckDB parses to f exactly (a plain
// decimal literal would be a DECIMAL, converted with rounding).
func doubleLit(f float64) (string, error) {
	s, err := floatLit(f)
	if err != nil {
		return "", err
	}
	return "DOUBLE '" + s + "'", nil
}

// indent indents every line of s by two spaces.
func indent(s string) string {
	lines := strings.SplitAfter(s, "\n")
	var out strings.Builder
	for _, l := range lines {
		if l != "" && l != "\n" {
			out.WriteString("  ")
		}
		out.WriteString(l)
	}
	return out.String()
}

// source is the table of q's stream: schema logs or spans, table the stream.
func (b *sqlBuilder) source(q Query, s schema.Schema) (string, error) {
	stream := q.Stream
	if stream == "" {
		stream = s.Stream
	}
	if stream == "" {
		return "", errors.New("sql: no stream")
	}
	src := q.Source
	if src == "" {
		src = s.Source
	}
	src, _ = record.ParseSource(src) // Validate checked q.Source
	sch := "logs"
	if src == record.SourceOtel {
		sch = "spans"
	}
	id, err := quoteIdent(stream)
	if err != nil {
		return "", err
	}
	return sch + "." + id, nil
}

// where is the list of conditions (ANDed) for service, range, search,
// filters and cell, in RunSQL's order.
func (b *sqlBuilder) where(q Query, s schema.Schema) ([]string, error) {
	var conds []string
	if q.Service != "" && q.Service != "*" {
		lit, err := quoteString(q.Service)
		if err != nil {
			return nil, err
		}
		conds = append(conds, "service = "+lit)
	}
	rc, err := b.rangeConds(q.Range)
	if err != nil {
		return nil, err
	}
	conds = append(conds, rc...)
	if q.Search != "" {
		pat, err := likePattern(q.Search)
		if err != nil {
			return nil, err
		}
		conds = append(conds, "raw ILIKE "+pat+` ESCAPE '\'`)
	}
	for _, f := range q.Filters {
		c, err := filterCond(f, s)
		if err != nil {
			return nil, err
		}
		conds = append(conds, c)
	}
	if q.Cell != nil {
		cc, err := cellConds(q.Cell, s)
		if err != nil {
			return nil, err
		}
		conds = append(conds, cc...)
	}
	return conds, nil
}

// rangeConds translates the range as stated: relative stays relative,
// absolute stays absolute, this-run stays symbolic. A record with no time
// is outside any range with a from, as in RunSQL, and inside one with only a to.
func (b *sqlBuilder) rangeConds(rg Range) ([]string, error) {
	var conds []string
	hasFrom := false
	switch f := strings.TrimSpace(rg.From); f {
	case "", "all":
	case "this-run":
		hasFrom = true
		conds = append(conds, `"time" >= loghorn.this_run_start()`,
			`("loghorn.run.id" = loghorn.this_run_id() OR "loghorn.run.id" IS NULL)`)
		b.notes = append(b.notes, thisRunNote)
	default:
		hasFrom = true
		if d, ok := ParseAgo(f); ok {
			iv, err := sqlInterval(d)
			if err != nil {
				return nil, fmt.Errorf("range from %q: %w", rg.From, err)
			}
			conds = append(conds, `"time" >= now() - `+iv)
		} else {
			t, err := time.Parse(time.RFC3339Nano, f)
			if err != nil {
				return nil, fmt.Errorf("range from %q: want this-run, all, a relative time or RFC 3339", rg.From)
			}
			lit, err := timestampLit(t)
			if err != nil {
				return nil, fmt.Errorf("range from %q: %w", rg.From, err)
			}
			conds = append(conds, `"time" >= `+lit)
		}
	}
	switch t := strings.TrimSpace(rg.To); t {
	case "", "now":
	default:
		tm, err := time.Parse(time.RFC3339Nano, t)
		if err != nil {
			return nil, fmt.Errorf("range to %q: want now or RFC 3339", rg.To)
		}
		lit, err := timestampLit(tm)
		if err != nil {
			return nil, fmt.Errorf("range to %q: %w", rg.To, err)
		}
		c := `"time" <= ` + lit
		if !hasFrom {
			c = `("time" IS NULL OR ` + c + ")"
		}
		conds = append(conds, c)
	}
	return conds, nil
}

// sqlInterval writes d as an exact DuckDB interval in hours, or else
// seconds, milliseconds or microseconds. Days are written as hours: a
// DuckDB day is a calendar day, which across a DST change is not 24 h.
func sqlInterval(d time.Duration) (string, error) {
	units := []struct {
		d    time.Duration
		name string
	}{
		{time.Hour, "hours"}, {time.Second, "seconds"},
		{time.Millisecond, "milliseconds"}, {time.Microsecond, "microseconds"},
	}
	for _, u := range units {
		if d%u.d == 0 {
			return "INTERVAL '" + strconv.FormatInt(int64(d/u.d), 10) + " " + u.name + "'", nil
		}
	}
	return "", fmt.Errorf("%v is finer than DuckDB's microseconds", d)
}

// timestampLit is a TIMESTAMPTZ literal in UTC. DuckDB keeps microseconds,
// so a finer instant is an error.
func timestampLit(t time.Time) (string, error) {
	if t.Nanosecond()%1000 != 0 {
		return "", fmt.Errorf("%s is finer than DuckDB's microseconds", t.Format(time.RFC3339Nano))
	}
	return "TIMESTAMPTZ '" + t.UTC().Format(time.RFC3339Nano) + "'", nil
}

// filterCond translates one filter as RunSQL applies it. An empty value
// (NULL) matches empty, != and not-in.
func filterCond(f Filter, s schema.Schema) (string, error) {
	id, err := fieldIdent(f.Field)
	if err != nil {
		return "", err
	}
	t := fieldType(s, f.Field)
	switch f.Op {
	case "empty":
		if t == schema.TString {
			return "(" + id + " IS NULL OR " + id + " = '')", nil
		}
		return id + " IS NULL", nil
	case "not-empty":
		if t == schema.TString {
			return "(" + id + " IS NOT NULL AND " + id + " <> '')", nil
		}
		return id + " IS NOT NULL", nil
	case "contains":
		pat, err := likePattern(f.Values[0])
		if err != nil {
			return "", err
		}
		return id + " ILIKE " + pat + ` ESCAPE '\'`, nil
	case "in", "not-in":
		lits := make([]string, len(f.Values))
		for i, v := range f.Values {
			if lits[i], err = operandLit(t, v); err != nil {
				return "", err
			}
		}
		list := "(" + strings.Join(lits, ", ") + ")"
		if f.Op == "in" {
			return id + " IN " + list, nil
		}
		return "(" + id + " IS NULL OR " + id + " NOT IN " + list + ")", nil
	}
	lit, err := operandLit(t, f.Values[0])
	if err != nil {
		return "", err
	}
	switch f.Op {
	case "=", "<", "<=", ">", ">=":
		return id + " " + f.Op + " " + lit, nil
	case "!=":
		return "(" + id + " IS NULL OR " + id + " <> " + lit + ")", nil
	}
	return "", fmt.Errorf("sql: operator %q has no translation", f.Op)
}

// operandLit is the SQL literal of filter value v for a field of type t, in
// the table model's unit (a duration in ms, rounded as exports round it).
func operandLit(t schema.Type, v string) (string, error) {
	tg, err := parseOperand(t, v)
	if err != nil {
		return "", err
	}
	switch t {
	case schema.TInt:
		return strconv.FormatInt(tg.i, 10), nil
	case schema.TFloat:
		return floatLit(tg.f)
	case schema.TDuration:
		return floatLit(Round15(tg.f * 1000))
	case schema.TTimestamp:
		return timestampLit(tg.t)
	case schema.TBool:
		return strconv.FormatBool(tg.b), nil
	}
	return quoteString(tg.s)
}

func floatLit(f float64) (string, error) {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if strings.ContainsAny(s, "IN") { // Inf, NaN
		return "", fmt.Errorf("%s has no SQL literal", s)
	}
	return s, nil
}

// cellConds narrows to one heatmap cell as HeatCell.contains does, which
// reads duration_ms as a raw number; only an int or float column holds that.
func cellConds(c *HeatCell, s schema.Schema) ([]string, error) {
	switch fieldType(s, durationField.Path) {
	case schema.TInt, schema.TFloat:
	default:
		return nil, fmt.Errorf("sql: a heatmap cell needs %s typed int or float", durationField.Path)
	}
	from, err := timestampLit(c.From)
	if err != nil {
		return nil, err
	}
	to, err := timestampLit(c.To)
	if err != nil {
		return nil, err
	}
	id, _ := fieldIdent(durationField.Path)
	conds := []string{`"time" >= ` + from, `"time" < ` + to}
	if c.Lo != nil {
		lo, err := floatLit(*c.Lo)
		if err != nil {
			return nil, err
		}
		conds = append(conds, id+" >= "+lo)
	}
	hi, err := floatLit(c.Hi)
	if err != nil {
		return nil, err
	}
	return append(conds, id+" < "+hi), nil
}

// sqlOrder is the ORDER BY list of RunSQL's sort: the effective time, or a
// field with missing values last in both directions (DuckDB's default
// NULLS LAST), ties broken on seq in the same direction.
func sqlOrder(so Sort, s schema.Schema) (string, error) {
	field, desc := so.Field, so.Desc
	if field == "" {
		field, desc = "timestamp", true
	}
	dir := " ASC"
	if desc {
		dir = " DESC"
	}
	if field == "timestamp" {
		// RunSQL sorts a record with no time as the zero time: first ascending.
		if desc {
			return `"time" DESC NULLS LAST, seq DESC`, nil
		}
		return `"time" ASC NULLS FIRST, seq ASC`, nil
	}
	id, err := fieldIdent(field)
	if err != nil {
		return "", err
	}
	// RunSQL sorts a missing or bad value last in either direction. Explicit,
	// so a changed default_null_order can't reorder the rows.
	return id + dir + " NULLS LAST, seq" + dir, nil
}

// fieldIdent quotes a field path as a column name. A path that is one of
// the model's own columns has no column of its own.
func fieldIdent(path string) (string, error) {
	for _, m := range modelColumns {
		if strings.EqualFold(path, m) {
			return "", fmt.Errorf("sql: field %q has no column: the table model's %s column takes its name", path, m)
		}
	}
	return quoteIdent(path)
}

// quoteIdent double-quotes an identifier, doubling any ".
func quoteIdent(s string) (string, error) {
	if err := sqlText(s); err != nil {
		return "", err
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`, nil
}

// quoteString single-quotes a string literal, doubling any '. DuckDB string
// literals take backslashes literally.
func quoteString(s string) (string, error) {
	if err := sqlText(s); err != nil {
		return "", err
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'", nil
}

// likePattern is the quoted ILIKE pattern matching v anywhere, with the
// escape character \ and the wildcards % and _ escaped. v is lowercased as
// RunSQL lowercases search and contains values.
func likePattern(v string) (string, error) {
	v = strings.ToLower(v)
	v = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)
	return quoteString("%" + v + "%")
}

// sqlText rejects what no DuckDB literal or identifier can hold.
func sqlText(s string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("sql: %q is not valid UTF-8", s)
	}
	if strings.IndexByte(s, 0) >= 0 {
		return fmt.Errorf("sql: %q holds a NUL byte", s)
	}
	return nil
}

// selectList writes the SELECT clause: on one line when short, else one
// column per line.
func selectList(cols []string) string {
	one := "SELECT " + strings.Join(cols, ", ")
	if len(one) <= 80 {
		return one + "\n"
	}
	return "SELECT\n  " + strings.Join(cols, ",\n  ") + "\n"
}

// whereClause writes the WHERE clause: one condition on the WHERE line, more
// one per line joined by AND; none writes nothing.
func whereClause(conds []string) string {
	switch len(conds) {
	case 0:
		return ""
	case 1:
		return "WHERE " + conds[0] + "\n"
	}
	return "WHERE\n  " + strings.Join(conds, "\n  AND ") + "\n"
}
