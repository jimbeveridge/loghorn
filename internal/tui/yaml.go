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
	keyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("39")) // blue
	strStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("green"))
	numStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // orange
	boolStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212")) // magenta-ish
	nullStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245")) // gray
)

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
	var b strings.Builder
	switch t := v.(type) {
	case map[string]any:
		writeYAMLMap(&b, t, 0, plain)
	case []any:
		writeYAMLArray(&b, t, 0, plain)
	default:
		writeYAMLScalar(&b, v, 0, plain)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeYAMLMap(b *strings.Builder, m map[string]any, depth int, plain bool) {
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
		b.WriteString(color(keyStyle, yamlScalarText(k), plain))
		b.WriteString(":")
		writeYAMLField(b, m[k], depth, plain)
	}
}

func writeYAMLArray(b *strings.Builder, arr []any, depth int, plain bool) {
	for _, item := range arr {
		b.WriteString(indent(depth))
		b.WriteString("-")
		writeYAMLField(b, item, depth, plain)
	}
}

// writeYAMLField writes what follows a mapping key's ':' or a sequence
// entry's '-': inline for scalars and empty collections, on indented lines
// beneath for non-empty ones.
func writeYAMLField(b *strings.Builder, v any, depth int, plain bool) {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			b.WriteString(" {}\n")
			return
		}
		b.WriteString("\n")
		writeYAMLMap(b, t, depth+1, plain)
	case []any:
		if len(t) == 0 {
			b.WriteString(" []\n")
			return
		}
		b.WriteString("\n")
		writeYAMLArray(b, t, depth+1, plain)
	default:
		b.WriteString(" ")
		writeYAMLScalar(b, v, depth, plain)
		b.WriteString("\n")
	}
}

func writeYAMLScalar(b *strings.Builder, v any, depth int, plain bool) {
	switch t := v.(type) {
	case string:
		if strings.Contains(t, "\n") {
			writeYAMLBlockString(b, t, depth, plain)
			return
		}
		b.WriteString(color(strStyle, yamlScalarText(t), plain))
	case float64:
		b.WriteString(color(numStyle, strconv.FormatFloat(t, 'g', -1, 64), plain))
	case bool:
		b.WriteString(color(boolStyle, strconv.FormatBool(t), plain))
	case nil:
		b.WriteString(color(nullStyle, "null", plain))
	default:
		b.WriteString(color(strStyle, yamlScalarText(fmt.Sprint(t)), plain))
	}
}

// writeYAMLBlockString renders a multi-line string as a YAML literal block
// scalar: a stack trace has to read as a stack trace, not as one endless line
// of \n escapes. Every content line is indented one level past its key or
// dash, verbatim — literal blocks need no escaping at all.
func writeYAMLBlockString(b *strings.Builder, s string, depth int, plain bool) {
	b.WriteString("|-\n")
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		b.WriteString(indent(depth + 1))
		b.WriteString(color(strStyle, line, plain))
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
