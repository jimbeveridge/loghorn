package adapter

import (
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/entry"
)

func TestLogEntryTimeAlias(t *testing.T) {
	// Real producers (pino) use `time`, not the canonical `timestamp`.
	a := LogEntryAdapter{}
	e, err := a.Parse([]byte(`{"severity":"INFO","time":"2026-07-15T23:58:30.636Z","message":"m"}`))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	want, _ := time.Parse(time.RFC3339, "2026-07-15T23:58:30.636Z")
	if !e.Timestamp.Equal(want) {
		t.Fatalf("`time` should populate Timestamp: got %v, want %v", e.Timestamp, want)
	}
}

func TestLogEntryParse(t *testing.T) {
	line := []byte(`{"severity":"ERROR","httpRequest":{"status":503},"textPayload":"boom","timestamp":"2026-08-05T10:00:00Z"}`)
	a := LogEntryAdapter{}
	if !a.Detect(line) {
		t.Fatalf("Detect should be true for a JSON object")
	}
	e, err := a.Parse(line)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if e.Format != entry.FormatLogEntry {
		t.Fatalf("Format = %v, want FormatLogEntry", e.Format)
	}
	if e.Severity != entry.SevError {
		t.Fatalf("Severity = %v, want SevError", e.Severity)
	}
	if e.HTTPStatus != 503 {
		t.Fatalf("HTTPStatus = %d, want 503", e.HTTPStatus)
	}
	if e.Message != "boom" {
		t.Fatalf("Message = %q, want boom", e.Message)
	}
	if e.Timestamp.IsZero() {
		t.Fatalf("Timestamp should be parsed")
	}
}

func TestLogEntryDetectFalseAndParseError(t *testing.T) {
	a := LogEntryAdapter{}
	if a.Detect([]byte("plain text line")) {
		t.Fatalf("Detect should be false for non-JSON")
	}
	if _, err := a.Parse([]byte("{not json")); err == nil {
		t.Fatalf("Parse should error on invalid JSON")
	}
}

func TestLogEntryMessageFallback(t *testing.T) {
	a := LogEntryAdapter{}
	e, err := a.Parse([]byte(`{"jsonPayload":{"message":"from-json-payload"}}`))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if e.Message != "from-json-payload" {
		t.Fatalf("Message = %q, want from-json-payload", e.Message)
	}
}

func TestLogEntryCorrelationID(t *testing.T) {
	a := LogEntryAdapter{}
	// requestId at the root (as our real backend emits it).
	e, err := a.Parse([]byte(`{"severity":"INFO","message":"m","requestId":"abc-123"}`))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if e.CorrelationID != "abc-123" {
		t.Fatalf("CorrelationID = %q, want abc-123", e.CorrelationID)
	}
	// trace wins over requestId when both are present (candidate order).
	e, _ = a.Parse([]byte(`{"trace":"t-1","requestId":"r-1"}`))
	if e.CorrelationID != "t-1" {
		t.Fatalf("CorrelationID = %q, want t-1 (trace precedes requestId)", e.CorrelationID)
	}
	// none present -> empty.
	e, _ = a.Parse([]byte(`{"severity":"INFO"}`))
	if e.CorrelationID != "" {
		t.Fatalf("CorrelationID = %q, want empty", e.CorrelationID)
	}
}

func TestParseLineRawText(t *testing.T) {
	e := ParseLine([]byte("just a plain log line"))
	if e.Format != entry.FormatRawText {
		t.Fatalf("Format = %v, want FormatRawText", e.Format)
	}
	if e.Message != "just a plain log line" {
		t.Fatalf("Message = %q", e.Message)
	}
	if e.Malformed {
		t.Fatalf("plain text is not malformed")
	}
}

func TestParseLineMalformedJSON(t *testing.T) {
	e := ParseLine([]byte(`{"severity":"ERROR" broken`))
	if e.Format != entry.FormatRawText {
		t.Fatalf("malformed JSON should fall back to raw text")
	}
	if !e.Malformed {
		t.Fatalf("malformed JSON should set Malformed=true")
	}
}

func TestParseLineValidJSON(t *testing.T) {
	e := ParseLine([]byte(`{"severity":"INFO","textPayload":"ok"}`))
	if e.Format != entry.FormatLogEntry {
		t.Fatalf("valid JSON should parse as LogEntry")
	}
}

func TestYAMLDetect(t *testing.T) {
	a := YAMLAdapter{}
	if !a.Detect([]byte("---\nseverity: INFO\n")) {
		t.Fatalf("Detect should be true for a record starting with ---")
	}
	if a.Detect([]byte("plain text line")) {
		t.Fatalf("Detect should be false for plain text")
	}
	if a.Detect([]byte(`{"a":1}`)) {
		t.Fatalf("Detect should be false for JSON")
	}
}

func TestYAMLParse(t *testing.T) {
	rec := []byte("---\nseverity: ERROR\nhttpRequest:\n  status: 503\ntextPayload: boom\ntimestamp: '2026-08-05T10:00:00Z'\n")
	a := YAMLAdapter{}
	e, err := a.Parse(rec)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if e.Format != entry.FormatLogEntry {
		t.Fatalf("Format = %v, want FormatLogEntry", e.Format)
	}
	if e.Severity != entry.SevError {
		t.Fatalf("Severity = %v, want SevError", e.Severity)
	}
	if e.HTTPStatus != 503 {
		t.Fatalf("HTTPStatus = %d, want 503", e.HTTPStatus)
	}
	if e.Message != "boom" {
		t.Fatalf("Message = %q, want boom", e.Message)
	}
	if e.Timestamp.IsZero() {
		t.Fatalf("Timestamp should be parsed")
	}
}

// Plain YAML integers decode as Go int, not float64 like encoding/json — they
// must be normalized so HTTPStatus's type assertion (and anything else
// downstream expecting JSON-shaped numbers) still works.
func TestYAMLParseNormalizesIntegers(t *testing.T) {
	rec := []byte("---\ncount: 42\nnested:\n  n: 7\n")
	a := YAMLAdapter{}
	e, err := a.Parse(rec)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if _, ok := e.JSON["count"].(float64); !ok {
		t.Fatalf("count should normalize to float64, got %T", e.JSON["count"])
	}
	nested, ok := e.JSON["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested should be map[string]any, got %T", e.JSON["nested"])
	}
	if _, ok := nested["n"].(float64); !ok {
		t.Fatalf("nested.n should normalize to float64, got %T", nested["n"])
	}
}

func TestParseLineYAMLDocument(t *testing.T) {
	rec := []byte("---\nseverity: WARNING\nmessage: from-yaml\n")
	e := ParseLine(rec)
	if e.Format != entry.FormatLogEntry {
		t.Fatalf("YAML document should parse as LogEntry, got Format=%v Malformed=%v", e.Format, e.Malformed)
	}
	if e.Severity != entry.SevWarning {
		t.Fatalf("Severity = %v, want SevWarning", e.Severity)
	}
	if e.Message != "from-yaml" {
		t.Fatalf("Message = %q, want from-yaml", e.Message)
	}
}

func TestParseLineMalformedYAML(t *testing.T) {
	// A tab after the "---" separator is invalid YAML indentation.
	e := ParseLine([]byte("---\n\tbad: [unterminated\n"))
	if e.Format != entry.FormatRawText || !e.Malformed {
		t.Fatalf("invalid YAML should fall back to raw text, flagged Malformed")
	}
}
