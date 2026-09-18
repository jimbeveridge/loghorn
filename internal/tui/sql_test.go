package tui

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/jimbeveridge/loghorn/internal/entry"
)

const stmt = "SELECT a, b FROM t WHERE id = $1"

func sqlEntry(s string) entry.Entry {
	return entry.Entry{
		Message:   "query",
		Important: true,
		JSON: map[string]any{
			"message":  "query",
			"metadata": map[string]any{"statement": s, "rows": 3.0},
		},
	}
}

// fakeFormatter stands in for sqlfmt.Format: a clause to a line, instantly, and
// it counts its calls.
type fakeFormatter struct {
	calls int
	err   error
}

func (f *fakeFormatter) format(s string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return strings.NewReplacer(" FROM ", "\nFROM ", " WHERE ", "\nWHERE ").Replace(s), nil
}

// sqlModel opens the detail pane on an entry holding stmt, with a fake
// formatter, and hands back the command the open produced without running it.
func sqlModel(t *testing.T, f *fakeFormatter) (Model, tea.Cmd) {
	t.Helper()
	m := NewModel(nil, 100)
	m.formatSQL = f.format
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = feed(m2.(Model), sqlEntry(stmt))
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = m2.(Model)
	if !m.showDetail {
		t.Fatalf("precondition: detail should be open")
	}
	return m, cmd
}

// deliver runs a command and feeds its messages back to the model, as Bubble Tea
// would.
func deliver(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatalf("expected a command")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = deliver(t, m, c)
		}
		return m
	}
	m2, _ := m.Update(msg)
	return m2.(Model)
}

func toggleDetail(m Model) (Model, tea.Cmd) {
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return m2.(Model), cmd
}

// Until the format lands the statement shows as logged; when it does, the open
// pane re-renders with the formatter's layout.
func TestStatementShownFormattedOnceReady(t *testing.T) {
	m, cmd := sqlModel(t, &fakeFormatter{})
	if got := m.plainDetail(); !strings.Contains(got, "  statement: |-\n    "+stmt) {
		t.Fatalf("before formatting, the statement should show as logged:\n%s", got)
	}

	m = deliver(t, m, cmd)
	want := "  statement: |-\n    SELECT a, b\n    FROM t\n    WHERE id = $1"
	if got := m.plainDetail(); !strings.Contains(got, want) {
		t.Fatalf("after formatting, want\n%s\nin\n%s", want, got)
	}
	if got := ansi.Strip(m.detail.View()); !strings.Contains(got, "    FROM t") {
		t.Fatalf("the open pane was not re-rendered:\n%s", got)
	}
}

// Each statement is formatted once: reopening it while the format runs, resizing,
// and reopening it afterwards all reuse the one result.
func TestStatementFormattedOnce(t *testing.T) {
	f := &fakeFormatter{}
	m, cmd := sqlModel(t, f)

	m, _ = toggleDetail(m) // close
	m, again := toggleDetail(m)
	if again != nil {
		t.Fatalf("reopening while the format runs should not start another")
	}

	m = deliver(t, m, cmd)
	m2, again := m.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
	m = m2.(Model)
	if again != nil {
		t.Fatalf("a resize should not reformat")
	}
	if got := m.plainDetail(); !strings.Contains(got, "    FROM t") {
		t.Fatalf("the formatted statement should survive a resize:\n%s", got)
	}

	m, _ = toggleDetail(m) // close
	if _, again = toggleDetail(m); again != nil {
		t.Fatalf("reopening a formatted statement should not reformat it")
	}
	if f.calls != 1 {
		t.Fatalf("formatter ran %d times, want 1", f.calls)
	}
}

// A statement the formatter can't parse stays as logged, and isn't retried.
func TestFormatFailureShowsStatementAsLogged(t *testing.T) {
	m, cmd := sqlModel(t, &fakeFormatter{err: errors.New("parse error")})
	m = deliver(t, m, cmd)
	if got := m.plainDetail(); !strings.Contains(got, "  statement: |-\n    "+stmt) {
		t.Fatalf("a failed format should leave the statement as logged:\n%s", got)
	}
	m, _ = toggleDetail(m) // close
	if _, again := toggleDetail(m); again != nil {
		t.Fatalf("a failed format should not be retried")
	}
}

// Yank copies the statement as formatted, and uncoloured.
func TestYankCopiesFormattedSQL(t *testing.T) {
	m, cmd := sqlModel(t, &fakeFormatter{})
	m = deliver(t, m, cmd)
	var copied string
	m.copy = func(s string) error { copied = s; return nil }
	m, _ = key(m, "y")
	if !strings.Contains(copied, "    SELECT a, b\n    FROM t") || strings.Contains(copied, "\x1b[") {
		t.Fatalf("yank should copy the formatted statement, uncoloured:\n%q", copied)
	}
}

// Only a statement inside a metadata object is SQL; a "statement" anywhere else
// is an ordinary string.
func TestOnlyMetadataStatementIsSQL(t *testing.T) {
	v := map[string]any{
		"statement": "SELECT 1",
		"metadata":  map[string]any{"statement": "SELECT 2"},
		"other":     map[string]any{"statement": "SELECT 3"},
	}
	got := renderYAML(v, yamlOpts{plain: true, sql: func(s string) string { return s + "\nFORMATTED" }})
	for _, want := range []string{
		"metadata:\n  statement: |-\n    SELECT 2\n    FORMATTED",
		"other:\n  statement: SELECT 3",
		"\nstatement: SELECT 1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("want\n%s\nin\n%s", want, got)
		}
	}
	if got := sqlStatements(v); !reflect.DeepEqual(got, []string{"SELECT 2"}) {
		t.Fatalf("sqlStatements = %q, want only the metadata one", got)
	}
}

// Statements are found at any depth, sequences included, so an entry holding
// several formats each of them.
func TestSQLStatementsAtAnyDepth(t *testing.T) {
	v := map[string]any{
		"jsonPayload": map[string]any{"metadata": map[string]any{"statement": "SELECT 1"}},
		"batch": []any{
			map[string]any{"metadata": map[string]any{"statement": "SELECT 2"}},
			map[string]any{"metadata": map[string]any{"statement": "  "}}, // blank: nothing to format
		},
	}
	got := sqlStatements(v)
	sort.Strings(got)
	if want := []string{"SELECT 1", "SELECT 2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sqlStatements = %q, want %q", got, want)
	}
}

// SQL is coloured token by token, and every line on its own, so a comment that
// spans a line break is still coloured after it.
func TestHighlightSQL(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	const src = "SELECT 1 /* one\ntwo */ FROM t"
	lines := highlightSQL(src, false)
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %q", lines)
	}
	if !strings.Contains(lines[0], sqlKeywordStyle.Render("SELECT")) || !strings.Contains(lines[0], numStyle.Render("1")) {
		t.Fatalf("keyword and number should be coloured: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "\x1b[") {
		t.Fatalf("the comment's second line lost its colour: %q", lines[1])
	}
	if !strings.Contains(lines[1], sqlKeywordStyle.Render("FROM")) {
		t.Fatalf("keyword after the comment should be coloured: %q", lines[1])
	}
	if got := ansi.Strip(strings.Join(lines, "\n")); got != src {
		t.Fatalf("highlighting changed the text: %q", got)
	}
	if got := highlightSQL(src, true); !reflect.DeepEqual(got, strings.Split(src, "\n")) {
		t.Fatalf("plain should be the statement's lines untouched: %q", got)
	}
}

// The shape our backend actually logs: metadata.statement is an object, and the
// SQL sits under its "sql" key alongside the driver's own flags. Detecting only
// a string statement left every real query rendered as one long green line —
// hard-wrapped as prose, never formatted, never coloured as SQL.
func TestStatementObjectSQLIsSQL(t *testing.T) {
	v := map[string]any{
		"metadata": map[string]any{
			"operation": "QUERY",
			"statement": map[string]any{
				"rowsAsArray": true,
				"sql":         "SELECT a, b FROM t WHERE id = $1",
			},
		},
	}
	got := renderYAML(v, yamlOpts{plain: true, sql: func(s string) string { return s + "\nFORMATTED" }})
	want := "statement:\n    rowsAsArray: true\n    sql: |-\n      SELECT a, b FROM t WHERE id = $1\n      FORMATTED"
	if !strings.Contains(got, want) {
		t.Fatalf("want\n%s\nin\n%s", want, got)
	}
	if got := sqlStatements(v); !reflect.DeepEqual(got, []string{"SELECT a, b FROM t WHERE id = $1"}) {
		t.Fatalf("sqlStatements = %q, want the statement object's sql", got)
	}
}

// A "sql" key only reads as SQL directly under a "statement" object, the same
// way a bare "statement" only reads as SQL directly under "metadata".
func TestSQLKeyOutsideStatementIsOrdinary(t *testing.T) {
	v := map[string]any{
		"sql":   "SELECT 1",
		"other": map[string]any{"sql": "SELECT 2"},
	}
	got := renderYAML(v, yamlOpts{plain: true, sql: func(s string) string { return s + "\nFORMATTED" }})
	for _, want := range []string{"\nsql: SELECT 1", "other:\n  sql: SELECT 2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("want\n%s\nin\n%s", want, got)
		}
	}
	if got := sqlStatements(v); len(got) != 0 {
		t.Fatalf("sqlStatements = %q, want none", got)
	}
}
