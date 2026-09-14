package tui

import (
	"strings"
	"testing"
)

// The reported bug: held content drifts even when none of the rows on screen
// were evicted. A single eviction anywhere in the ring — including well
// before the visible window — used to shift every row's index, so the held
// window silently slid forward to show newer content one row at a time as
// fast as lines arrived. That reads as "still scrolling" even though the
// shade is down.
func TestHeldContentStaysPutWhenOlderRowsAreEvicted(t *testing.T) {
	m := NewModel(nil, 20) // ring holds exactly 20
	m.width, m.height = 80, 10
	m.showAll = true

	m = imps(m, 20) // fill the ring exactly: E0..E19, no eviction yet

	// Hold with the cursor mid-stream; the tail-anchored window lands on the
	// newest rows (E11..E19 for a budget of 9), same as pressing space live.
	m = m.setSelected(15)
	if m.onShade() {
		t.Fatalf("precondition: should be held")
	}
	before := m.listLines(m.width)
	if len(before) == 0 {
		t.Fatalf("precondition: expected visible rows")
	}
	beforeText := renderTexts(before)

	// 5 more lines arrive, evicting the 5 oldest (E0..E4) — none of which are
	// anywhere near the visible window.
	m = imps(m, 5)

	after := m.listLines(m.width)
	afterText := renderTexts(after)

	if beforeText != afterText {
		t.Fatalf("held view changed even though none of its rows were evicted:\nbefore:\n%s\nafter:\n%s",
			beforeText, afterText)
	}
}

func renderTexts(lines []listLine) string {
	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i] = l.text
	}
	return strings.Join(texts, "\n")
}
