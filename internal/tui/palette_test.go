package tui

import (
	"strconv"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
	"github.com/muesli/termenv"
)

// useProfile renders the rest of the test as a terminal with colour profile p
// would. Styles are resolved for the profile, so the cleanup restores the previous
// profile before resolving black again, leaving the package as init left it.
func useProfile(t *testing.T, p termenv.Profile) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(p)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(prev)
		SetBackground(black)
	})
}

// Every colour in the detail pane's palette actually colours. lipgloss.Color
// takes an ANSI number or a hex value; anything else, such as a colour name,
// renders silently as plain text — which is how string values went uncoloured.
func TestPaletteColours(t *testing.T) {
	useProfile(t, termenv.ANSI256)
	SetBackground(black)

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

// textStyles is every style that draws text, by name. The style variables are
// reassigned by SetBackground, so call it after that.
func textStyles() map[string]lipgloss.Style {
	return map[string]lipgloss.Style{
		"dim": dimStyle, "imp": impStyle, "status": statusStyle, "more": moreStyle, "ts": tsStyle,
		"helpHead": helpHeadStyle, "helpKey": helpKeyStyle, "helpNote": helpNoteStyle,
		"key": keyStyle, "str": strStyle, "num": numStyle, "bool": boolStyle, "null": nullStyle,
		"sqlKeyword": sqlKeywordStyle, "sqlType": sqlTypeStyle,
	}
}

// colourOf reads a lipgloss colour as the colour a terminal shows: a 256-colour
// index is looked up in the xterm table, anything else is parsed as hex.
func colourOf(t *testing.T, name string, tc lipgloss.TerminalColor) colorful.Color {
	t.Helper()
	c, ok := tc.(lipgloss.Color)
	if !ok {
		t.Fatalf("%s is not a lipgloss.Color: %#v", name, tc)
	}
	if i, err := strconv.Atoi(string(c)); err == nil {
		return termenv.ConvertToRGB(termenv.ANSI256Color(i))
	}
	return mustHex(t, string(c))
}

// fgOf reads a style's foreground as a colour. Every palette style sets one.
func fgOf(t *testing.T, name string, s lipgloss.Style) colorful.Color {
	t.Helper()
	return colourOf(t, name, s.GetForeground())
}

// indexOf reads a colour that must be a 256-colour index loghorn chose itself:
// one of 16–255, never 0–15, whose shades the terminal's theme decides.
func indexOf(t *testing.T, name string, tc lipgloss.TerminalColor) int {
	t.Helper()
	c, ok := tc.(lipgloss.Color)
	if !ok {
		t.Fatalf("%s is not a lipgloss.Color: %#v", name, tc)
	}
	i, err := strconv.Atoi(string(c))
	if err != nil || i < 16 || i > 255 {
		t.Fatalf("%s = %q, want a 256-colour index from 16 to 255", name, c)
	}
	return i
}

// Every style reads on the background it was resolved for, and on the selected row.
func TestSetBackgroundMakesEveryStyleReadable(t *testing.T) {
	useProfile(t, termenv.TrueColor)
	for _, bgHex := range []string{"#cee8be", "#ffffff", "#000000"} {
		bg := mustHex(t, bgHex)
		SetBackground(bg)
		sel := selectionFor(bg)

		if got, want := selStyle.GetBackground(), lipgloss.Color(sel.Hex()); got != want {
			t.Errorf("on %s selStyle background = %v, want %v", bgHex, got, want)
		}
		for name, s := range textStyles() {
			if c := worstContrast(fgOf(t, name, s), bg, sel); c < textContrast {
				t.Errorf("on %s %sStyle reaches only %.2f", bgHex, name, c)
			}
		}
		if c := worstContrast(fgOf(t, "divider", dividerStyle), bg, sel); c < ruleContrast {
			t.Errorf("on %s dividerStyle reaches only %.2f", bgHex, c)
		}
	}
}

// On a 256-colour terminal the colours the terminal actually shows — indices, not
// the hex values they approximate — read on the background and on the selected
// row, and the selected row stays visible against the background the terminal
// really draws. #d7ffaf is itself an index, 193; the first nudge already lands
// on a different one, 150.
func TestSetBackgroundReadableOnANSI256(t *testing.T) {
	useProfile(t, termenv.ANSI256)
	for _, bgHex := range []string{"#000000", "#1e1e1e", "#ffffff", "#cee8be", "#fdf6e3", "#d7ffaf"} {
		bg := mustHex(t, bgHex)
		SetBackground(bg)
		selIdx := indexOf(t, "selStyle background", selStyle.GetBackground())
		sel := termenv.ConvertToRGB(termenv.ANSI256Color(selIdx))
		if darker := luminance(sel) < luminance(bg); darker != isLight(bg) {
			t.Errorf("on %s the selection %d (%s) moved away from the text direction", bgHex, selIdx, sel.Hex())
		}
		if c := contrast(sel, bg); c < selectionVisible {
			t.Errorf("on %s the selection %d (%s) shows at only %.2f:1 against the background, want %.1f",
				bgHex, selIdx, sel.Hex(), c, selectionVisible)
		}

		for name, s := range textStyles() {
			i := indexOf(t, name+"Style foreground", s.GetForeground())
			fg := termenv.ConvertToRGB(termenv.ANSI256Color(i))
			if c := worstContrast(fg, bg, sel); c < textContrast {
				t.Errorf("on %s %sStyle = %d (%s) reaches only %.2f against the background and selection %d",
					bgHex, name, i, fg.Hex(), c, selIdx)
			}
		}
		i := indexOf(t, "dividerStyle foreground", dividerStyle.GetForeground())
		fg := termenv.ConvertToRGB(termenv.ANSI256Color(i))
		if c := worstContrast(fg, bg, sel); c < ruleContrast {
			t.Errorf("on %s dividerStyle = %d (%s) reaches only %.2f", bgHex, i, fg.Hex(), c)
		}
	}
}

// Moving a colour's lightness must not move its hue. Truecolor only: a 256-colour
// index can't hold a hue to a degree.
func TestSetBackgroundKeepsHue(t *testing.T) {
	useProfile(t, termenv.TrueColor)
	for _, bgHex := range []string{"#cee8be", "#ffffff"} {
		SetBackground(mustHex(t, bgHex))
		for name, tc := range map[string]struct {
			s    lipgloss.Style
			base colorful.Color
		}{
			"status": {statusStyle, accentBase}, "helpKey": {helpKeyStyle, attentionBase},
			"imp": {impStyle, importantBase}, "str": {strStyle, stringBase}, "bool": {boolStyle, booleanBase},
			"sqlKeyword": {sqlKeywordStyle, sqlKeywordBase}, "sqlType": {sqlTypeStyle, sqlTypeBase},
		} {
			got := fgOf(t, name, tc.s)
			gotH, gotC, _ := got.Hcl()
			if gotC < 0.05 {
				// Nearly grey: hue is undefined, and rounding to 8 bits a channel
				// swings it freely.
				continue
			}
			baseH, _, _ := tc.base.Hcl()
			// sqlType on #ffffff passes at 0.98°, close to this 1° limit: the margin
			// is 8-bit RGB rounding, which is deterministic, not test flakiness. If a
			// future change to a base colour or lightnessStep trips this, measure the
			// hue before rounding to confirm it's still the same rounding effect
			// rather than loosening the limit.
			if diff := hueDiff(baseH, gotH); diff > 1 {
				t.Errorf("on %s %sStyle = %s; hue %.1f is %.1f° from the base's %.1f, want within 1°",
					bgHex, name, got.Hex(), gotH, diff, baseH)
			}
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

// Package init resolves the palette for a black terminal, and on black every text
// style keeps its role's base colour. Checking every style against its own role's
// base is the guard against a style wired to the wrong role. The divider is the
// exception: its base doesn't reach 3:1 against black's selection shade, so it is
// lightened. Other tests' cleanups resolve black again, so the state seen here is
// init's whichever order tests run in.
func TestDefaultPaletteIsTodaysColours(t *testing.T) {
	for name, tc := range map[string]struct {
		s    lipgloss.Style
		want string
	}{
		"status": {statusStyle, "#00afff"}, "helpHead": {helpHeadStyle, "#00afff"}, "key": {keyStyle, "#00afff"},
		"helpKey": {helpKeyStyle, "#ffaf00"}, "more": {moreStyle, "#ffaf00"}, "num": {numStyle, "#ffaf00"},
		"imp": {impStyle, "#ff5f5f"},
		"dim": {dimStyle, "#8a8a8a"}, "helpNote": {helpNoteStyle, "#8a8a8a"}, "null": {nullStyle, "#8a8a8a"}, "ts": {tsStyle, "#8a8a8a"},
		"str": {strStyle, "#5faf5f"}, "bool": {boolStyle, "#ff87d7"},
		"sqlKeyword": {sqlKeywordStyle, "#af87ff"}, "sqlType": {sqlTypeStyle, "#5fd7ff"},
	} {
		// Every base is an exact 256-colour index, so whether init resolved for
		// truecolor or 256 colours the terminal shows the base itself.
		if got := fgOf(t, name, tc.s).Hex(); got != tc.want {
			t.Errorf("%sStyle at init = %s, want %s", name, got, tc.want)
		}
	}
	if len(textStyles()) != 15 {
		t.Errorf("textStyles has %d styles; add the new one to this test with its role's base", len(textStyles()))
	}

	divider := fgOf(t, "divider", dividerStyle)
	sel := colourOf(t, "selection", selStyle.GetBackground())
	if divider.Hex() == ruleBase.Hex() {
		t.Errorf("dividerStyle at init = %s, want it lightened from the base", divider.Hex())
	}
	if c := worstContrast(divider, black, sel); c < ruleContrast {
		t.Errorf("dividerStyle at init = %s reaches only %.2f, want %.1f", divider.Hex(), c, ruleContrast)
	}
}

// The selection is nudged no further than it must be. On #cee8be index 151 already
// shows at 1.21:1, level with the truecolor selection; pushing on to 108 darkened
// the row enough to cost four roles their colour. #000055 exercises the other
// branch, where the first nudge isn't enough: its cube has few dark blues, so
// showSelection keeps stepping until index 54, 1.62:1, seven steps past the first.
func TestSelectionStopsWhenVisible(t *testing.T) {
	for bgHex, want := range map[string]lipgloss.Color{"#cee8be": "151", "#000000": "234", "#ffffff": "254", "#000055": "54"} {
		if _, got := showSelection(mustHex(t, bgHex), ansi256); got != want {
			t.Errorf("on %s the 256-colour selection = %s, want %s", bgHex, got, want)
		}
	}
}
