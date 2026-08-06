package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"clog/internal/adapter"
)

// A real, large backend.log-style LogEntry line (the 401 API request) with a
// userAgent value far wider than a half-width detail pane.
const bigLogEntry = `{"severity":"INFO","time":"2026-07-15T23:58:30.636Z","pid":4481,"hostname":"Jims-MacBook-Air.local","message":"API request: POST /api/v1/auth/login returned 401 in 0.055s","serviceName":"theauctionapp.googleapis.com","methodName":"POST /api/v1/auth/login","latency":"0.055s","requestId":"a673e05f-9ce8-4762-8aae-2ff182b28efe","httpRequest":{"requestMethod":"POST","requestUrl":"http://localhost:8787/api/v1/auth/login","requestSize":55,"status":401,"responseSize":0,"userAgent":"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36","remoteIp":"","latency":"0.055s"},"apiMetadata":{"queryParams":{}}}`

func maxLineWidth(s string) int {
	max := 0
	for _, ln := range strings.Split(s, "\n") {
		if w := lipgloss.Width(ln); w > max {
			max = w
		}
	}
	return max
}

// The detail pane must wrap wide content to its width so nothing is clipped off
// the right edge (unreachable), and so oversized entries become scrollable.
func TestDetailWrapsWideContent(t *testing.T) {
	e := adapter.ParseLine([]byte(bigLogEntry))
	e.Important = true

	raw := renderDetail(e)
	rawLines := strings.Count(raw, "\n") + 1

	m := NewModel(nil, 100, 2)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = m2.(Model)
	m2, _ = m.Update(entryMsg(e))
	m = m2.(Model)
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)
	if !m.showDetail {
		t.Fatalf("enter should open detail")
	}

	paneW := m.detail.Width
	// Precondition: the raw content really is wider than the pane, else the
	// test proves nothing.
	if maxLineWidth(raw) <= paneW {
		t.Fatalf("precondition failed: raw max width %d not wider than pane %d", maxLineWidth(raw), paneW)
	}

	// The viewport must now hold content wrapped to the pane width: every line
	// fits, and the wrapped line count (which the viewport holds) exceeds the
	// unwrapped count. On the old clip-only code the viewport held the raw
	// lines and this count check fails.
	wrapped := lipgloss.NewStyle().Width(paneW).Render(raw)
	if w := maxLineWidth(wrapped); w > paneW {
		t.Fatalf("wrapped content still exceeds pane width: %d > %d", w, paneW)
	}
	wantLines := strings.Count(wrapped, "\n") + 1
	if wantLines <= rawLines {
		t.Fatalf("wrapping did not expand line count (%d <= %d) — pick a wider fixture", wantLines, rawLines)
	}
	if m.detail.TotalLineCount() != wantLines {
		t.Fatalf("detail viewport holds %d lines, expected wrapped %d (raw %d) — content not wrapped to pane width",
			m.detail.TotalLineCount(), wantLines, rawLines)
	}
}
