package tui

import (
	"strings"
	"testing"
)

// In -historical mode the mode word is always HISTORICAL, on the handle and
// off it, since there is no live input to distinguish from held.
func TestHistoricalBarAlwaysSaysHistorical(t *testing.T) {
	m := NewModel(nil, 100)
	m.width, m.height = 200, 12
	m.SetHistorical()
	m = imps(m, 5)

	got := m.statusBar()
	if !strings.Contains(got, "HISTORICAL") {
		t.Fatalf("bar should read HISTORICAL on the handle:\n%s", got)
	}
	if strings.Contains(got, "LIVE") || strings.Contains(got, "HELD") {
		t.Fatalf("bar should not say LIVE or HELD on the handle:\n%s", got)
	}

	m = press(m, 'k') // pull the shade down
	got = m.statusBar()
	if !strings.Contains(got, "HISTORICAL") {
		t.Fatalf("bar should read HISTORICAL off the handle:\n%s", got)
	}
	if strings.Contains(got, "LIVE") || strings.Contains(got, "HELD") {
		t.Fatalf("bar should not say LIVE or HELD off the handle:\n%s", got)
	}
}

// The spacebar hint says "latest" rather than "live" in historical mode,
// since space moves to the newest stored record instead of resuming a live
// stream that doesn't exist.
func TestHistoricalHintSaysLatest(t *testing.T) {
	m := NewModel(nil, 100)
	m.width, m.height = 200, 12
	m.SetHistorical()
	m = imps(m, 5)

	m = press(m, 'k') // pull the shade down
	got := m.statusBar()
	if !strings.Contains(got, "spc latest") {
		t.Fatalf("held hint should say latest in historical mode:\n%s", got)
	}
	if strings.Contains(got, "spc live") {
		t.Fatalf("held hint should not say live in historical mode:\n%s", got)
	}

	m = press(m, 'j') // grab the handle back
	got = m.statusBar()
	if !strings.Contains(got, "spc hold") {
		t.Fatalf("handle hint should still say hold in historical mode:\n%s", got)
	}
}

// space still moves to the handle — the newest record — in historical mode,
// exactly as it does in live mode; only the mode word on the bar changes.
func TestHistoricalSpaceReturnsToHandle(t *testing.T) {
	m := NewModel(nil, 100)
	m.width, m.height = 200, 12
	m.SetHistorical()
	m = imps(m, 5)

	m = press(m, 'k') // pull the shade down
	if m.onShade() {
		t.Fatalf("k should pull the shade down")
	}

	m = press(m, ' ')
	if !m.onShade() {
		t.Fatalf("space from held should return to the handle in historical mode")
	}
}
