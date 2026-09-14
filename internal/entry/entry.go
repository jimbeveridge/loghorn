// Package entry defines the normalized log record every loghorn stage speaks.
package entry

import (
	"strings"
	"time"
)

type Format int

const (
	FormatRawText Format = iota
	FormatLogEntry
)

// Severity mirrors GCP LogEntry severity, ordered low->high so comparisons work.
type Severity int

const (
	SevDefault Severity = iota
	SevDebug
	SevInfo
	SevNotice
	SevWarning
	SevError
	SevCritical
	SevAlert
	SevEmergency
)

var sevNames = map[Severity]string{
	SevDefault: "DEFAULT", SevDebug: "DEBUG", SevInfo: "INFO", SevNotice: "NOTICE",
	SevWarning: "WARNING", SevError: "ERROR", SevCritical: "CRITICAL",
	SevAlert: "ALERT", SevEmergency: "EMERGENCY",
}

func (s Severity) String() string {
	if n, ok := sevNames[s]; ok {
		return n
	}
	return "DEFAULT"
}

// ParseSeverity is case-insensitive; unknown/empty input maps to SevDefault.
func ParseSeverity(s string) Severity {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return SevDebug
	case "INFO":
		return SevInfo
	case "NOTICE":
		return SevNotice
	case "WARNING":
		return SevWarning
	case "ERROR":
		return SevError
	case "CRITICAL":
		return SevCritical
	case "ALERT":
		return SevAlert
	case "EMERGENCY":
		return SevEmergency
	default:
		return SevDefault
	}
}

// Entry is the normalized record produced by an Adapter and consumed by every
// downstream stage. Raw is the original line, byte-for-byte. JSON is the parsed
// object for FormatLogEntry (nil for raw text). Important is computed by the engine.
type Entry struct {
	Raw           []byte
	Format        Format
	Timestamp     time.Time // the log line's own timestamp (may be zero)
	Received      time.Time // wall-clock time loghorn ingested the line
	Seq           int       // ingestion order, 1-based; identifies an entry across a rebuilt row list even after the ring evicts older ones
	Severity      Severity
	HTTPStatus    int
	Message       string
	CorrelationID string // first of trace/requestId/…; captured in v0, correlated view is v1
	JSON          map[string]any
	Malformed     bool
	Important     bool
}
