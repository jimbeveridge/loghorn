package tui

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jimbeveridge/loghorn/internal/entry"
)

// SQL logged under a "metadata" object's "statement" key is shown formatted and
// coloured in the detail pane. Formatting is slow (see sqlfmt.Format), so it runs
// as a command when the pane opens and lands back as a sqlFormattedMsg. Until then
// the statement shows as logged, still coloured — and stays that way if the
// formatter can't parse it, which is no worse than before.
//
// The formatter has no line width to give it, but it doesn't need one: it puts
// each clause and select item on its own line. A resize re-wraps whatever is
// still too wide and never reformats.

var (
	sqlKeywordStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("141")) // purple
	sqlTypeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))  // cyan
)

// Generic SQL: loghorn doesn't know which database wrote the statement.
var sqlLexer = chroma.Coalesce(lexers.Get("sql"))

// sqlStatementAt reports whether the value under key, inside a map that was itself
// the value of parent, is a SQL statement to format.
func sqlStatementAt(parent, key string, v any) (string, bool) {
	s, ok := v.(string)
	if !ok || parent != "metadata" || key != "statement" || strings.TrimSpace(s) == "" {
		return "", false
	}
	return s, true
}

// sqlStatements finds every statement in a decoded entry that the YAML renderer
// will show as SQL, at any depth.
func sqlStatements(v any) []string {
	var out []string
	var walk func(v any, parent string)
	walk = func(v any, parent string) {
		switch t := v.(type) {
		case map[string]any:
			for k, child := range t {
				if s, ok := sqlStatementAt(parent, k, child); ok {
					out = append(out, s)
					continue
				}
				walk(child, k)
			}
		case []any:
			for _, item := range t {
				walk(item, "")
			}
		}
	}
	walk(v, "")
	return out
}

// sqlCache maps a statement as logged to the text to show for it. A key held
// with an empty value is still being formatted — a finished one is never empty,
// since a failure stores the statement itself — so opening the same statement
// again doesn't start a second format.
type sqlCache map[string]string

func (c sqlCache) text(stmt string) string {
	if f := c[stmt]; f != "" {
		return f
	}
	return stmt
}

type sqlFormattedMsg struct{ stmt, text string }

// formatSQLCmd starts formatting each statement in e that isn't already
// formatted or in flight. It returns nil when there is nothing to start.
func (m Model) formatSQLCmd(e entry.Entry) tea.Cmd {
	if m.sql == nil || m.formatSQL == nil {
		return nil // a Model not built by NewModel; show statements as logged
	}
	var cmds []tea.Cmd
	for _, stmt := range sqlStatements(e.JSON) {
		if _, seen := m.sql[stmt]; seen {
			continue
		}
		m.sql[stmt] = "" // in flight
		format := m.formatSQL
		cmds = append(cmds, func() tea.Msg {
			text, err := format(stmt)
			if err != nil || strings.TrimSpace(text) == "" {
				text = stmt
			}
			return sqlFormattedMsg{stmt: stmt, text: text}
		})
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// highlightSQL colours sql and returns it split into lines, each coloured on its
// own: the pane is composited line by line, so a style can't be left open across
// a line break the way chroma's terminal formatters leave it for a multi-line
// comment or string. The colours are the YAML palette's, so a statement reads as
// part of the same entry.
func highlightSQL(sql string, plain bool) []string {
	if plain {
		return strings.Split(sql, "\n")
	}
	it, err := sqlLexer.Tokenise(nil, sql)
	if err != nil {
		return strings.Split(sql, "\n")
	}
	var lines []string
	var cur strings.Builder
	for tok := it(); tok != chroma.EOF; tok = it() {
		style, styled := sqlTokenStyle(tok.Type)
		for i, piece := range strings.Split(tok.Value, "\n") {
			if i > 0 {
				lines = append(lines, cur.String())
				cur.Reset()
			}
			if piece == "" {
				continue
			}
			if styled {
				piece = style.Render(piece)
			}
			cur.WriteString(piece)
		}
	}
	return append(lines, cur.String())
}

func sqlTokenStyle(t chroma.TokenType) (lipgloss.Style, bool) {
	switch {
	case t.InCategory(chroma.Keyword):
		return sqlKeywordStyle, true
	case t.InSubCategory(chroma.NameBuiltin): // data types
		return sqlTypeStyle, true
	case t.InSubCategory(chroma.NameVariable): // bind parameters
		return boolStyle, true
	case t.InSubCategory(chroma.LiteralString):
		return strStyle, true
	case t.InSubCategory(chroma.LiteralNumber):
		return numStyle, true
	case t.InCategory(chroma.Comment):
		return nullStyle, true
	}
	return lipgloss.Style{}, false
}
