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

// G also returns to the handle in historical mode, same as live, and the bar
// still reads HISTORICAL once there.
func TestHistoricalGReturnsToHandle(t *testing.T) {
	m := NewModel(nil, 100)
	m.width, m.height = 200, 12
	m.SetHistorical()
	m = imps(m, 5)

	m = press(m, 'k') // pull the shade down
	if m.onShade() {
		t.Fatalf("k should pull the shade down")
	}

	m = press(m, 'G')
	if !m.onShade() {
		t.Fatalf("G should return to the handle in historical mode")
	}
	if got := m.statusBar(); !strings.Contains(got, "HISTORICAL") {
		t.Fatalf("bar should still read HISTORICAL after G:\n%s", got)
	}
}

// Clicking the bar grabs the handle in historical mode too, exactly as it
// does live.
func TestHistoricalClickingBarGrabsHandle(t *testing.T) {
	m := NewModel(nil, 100)
	m.width, m.height = 200, 12
	m.SetHistorical()
	m = imps(m, 5)

	m = press(m, 'k') // pull the shade down
	if m.onShade() {
		t.Fatalf("k should pull the shade down")
	}

	m = clickAt(m, 10, m.height-1) // the bar
	if !m.onShade() {
		t.Fatalf("clicking the bar should grab the handle in historical mode")
	}
	if got := m.statusBar(); !strings.Contains(got, "HISTORICAL") {
		t.Fatalf("bar should still read HISTORICAL after clicking the handle:\n%s", got)
	}
}

// Held with rows arriving behind the shade, the bar still reads HISTORICAL
// alongside the usual ▼ waiting counter.
func TestHistoricalHeldShowsBacklog(t *testing.T) {
	m := NewModel(nil, 1000)
	m.width, m.height = 60, 5
	m.SetHistorical()
	m = imps(m, 5)

	m = press(m, 'k') // pull the shade down
	m = imps(m, 3)    // more rows arrive behind the shade

	got := m.statusBar()
	if !strings.Contains(got, "HISTORICAL") {
		t.Fatalf("bar should read HISTORICAL while held with a backlog:\n%s", got)
	}
	// ▼ alone would also match the empty "▼0 of 0 waiting" printed the instant
	// the shade comes down, so assert the actual count once rows have arrived.
	if !strings.Contains(got, "▼3 of 3 waiting") {
		t.Fatalf("bar should show 3 of 3 waiting:\n%s", got)
	}
}

// The help overlay describes the handle as the newest record in historical
// mode, since there is no live stream to describe; live mode's own wording
// is unchanged.
func TestHistoricalHelpOverlayWording(t *testing.T) {
	live := NewModel(nil, 100)
	live.width, live.height = 100, 20
	if got := live.helpContent(); !strings.Contains(got, "on it live") {
		t.Fatalf("live mode help should still describe the handle as live:\n%s", got)
	}

	m := NewModel(nil, 100)
	m.width, m.height = 100, 20
	m.SetHistorical()
	got := m.helpContent()
	if strings.Contains(got, "live") {
		t.Fatalf("historical mode help should not say live:\n%s", got)
	}
	if !strings.Contains(got, "newest record") {
		t.Fatalf("historical mode help should describe the handle by the newest record:\n%s", got)
	}
}

// The help overlay's Producer section says "reading a pipe" for any run with
// no child, which is wrong for -historical: it reads stored files and
// refuses a command entirely, so "start as loghorn -- <command>" is not
// applicable advice. Historical mode gets its own note; live/pipe mode's
// wording is unchanged.
func TestHistoricalHelpOverlayProducerWording(t *testing.T) {
	live := NewModel(nil, 100)
	live.width, live.height = 100, 20
	if got := live.helpContent(); !strings.Contains(got, "reading a pipe") {
		t.Fatalf("pipe mode help should still say reading a pipe:\n%s", got)
	}

	m := NewModel(nil, 100)
	m.width, m.height = 100, 20
	m.SetHistorical()
	got := m.helpContent()
	if strings.Contains(got, "reading a pipe") {
		t.Fatalf("historical mode help should not say reading a pipe:\n%s", got)
	}
	if !strings.Contains(got, "stored logs") {
		t.Fatalf("historical mode help should mention the stored logs:\n%s", got)
	}
}
