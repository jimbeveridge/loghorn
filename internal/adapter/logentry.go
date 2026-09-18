package adapter

import (
	"bytes"
	"encoding/json"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/entry"
)

// LogEntryAdapter parses one JSON LogEntry. In selects which spelling of a
// field name and which encoding of a severity it accepts; a zero value
// behaves as "auto".
type LogEntryAdapter struct{ In config.Input }

func (LogEntryAdapter) Detect(line []byte) bool {
	t := bytes.TrimSpace(line)
	return len(t) > 0 && t[0] == '{'
}

func (a LogEntryAdapter) Parse(line []byte) (entry.Entry, error) {
	var obj map[string]any
	if err := json.Unmarshal(line, &obj); err != nil {
		return entry.Entry{}, err
	}
	return fromLogEntryObject(line, obj, a.In), nil
}

// fromLogEntryObject builds an Entry from a decoded LogEntry object. Shared by
// the JSON and YAML adapters, which differ only in how they get from bytes to
// this map — the GCP LogEntry schema and every field below it are the same
// either way.
func fromLogEntryObject(raw []byte, obj map[string]any, in config.Input) entry.Entry {
	e := entry.Entry{
		Raw:    append([]byte(nil), raw...),
		Format: entry.FormatLogEntry,
		JSON:   obj,
	}
	e.Severity = severityOf(obj, in.Severity)
	if hr, ok := fieldMap(obj, "httpRequest", in.FieldNaming); ok {
		if st, ok := hr["status"].(float64); ok {
			e.HTTPStatus = httpStatus(st)
		}
	}
	e.Timestamp = logEntryTimestamp(obj)
	e.Message = logEntryMessage(obj, in.FieldNaming)
	e.CorrelationID = correlationID(obj, in.FieldNaming)
	return e
}

// field returns obj's value for a canonical camelCase LogEntry key, honouring
// the configured spelling. gcloud names every LogEntry field in snake_case
// (json_payload, http_request, …) while the canonical schema and most other
// producers use camelCase. Under auto — the default, and any value other than
// an explicit camel or snake — both are accepted: one document can't spell the
// same key both ways, so there is nothing to disambiguate.
func field(obj map[string]any, camel string, naming config.FieldNaming) (any, bool) {
	if naming != config.NamingSnake {
		if v, ok := obj[camel]; ok {
			return v, true
		}
		if naming == config.NamingCamel {
			return nil, false
		}
	}
	v, ok := obj[snakeCase(camel)]
	return v, ok
}

func fieldString(obj map[string]any, camel string, naming config.FieldNaming) (string, bool) {
	v, ok := field(obj, camel, naming)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func fieldMap(obj map[string]any, camel string, naming config.FieldNaming) (map[string]any, bool) {
	v, ok := field(obj, camel, naming)
	if !ok {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}

// snakeCase converts a camelCase LogEntry key to gcloud's spelling:
// jsonPayload becomes json_payload. Only ever called with LogEntry key names,
// all of which are plain camelCase with no acronyms or digits.
func snakeCase(camel string) string {
	var b strings.Builder
	b.Grow(len(camel) + 2)
	for i := 0; i < len(camel); i++ {
		if c := camel[i]; c >= 'A' && c <= 'Z' {
			b.WriteByte('_')
			b.WriteByte(c + 'a' - 'A')
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// severityOf reads the entry's severity. The canonical LogEntry carries a
// string name; gcloud's --json and --format=yaml emit the numeric LogSeverity
// code instead. Under auto either is accepted.
func severityOf(obj map[string]any, mode config.SeverityMode) entry.Severity {
	v, ok := obj["severity"] // spelled the same in both conventions
	if !ok {
		return entry.SevDefault
	}
	if mode != config.SeverityNumeric {
		if s, ok := v.(string); ok {
			return entry.ParseSeverity(s)
		}
		if mode == config.SeverityString {
			return entry.SevDefault
		}
	}
	if n, ok := v.(float64); ok {
		return numericSeverity(n)
	}
	return entry.SevDefault
}

// severityLadder is GCP's LogSeverity enum, highest first so the first code at
// or below a value wins.
var severityLadder = []struct {
	code int
	sev  entry.Severity
}{
	{800, entry.SevEmergency}, {700, entry.SevAlert}, {600, entry.SevCritical},
	{500, entry.SevError}, {400, entry.SevWarning}, {300, entry.SevNotice},
	{200, entry.SevInfo}, {100, entry.SevDebug},
}

// numericSeverity maps a LogSeverity code. A value not exactly on the ladder
// takes the nearest one at or below it, so a future intermediate code degrades
// sensibly instead of becoming DEFAULT. A negative or fractional value is no
// LogSeverity code at all, so it is DEFAULT.
//
// The comparison stays in float space rather than converting to int: a
// converted float too large for an int is implementation-dependent in Go, and
// {"severity":1e19} parses cleanly and is integral, so it reaches here. On
// arm64 int(n) saturates high and the entry became EMERGENCY; on amd64 it
// wraps and the same entry became DEFAULT. Comparing the float keeps the
// ladder's own answer — 1e19 is above 800, so EMERGENCY — on every machine.
func numericSeverity(n float64) entry.Severity {
	if n < 0 || n != math.Trunc(n) {
		return entry.SevDefault
	}
	for _, l := range severityLadder {
		if n >= float64(l.code) {
			return l.sev
		}
	}
	return entry.SevDefault
}

// httpStatus narrows a decoded status without an implementation-dependent
// conversion: a float too large for an int converts unpredictably in Go —
// arm64 saturates, amd64 wraps — and HTTPStatus feeds engine.IsImportant's
// ">= 500" test, so {"status":1e19} made one machine call a record important
// and the other call it routine. 5xx is the highest assigned class, so 599 is
// the ceiling and 100 the floor; a value outside that is not a status and reads
// as absent rather than being carried through. The only observable effect is
// that 600 or more no longer counts as important.
func httpStatus(n float64) int {
	if n != math.Trunc(n) || n < 100 || n > 599 {
		return 0
	}
	return int(n)
}

// logEntryTimestamp parses the entry's own timestamp. Canonical LogEntry uses
// `timestamp`, but real producers (e.g. pino) emit `time` — accept either.
func logEntryTimestamp(obj map[string]any) time.Time {
	for _, k := range []string{"timestamp", "time"} {
		if ts, ok := obj[k].(string); ok {
			if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
				return parsed
			}
		}
	}
	return time.Time{}
}

// logEntryMessage picks the best human-facing message: textPayload, then
// jsonPayload.message, then top-level message, then a summary of httpRequest,
// else the compact JSON.
func logEntryMessage(obj map[string]any, naming config.FieldNaming) string {
	if tp, ok := fieldString(obj, "textPayload", naming); ok && tp != "" {
		return tp
	}
	if jp, ok := fieldMap(obj, "jsonPayload", naming); ok {
		if m, ok := jp["message"].(string); ok && m != "" {
			return m
		}
	}
	if m, ok := obj["message"].(string); ok && m != "" {
		return m
	}
	if hr, ok := fieldMap(obj, "httpRequest", naming); ok {
		if s := httpRequestSummary(hr, naming); s != "" {
			return s
		}
	}
	if b, err := json.Marshal(obj); err == nil {
		return string(b)
	}
	return ""
}

// httpRequestSummary builds a message for an entry whose only content is its
// httpRequest. A Cloud Run request log — log_name
// projects/P/logs/run.googleapis.com%2Frequests — carries no payload of any
// kind, so without this it reached the compact-JSON dump above and the list row
// read as raw text rather than as a request.
//
// Method and status are what make such a row scannable; the URL contributes its
// path and query, since scheme and host repeat identically down a whole stream
// and are pure noise at list width. A missing piece is skipped rather than
// filled with a placeholder, and an httpRequest that yields no piece at all
// returns "" so the caller falls through to the dump exactly as before.
func httpRequestSummary(hr map[string]any, naming config.FieldNaming) string {
	var parts []string
	if m, ok := fieldString(hr, "requestMethod", naming); ok && m != "" {
		parts = append(parts, m)
	}
	if u, ok := fieldString(hr, "requestUrl", naming); ok && u != "" {
		parts = append(parts, requestTarget(u))
	}
	// status is spelled the same either way. Only an integral value is a status
	// code; anything else is left out rather than printed as a fraction.
	if st, ok := hr["status"].(float64); ok && st == math.Trunc(st) {
		parts = append(parts, strconv.FormatFloat(st, 'f', -1, 64))
	}
	// latency is a protobuf Duration, which is a string like "0.002587500s" in
	// both spellings and which time.ParseDuration reads directly.
	if l, ok := hr["latency"].(string); ok {
		if d, err := time.ParseDuration(l); err == nil {
			parts = append(parts, formatLatency(d))
		}
	}
	return strings.Join(parts, " ")
}

// requestTarget reduces a request URL to the part that differs between rows.
// An absolute URL gives up its scheme and host; anything else — a relative URL,
// or a value that does not parse as a URL at all — is kept whole, since there is
// no host to drop and guessing at the shape would lose bytes the row needs.
func requestTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	if u.RawQuery != "" {
		return u.Path + "?" + u.RawQuery
	}
	return u.Path
}

// formatLatency renders a request latency for a list row: milliseconds to one
// decimal below a second, seconds to two at or above it. Duration.String prints
// full precision — 2.5875ms — and those digits are noise at a glance.
func formatLatency(d time.Duration) string {
	if d >= time.Second {
		return strconv.FormatFloat(d.Seconds(), 'f', 2, 64) + "s"
	}
	return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 1, 64) + "ms"
}

// correlationCandidates are checked in order, at the root then inside the
// payload — both phases share this one list, so gcloud's span_id spelling is
// recognised in the payload too, not just at the root. What the payload phase
// does not get is snakeCase translation of the camelCase candidates
// (requestId, spanId): the payload is whatever the application emitted, and
// gcloud renames only LogEntry's own fields, never the application's.
// Captured in v0; the correlated request view that consumes it is a v1 feature.
var correlationCandidates = []string{
	"trace", "requestId", "logging.googleapis.com/trace",
	"logging.googleapis.com/spanId", // what Cloud Run actually emits; bare "spanId" is the local convention
	"spanId", "span_id",
}

func correlationID(obj map[string]any, naming config.FieldNaming) string {
	for _, k := range correlationCandidates {
		if s, ok := obj[k].(string); ok && s != "" {
			return s
		}
	}
	if jp, ok := fieldMap(obj, "jsonPayload", naming); ok {
		for _, k := range correlationCandidates {
			if s, ok := jp[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
