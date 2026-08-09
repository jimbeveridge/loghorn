package adapter

import (
	"bytes"
	"encoding/json"
	"time"

	"clog/internal/entry"
)

type LogEntryAdapter struct{}

func (LogEntryAdapter) Detect(line []byte) bool {
	t := bytes.TrimSpace(line)
	return len(t) > 0 && t[0] == '{'
}

func (LogEntryAdapter) Parse(line []byte) (entry.Entry, error) {
	var obj map[string]any
	if err := json.Unmarshal(line, &obj); err != nil {
		return entry.Entry{}, err
	}
	e := entry.Entry{
		Raw:    append([]byte(nil), line...),
		Format: entry.FormatLogEntry,
		JSON:   obj,
	}
	if s, ok := obj["severity"].(string); ok {
		e.Severity = entry.ParseSeverity(s)
	}
	if hr, ok := obj["httpRequest"].(map[string]any); ok {
		if st, ok := hr["status"].(float64); ok {
			e.HTTPStatus = int(st)
		}
	}
	e.Timestamp = logEntryTimestamp(obj)
	e.Message = logEntryMessage(obj)
	e.CorrelationID = correlationID(obj)
	return e, nil
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
func logEntryMessage(obj map[string]any) string {
	if tp, ok := obj["textPayload"].(string); ok && tp != "" {
		return tp
	}
	if jp, ok := obj["jsonPayload"].(map[string]any); ok {
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

// correlationCandidates are checked in order, at the root then inside jsonPayload.
// Captured in v0; the correlated request view that consumes it is a v1 feature.
var correlationCandidates = []string{
	"trace", "requestId", "logging.googleapis.com/trace",
	"logging.googleapis.com/spanId", // what Cloud Run actually emits; bare "spanId" is the local convention
	"spanId",
}

func correlationID(obj map[string]any) string {
	for _, k := range correlationCandidates {
		if s, ok := obj[k].(string); ok && s != "" {
			return s
		}
	}
	if jp, ok := obj["jsonPayload"].(map[string]any); ok {
		for _, k := range correlationCandidates {
			if s, ok := jp[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
