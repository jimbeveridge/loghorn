package adapter

import (
	"bytes"
	"encoding/json"
	"math"
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
			e.HTTPStatus = int(st)
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
func numericSeverity(n float64) entry.Severity {
	if n < 0 || n != math.Trunc(n) {
		return entry.SevDefault
	}
	for _, l := range severityLadder {
		if int(n) >= l.code {
			return l.sev
		}
	}
	return entry.SevDefault
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
// jsonPayload.message, then top-level message, else the compact JSON.
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
	if b, err := json.Marshal(obj); err == nil {
		return string(b)
	}
	return ""
}

// correlationCandidates are checked in order, at the root then inside the
// payload. The root list carries gcloud's spelling (span_id) directly rather
// than going through snakeCase, because two of these keys are neither
// camelCase nor gcloud's to rename. The payload is whatever the application
// emitted — gcloud renames only LogEntry's own fields — so payload keys get no
// snake_case variants.
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
