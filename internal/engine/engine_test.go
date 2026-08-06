package engine

import (
	"testing"

	"clog/internal/entry"
)

func TestIsImportant(t *testing.T) {
	cases := []struct {
		name string
		e    entry.Entry
		want bool
	}{
		{"error severity", entry.Entry{Severity: entry.SevError}, true},
		{"critical severity", entry.Entry{Severity: entry.SevCritical}, true},
		{"warning is not important", entry.Entry{Severity: entry.SevWarning}, false},
		{"5xx status", entry.Entry{HTTPStatus: 503}, true},
		{"4xx is not important", entry.Entry{HTTPStatus: 404}, false},
		{"panic text", entry.Entry{Message: "goroutine panic: nil deref"}, true},
		{"exception mixed case", entry.Entry{Message: "Unhandled Exception thrown"}, true},
		{"benign info", entry.Entry{Severity: entry.SevInfo, Message: "served ok"}, false},
	}
	for _, c := range cases {
		if got := IsImportant(c.e); got != c.want {
			t.Errorf("%s: IsImportant = %v, want %v", c.name, got, c.want)
		}
	}
}
