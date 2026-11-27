package schema

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jimbeveridge/loghorn/internal/record"
)

const maxDefaultColumns = 8

// kind is what one observed value looks like, before the path's values are
// merged into a single Type.
type kind int

const (
	kNone kind = iota // null or empty: says nothing about the type
	kInt
	kFloat
	kBool
	kJSON
	kDuration
	kTimestamp
	kString
)

func classify(v any) kind {
	switch x := v.(type) {
	case nil:
		return kNone
	case json.Number:
		if _, err := strconv.ParseInt(x.String(), 10, 64); err == nil {
			return kInt
		}
		return kFloat
	case bool:
		return kBool
	case []any:
		return kJSON
	case string:
		switch {
		case x == "":
			return kNone
		case isRFC3339(x):
			return kTimestamp
		case isUnitDuration(x):
			return kDuration
		}
		return kString
	}
	return kString
}

func isRFC3339(s string) bool {
	_, err := time.Parse(time.RFC3339, s)
	return err == nil
}

// isUnitDuration is a duration string that carries a unit; a bare "42" is
// just text for inference (a saved override can still make it a duration).
func isUnitDuration(s string) bool {
	if _, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return false
	}
	_, ok := ParseDuration(s)
	return ok
}

// KindSet is a set of observed value kinds, one bit per kind, plus Lines.
type KindSet uint16

// Lines is set in a KindSet when some string value holds a newline, so a
// string field shows as multiline text. It is not a kind: types ignore it.
const Lines KindSet = 1 << 15

// Classify returns the one-kind set of v, with Lines for a multiline string.
func Classify(v any) KindSet {
	k := KindSet(1) << classify(v)
	if str, ok := v.(string); ok && Multiline(str) {
		k |= Lines
	}
	return k
}

// Tally is what inference keeps per path: how many records hold the path
// (nulls included) and the kinds of value seen.
type Tally struct {
	Count int
	Kinds KindSet
}

// TallyType is the type inference gives a path with kinds k: one kind gives
// that type, int with float gives float, anything else (or nothing but
// nulls) gives string; timestamp and receiveTimestamp are always timestamp.
func TallyType(path string, k KindSet) Type {
	if path == "timestamp" || path == "receiveTimestamp" {
		return TTimestamp
	}
	k &^= 1<<kNone | Lines
	switch k {
	case 1 << kInt:
		return TInt
	case 1 << kFloat, 1<<kInt | 1<<kFloat:
		return TFloat
	case 1 << kBool:
		return TBool
	case 1 << kJSON:
		return TJSON
	case 1 << kDuration:
		return TDuration
	case 1 << kTimestamp:
		return TTimestamp
	}
	return TString
}

// FromTallies builds a stream's schema from its per-path tallies. source must
// already be parsed (or is kept as given).
func FromTallies(source, stream string, tallies map[string]Tally) Schema {
	s := Schema{Stream: stream, Source: source, Fields: make([]Field, 0, len(tallies))}
	for path, a := range tallies {
		t := TallyType(path, a.Kinds)
		vis := DefaultVisualizer(path, t)
		if vis == VRaw && t == TString && a.Kinds&Lines != 0 {
			vis = VMultiline
		}
		s.Fields = append(s.Fields, Field{
			Path: path, Type: t, Inferred: t, Count: a.Count,
			Visualizer: vis,
		})
	}
	sort.Slice(s.Fields, func(i, j int) bool { return s.Fields[i].Path < s.Fields[j].Path })
	s.Columns = DefaultColumns(source, s.Fields)
	return s
}

// alwaysColumns are shown by default whenever present, after the source's
// lead and in this order, however many there are; then, for spans, kind;
// then every attribute by path (otel.attributes.* for logs, each field from
// the span's attributes for spans).
var alwaysColumns = []string{"timestamp", "severity", "message", "latency", "duration_ms", "span_id"}

// DefaultColumns picks the columns shown when a query names none: the
// source's lead fields that are present (timestamp, severity, message for
// log; timestamp, name, duration_ms for otel), then the always-shown fields
// (alwaysColumns, for spans kind, then attributes by path), then the rest by Count
// descending while fewer than maxDefaultColumns are picked. Hidden, labels.*
// and json fields are skipped.
func DefaultColumns(source string, fields []Field) []string {
	lead := []string{"timestamp", "severity", "message"}
	if src, _ := record.ParseSource(source); src == record.SourceOtel {
		lead = []string{"timestamp", "name", "duration_ms"}
	}
	byPath := map[string]Field{}
	for _, f := range fields {
		byPath[f.Path] = f
	}
	shown := func(f Field) bool {
		return !f.Hidden && !strings.HasPrefix(f.Path, "labels.") && f.Type != TJSON
	}
	var cols []string
	used := map[string]bool{}
	pick := func(p string) {
		if f, ok := byPath[p]; ok && !used[p] && shown(f) {
			cols = append(cols, p)
			used[p] = true
		}
	}
	for _, p := range lead {
		if _, ok := byPath[p]; ok {
			cols = append(cols, p)
			used[p] = true
		}
	}
	for _, p := range alwaysColumns {
		pick(p)
	}
	isAttr := func(p string) bool { return strings.HasPrefix(p, "otel.attributes.") }
	if src, _ := record.ParseSource(source); src == record.SourceOtel {
		pick("kind")
		isAttr = record.IsSpanAttribute
	}
	var attrs []string
	for _, f := range fields {
		if isAttr(f.Path) {
			attrs = append(attrs, f.Path)
		}
	}
	sort.Strings(attrs)
	for _, p := range attrs {
		pick(p)
	}
	rest := make([]Field, 0, len(fields))
	for _, f := range fields {
		if used[f.Path] || !shown(f) {
			continue
		}
		rest = append(rest, f)
	}
	sort.SliceStable(rest, func(i, j int) bool {
		if rest[i].Count != rest[j].Count {
			return rest[i].Count > rest[j].Count
		}
		return rest[i].Path < rest[j].Path
	})
	for _, f := range rest {
		if len(cols) >= maxDefaultColumns {
			break
		}
		cols = append(cols, f.Path)
	}
	return cols
}

// Multiline reports whether s has more than one line once trailing
// whitespace is dropped: a message that ends in a newline is one line.
func Multiline(s string) bool {
	return strings.Contains(strings.TrimRight(s, " \t\r\n"), "\n")
}

// isStackPath is a path whose last segment names a stack trace: stack,
// stacktrace or stack_trace in any case (exception.stacktrace, error.stack,
// stackTrace).
func isStackPath(path string) bool {
	last := strings.ToLower(path[strings.LastIndex(path, ".")+1:])
	return last == "stack" || last == "stacktrace" || last == "stack_trace"
}

// DefaultVisualizer picks how an unconfigured field is shown from its path
// and type. FromTallies also shows a string field as multiline when one of its
// values holds a newline (the Lines kind); that needs the values, so it is
// not done here.
func DefaultVisualizer(path string, t Type) string {
	switch {
	case path == "pid", path == "httpRequest.status":
		return VPlain
	case path == "insertId", path == "trace", path == "spanId", path == "hostname", strings.HasPrefix(path, "labels."):
		return VRaw
	case path == "timestamp", path == "receiveTimestamp":
		return VLocalTime
	case path == "severity":
		return VSeverity
	case path == "httpRequest.latency":
		return VDuration
	case path == "httpRequest.requestUrl":
		return VLink
	case path == "db.query.text", path == "db.statement": // OTel's SQL text, current and older name
		return VSQL
	case t == TString && isStackPath(path):
		return VMultiline
	}
	switch t {
	case TInt, TFloat:
		return VNumber
	case TDuration:
		return VDuration
	case TTimestamp:
		return VLocalTime
	case TJSON:
		return VJSON
	}
	return VRaw
}
