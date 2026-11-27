package query

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
	"github.com/jimbeveridge/loghorn/internal/sqlcolor"
	"github.com/jimbeveridge/loghorn/internal/sqlfmt"
)

// Column describes one result column.
type Column struct {
	Path       string      `json:"path"`
	Label      string      `json:"label"`
	Type       schema.Type `json:"type"`
	Unit       string      `json:"unit"`
	Visualizer string      `json:"visualizer"`
	Group      bool        `json:"group,omitempty"` // a group-by column (table view)
}

// Cell is one value; V is nil for empty, the raw string with Bad set when the
// value does not fit the column's type.
type Cell struct {
	V   any  `json:"v"`
	Bad bool `json:"bad,omitempty"`
	// SQL colours a string in a column shown with the sql visualizer.
	SQL []sqlcolor.Token `json:"sql,omitempty"`
}

// Row is one matched record.
type Row struct {
	Seq           int       `json:"seq"`
	Time          time.Time `json:"time"`
	TimeInherited bool      `json:"timeInherited,omitempty"`
	// SpanRoot is true for a span record with no parent span, the entry
	// point to the aggregate operation view.
	SpanRoot bool   `json:"spanRoot,omitempty"`
	Cells    []Cell `json:"cells"`
}

// Result is the answer to a query. A zero From means unbounded.
type Result struct {
	View        string    `json:"view"`
	Groups      int       `json:"groups,omitempty"` // table: groups before the limit
	Columns     []Column  `json:"columns"`
	Rows        []Row     `json:"rows"`
	Graph       *Graph    `json:"graph,omitempty"`
	Heatmap     *Heatmap  `json:"heatmap,omitempty"`
	Matched     int       `json:"matched"`
	StreamTotal int       `json:"streamTotal"`
	Truncated   bool      `json:"truncated"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
}

// maxExactInt is the largest magnitude a browser number holds exactly.
const maxExactInt = 1 << 53

type compiled struct {
	field   string
	typ     schema.Type
	sfield  schema.Field
	op      string
	targets []target
}

func compile(s schema.Schema, q Query) []compiled {
	out := make([]compiled, 0, len(q.Filters))
	for _, f := range q.Filters {
		t := fieldType(s, f.Field)
		sf, ok := s.Field(f.Field)
		if !ok {
			sf = schema.Field{Path: f.Field, Type: t}
		}
		c := compiled{field: f.Field, typ: t, sfield: sf, op: f.Op}
		for _, v := range f.Values {
			tg, _ := parseOperand(t, v) // Validate has checked
			if t == schema.TString || t == schema.TJSON {
				if f.Op == "contains" {
					tg.s = strings.ToLower(v)
				}
			}
			c.targets = append(c.targets, tg)
		}
		out = append(out, c)
	}
	return out
}

// samplesRows fills res's columns and rows from the page of matched records.
func samplesRows(matched []*record.Record, s schema.Schema, q Query, res *Result) {
	paths := q.Columns
	if len(paths) == 0 {
		paths = intervalColumns(matched, s)
	}
	if len(paths) == 0 {
		// Nothing matched, so no field is present in the interval: keep the
		// stream's own default columns, or the table loses its headings.
		paths = s.Columns
	}
	res.Columns = []Column{}
	fields := make([]schema.Field, len(paths))
	for i, p := range paths {
		f, ok := s.Field(p)
		if !ok {
			f = schema.Field{Path: p, Type: schema.TString, Visualizer: schema.DefaultVisualizer(p, schema.TString)}
		}
		fields[i] = f
		label := f.Label
		if label == "" {
			label = p
		}
		res.Columns = append(res.Columns, Column{Path: p, Label: label, Type: f.Type, Unit: f.Unit, Visualizer: f.Visualizer})
	}
	res.Rows = make([]Row, 0, len(matched))
	for _, r := range matched {
		row := Row{Seq: r.Seq, Time: r.Time, TimeInherited: r.TimeInherited, Cells: make([]Cell, len(fields))}
		if r.Source == record.SourceOtel {
			ps, has := r.Fields["parent_span_id"]
			str, isStr := ps.(string)
			row.SpanRoot = !has || ps == nil || (isStr && str == "")
		}
		for i, f := range fields {
			row.Cells[i] = cell(r.Fields[f.Path], f)
			if s, ok := row.Cells[i].V.(string); ok && f.Visualizer == schema.VSQL && !row.Cells[i].Bad {
				row.Cells[i].SQL = sqlcolor.Tokens(s, sqlfmt.SystemOf(r.Fields))
			}
		}
		res.Rows = append(res.Rows, row)
	}
}

// intervalColumns is the default column list inferred from the matched
// records: each schema field's Count becomes the number of matched records
// holding the path, and fields with none are dropped. Types come from s.
func intervalColumns(matched []*record.Record, s schema.Schema) []string {
	counts := map[string]int{}
	for _, r := range matched {
		for p := range r.Fields {
			counts[p]++
		}
	}
	fs := make([]schema.Field, 0, len(s.Fields))
	for _, f := range s.Fields {
		if f.Count = counts[f.Path]; f.Count > 0 {
			fs = append(fs, f)
		}
	}
	return schema.DefaultColumns(s.Source, fs)
}

// cell encodes a record value for the browser.
func cell(raw any, f schema.Field) Cell {
	v := schema.Convert(raw, f)
	switch {
	case v.Bad:
		return Cell{V: rawString(raw), Bad: true}
	case v.Empty:
		return Cell{}
	}
	switch f.Type {
	case schema.TInt:
		if v.Int > maxExactInt || v.Int < -maxExactInt {
			return Cell{V: strconv.FormatInt(v.Int, 10)}
		}
		return Cell{V: v.Int}
	case schema.TFloat, schema.TDuration:
		return Cell{V: v.Num}
	case schema.TTimestamp:
		return Cell{V: v.Time.UTC().Format(time.RFC3339Nano)}
	case schema.TBool:
		return Cell{V: v.Bool}
	case schema.TJSON:
		// JSON text, not the decoded value: the browser's JSON.parse would
		// round integers above 2^53, and json.Marshal keeps json.Number digits.
		return Cell{V: rawString(raw)}
	}
	return Cell{V: v.Str}
}

func rawString(raw any) string {
	switch x := raw.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	return string(b)
}
