package query

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
)

// Backend is the SQL store RunSQL reads. Relation and Rebuild are described
// by duckstore.Engine.
type Backend interface {
	Relation(source, service, stream string, cols []schema.Field) (string, []any)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	Rebuild(id int64, t time.Time, inherited bool, service, source, stream, raw string) record.Record
}

// RunSQL answers q, with SQL deciding which records match, their order,
// groups and metrics; what is shown is built from the records rebuilt from
// their raw lines.
func RunSQL(ctx context.Context, b Backend, s schema.Schema, q Query, now, runStart time.Time) (Result, error) {
	if err := q.Validate(s); err != nil {
		return Result{}, err
	}
	from, to, err := q.Range.Resolve(now, runStart)
	if err != nil {
		return Result{}, err
	}
	stream := q.Stream
	if stream == "" {
		stream = s.Stream
	}
	src := q.Source
	if src == "" {
		src = s.Source
	}
	src, _ = record.ParseSource(src)

	x := &sqlRun{ctx: ctx, b: b, s: s, q: q}
	conds := x.where(from, to)
	view := q.view()
	switch view {
	case "table":
		x.tableCols()
	case "graph":
		x.graphCols()
	case "heatmap":
		x.dur = x.need(durationField)
	default:
		if f := q.Sort.Field; f != "" && f != "timestamp" {
			sf, ok := s.Field(f)
			if !ok {
				sf = schema.Field{Path: f, Type: schema.TString}
			}
			x.sort = x.need(sf)
		}
	}
	rel, args := b.Relation(src, q.Service, stream, x.cols)
	x.with = "WITH r AS (" + rel + "), m AS (SELECT * FROM r WHERE " + conds() + ") "
	x.args = append(args, x.args...)

	res := Result{From: from, To: to, View: view}
	if to.IsZero() {
		res.To = now
	}
	var total, matched int64
	if err := x.row("SELECT (SELECT count(*) FROM r), (SELECT count(*) FROM m)", nil, &total, &matched); err != nil {
		return Result{}, err
	}
	res.StreamTotal, res.Matched = int(total), int(matched)

	switch view {
	case "table":
		err = x.table(&res)
	case "graph":
		res.Graph, err = x.graph(from, to, time.Local)
		res.Columns, res.Rows = []Column{}, []Row{}
	case "heatmap":
		res.Heatmap, err = x.heatmap(from, to, time.Local)
		res.Columns, res.Rows = []Column{}, []Row{}
	default:
		err = x.samples(&res)
	}
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

type sqlRun struct {
	ctx  context.Context
	b    Backend
	s    schema.Schema
	q    Query
	cols []schema.Field
	with string
	args []any // the WITH clause's arguments, after the relation's

	sort, dur, sev int
	groups         []int
	aggs           []int // per q.Aggregates, the value column (-1 for none)
	order          int   // value column of the order metric (-1 for count/error_rate)
	plot           int
}

// need adds a projected field and returns its index.
func (x *sqlRun) need(f schema.Field) int {
	if f.Type == "" {
		f.Type = schema.TString
	}
	for i, c := range x.cols {
		if c.Path == f.Path && c.Type == f.Type && c.Unit == f.Unit {
			return i
		}
	}
	x.cols = append(x.cols, schema.Field{Path: f.Path, Type: f.Type, Unit: f.Unit})
	return len(x.cols) - 1
}

func v(i int) string  { return "v" + strconv.Itoa(i) }
func xb(i int) string { return "x" + strconv.Itoa(i) }

// nsArg is a TIMESTAMP_NS parameter passed as Unix nanoseconds.
func nsArg(time.Time) string { return "make_timestamp_ns(CAST(? AS BIGINT))" }

// where builds the matching condition: range, search, filters and the
// heatmap cell. Its arguments go to x.args; it returns a func because the
// columns it needs are numbered as they are added.
func (x *sqlRun) where(from, to time.Time) func() string {
	var conds []string
	if from.IsZero() {
		if !to.IsZero() {
			conds = append(conds, "(_time IS NULL OR _time <= "+nsArg(to)+")")
			x.args = append(x.args, to.UnixNano())
		}
	} else {
		c := "(_time >= " + nsArg(from)
		x.args = append(x.args, from.UnixNano())
		if !to.IsZero() {
			c += " AND _time <= " + nsArg(to)
			x.args = append(x.args, to.UnixNano())
		}
		conds = append(conds, c+")")
	}
	if x.q.Search != "" {
		conds = append(conds, "contains(lower(_text), ?)")
		x.args = append(x.args, strings.ToLower(x.q.Search))
	}
	for _, c := range compile(x.s, x.q) {
		conds = append(conds, x.filter(c))
	}
	if c := x.q.Cell; c != nil {
		d := v(x.need(durationField))
		cond := "(_time IS NOT NULL AND _time >= " + nsArg(c.From) + " AND _time < " + nsArg(c.To) + " AND " + d + " IS NOT NULL AND " + d + " < ?"
		x.args = append(x.args, c.From.UnixNano(), c.To.UnixNano(), c.Hi)
		if c.Lo != nil {
			cond += " AND " + d + " >= ?"
			x.args = append(x.args, *c.Lo)
		}
		conds = append(conds, cond+")")
	}
	return func() string {
		if len(conds) == 0 {
			return "true"
		}
		return strings.Join(conds, " AND ")
	}
}

// filter is one compiled filter in SQL. Values that are bad have a NULL value
// and a true bad flag; empty ones have both NULL/false.
func (x *sqlRun) filter(c compiled) string {
	i := x.need(c.sfield)
	val, bad := v(i), xb(i)
	switch c.op {
	case "empty":
		return "(" + val + " IS NULL AND NOT " + bad + ")"
	case "not-empty":
		return "(" + val + " IS NOT NULL OR " + bad + ")"
	}
	operand := func(t target) string {
		switch c.typ {
		case schema.TInt:
			x.args = append(x.args, t.i)
		case schema.TFloat, schema.TDuration:
			x.args = append(x.args, t.f)
		case schema.TTimestamp:
			x.args = append(x.args, t.t.UnixNano())
			return nsArg(t.t)
		case schema.TBool:
			x.args = append(x.args, t.b)
		default:
			x.args = append(x.args, t.s)
		}
		return "?"
	}
	switch c.typ {
	case schema.TBool:
		return "(" + val + " = " + operand(c.targets[0]) + ")"
	case schema.TJSON:
		return "contains(lower(" + val + "), " + operand(c.targets[0]) + ")"
	case schema.TInt, schema.TFloat, schema.TDuration, schema.TTimestamp:
		if c.op == "!=" {
			return "(NOT " + bad + " AND (" + val + " IS NULL OR " + val + " <> " + operand(c.targets[0]) + "))"
		}
		return "(" + val + " " + c.op + " " + operand(c.targets[0]) + ")"
	}
	switch c.op {
	case "=":
		return "(" + val + " = " + operand(c.targets[0]) + ")"
	case "!=":
		return "(NOT " + bad + " AND (" + val + " IS NULL OR " + val + " <> " + operand(c.targets[0]) + "))"
	case "in", "not-in":
		ps := make([]string, len(c.targets))
		for j, t := range c.targets {
			ps[j] = operand(t)
		}
		in := val + " IN (" + strings.Join(ps, ", ") + ")"
		if c.op == "in" {
			return "(" + in + ")"
		}
		return "(NOT " + bad + " AND (" + val + " IS NULL OR NOT (" + in + ")))"
	case "contains":
		return "contains(lower(" + val + "), " + operand(c.targets[0]) + ")"
	}
	return "false"
}

func (x *sqlRun) query(q string, extra []any) (*sql.Rows, error) {
	args := append(append([]any{}, x.args...), extra...)
	return x.b.QueryContext(x.ctx, x.with+q, args...)
}

func (x *sqlRun) row(q string, extra []any, dest ...any) error {
	rows, err := x.query(q, extra)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := rows.Scan(dest...); err != nil {
		return err
	}
	return rows.Close()
}

// samples orders and pages the matched records, then shows them with
// samplesRows over the rebuilt records.
func (x *sqlRun) samples(res *Result) error {
	order := "_time DESC NULLS LAST, _id DESC"
	desc := x.q.Sort.Desc
	if f := x.q.Sort.Field; f == "" || f == "timestamp" {
		if f == "timestamp" && !desc {
			order = "_time ASC NULLS FIRST, _id ASC"
		}
	} else {
		dir := " ASC"
		if desc {
			dir = " DESC"
		}
		order = "(" + v(x.sort) + " IS NULL), " + v(x.sort) + dir + ", _id" + dir
	}
	limit := x.q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	off := min(x.q.Offset, res.Matched)
	res.Truncated = res.Matched-off > limit
	rows, err := x.query("SELECT _id, _time, _time_inherited, _service, _source, _stream, _raw FROM m ORDER BY "+order+" LIMIT ? OFFSET ?", []any{limit, off})
	if err != nil {
		return err
	}
	defer rows.Close()
	var page []*record.Record
	for rows.Next() {
		var id int64
		var tm sql.NullTime
		var inh sql.NullBool
		var svc, src, stream, raw string
		if err := rows.Scan(&id, &tm, &inh, &svc, &src, &stream, &raw); err != nil {
			return err
		}
		r := x.b.Rebuild(id, tm.Time, inh.Bool, svc, src, stream, raw)
		page = append(page, &r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	samplesRows(page, x.s, x.q, res)
	return nil
}

func (x *sqlRun) groupCols() {
	for _, p := range x.q.GroupBy {
		x.groups = append(x.groups, x.need(lookupField(x.s, p)))
	}
}

func (x *sqlRun) metricCol(a Aggregate) int {
	switch a.Op {
	case "count":
		return -1
	case "error_rate":
		x.sev = x.need(schema.Field{Path: "severity", Type: schema.TString})
		return -1
	}
	return x.need(metricField(x.s, a))
}

func (x *sqlRun) orderAgg() Aggregate {
	if x.q.Order == nil {
		return Aggregate{Op: "count"}
	}
	return Aggregate{Op: x.q.Order.Op, Field: x.q.Order.Field}
}

func (x *sqlRun) tableCols() {
	x.groupCols()
	for _, a := range x.q.Aggregates {
		x.aggs = append(x.aggs, x.metricCol(a))
	}
	x.order = x.metricCol(x.orderAgg())
}

func (x *sqlRun) graphCols() {
	x.groupCols()
	x.order = x.metricCol(x.orderAgg())
	plot := Aggregate{Op: "count"}
	if x.q.Plot != nil {
		plot = *x.q.Plot
	}
	x.plot = x.metricCol(plot)
}

// metricSQL is aggregate a over the rows of a group. A group with no
// usable value, or a result that is not finite, has no value (metric).
func (x *sqlRun) metricSQL(a Aggregate, col int) string {
	switch a.Op {
	case "count":
		return "CAST(count(*) AS DOUBLE)"
	case "error_rate":
		// A severity of ERROR or higher, in any case, trimmed as
		// strings.TrimSpace trims: tabs, newlines and Unicode spaces too.
		return "CAST(count(*) FILTER (WHERE upper(trim(" + v(x.sev) + ", '" + schema.Space + "')) IN ('ERROR','CRITICAL','ALERT','EMERGENCY','FATAL')) AS DOUBLE) / count(*)"
	}
	val := "CAST(" + v(col) + " AS DOUBLE)"
	switch a.Op {
	case "sum":
		return "sum(" + val + ")"
	case "avg":
		return "avg(" + val + ")"
	case "min":
		return "min(" + val + ")"
	case "max":
		return "max(" + val + ")"
	case "p50", "p90", "p95", "p99":
		p, _ := strconv.ParseFloat(a.Op[1:], 64)
		frac := strconv.FormatFloat(p/100, 'g', -1, 64)
		n := "count(" + val + ")"
		return "list_sort(list(" + val + ") FILTER (WHERE " + val + " IS NOT NULL))[greatest(1, least(" + n + ", CAST(ceil(CAST('" + frac + "' AS DOUBLE) * " + n + ") AS BIGINT)))]"
	}
	return "NULL"
}

// scanned is one group's row: group values, count, order metric, metrics.
type scanned struct {
	vals   []any
	labels []string
	empty  []bool
	key    string
	n      int
	order  sql.NullFloat64
	mets   []sql.NullFloat64
}

func (x *sqlRun) groupFields() []schema.Field {
	fs := make([]schema.Field, len(x.q.GroupBy))
	for i, p := range x.q.GroupBy {
		fs[i] = lookupField(x.s, p)
	}
	return fs
}

// groupRows runs one GROUP BY over m (restricted by cond) and returns each
// group with its labels and key. An empty or bad value is labelled
// "(empty)" and flagged; its key marks it with \x01 so it never merges
// with a real "(empty)" value.
func (x *sqlRun) groupRows(cond string, extra []any, mets []string) ([]*scanned, error) {
	gf := x.groupFields()
	sel := make([]string, 0, len(x.groups)+2+len(mets))
	var gb []string
	for i, g := range x.groups {
		sel = append(sel, v(g)+" AS g"+strconv.Itoa(i))
		gb = append(gb, "g"+strconv.Itoa(i))
	}
	sel = append(sel, "count(*)", x.metricSQL(x.orderAgg(), x.order))
	sel = append(sel, mets...)
	q := "SELECT " + strings.Join(sel, ", ") + " FROM m"
	if cond != "" {
		q += " WHERE " + cond
	}
	if len(gb) > 0 {
		q += " GROUP BY " + strings.Join(gb, ", ")
	}
	rows, err := x.query(q, extra)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*scanned
	for rows.Next() {
		g := &scanned{vals: make([]any, len(x.groups)), mets: make([]sql.NullFloat64, len(mets))}
		var n int64
		dest := make([]any, 0, len(sel))
		for i := range g.vals {
			dest = append(dest, &g.vals[i])
		}
		dest = append(dest, &n, &g.order)
		for i := range g.mets {
			dest = append(dest, &g.mets[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if n == 0 {
			continue // no rows (an ungrouped query over nothing)
		}
		g.n = int(n)
		g.labels, g.empty = make([]string, len(gf)), make([]bool, len(gf))
		parts := make([]string, len(gf))
		for i, f := range gf {
			c := sqlCell(g.vals[i], f)
			if c.V == nil {
				g.labels[i], g.empty[i], parts[i] = emptyLabel, true, "\x01"
			} else {
				g.labels[i] = fmt.Sprint(c.V)
				parts[i] = g.labels[i]
			}
		}
		g.key = strings.Join(parts, "\x00")
		out = append(out, g)
	}
	return out, rows.Err()
}

func finite(n sql.NullFloat64) (float64, bool) {
	if !n.Valid || math.IsNaN(n.Float64) || math.IsInf(n.Float64, 0) {
		return 0, false
	}
	return n.Float64, true
}

// metric is a's value from n: error rates, averages
// and sums of non-integers are rounded to 15 significant digits (Round15).
func (x *sqlRun) metric(a Aggregate, n sql.NullFloat64) (float64, bool) {
	v, ok := finite(n)
	if ok && (a.Op == "error_rate" || a.Op == "avg" || (a.Op == "sum" && metricField(x.s, a).Type != schema.TInt)) {
		v = Round15(v)
	}
	return v, ok
}

func (x *sqlRun) rank(groups []*scanned) {
	o := x.orderAgg()
	asc := x.q.Order != nil && x.q.Order.Asc
	rankStats(groups, func(g *scanned) rankStat {
		r := rankStat{n: g.n, key: g.key}
		if o.Op == "count" {
			r.v, r.ok = float64(g.n), true
		} else {
			r.v, r.ok = x.metric(o, g.order)
		}
		return r
	}, asc)
}

func (x *sqlRun) table(res *Result) error {
	mets := make([]string, len(x.q.Aggregates))
	for i, a := range x.q.Aggregates {
		mets[i] = x.metricSQL(a, x.aggs[i])
	}
	groups, err := x.groupRows("", nil, mets)
	if err != nil {
		return err
	}
	if len(x.q.GroupBy) == 0 {
		if len(groups) == 0 { // the table still shows one row for no records
			groups = []*scanned{{mets: make([]sql.NullFloat64, len(mets))}}
		}
	} else {
		x.rank(groups)
	}
	res.Groups = len(groups)
	limit := x.q.Top
	if limit <= 0 {
		limit = x.q.Limit
	}
	if limit <= 0 {
		limit = DefaultTop
	}
	limit = min(limit, MaxLimit)
	if len(groups) > limit {
		groups, res.Truncated = groups[:limit], true
	}
	gf := x.groupFields()
	for _, f := range gf {
		label := f.Label
		if label == "" {
			label = f.Path
		}
		res.Columns = append(res.Columns, Column{Path: f.Path, Label: label, Type: f.Type, Unit: f.Unit, Visualizer: f.Visualizer, Group: true})
	}
	res.Columns = append(res.Columns, metricColumn(Aggregate{Op: "count"}, x.s))
	for _, a := range x.q.Aggregates {
		res.Columns = append(res.Columns, metricColumn(a, x.s))
	}
	res.Rows = make([]Row, 0, len(groups))
	for _, g := range groups {
		cells := make([]Cell, 0, len(res.Columns))
		for i, f := range gf {
			if g.empty[i] {
				cells = append(cells, Cell{})
			} else {
				cells = append(cells, sqlCell(g.vals[i], f))
			}
		}
		cells = append(cells, Cell{V: int64(g.n)})
		for i, a := range x.q.Aggregates {
			val, ok := x.metric(a, g.mets[i])
			cells = append(cells, metricCell(val, ok, metricColumn(a, x.s).Type))
		}
		res.Rows = append(res.Rows, Row{Cells: cells})
	}
	return nil
}

// timeBounds returns the earliest and latest _time of m's rows with cond.
func (x *sqlRun) timeBounds(cond string) (first, last time.Time, n int64, err error) {
	var lo, hi sql.NullTime
	err = x.row("SELECT min(_time), max(_time), count(_time) FROM m WHERE "+cond, nil, &lo, &hi, &n)
	return lo.Time, hi.Time, n, err
}

// bucketTable is a VALUES table b(i, s) of bucket starts.
func bucketTable(starts []time.Time) (string, []any) {
	rows := make([]string, len(starts))
	args := make([]any, 0, 2*len(starts))
	for i, s := range starts {
		rows[i] = "(?, make_timestamp_ns(CAST(? AS BIGINT)))"
		args = append(args, i, s.UnixNano())
	}
	return "(VALUES " + strings.Join(rows, ", ") + ") b(i, s)", args
}

func (x *sqlRun) graph(from, to time.Time, loc *time.Location) (*Graph, error) {
	plot := Aggregate{Op: "count"}
	if x.q.Plot != nil {
		plot = *x.q.Plot
	}
	g := &Graph{Metric: metricColumn(plot, x.s), GroupBy: append([]string(nil), x.q.GroupBy...), Starts: []time.Time{}, Series: []Series{}}
	first, last, n, err := x.timeBounds("_time IS NOT NULL")
	if err != nil {
		return nil, err
	}
	starts, size, name, last, err := axis(first, last, n > 0, x.q.Bucket, from, to, loc)
	if err != nil {
		return nil, err
	}
	g.Starts, g.Bucket, g.BucketSeconds = starts, name, size.Seconds()
	if n == 0 {
		return g, nil
	}
	inRange := "_time IS NOT NULL AND _time >= make_timestamp_ns(CAST(? AS BIGINT)) AND _time <= make_timestamp_ns(CAST(? AS BIGINT))"
	rangeArgs := []any{starts[0].UnixNano(), last.UnixNano()}
	groups, err := x.groupRows(inRange, rangeArgs, nil)
	if err != nil {
		return nil, err
	}
	x.rank(groups)

	top := len(groups)
	if len(x.q.GroupBy) > 0 {
		top = x.q.Top
		if top <= 0 {
			top = DefaultTop
		}
		top = min(top, MaxGraphTop)
		g.Groups, g.Top = len(groups), top
	}
	kept := groups
	if len(kept) > top {
		kept = kept[:top]
	}

	// Series per bucket: each row's series is its top group's rank, or -1
	// for "other".
	bt, bargs := bucketTable(starts)
	series := "0"
	join := ""
	var targs []any
	if len(x.q.GroupBy) > 0 {
		gf := x.groupFields()
		trows := make([]string, len(kept))
		for r, k := range kept {
			cells := []string{"?"}
			targs = append(targs, r)
			for i, f := range gf {
				cells = append(cells, groupParam(f))
				targs = append(targs, groupArg(k.vals[i], f))
			}
			trows[r] = "(" + strings.Join(cells, ", ") + ")"
		}
		cols := []string{"r"}
		var on []string
		for i := range gf {
			cols = append(cols, "k"+strconv.Itoa(i))
			on = append(on, "mm."+v(x.groups[i])+" IS NOT DISTINCT FROM tk.k"+strconv.Itoa(i))
		}
		join = " LEFT JOIN (VALUES " + strings.Join(trows, ", ") + ") tk(" + strings.Join(cols, ", ") + ") ON " + strings.Join(on, " AND ")
		series = "coalesce(tk.r, -1)"
	}
	q := "SELECT " + series + " AS sr, b.i, " + x.metricSQL(plot, x.plot) + " FROM (SELECT * FROM m WHERE " + inRange + ") mm ASOF JOIN " + bt + " ON mm._time >= b.s" + join + " GROUP BY 1, 2"
	rows, err := x.query(q, append(append(append([]any{}, rangeArgs...), bargs...), targs...))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	vals := map[int][]*float64{}
	get := func(sr int) []*float64 {
		vs := vals[sr]
		if vs == nil {
			vs = make([]*float64, len(starts))
			if plot.Op == "count" {
				for i := range vs {
					zero := 0.0
					vs[i] = &zero
				}
			}
			vals[sr] = vs
		}
		return vs
	}
	for rows.Next() {
		var sr, bi int64
		var m sql.NullFloat64
		if err := rows.Scan(&sr, &bi, &m); err != nil {
			return nil, err
		}
		if f, ok := x.metric(plot, m); ok {
			get(int(sr))[bi] = &f
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(x.q.GroupBy) == 0 {
		g.Series = append(g.Series, Series{Label: "all", Group: []string{}, Count: groups[0].n, Values: get(0)})
		return g, nil
	}
	for r, k := range kept {
		g.Series = append(g.Series, Series{Label: strings.Join(k.labels, " · "), Group: k.labels, Count: k.n, Values: get(r)})
	}
	if len(groups) > top {
		rest := 0
		for _, k := range groups[top:] {
			rest += k.n
		}
		g.Series = append(g.Series, Series{Label: "other", Group: []string{}, Other: true, Count: rest, Values: get(-1)})
	}
	return g, nil
}

// groupParam and groupArg pass a scanned group value back to SQL with its
// type kept exactly.
func groupParam(f schema.Field) string {
	switch f.Type {
	case schema.TInt:
		return "CAST(? AS BIGINT)"
	case schema.TFloat, schema.TDuration:
		return "CAST(? AS DOUBLE)"
	case schema.TBool:
		return "CAST(? AS BOOLEAN)"
	case schema.TTimestamp:
		return "make_timestamp_ns(CAST(? AS BIGINT))"
	}
	return "CAST(? AS VARCHAR)"
}

func groupArg(v any, f schema.Field) any {
	if t, ok := v.(time.Time); ok {
		return t.UnixNano()
	}
	return v
}

func (x *sqlRun) heatmap(from, to time.Time, loc *time.Location) (*Heatmap, error) {
	d := v(x.dur)
	h := &Heatmap{}
	var skipped int64
	var minPos, maxD sql.NullFloat64
	var first, last sql.NullTime
	var n int64
	ok := d + " IS NOT NULL AND _time IS NOT NULL"
	err := x.row("SELECT count(*) FILTER (WHERE NOT ("+ok+")), min("+d+") FILTER (WHERE "+ok+" AND "+d+" > 0), max("+d+") FILTER (WHERE "+ok+"), min(_time) FILTER (WHERE "+ok+"), max(_time) FILTER (WHERE "+ok+"), count(*) FILTER (WHERE "+ok+") FROM m",
		nil, &skipped, &minPos, &maxD, &first, &last, &n)
	if err != nil {
		return nil, err
	}
	h.Skipped = int(skipped)
	lo, hi := 1.0, math.Inf(-1)
	if minPos.Valid {
		lo = minPos.Float64
	}
	if maxD.Valid {
		hi = maxD.Float64
	}
	starts, size, name, _, err := axis(first.Time, last.Time, n > 0, x.q.Bucket, from, to, loc)
	if err != nil {
		return nil, err
	}
	h.Bucket, h.BucketSeconds, h.Starts = name, size.Seconds(), starts
	h.Bands = bands(lo, hi)
	h.Counts = make([][]int, len(starts))
	for i := range h.Counts {
		h.Counts[i] = make([]int, len(h.Bands))
	}
	if n == 0 {
		return h, nil
	}
	bt, bargs := bucketTable(starts)
	hrows := make([]string, len(h.Bands))
	for i, band := range h.Bands {
		hrows[i] = "(" + strconv.Itoa(i) + ", CAST('" + strconv.FormatFloat(band.Hi, 'g', -1, 64) + "' AS DOUBLE))"
	}
	q := "SELECT b.i, coalesce(hb.k + 1, 0), count(*) FROM (SELECT _time, " + d + " AS d FROM m WHERE " + ok + ") mm ASOF JOIN " + bt + " ON mm._time >= b.s ASOF LEFT JOIN (VALUES " + strings.Join(hrows, ", ") + ") hb(k, hi) ON mm.d >= hb.hi GROUP BY 1, 2"
	rows, err := x.query(q, bargs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ti, bi, c int64
		if err := rows.Scan(&ti, &bi, &c); err != nil {
			return nil, err
		}
		if int(bi) >= len(h.Bands) {
			h.Skipped += int(c)
			continue
		}
		h.Counts[ti][bi] += int(c)
		h.Total += int(c)
		h.Max = max(h.Max, h.Counts[ti][bi])
	}
	return h, rows.Err()
}

// sqlCell is cell() for a value scanned from a converted column.
func sqlCell(val any, f schema.Field) Cell {
	switch t := val.(type) {
	case nil:
		return Cell{}
	case int64:
		if t > maxExactInt || t < -maxExactInt {
			return Cell{V: strconv.FormatInt(t, 10)}
		}
		return Cell{V: t}
	case float64:
		return Cell{V: t}
	case bool:
		return Cell{V: t}
	case time.Time:
		return Cell{V: t.UTC().Format(time.RFC3339Nano)}
	case string:
		return Cell{V: t}
	}
	return Cell{V: fmt.Sprint(val)}
}
