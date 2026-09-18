package tui

import (
	"regexp"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jimbeveridge/loghorn/internal/entry"
)

// SQL logged under a "metadata" object is shown formatted and coloured in the
// detail pane — see sqlStatementAt for the shapes that qualify. Formatting is
// slow (see sqlfmt.Format), so it runs as a command when the pane opens and lands
// back as a sqlFormattedMsg. Until then the statement shows as logged, still
// coloured — and stays that way if the formatter can't parse it, which is no
// worse than before.
//
// The formatter has no line width to give it, but it doesn't need one: it puts
// each clause and select item on its own line. A resize re-wraps whatever is
// still too wide and never reformats.

// Generic SQL: loghorn doesn't know which database wrote the statement.
var sqlLexer = chroma.Coalesce(lexers.Get("sql"))

// sqlStatementAt reports whether the value under key, inside a map that was itself
// the value of parent, is a SQL statement to format. Two shapes qualify:
//
//	metadata.statement   the statement as a bare string
//	statement.sql        the statement object a driver logs, which carries its own
//	                     flags (rowsAsArray, …) alongside the SQL
//
// Each is anchored by the key above it, so a "statement" or a "sql" anywhere else
// in an entry stays an ordinary string. Recognizing only the first shape left our
// own backend's queries — which log the second — rendered as one long green line,
// hard-wrapped as prose and never formatted.
func sqlStatementAt(parent, key string, v any) (string, bool) {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", false
	}
	switch {
	case parent == "metadata" && key == "statement":
	case parent == "statement" && key == "sql":
	default:
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

// bareIdentifier matches an identifier that needs no quoting: a word of the
// characters an unquoted SQL name may hold, not starting with a digit. The
// lexer settles the harder question — whether the word is reserved — so this
// only has to rule out shapes no dialect would accept bare.
var bareIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

// unquoteIdentifiers drops the backticks around an identifier that does not need
// them, so a generated statement reads as SQL rather than as punctuation:
//
//	select `id`, `user_id` from `grants`   ->  select id, user_id from grants
//
// ORMs quote every identifier unconditionally, which is right for them and
// unreadable for us. Dropping the quotes is only safe where they carry no
// meaning, and the SQL lexer already decides that: it emits each backtick as its
// own Operator token and lexes what sits between them normally, so `order` comes
// back as a Keyword and `id` as a Name. A quoted run is unquoted only when it is
// exactly one plain Name token that also looks like a bare identifier —
// `order`, `weird name` and `2fast` all keep their quotes, and so does anything
// the lexer typed as something other than a plain Name, such as a data-type
// word. Backticks inside a string literal are never even seen, since the lexer
// hands the literal over whole.
//
// Only what the detail pane shows is rewritten. Entry.Raw and the log file keep
// the statement as it was logged, and the statement this works from is the
// formatter's output, so a wrong call here can never turn into a parse failure.
// It runs only with [display] unquote-identifiers on.
func unquoteIdentifiers(sql string) string {
	it, err := sqlLexer.Tokenise(nil, sql)
	if err != nil {
		return sql
	}
	var toks []chroma.Token
	for tok := it(); tok != chroma.EOF; tok = it() {
		toks = append(toks, tok)
	}
	var b strings.Builder
	b.Grow(len(sql))
	for i := 0; i < len(toks); i++ {
		if i+2 < len(toks) && isBacktick(toks[i]) && isBacktick(toks[i+2]) &&
			toks[i+1].Type == chroma.Name && bareIdentifier.MatchString(toks[i+1].Value) {
			b.WriteString(toks[i+1].Value)
			i += 2
			continue
		}
		b.WriteString(toks[i].Value)
	}
	return b.String()
}

func isBacktick(t chroma.Token) bool { return t.Type == chroma.Operator && t.Value == "`" }

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
	if !m.display.FormatSQL {
		return nil // [display] format-sql is off; show statements as logged
	}
	var cmds []tea.Cmd
	for _, stmt := range sqlStatements(e.JSON) {
		if _, seen := m.sql[stmt]; seen {
			continue
		}
		m.sql[stmt] = "" // in flight
		format, opts := m.formatSQL, m.sqlOptions()
		cmds = append(cmds, func() tea.Msg {
			text, err := format(stmt, opts)
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
