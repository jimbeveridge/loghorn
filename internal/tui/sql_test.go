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

	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/entry"
	"github.com/jimbeveridge/loghorn/internal/sqlfmt"
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

// fakeFormatter stands in for sqlfmt.FormatWith: a clause to a line, instantly.
// It counts its calls and keeps the options it was handed, so a test can check
// what the model asked for without re-testing sqlfmt's own behaviour.
type fakeFormatter struct {
	calls int
	err   error
	opts  sqlfmt.Options
}

func (f *fakeFormatter) format(s string, o sqlfmt.Options) (string, error) {
	f.calls++
	f.opts = o
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

// Backticks that carry no meaning come off; the ones holding a statement
// together stay on. The rule is the lexer's own: a quoted run that lexes to
// exactly one plain Name token is a bare identifier, and anything else — a
// reserved word, a name with a space in it, a digit-leading name — keeps its
// quotes. Backticks inside a string literal are never touched, because the
// lexer hands the whole literal over as one token.
func TestUnquoteIdentifiers(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain identifiers", "select `id`, `user_id` from `grants`", "select id, user_id from grants"},
		{"qualified", "where `grants`.`status` = ?", "where grants.status = ?"},
		{"reserved word kept", "select `order`, `id` from `grants`", "select `order`, id from grants"},
		{"name with a space kept", "select `weird name` from t", "select `weird name` from t"},
		{"digit-leading kept", "select `2fast` from t", "select `2fast` from t"},
		{"inside a string literal", "select 'a `b` c' from `t`", "select 'a `b` c' from t"},
		{"already bare", "select id from grants", "select id from grants"},
		{"empty quotes kept", "select `` from t", "select `` from t"},
		{"newlines preserved", "select\n  `id`,\n  `order`\nfrom\n  `grants`", "select\n  id,\n  `order`\nfrom\n  grants"},
	} {
		if got := unquoteIdentifiers(tc.in); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// End to end through the model: with the setting off a statement keeps every
// backtick it was logged with, and with it on the pane shows the unquoted form.
// The formatter runs first and the unquoting works on its output, so this also
// pins that the two steps compose in that order.
func TestUnquoteIdentifiersSetting(t *testing.T) {
	const quoted = "select `id` FROM `grants` WHERE `order` = 1"
	for _, tc := range []struct {
		name string
		on   bool
		want string
	}{
		{"off", false, "statement: |-\n    select `id`\n    FROM `grants`\n    WHERE `order` = 1"},
		{"on", true, "statement: |-\n    select id\n    FROM grants\n    WHERE `order` = 1"},
	} {
		d := config.Default().Display
		d.UnquoteIdentifiers = tc.on
		m := NewModel(nil, 100)
		m.SetDisplay(d)
		m.formatSQL = (&fakeFormatter{}).format
		m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
		m = feed(m2.(Model), sqlEntry(quoted))
		m, cmd := toggleDetail(m)
		m = deliver(t, m, cmd)
		if got := m.plainDetail(); !strings.Contains(got, tc.want) {
			t.Errorf("%s: want\n%s\nin\n%s", tc.name, tc.want, got)
		}
	}
}

// The configured keyword case reaches the formatter. What it does with it is
// sqlfmt's business and is tested there; what matters here is that the setting
// is read when the format is started, so replacing formatSQL in a test never
// has to be ordered against SetDisplay.
func TestKeywordCaseReachesTheFormatter(t *testing.T) {
	for _, kc := range []config.KeywordCase{config.KeywordPreserve, config.KeywordUpper, config.KeywordLower} {
		d := config.Default().Display
		d.KeywordCase = kc
		f := &fakeFormatter{}
		m := NewModel(nil, 100)
		m.SetDisplay(d)
		m.formatSQL = f.format
		m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
		m = feed(m2.(Model), sqlEntry(stmt))
		m, cmd := toggleDetail(m)
		deliver(t, m, cmd)
		if f.calls == 0 {
			t.Fatalf("%s: the formatter was never called", kc)
		}
		if f.opts.KeywordCase != string(kc) {
			t.Errorf("KeywordCase = %q, want %q", f.opts.KeywordCase, kc)
		}
	}
}

// format-sql off shows the statement as logged and never calls the formatter.
// It is still a literal block and still coloured — that is exactly the state a
// statement is already in while its format is in flight — so turning the key off
// changes the layout and nothing else.
func TestFormatSQLOff(t *testing.T) {
	f := &fakeFormatter{}
	d := config.Default().Display
	d.FormatSQL = false
	m := NewModel(nil, 100)
	m.SetDisplay(d)
	m.formatSQL = f.format
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = feed(m2.(Model), sqlEntry(stmt))
	m, cmd := toggleDetail(m)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			m2, _ = m.Update(msg)
			m = m2.(Model)
		}
	}
	if f.calls != 0 {
		t.Errorf("the formatter was called %d times with format-sql off", f.calls)
	}
	if got := m.plainDetail(); !strings.Contains(got, "statement: |-\n    "+stmt) {
		t.Errorf("the statement should show as logged:\n%s", got)
	}
}

// A Model that was never given a Display formats as loghorn always has. This is
// the trap in a default-true boolean: a zero config.Display would turn SQL
// formatting off for every caller that didn't ask for it.
func TestFormatSQLDefaultsOnWithoutSetDisplay(t *testing.T) {
	f := &fakeFormatter{}
	m := NewModel(nil, 100)
	m.formatSQL = f.format
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = feed(m2.(Model), sqlEntry(stmt))
	m, cmd := toggleDetail(m)
	deliver(t, m, cmd)
	if f.calls == 0 {
		t.Error("a Model with no SetDisplay should still format SQL")
	}
}
