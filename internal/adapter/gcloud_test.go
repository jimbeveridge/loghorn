package adapter

import (
	"testing"

	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/entry"
)

// What gcloud beta logging tail --json actually emits: snake_case LogEntry
// field names and a numeric severity.
const gcloudJSON = `{
  "http_request": {
    "request_method": "GET",
    "status": 503,
    "user_agent": "curl/8.7.1"
  },
  "insert_id": "6aad671100091ffaa983635f",
  "json_payload": {
    "message": "Database: QUERY failed",
    "requestId": "f17c46797e7721ce34df0c5a65fdac5d"
  },
  "log_name": "projects/p/logs/run.googleapis.com%2Fstdout",
  "receive_timestamp": "2026-09-18T16:30:09.936810442Z",
  "severity": 500,
  "span_id": "43e3c93c8a43b1cb",
  "timestamp": "2026-09-18T16:30:09.586452Z"
}`

func TestParseLineGcloudJSON(t *testing.T) {
	e := ParseLine([]byte(gcloudJSON))
	if e.Format != entry.FormatLogEntry || e.Malformed {
		t.Fatalf("Format = %v Malformed = %v, want FormatLogEntry", e.Format, e.Malformed)
	}
	if e.Severity != entry.SevError {
		t.Errorf("Severity = %v, want SevError from the numeric 500", e.Severity)
	}
	if e.HTTPStatus != 503 {
		t.Errorf("HTTPStatus = %d, want 503 from http_request.status", e.HTTPStatus)
	}
	if e.Message != "Database: QUERY failed" {
		t.Errorf("Message = %q, want json_payload.message", e.Message)
	}
	// The root span_id wins over the payload's requestId: candidates are
	// checked everywhere at the root before the payload is consulted at all.
	// This is the pre-existing precedence, deliberately unchanged here —
	// ROADMAP files a configurable candidate list as a v1 item, and that is
	// where reordering belongs.
	if e.CorrelationID != "43e3c93c8a43b1cb" {
		t.Errorf("CorrelationID = %q, want the root span_id", e.CorrelationID)
	}
	if e.Timestamp.IsZero() {
		t.Error("Timestamp should be parsed")
	}
}

// The gcloud --format=yaml shape, which shares the JSON one's field names.
func TestParseLineGcloudYAML(t *testing.T) {
	rec := []byte("---\n" +
		"http_request:\n  status: 503\n" +
		"json_payload:\n  message: 'Database: QUERY failed'\n" +
		"severity: 500\n" +
		"span_id: ''\n" +
		"timestamp: '2026-09-18T16:45:24.697Z'\n")
	e := ParseLine(rec)
	if e.Format != entry.FormatLogEntry || e.Malformed {
		t.Fatalf("Format = %v Malformed = %v, want FormatLogEntry", e.Format, e.Malformed)
	}
	if e.Severity != entry.SevError {
		t.Errorf("Severity = %v, want SevError from the numeric 500", e.Severity)
	}
	if e.HTTPStatus != 503 {
		t.Errorf("HTTPStatus = %d, want 503", e.HTTPStatus)
	}
	if e.Message != "Database: QUERY failed" {
		t.Errorf("Message = %q, want json_payload.message", e.Message)
	}
	// An empty span_id is no correlation id.
	if e.CorrelationID != "" {
		t.Errorf("CorrelationID = %q, want empty: span_id is ''", e.CorrelationID)
	}
}

// Both spellings must produce the same Entry, so nothing downstream has to
// care which producer a record came from.
func TestBothSpellingsAgree(t *testing.T) {
	camel := []byte(`{"severity":"ERROR","httpRequest":{"status":503},"textPayload":"boom","trace":"t1"}`)
	snake := []byte(`{"severity":500,"http_request":{"status":503},"text_payload":"boom","trace":"t1"}`)
	c, s := ParseLine(camel), ParseLine(snake)
	if c.Severity != s.Severity || c.HTTPStatus != s.HTTPStatus ||
		c.Message != s.Message || c.CorrelationID != s.CorrelationID {
		t.Errorf("spellings disagree:\ncamel: %+v\nsnake: %+v", c, s)
	}
}

func TestFieldNamingRestrictions(t *testing.T) {
	snake := []byte(`{"severity":500,"http_request":{"status":503},"text_payload":"boom"}`)
	camel := []byte(`{"severity":"ERROR","httpRequest":{"status":503},"textPayload":"boom"}`)

	// camel refuses to read gcloud's spelling.
	e := New(config.Input{FieldNaming: config.NamingCamel}).ParseLine(snake)
	if e.HTTPStatus != 0 {
		t.Errorf("HTTPStatus = %d under camel, want 0: http_request must be ignored", e.HTTPStatus)
	}
	// snake refuses to read the canonical spelling.
	e = New(config.Input{FieldNaming: config.NamingSnake}).ParseLine(camel)
	if e.HTTPStatus != 0 {
		t.Errorf("HTTPStatus = %d under snake, want 0: httpRequest must be ignored", e.HTTPStatus)
	}
	// Each reads its own.
	if e := New(config.Input{FieldNaming: config.NamingSnake}).ParseLine(snake); e.HTTPStatus != 503 {
		t.Errorf("HTTPStatus = %d under snake with snake input, want 503", e.HTTPStatus)
	}
	if e := New(config.Input{FieldNaming: config.NamingCamel}).ParseLine(camel); e.HTTPStatus != 503 {
		t.Errorf("HTTPStatus = %d under camel with camel input, want 503", e.HTTPStatus)
	}
}

func TestNumericSeverityLadder(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want entry.Severity
	}{
		{"0", entry.SevDefault},
		{"100", entry.SevDebug},
		{"200", entry.SevInfo},
		{"300", entry.SevNotice},
		{"400", entry.SevWarning},
		{"500", entry.SevError},
		{"600", entry.SevCritical},
		{"700", entry.SevAlert},
		{"800", entry.SevEmergency},
		// Off the ladder: the nearest value at or below.
		{"250", entry.SevInfo},
		{"900", entry.SevEmergency},
		{"50", entry.SevDefault},
		// Neither of these is a LogSeverity code.
		{"-100", entry.SevDefault},
		{"200.5", entry.SevDefault},
	} {
		e := ParseLine([]byte(`{"severity":` + tc.in + `}`))
		if e.Severity != tc.want {
			t.Errorf("severity %s = %v, want %v", tc.in, e.Severity, tc.want)
		}
	}
}

// A severity too large for an int must not depend on the machine. int(n) on an
// out-of-range float is implementation-dependent in Go: 1e19 parses cleanly and
// is integral, so it reaches the ladder, and it used to saturate high on arm64
// (EMERGENCY) while wrapping on amd64 (DEFAULT). The ladder's own rule —
// nearest code at or below — says EMERGENCY, everywhere.
func TestHugeNumericSeverityIsArchitectureIndependent(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want entry.Severity
	}{
		{"1e19", entry.SevEmergency},
		{"99999999999999999999", entry.SevEmergency},
		{"-1e19", entry.SevDefault},
	} {
		if e := ParseLine([]byte(`{"severity":` + tc.in + `}`)); e.Severity != tc.want {
			t.Errorf("severity %s = %v, want %v", tc.in, e.Severity, tc.want)
		}
	}
}

func TestSeverityModeRestrictions(t *testing.T) {
	if e := New(config.Input{Severity: config.SeverityString}).ParseLine([]byte(`{"severity":500}`)); e.Severity != entry.SevDefault {
		t.Errorf("Severity = %v under string mode, want SevDefault for a number", e.Severity)
	}
	if e := New(config.Input{Severity: config.SeverityNumeric}).ParseLine([]byte(`{"severity":"ERROR"}`)); e.Severity != entry.SevDefault {
		t.Errorf("Severity = %v under numeric mode, want SevDefault for a string", e.Severity)
	}
}

// gcloud renames LogEntry's own fields but not the payload's, which is
// whatever the application emitted — so span_id is aliased at the root and
// requestId is not inside the payload.
func TestCorrelationFromGcloudSpanID(t *testing.T) {
	if e := ParseLine([]byte(`{"span_id":"43e3c93c"}`)); e.CorrelationID != "43e3c93c" {
		t.Errorf("CorrelationID = %q, want span_id", e.CorrelationID)
	}
	if e := ParseLine([]byte(`{"json_payload":{"requestId":"r1"}}`)); e.CorrelationID != "r1" {
		t.Errorf("CorrelationID = %q, want json_payload.requestId", e.CorrelationID)
	}
}

// Location outranks key rank: every candidate is tried at the root before the
// payload is consulted, so a root span_id beats a payload requestId even
// though requestId sits higher in the candidate list. Pinned because the
// candidate list reads like a pure priority order and isn't one.
func TestRootCorrelationOutranksPayload(t *testing.T) {
	e := ParseLine([]byte(`{"span_id":"root-span","json_payload":{"requestId":"payload-req"}}`))
	if e.CorrelationID != "root-span" {
		t.Errorf("CorrelationID = %q, want root-span: the root is searched first", e.CorrelationID)
	}
}

// A zero-value Input must behave as auto, so the package-level adapters and
// every existing test construction keep working.
func TestZeroInputBehavesAsAuto(t *testing.T) {
	if New(config.Input{}).ParseLine([]byte(gcloudJSON)).Severity != entry.SevError {
		t.Error("a zero-value Input should behave as auto")
	}
	if (LogEntryAdapter{}).Detect([]byte(gcloudJSON)) != true {
		t.Error("LogEntryAdapter{} should still detect a JSON object")
	}
}

// A status too large for an int must not depend on the machine. int(n) on an
// out-of-range float is implementation-dependent in Go, and HTTPStatus feeds
// engine.IsImportant's ">= 500" test, so 1e19 used to make a record important
// on arm64 and routine on amd64.
func TestHTTPStatusIsArchitectureIndependent(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"503", 503},
		{"200", 200},
		{"100", 100},
		{"599", 599},
		// Not HTTP statuses, so absent rather than wrapped or saturated.
		{"1e19", 0},
		{"99999999999999999999", 0},
		{"-1e19", 0},
		{"600", 0},
		{"99", 0},
		{"503.5", 0},
	} {
		if e := ParseLine([]byte(`{"http_request":{"status":` + tc.in + `}}`)); e.HTTPStatus != tc.want {
			t.Errorf("status %s = %d, want %d", tc.in, e.HTTPStatus, tc.want)
		}
	}
}

// A Cloud Run request log (log_name .../logs/run.googleapis.com%2Frequests)
// carries an http_request block and no payload at all — no text_payload, no
// json_payload, no root message. Before this case existed, logEntryMessage
// fell through to its last resort and dumped the whole entry as compact JSON
// into the list row, which read as raw text.
func TestMessageFromHTTPRequest(t *testing.T) {
	rec := []byte("---\n" +
		"http_request:\n" +
		"  latency: 0.002587500s\n" +
		"  request_method: OPTIONS\n" +
		"  request_url: https://api.example.com/api/v1/testing/capabilities\n" +
		"  status: 204\n" +
		"insert_id: 6aad6aa40008b65fc306077e\n" +
		"severity: 200\n" +
		"timestamp: '2026-09-18T16:45:24.566824Z'\n")
	e := ParseLine(rec)
	if want := "OPTIONS /api/v1/testing/capabilities 204 2.6ms"; e.Message != want {
		t.Errorf("Message = %q, want %q", e.Message, want)
	}
}

// The same summary is built from the canonical camelCase spelling, and a
// latency of a second or more reads in seconds.
func TestMessageFromHTTPRequestCamelCase(t *testing.T) {
	e := ParseLine([]byte(`{"httpRequest":{"requestMethod":"GET","requestUrl":"https://api.example.com/api/v1/auth/me","status":500,"latency":"1.2344s"}}`))
	if want := "GET /api/v1/auth/me 500 1.23s"; e.Message != want {
		t.Errorf("Message = %q, want %q", e.Message, want)
	}
}

// A payload message outranks the http_request summary: an entry carrying both
// (Cloud Run emits stdout logs with an http_request attached) already has a
// better message than anything synthesised from the request line.
func TestPayloadMessageOutranksHTTPRequest(t *testing.T) {
	e := ParseLine([]byte(`{"httpRequest":{"requestMethod":"GET","requestUrl":"/x","status":200},"json_payload":{"message":"from-payload"}}`))
	if e.Message != "from-payload" {
		t.Errorf("Message = %q, want from-payload", e.Message)
	}
}
