package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
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

// fgOf reads a style's foreground as a colour. Every palette style sets one.
func fgOf(t *testing.T, name string, s lipgloss.Style) colorful.Color {
	t.Helper()
	c, ok := s.GetForeground().(lipgloss.Color)
	if !ok {
		t.Fatalf("%s has no hex foreground: %#v", name, s.GetForeground())
	}
	return mustHex(t, string(c))
}

// Every style reads on the background it was resolved for, and on the selected row.
func TestSetBackgroundMakesEveryStyleReadable(t *testing.T) {
	t.Cleanup(func() { SetBackground(black) })
	for _, bgHex := range []string{"#cee8be", "#ffffff", "#000000"} {
		bg := mustHex(t, bgHex)
		SetBackground(bg)
		sel := selectionFor(bg)

		if got, want := selStyle.GetBackground(), lipgloss.Color(sel.Hex()); got != want {
			t.Errorf("on %s selStyle background = %v, want %v", bgHex, got, want)
		}
		text := map[string]lipgloss.Style{
			"dim": dimStyle, "imp": impStyle, "status": statusStyle, "more": moreStyle, "ts": tsStyle,
			"helpHead": helpHeadStyle, "helpKey": helpKeyStyle, "helpNote": helpNoteStyle,
			"key": keyStyle, "str": strStyle, "num": numStyle, "bool": boolStyle, "null": nullStyle,
			"sqlKeyword": sqlKeywordStyle, "sqlType": sqlTypeStyle,
		}
		for name, s := range text {
			if c := worstContrast(fgOf(t, name, s), bg, sel); c < textContrast {
				t.Errorf("on %s %sStyle reaches only %.2f", bgHex, name, c)
			}
		}
		if c := worstContrast(fgOf(t, "divider", dividerStyle), bg, sel); c < ruleContrast {
			t.Errorf("on %s dividerStyle reaches only %.2f", bgHex, c)
		}
	}
}

// Resolving colours must not change which styles are bold.
func TestSetBackgroundKeepsBold(t *testing.T) {
	t.Cleanup(func() { SetBackground(black) })
	SetBackground(mustHex(t, "#cee8be"))
	for name, s := range map[string]lipgloss.Style{"imp": impStyle, "more": moreStyle, "helpHead": helpHeadStyle} {
		if !s.GetBold() {
			t.Errorf("%sStyle lost its bold", name)
		}
	}
	for name, s := range map[string]lipgloss.Style{"helpKey": helpKeyStyle, "num": numStyle, "status": statusStyle, "key": keyStyle, "dim": dimStyle} {
		if s.GetBold() {
			t.Errorf("%sStyle became bold", name)
		}
	}
}

// Without a call to SetBackground (package init), and on black, the colours are
// today's dark-terminal choices.
func TestDefaultPaletteIsTodaysColours(t *testing.T) {
	t.Cleanup(func() { SetBackground(black) })
	SetBackground(black)
	for name, tc := range map[string]struct {
		s    lipgloss.Style
		want string
	}{
		"status": {statusStyle, "#00afff"}, "helpKey": {helpKeyStyle, "#ffaf00"}, "imp": {impStyle, "#ff5f5f"},
		"dim": {dimStyle, "#8a8a8a"}, "str": {strStyle, "#5faf5f"}, "bool": {boolStyle, "#ff87d7"},
		"sqlKeyword": {sqlKeywordStyle, "#af87ff"}, "sqlType": {sqlTypeStyle, "#5fd7ff"},
	} {
		if got := fgOf(t, name, tc.s).Hex(); got != tc.want {
			t.Errorf("%sStyle on black = %s, want %s", name, got, tc.want)
		}
	}
}
