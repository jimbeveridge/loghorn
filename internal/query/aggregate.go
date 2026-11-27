package query

import (
	"math"
	"sort"
	"strconv"

	"github.com/jimbeveridge/loghorn/internal/schema"
)

// AggregateOps lists the metric operators; count is implicit and always first.
var AggregateOps = []string{"sum", "avg", "min", "max", "p50", "p90", "p95", "p99", "error_rate"}

// Buckets lists the valid time bucket widths for the graph view.
var Buckets = []string{"auto", "1s", "5s", "10s", "30s", "1m", "5m", "15m", "1h", "6h", "1d"}

// emptyLabel is the group label of an empty or bad group value.
const emptyLabel = "(empty)"

func isNumericType(t schema.Type) bool {
	return t == schema.TInt || t == schema.TFloat || t == schema.TDuration
}

func inList(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// view returns the query's view, "samples" when unset.
func (q Query) view() string {
	if q.View == "" {
		return "samples"
	}
	return q.View
}

// lookupField returns the schema field for path, or a string field.
func lookupField(s schema.Schema, path string) schema.Field {
	if f, ok := s.Field(path); ok {
		if f.Type == "" {
			f.Type = schema.TString
		}
		return f
	}
	return schema.Field{Path: path, Type: schema.TString, Visualizer: schema.DefaultVisualizer(path, schema.TString)}
}

// metricColumn describes the result column of aggregate a.
func metricColumn(a Aggregate, s schema.Schema) Column {
	if a.Op == "count" {
		return Column{Path: "count", Label: "count", Type: schema.TInt, Visualizer: "number"}
	}
	if a.Op == "error_rate" {
		return Column{Path: "error_rate", Label: "error rate", Type: schema.TFloat, Visualizer: "number"}
	}
	f := lookupField(s, a.Field)
	name := f.Label
	if name == "" {
		name = f.Path
	}
	typ := f.Type
	if a.Op == "avg" && typ == schema.TInt {
		typ = schema.TFloat
	}
	vis := f.Visualizer
	if (typ == schema.TInt || typ == schema.TFloat) && vis != "number" {
		vis = "number"
	}
	return Column{
		Path:       a.Op + "(" + a.Field + ")",
		Label:      a.Op + " " + name,
		Type:       typ,
		Unit:       f.Unit,
		Visualizer: vis,
	}
}

// metricField is the schema field aggregate a reads; error_rate reads none.
func metricField(s schema.Schema, a Aggregate) schema.Field {
	if a.Op == "error_rate" {
		return schema.Field{}
	}
	return lookupField(s, a.Field)
}

// Round15 rounds v to 15 significant digits, the digits a float64 holds
// reliably, so arithmetic noise (0.30000000000000004) goes. NaN and ±Inf
// pass through.
func Round15(v float64) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	r, err := strconv.ParseFloat(strconv.FormatFloat(v, 'g', 15, 64), 64)
	if err != nil {
		return v
	}
	return r
}

// metricCell encodes a metric value for a column of type typ.
func metricCell(v float64, ok bool, typ schema.Type) Cell {
	if !ok {
		return Cell{}
	}
	if typ == schema.TInt {
		if math.Abs(v) >= 1<<62 {
			return Cell{} // beyond what an int64 holds
		}
		n := int64(math.Round(v))
		if n > maxExactInt || n < -maxExactInt {
			return Cell{V: strconv.FormatInt(n, 10)}
		}
		return Cell{V: n}
	}
	return Cell{V: v}
}

// rankStat is what ranking reads of a group: its record count, its order
// metric (ok false when it has none) and its key.
type rankStat struct {
	n   int
	v   float64
	ok  bool
	key string
}

// rankStats orders groups by their stats, as rankGroups describes.
func rankStats[G any](groups []G, stat func(G) rankStat, asc bool) {
	type ranked struct {
		g G
		rankStat
	}
	rs := make([]ranked, len(groups))
	for i, g := range groups {
		rs[i] = ranked{g, stat(g)}
	}
	sort.SliceStable(rs, func(a, b int) bool {
		x, y := rs[a], rs[b]
		if x.ok != y.ok {
			return x.ok
		}
		if x.ok && x.v != y.v {
			if asc {
				return x.v < y.v
			}
			return x.v > y.v
		}
		if x.n != y.n {
			return x.n > y.n
		}
		return x.key < y.key
	})
	for i, r := range rs {
		groups[i] = r.g
	}
}
