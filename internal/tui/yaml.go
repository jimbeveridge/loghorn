package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"
)

var (
	keyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))  // blue
	strStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))   // green
	numStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // orange
	boolStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212")) // magenta-ish
	nullStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245")) // gray
)

// yamlOpts is what the YAML writers share beyond the value itself.
type yamlOpts struct {
	plain bool // suppress all colour
	// sql maps a SQL statement, as logged, to the text to show for it: the
	// formatted statement once it's ready. Nil shows statements as logged.
	sql func(stmt string) string
}

// RenderYAML pretty-prints a decoded JSON/YAML value as YAML, with sorted
// keys and 2-space indentation. plain=true suppresses all color.
//
// This replaced a JSON pretty-printer that quoted every string and escaped
// embedded newlines as literal \n — unreadable for stack traces, and
// error-prone once a value itself contained a quote. Single-line scalars are
// quoted exactly as the yaml.v3 encoder would quote them (so a value like
// "16" still reads as a string, not a number), and multi-line strings use a
// literal block (|-) instead: real line breaks, no escaping at all.
func RenderYAML(v any, plain bool) string {
	return renderYAML(v, yamlOpts{plain: plain})
}

func renderYAML(v any, o yamlOpts) string {
	var b strings.Builder
	switch t := v.(type) {
	case map[string]any:
		writeYAMLMap(&b, t, 0, "", o)
	case []any:
		writeYAMLArray(&b, t, 0, o)
	default:
		writeYAMLScalar(&b, v, 0, o)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// writeYAMLMap writes m's entries at depth. parent is the key m itself sits
// under, or "" at the top level and inside sequences; it is how a
// metadata.statement is told apart from any other "statement".
func writeYAMLMap(b *strings.Builder, m map[string]any, depth int, parent string, o yamlOpts) {
	if len(m) == 0 {
		b.WriteString(indent(depth))
		b.WriteString("{}\n")
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(indent(depth))
		b.WriteString(color(keyStyle, yamlScalarText(k), o.plain))
		b.WriteString(":")
		if stmt, ok := sqlStatementAt(parent, k, m[k]); ok {
			b.WriteString(" ")
			writeYAMLSQL(b, stmt, depth, o)
			b.WriteString("\n")
			continue
		}
		writeYAMLField(b, m[k], depth, k, o)
	}
}

func writeYAMLArray(b *strings.Builder, arr []any, depth int, o yamlOpts) {
	for _, item := range arr {
		b.WriteString(indent(depth))
		b.WriteString("-")
		writeYAMLField(b, item, depth, "", o)
	}
}

// writeYAMLField writes what follows a mapping key's ':' or a sequence
// entry's '-': inline for scalars and empty collections, on indented lines
// beneath for non-empty ones. key is the mapping key, or "" for a sequence
// entry.
func writeYAMLField(b *strings.Builder, v any, depth int, key string, o yamlOpts) {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			b.WriteString(" {}\n")
			return
		}
		b.WriteString("\n")
		writeYAMLMap(b, t, depth+1, key, o)
	case []any:
		if len(t) == 0 {
			b.WriteString(" []\n")
			return
		}
		b.WriteString("\n")
		writeYAMLArray(b, t, depth+1, o)
	default:
		b.WriteString(" ")
		writeYAMLScalar(b, v, depth, o)
		b.WriteString("\n")
	}
}

func writeYAMLScalar(b *strings.Builder, v any, depth int, o yamlOpts) {
	switch t := v.(type) {
	case string:
		if strings.Contains(t, "\n") {
			writeYAMLBlockString(b, t, depth, o)
			return
		}
		b.WriteString(color(strStyle, yamlScalarText(t), o.plain))
	case float64:
		b.WriteString(color(numStyle, strconv.FormatFloat(t, 'g', -1, 64), o.plain))
	case bool:
		b.WriteString(color(boolStyle, strconv.FormatBool(t), o.plain))
	case nil:
		b.WriteString(color(nullStyle, "null", o.plain))
	default:
		b.WriteString(color(strStyle, yamlScalarText(fmt.Sprint(t)), o.plain))
	}
}

// writeYAMLBlockString renders a multi-line string as a YAML literal block
// scalar: a stack trace has to read as a stack trace, not as one endless line
// of \n escapes. Every content line is indented one level past its key or
// dash, verbatim — literal blocks need no escaping at all.
func writeYAMLBlockString(b *strings.Builder, s string, depth int, o yamlOpts) {
	b.WriteString("|-\n")
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		b.WriteString(indent(depth + 1))
		b.WriteString(color(strStyle, line, o.plain))
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
}

// writeYAMLSQL renders a SQL statement as a literal block, the way a multi-line
// string is, but with the formatter's layout once it's ready, and coloured as
// SQL rather than as one green string. It is a block even while the statement is
// still one line, so the pane doesn't change shape when the format lands.
func writeYAMLSQL(b *strings.Builder, stmt string, depth int, o yamlOpts) {
	text := stmt
	if o.sql != nil {
		text = o.sql(stmt)
	}
	b.WriteString("|-\n")
	lines := highlightSQL(strings.TrimRight(text, "\n"), o.plain)
	for i, line := range lines {
		b.WriteString(indent(depth + 1))
		b.WriteString(strings.TrimSuffix(line, "\r"))
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
}

// yamlScalarText renders a single-line string as yaml.v3 itself would quote
// it, so quoting rules (numeric- or boolean-looking strings, leading dashes,
// embedded ": ", …) stay correct without reimplementing the YAML spec.
func yamlScalarText(s string) string {
	b, err := yaml.Marshal(s)
	if err != nil {
		return s
	}
	return strings.TrimRight(string(b), "\n")
}

func indent(depth int) string { return strings.Repeat("  ", depth) }

func color(style lipgloss.Style, s string, plain bool) string {
	if plain {
		return s
	}
	return style.Render(s)
}
