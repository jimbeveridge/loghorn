package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	keyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("39")) // blue
	strStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("green"))
	numStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // orange
	boolStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212")) // magenta-ish
	nullStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245")) // gray
)

// RenderJSON pretty-prints a decoded JSON value with sorted keys and 2-space
// indentation. plain=true suppresses all color.
func RenderJSON(v any, plain bool) string {
	var b strings.Builder
	writeValue(&b, v, 0, plain)
	return b.String()
}

func writeValue(b *strings.Builder, v any, depth int, plain bool) {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			b.WriteString("{}")
			return
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("{\n")
		for i, k := range keys {
			b.WriteString(indent(depth + 1))
			b.WriteString(color(keyStyle, strconv.Quote(k), plain))
			b.WriteString(": ")
			writeValue(b, t[k], depth+1, plain)
			if i < len(keys)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(indent(depth))
		b.WriteString("}")
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, item := range t {
			b.WriteString(indent(depth + 1))
			writeValue(b, item, depth+1, plain)
			if i < len(t)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(indent(depth))
		b.WriteString("]")
	case string:
		b.WriteString(color(strStyle, strconv.Quote(t), plain))
	case float64:
		b.WriteString(color(numStyle, strconv.FormatFloat(t, 'g', -1, 64), plain))
	case bool:
		b.WriteString(color(boolStyle, strconv.FormatBool(t), plain))
	case nil:
		b.WriteString(color(nullStyle, "null", plain))
	default:
		b.WriteString(color(strStyle, fmt.Sprintf("%q", fmt.Sprint(t)), plain))
	}
}

func indent(depth int) string { return strings.Repeat("  ", depth) }

func color(style lipgloss.Style, s string, plain bool) string {
	if plain {
		return s
	}
	return style.Render(s)
}
