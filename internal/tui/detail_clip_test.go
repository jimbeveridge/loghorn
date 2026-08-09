package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"clog/internal/adapter"
)

// A real, large backend.log-style LogEntry line (the 401 API request) with a
// userAgent value far wider than a half-width detail pane.
const bigLogEntry = `{"severity":"INFO","time":"2026-07-15T23:58:30.636Z","pid":4481,"hostname":"Jims-MacBook-Air.local","message":"API request: POST /api/v1/auth/login returned 401 in 0.055s","serviceName":"theauctionapp.googleapis.com","methodName":"POST /api/v1/auth/login","latency":"0.055s","requestId":"a673e05f-9ce8-4762-8aae-2ff182b28efe","httpRequest":{"requestMethod":"POST","requestUrl":"http://localhost:8787/api/v1/auth/login","requestSize":55,"status":401,"responseSize":0,"userAgent":"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36","remoteIp":"","latency":"0.055s"},"apiMetadata":{"queryParams":{}}}`

// Content too wide for the pane is clipped at the right edge, not folded onto
// continuation lines. One log line stays one line, so the shape of the JSON
// survives; folding turned every long value into a ragged block.
func TestDetailClipsRatherThanFolds(t *testing.T) {
	e := adapter.ParseLine([]byte(bigLogEntry))
	e.Important = true

	raw := renderDetail(e)
	rawLines := strings.Count(raw, "\n") + 1

	m := NewModel(nil, 100)
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
	// Precondition: the content really is wider than the pane, or the test
	// proves nothing.
	if maxLineWidth(raw) <= paneW {
		t.Fatalf("precondition failed: raw max width %d not wider than pane %d", maxLineWidth(raw), paneW)
	}

	// Line count is unchanged — nothing folded.
	if got := m.detail.TotalLineCount(); got != rawLines {
		t.Fatalf("clipping must not change the line count: %d held, %d in the source", got, rawLines)
	}
	// And every line fits, so nothing spills past the pane into the list.
	if got := maxLineWidth(m.clippedDetail()); got > paneW {
		t.Fatalf("clipped content is %d columns, pane is %d", got, paneW)
	}
}

// Clipping keeps the head of each line, which is where the key and the start of
// the value are.
func TestClippingKeepsTheStartOfTheLine(t *testing.T) {
	e := adapter.ParseLine([]byte(bigLogEntry))
	e.Important = true

	m := NewModel(nil, 100)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 40}) // forces a narrow cap
	m = m2.(Model)
	m2, _ = m.Update(entryMsg(e))
	m = m2.(Model)
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)

	content := m.clippedDetail()
	if !strings.Contains(content, `"userAgent"`) {
		t.Fatalf("the key should survive clipping:\n%s", content)
	}
	// The tail of the long value is gone — that is the trade being made.
	if strings.Contains(content, "Safari/537.36") {
		t.Fatalf("the far end of a long value should be clipped away:\n%s", content)
	}
}
