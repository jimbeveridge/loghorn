package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbeveridge/loghorn/internal/entry"
)

// typeFind types '/', the text, and enter.
func typeFind(m Model, text string) Model {
	m, _ = key(m, "/")
	for _, r := range text {
		m, _ = key(m, string(r))
	}
	m, _ = key(m, "enter")
	return m
}

func backspace(m Model) Model {
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	return m2.(Model)
}

// A find keeps only the lines containing its text.
func TestFindNarrowsTheView(t *testing.T) {
	m := filterModel()
	m, _ = key(m, "a")
	m = typeFind(m, "query")
	if want := []string{"A query", "B query"}; !equal(messages(m), want) {
		t.Fatalf("got %v, want %v", messages(m), want)
	}
	if m.finding || m.find != "query" {
		t.Fatalf("enter should close the prompt and apply the find (finding=%v find=%q)", m.finding, m.find)
	}
}

func TestFindIgnoresCase(t *testing.T) {
	m := filterModel()
	m, _ = key(m, "a")
	m = typeFind(m, "BOOM")
	if want := []string{"A boom", "B boom"}; !equal(messages(m), want) {
		t.Fatalf("got %v, want %v", messages(m), want)
	}
}

// The find stacks with the pin and the failures filter, and survives both
// changing around it.
func TestFindStacksWithOtherFilters(t *testing.T) {
	m := filterModel()
	m, _ = key(m, "k")
	m, _ = key(m, "k") // "A boom"
	m, _ = key(m, "c")
	m, _ = key(m, "a")
	if want := []string{"A start", "A query", "A boom"}; !equal(messages(m), want) {
		t.Fatalf("precondition: request A entire, got %v", messages(m))
	}

	m = typeFind(m, "query")
	if want := []string{"A query"}; !equal(messages(m), want) {
		t.Fatalf("find within the pin: got %v, want %v", messages(m), want)
	}
	m, _ = key(m, "a") // failures only: A query isn't one
	if len(m.rows) != 0 {
		t.Fatalf("find + pin + failures should be empty, got %v", messages(m))
	}
	m, _ = key(m, "a")
	if want := []string{"A query"}; !equal(messages(m), want) {
		t.Fatalf("the find should survive toggling a: got %v", messages(m))
	}
}

// Like the other filters, the find applies to lines as they arrive.
func TestFindAppliesToNewLines(t *testing.T) {
	m := filterModel()
	m, _ = key(m, "a")
	m = typeFind(m, "boom")
	m = feed(m, req("C", "C boom", false), req("C", "C fine", false))
	if want := []string{"A boom", "B boom", "C boom"}; !equal(messages(m), want) {
		t.Fatalf("got %v, want %v", messages(m), want)
	}
}

// '/' then a bare enter turns the find off.
func TestSlashEnterClearsFind(t *testing.T) {
	m := filterModel()
	m, _ = key(m, "a")
	m = typeFind(m, "query")
	m = typeFind(m, "")
	if m.find != "" || len(m.rows) != 7 {
		t.Fatalf("find should be off and every line back: find=%q rows=%v", m.find, messages(m))
	}
}

// esc abandons what was typed and leaves the active find alone.
func TestEscKeepsTheActiveFind(t *testing.T) {
	m := filterModel()
	m, _ = key(m, "a")
	m = typeFind(m, "query")
	m, _ = key(m, "/")
	m, _ = key(m, "x")
	m, _ = key(m, "esc")
	if m.finding || m.find != "query" || len(m.rows) != 2 {
		t.Fatalf("esc should keep the find (finding=%v find=%q rows=%v)", m.finding, m.find, messages(m))
	}
}

// While the prompt is open every key is text: nothing toggles, opens or quits.
func TestPromptTakesEveryKeyAsText(t *testing.T) {
	m := filterModel()
	m, _ = key(m, "a")
	m, _ = key(m, "/")
	for _, k := range []string{"a", "c", "q", " ", "?", "j"} {
		var cmd tea.Cmd
		if m, cmd = key(m, k); cmd != nil {
			t.Fatalf("%q in the prompt returned a command", k)
		}
	}
	if m.findInput != "acq ?j" {
		t.Fatalf("prompt holds %q, want %q", m.findInput, "acq ?j")
	}
	if !m.showAll || m.pinned || m.showHelp {
		t.Fatalf("keys in the prompt fired actions (all=%v pinned=%v help=%v)", m.showAll, m.pinned, m.showHelp)
	}
}

// Backspace edits, and backspacing past the start closes the prompt.
func TestFindBackspace(t *testing.T) {
	m := filterModel()
	m, _ = key(m, "/")
	m, _ = key(m, "a")
	m, _ = key(m, "b")
	if m = backspace(m); m.findInput != "a" {
		t.Fatalf("backspace should delete one rune, prompt holds %q", m.findInput)
	}
	m = backspace(m)
	if !m.finding {
		t.Fatalf("backspacing to empty should leave the prompt open")
	}
	if m = backspace(m); m.finding {
		t.Fatalf("backspace on an empty prompt should close it")
	}
}

// The raw line is searched, so a field the list doesn't show still matches.
func TestFindMatchesTheWholeLine(t *testing.T) {
	m := NewModel(nil, 100)
	m.width, m.height = 120, 24
	m = feed(m,
		entry.Entry{Message: "login failed", Important: true, Raw: []byte(`{"message":"login failed","user":"bob@example.com"}`)},
		entry.Entry{Message: "login failed", Important: true, Raw: []byte(`{"message":"login failed","user":"eve@example.com"}`)},
	)
	m = typeFind(m, "bob@")
	if len(m.rows) != 1 || !strings.Contains(string(m.rows[0].Entry.Raw), "bob@") {
		t.Fatalf("want just bob's line, got %d rows", len(m.rows))
	}
}

// The bar shows the prompt while typing, names an active find like the other
// filters, and says what a bare enter will do.
func TestStatusBarShowsFind(t *testing.T) {
	m := filterModel()
	m, _ = key(m, "/")
	m, _ = key(m, "b")
	m, _ = key(m, "o")
	if bar := m.statusBar(); !strings.Contains(bar, "/bo") || !strings.Contains(bar, "esc cancel") {
		t.Fatalf("the bar should show the prompt:\n%s", bar)
	}
	m, _ = key(m, "enter")
	if bar := m.statusBar(); !strings.Contains(bar, " · /bo") {
		t.Fatalf("the bar should name the active find:\n%s", bar)
	}
	m, _ = key(m, "/")
	if bar := m.statusBar(); !strings.Contains(bar, "enter clears /bo") {
		t.Fatalf("an empty prompt should say enter clears the find:\n%s", bar)
	}
	m, _ = key(m, "enter")
	if bar := m.statusBar(); strings.Contains(bar, "/bo") {
		t.Fatalf("a cleared find should leave the bar:\n%s", bar)
	}
}

// '/' works from the detail pane too, like the other filters, and leaves it open.
func TestFindFromDetailPane(t *testing.T) {
	m := filterModel()
	m, _ = key(m, "k")
	m, _ = key(m, "enter")
	if !m.showDetail {
		t.Fatalf("precondition: detail should be open")
	}
	m = typeFind(m, "boom")
	if !m.showDetail || m.find != "boom" {
		t.Fatalf("find from the pane: detail=%v find=%q", m.showDetail, m.find)
	}
}

// The verdict cache is bounded by the ring, not by everything ever read, and
// stays correct as the ring evicts.
func TestFindCacheStaysBounded(t *testing.T) {
	const capacity = 10
	m := NewModel(nil, capacity)
	m.width, m.height = 120, 24
	m = typeFind(m, "keep")
	for i := 0; i < 500; i++ {
		m = feed(m,
			entry.Entry{Message: fmt.Sprintf("keep %d", i), Important: true},
			entry.Entry{Message: fmt.Sprintf("drop %d", i), Important: true},
		)
	}
	if len(m.rows) != capacity/2 {
		t.Fatalf("want the %d kept lines still in the ring, got %v", capacity/2, messages(m))
	}
	for _, msg := range messages(m) {
		if !strings.HasPrefix(msg, "keep") {
			t.Fatalf("a non-matching line got through: %v", messages(m))
		}
	}
	if got, limit := len(m.findHits), 2*capacity+64; got > limit {
		t.Fatalf("cache holds %d verdicts, bound is %d", got, limit)
	}
}
