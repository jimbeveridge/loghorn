package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Every colour in the detail pane's palette actually colours. lipgloss.Color
// takes an ANSI number or a hex value; anything else, such as a colour name,
// renders silently as plain text — which is how string values went uncoloured.
func TestPaletteColours(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	styles := map[string]lipgloss.Style{
		"key": keyStyle, "str": strStyle, "num": numStyle, "bool": boolStyle, "null": nullStyle,
		"sqlKeyword": sqlKeywordStyle, "sqlType": sqlTypeStyle,
	}
	for name, style := range styles {
		if got := style.Render("x"); got == "x" {
			t.Errorf("%sStyle renders no colour", name)
		}
	}
}
