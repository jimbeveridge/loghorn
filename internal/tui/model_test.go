package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"clog/internal/entry"
)

func TestModelAppendsAndSelects(t *testing.T) {
	m := NewModel(nil, 100, 2)
	m.width, m.height = 80, 24

	// Feed two important entries via entryMsg.
	m2, _ := m.Update(entryMsg(entry.Entry{Message: "E1", Important: true}))
	m = m2.(Model)
	m2, _ = m.Update(entryMsg(entry.Entry{Message: "E2", Important: true}))
	m = m2.(Model)

	if len(m.rows) != 2 {
		t.Fatalf("expected 2 display rows, got %d", len(m.rows))
	}

	// Follow mode keeps selection on the last row.
	if m.selected != 1 {
		t.Fatalf("follow should select last row, got %d", m.selected)
	}

	// 'k' moves selection up and turns off follow.
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = m2.(Model)
	if m.selected != 0 || m.follow {
		t.Fatalf("after k: selected=%d follow=%v, want 0/false", m.selected, m.follow)
	}

	// 'enter' opens detail; 'esc' closes it.
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)
	if !m.showDetail {
		t.Fatalf("enter should open detail")
	}
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = m2.(Model)
	if m.showDetail {
		t.Fatalf("esc should close detail")
	}
}

func TestDetailPaneScrolls(t *testing.T) {
	m := NewModel(nil, 100, 2)
	m.width, m.height = 40, 10 // small viewport so content overflows

	// A raw entry whose content is far taller than the detail pane.
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	long := entry.Entry{Message: "big", Important: true, Raw: []byte(sb.String())}
	m2, _ := m.Update(entryMsg(long))
	m = m2.(Model)

	// Open the detail pane; it should start scrolled to the top.
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)
	if !m.showDetail {
		t.Fatalf("enter should open detail")
	}
	if m.detail.YOffset != 0 {
		t.Fatalf("detail should open at top, YOffset=%d", m.detail.YOffset)
	}

	// 'j' scrolls the detail viewport down (does NOT move the list selection).
	beforeSel := m.selected
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = m2.(Model)
	if m.detail.YOffset <= 0 {
		t.Fatalf("j should scroll the detail down, YOffset=%d", m.detail.YOffset)
	}
	if m.selected != beforeSel {
		t.Fatalf("scrolling detail must not move list selection (%d -> %d)", beforeSel, m.selected)
	}

	// 'esc' closes the detail pane.
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = m2.(Model)
	if m.showDetail {
		t.Fatalf("esc should close detail")
	}
}
