package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/jimbeveridge/loghorn/internal/record"
)

// tallied is the schema of a log stream whose records hold fields, each
// value tallied with Classify as the writer does.
func tallied(fields ...map[string]any) Schema {
	tl := map[string]Tally{}
	for _, f := range fields {
		for path, v := range f {
			t := tl[path]
			t.Count++
			t.Kinds |= Classify(v)
			tl[path] = t
		}
	}
	return FromTallies(record.SourceLog, "a", tl)
}

func typeOf(t *testing.T, s Schema, path string) Type {
	t.Helper()
	f, ok := s.Field(path)
	if !ok {
		t.Fatalf("no field %q", path)
	}
	return f.Type
}

func TestInferTypes(t *testing.T) {
	s := tallied(
		map[string]any{
			"timestamp": "2026-11-17T17:00:01.123Z", "message": "query ok",
			"databaseContext.metadata.rowsAffected": json.Number("0"),
			"databaseContext.metadata.latency":      "0.4ms",
			"ok":                                    true, "tags": []any{"a"}, "ratio": json.Number("1.5"),
		},
		map[string]any{
			"timestamp": "2026-11-17T17:00:02.123Z", "message": "slow",
			"databaseContext.metadata.rowsAffected": json.Number("42"),
			"databaseContext.metadata.latency":      "87ms",
			"ok":                                    false, "tags": []any{}, "ratio": json.Number("2"),
		},
	)
	want := map[string]Type{
		"databaseContext.metadata.rowsAffected": TInt, "databaseContext.metadata.latency": TDuration,
		"timestamp": TTimestamp, "message": TString, "ok": TBool, "tags": TJSON, "ratio": TFloat,
	}
	for p, ty := range want {
		if got := typeOf(t, s, p); got != ty {
			t.Errorf("%s=%s want %s", p, got, ty)
		}
	}
	f, _ := s.Field("databaseContext.metadata.latency")
	if f.Count != 2 || f.Inferred != TDuration || f.Visualizer != VDuration {
		t.Errorf("%+v", f)
	}
	for i := 1; i < len(s.Fields); i++ {
		if s.Fields[i-1].Path >= s.Fields[i].Path {
			t.Fatal("fields not sorted")
		}
	}
}

func TestInferMixedIsString(t *testing.T) {
	s := tallied(
		map[string]any{"rows": json.Number("3")},
		map[string]any{"rows": "n/a"},
	)
	if got := typeOf(t, s, "rows"); got != TString {
		t.Fatalf("rows=%s", got)
	}
}

func TestInferNumberNeverTimestamp(t *testing.T) {
	s := tallied(map[string]any{"at": json.Number("1763400000123"), "bare": "42"})
	if got := typeOf(t, s, "at"); got != TInt {
		t.Errorf("at=%s", got)
	}
	if got := typeOf(t, s, "bare"); got != TString {
		t.Errorf("bare=%s", got)
	}
	// the well-known timestamp fields are timestamps whatever they hold
	s = tallied(map[string]any{"receiveTimestamp": json.Number("1")})
	if got := typeOf(t, s, "receiveTimestamp"); got != TTimestamp {
		t.Errorf("receiveTimestamp=%s", got)
	}
}

func TestInferNullsIgnored(t *testing.T) {
	s := tallied(
		map[string]any{"n": nil}, map[string]any{"n": json.Number("3")}, map[string]any{"n": ""},
	)
	f, _ := s.Field("n")
	if f.Type != TInt || f.Count != 3 {
		t.Errorf("%+v", f)
	}
}

func TestDefaultColumns(t *testing.T) {
	f := map[string]any{"timestamp": "2026-11-17T17:00:01Z", "severity": "INFO", "message": "m", "labels.schema": "a", "tags": []any{"x"}}
	for i := 0; i < 10; i++ {
		f[fmt.Sprintf("f%02d", i)] = "v"
	}
	s := tallied(f, map[string]any{"f09": "v", "f08": "v"})
	want := []string{"timestamp", "severity", "message", "f08", "f09", "f00", "f01", "f02"}
	if !reflect.DeepEqual(s.Columns, want) {
		t.Fatalf("columns=%v want %v", s.Columns, want)
	}
	// absent well-known columns are skipped
	s = tallied(map[string]any{"x": "1"})
	if !reflect.DeepEqual(s.Columns, []string{"x"}) {
		t.Errorf("columns=%v", s.Columns)
	}
}

func TestDefaultVisualizer(t *testing.T) {
	for _, c := range []struct {
		p string
		t Type
		w string
	}{{"x", TInt, VNumber}, {"x", TFloat, VNumber}, {"x", TDuration, VDuration}, {"x", TTimestamp, VLocalTime}, {"x", TJSON, VJSON}, {"severity", TString, VSeverity}, {"x", TString, VRaw}, {"x", TBool, VRaw}} {
		if got := DefaultVisualizer(c.p, c.t); got != c.w {
			t.Errorf("%s/%s=%s want %s", c.p, c.t, got, c.w)
		}
	}
}

func TestDefaultVisualizerLogEntry(t *testing.T) {
	for p, w := range map[string]string{
		"pid": VPlain, "httpRequest.status": VPlain,
		"insertId": VRaw, "trace": VRaw, "spanId": VRaw, "hostname": VRaw, "labels.schema": VRaw,
		"timestamp": VLocalTime, "receiveTimestamp": VLocalTime, "severity": VSeverity,
		"httpRequest.latency": VDuration, "httpRequest.requestUrl": VLink,
	} {
		if got := DefaultVisualizer(p, TString); got != w {
			t.Errorf("%s=%s want %s", p, got, w)
		}
	}
	for _, p := range []string{"db.query.text", "db.statement"} {
		if got := DefaultVisualizer(p, TString); got != VSQL {
			t.Errorf("%s=%s want sql", p, got)
		}
	}
	if got := DefaultVisualizer("other", TInt); got != VNumber {
		t.Errorf("int=%s", got)
	}
}

func TestDefaultColumnsSpanLead(t *testing.T) {
	var fs []Field
	for i, p := range []string{"timestamp", "name", "duration_ms", "severity", "trace_id"} {
		fs = append(fs, Field{Path: p, Type: TString, Count: 10 - i})
	}
	got := DefaultColumns("otel", fs)
	if !reflect.DeepEqual(got[:3], []string{"timestamp", "name", "duration_ms"}) {
		t.Fatalf("otel lead = %v", got)
	}
	got = DefaultColumns("", fs)
	if got[0] != "timestamp" || got[1] != "severity" {
		t.Fatalf("log lead = %v", got)
	}
}

// The always-shown fields are kept regardless of the cap, after the lead and
// before any count-ordered filler; filler only fills up to maxDefaultColumns.
func TestDefaultColumnsAlways(t *testing.T) {
	var fs []Field
	add := func(p string, n int) { fs = append(fs, Field{Path: p, Type: TString, Count: n}) }
	add("timestamp", 100)
	add("severity", 100)
	add("message", 100)
	add("otel.resource.host.name", 100)
	add("loghorn.run.id", 100)
	add("latency", 40)
	add("span_id", 30)
	add("duration_ms", 20)
	for _, p := range []string{"otel.attributes.z", "otel.attributes.a.b", "otel.attributes.c", "otel.attributes.d", "otel.attributes.e", "otel.attributes.f"} {
		add(p, 5)
	}
	fs = append(fs, Field{Path: "otel.attributes.hidden", Type: TString, Count: 5, Hidden: true})
	fs = append(fs, Field{Path: "otel.attributes.obj", Type: TJSON, Count: 5})
	got := DefaultColumns("", fs)
	want := []string{"timestamp", "severity", "message", "latency", "duration_ms", "span_id",
		"otel.attributes.a.b", "otel.attributes.c", "otel.attributes.d", "otel.attributes.e", "otel.attributes.f", "otel.attributes.z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("columns=%v\nwant     %v", got, want)
	}

	// Fewer always-shown fields: filler by count tops up to the cap.
	got = DefaultColumns("", []Field{
		{Path: "timestamp", Count: 9}, {Path: "message", Count: 9}, {Path: "otel.attributes.x", Count: 1},
		{Path: "a", Count: 8}, {Path: "b", Count: 7}, {Path: "c", Count: 6}, {Path: "d", Count: 5}, {Path: "e", Count: 4}, {Path: "f", Count: 3},
	})
	want = []string{"timestamp", "message", "otel.attributes.x", "a", "b", "c", "d", "e"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("columns=%v\nwant     %v", got, want)
	}

	// Spans keep their lead, then the always-shown fields.
	got = DefaultColumns("otel", []Field{
		{Path: "timestamp", Count: 9}, {Path: "name", Count: 9}, {Path: "duration_ms", Count: 9},
		{Path: "severity", Count: 9}, {Path: "span_id", Count: 9}, {Path: "trace_id", Count: 9},
	})
	want = []string{"timestamp", "name", "duration_ms", "severity", "span_id", "trace_id"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("otel columns=%v\nwant     %v", got, want)
	}
}

// Spans always show kind, span_id and every attribute, after the span lead.
func TestDefaultColumnsSpanAttributes(t *testing.T) {
	var fs []Field
	for _, p := range []string{"timestamp", "name", "duration_ms", "severity", "trace_id", "parent_span_id",
		"kind", "span_id", "span.type", "end", "otel.resource.host.name", "otel.scope.name",
		"db.system.name", "db.query.text", "db.namespace", "db.operation.name", "db.collection.name", "db.response.returned_rows", "attributes.name"} {
		fs = append(fs, Field{Path: p, Type: TString, Count: 10})
	}
	got := DefaultColumns("otel", fs)
	want := []string{"timestamp", "name", "duration_ms", "severity", "span_id", "kind",
		"attributes.name", "db.collection.name", "db.namespace", "db.operation.name", "db.query.text", "db.response.returned_rows", "db.system.name"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("columns=%v\nwant     %v", got, want)
	}
}

func TestDefaultVisualizerStackPaths(t *testing.T) {
	for _, p := range []string{"stack", "stacktrace", "stack_trace", "stackTrace", "error.stack", "exception.stacktrace", "otel.attributes.exception.stacktrace"} {
		if got := DefaultVisualizer(p, TString); got != VMultiline {
			t.Errorf("%s=%s want multiline", p, got)
		}
	}
	for _, p := range []string{"stacks", "haystack", "stack.depth"} {
		if got := DefaultVisualizer(p, TString); got == VMultiline {
			t.Errorf("%s=multiline, want raw", p)
		}
	}
	if got := DefaultVisualizer("stack", TJSON); got != VJSON {
		t.Errorf("json stack=%s want json", got)
	}
}

func TestInferMultilineStrings(t *testing.T) {
	s := tallied(
		map[string]any{"msg": "one line", "err": "boom\n", "sev": "INFO", "n": json.Number("1")},
		map[string]any{"msg": "panic: x\n\ngoroutine 1 [running]:\nmain.main()", "err": "boom", "sev": "ERROR\n", "n": json.Number("2")},
	)
	vis := func(p string) string {
		f, ok := s.Field(p)
		if !ok {
			t.Fatalf("no field %q", p)
		}
		return f.Visualizer
	}
	if v := vis("msg"); v != VMultiline {
		t.Errorf("msg=%s want multiline", v)
	}
	if v := vis("err"); v != VRaw {
		t.Errorf("err=%s want raw", v)
	}
	if v := vis("n"); v != VNumber {
		t.Errorf("n=%s want number", v)
	}
	// A path with its own visualizer keeps it.
	s = tallied(map[string]any{"severity": "ERROR\nmore", "db.statement": "select 1\nfrom t"})
	if v := vis("severity"); v != VSeverity {
		t.Errorf("severity=%s", v)
	}
	if v := vis("db.statement"); v != VSQL {
		t.Errorf("db.statement=%s", v)
	}
}
