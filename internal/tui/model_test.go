package tui

import (
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
