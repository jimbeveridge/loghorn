// Package engine holds loghorn's fixed v0 importance classifier.
package engine

import (
	"regexp"

	"github.com/jimbeveridge/loghorn/internal/entry"
)

var failureText = regexp.MustCompile(`(?i)panic|fatal|exception|traceback`)

// IsImportant reports whether an entry is worth the user's attention under the
// fixed v0 "failures" definition. v1 replaces this with user-defined filters.
func IsImportant(e entry.Entry) bool {
	if e.Severity >= entry.SevError {
		return true
	}
	if e.HTTPStatus >= 500 {
		return true
	}
	return failureText.MatchString(e.Message)
}
