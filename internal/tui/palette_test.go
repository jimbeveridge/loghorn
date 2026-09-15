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

// styleTargets is each style's contrast target, keyed the same as textStyles.
// Every style defaults to WCAG's normal-text 4.5:1; the attention role
// (helpKeyStyle, numStyle, moreStyle) targets accentContrast, 3:1, the
// bold/large-text threshold, because those styles are short bold tokens on a
// light background.
func styleTargets() map[string]float64 {
	targets := make(map[string]float64, len(textStyles()))
	for name := range textStyles() {
		targets[name] = textContrast
	}
	targets["helpKey"], targets["num"], targets["more"] = accentContrast, accentContrast, accentContrast
	return targets
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
		targets := styleTargets()
		for name, s := range textStyles() {
			if name == "dim" && isLight(bg) {
				// On a light background dimStyle carries no foreground at all — the
				// terminal's own text colour, not a role colour — so there is no
				// contrast to check here; TestAttentionTargetsThreeToOne covers it.
				if got := s.GetForeground(); got != (lipgloss.NoColor{}) {
					t.Errorf("on %s dimStyle foreground = %#v, want NoColor", bgHex, got)
				}
				continue
			}
			if c := worstContrast(fgOf(t, name, s), bg, sel); c < targets[name] {
				t.Errorf("on %s %sStyle reaches only %.2f, want %.1f", bgHex, name, c, targets[name])
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
// on a different one, 150. #000044 is a known limit (see the spec): its
// saturated dark blue leaves the cube few nearby shades, so the selection lands
// much heavier than usual, but text still reaches its target.
func TestSetBackgroundReadableOnANSI256(t *testing.T) {
	useProfile(t, termenv.ANSI256)
	for _, bgHex := range []string{"#000000", "#1e1e1e", "#ffffff", "#cee8be", "#fdf6e3", "#d7ffaf", "#000044"} {
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

		targets := styleTargets()
		for name, s := range textStyles() {
			if name == "dim" && isLight(bg) {
				if got := s.GetForeground(); got != (lipgloss.NoColor{}) {
					t.Errorf("on %s dimStyle foreground = %#v, want NoColor", bgHex, got)
				}
				continue
			}
			i := indexOf(t, name+"Style foreground", s.GetForeground())
			fg := termenv.ConvertToRGB(termenv.ANSI256Color(i))
			if c := worstContrast(fg, bg, sel); c < targets[name] {
				t.Errorf("on %s %sStyle = %d (%s) reaches only %.2f against the background and selection %d, want %.1f",
					bgHex, name, i, fg.Hex(), c, selIdx, targets[name])
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

// The selected row's background must keep the terminal's own hue, not swing to
// an unrelated one. #003357 and #002f39 are real, clearly chromatic navies that
// happen to sit on go-colorful's own Hcl's zero-hue axes (nearly a=0, or a≈b);
// reading their hue through that accessor, as nudge once did, forced it to 0
// (red) and produced a maroon selection instead of a darker navy.
func TestSelectionKeepsBackgroundHue(t *testing.T) {
	for _, bgHex := range []string{"#003357", "#002f39"} {
		bg := mustHex(t, bgHex)
		bgH, bgC, _ := hcl(bg)
		sel := selectionFor(bg)
		selH, _, _ := hcl(sel)
		if diff := hueDiff(bgH, selH); diff > 3 {
			t.Errorf("selectionFor(%s) hue = %.1f, background hue = %.1f (chroma %.3f); %.1f° apart, want within 3°",
				bgHex, selH, bgH, bgC, diff)
		}
	}
}

// Resolving colours must not change which styles are bold, except that on a
// light background helpKeyStyle and numStyle join the attention role's
// moreStyle in being bold — WCAG's 3:1 attention target is the bold/large-text
// threshold, so those two must actually be bold to claim it (ruling 2).
func TestSetBackgroundKeepsBold(t *testing.T) {
	t.Cleanup(func() { SetBackground(black) })

	SetBackground(mustHex(t, "#cee8be"))
	for name, s := range map[string]lipgloss.Style{
		"imp": impStyle, "more": moreStyle, "helpHead": helpHeadStyle,
		"helpKey": helpKeyStyle, "num": numStyle,
	} {
		if !s.GetBold() {
			t.Errorf("on a light background %sStyle lost its bold", name)
		}
	}
	for name, s := range map[string]lipgloss.Style{"status": statusStyle, "key": keyStyle} {
		if s.GetBold() {
			t.Errorf("on a light background %sStyle became bold", name)
		}
	}

	SetBackground(mustHex(t, "#1e1e1e"))
	for name, s := range map[string]lipgloss.Style{"helpKey": helpKeyStyle, "num": numStyle, "status": statusStyle, "key": keyStyle, "dim": dimStyle} {
		if s.GetBold() {
			t.Errorf("on a dark background %sStyle became bold", name)
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
// branch, where the first nudge isn't enough: the cube has few dark blues, so
// showSelection keeps stepping until index 54, 1.62:1, seven steps past the first.
func TestSelectionStopsWhenVisible(t *testing.T) {
	for bgHex, want := range map[string]lipgloss.Color{"#cee8be": "151", "#000000": "234", "#ffffff": "254", "#000055": "54"} {
		if _, got := showSelection(mustHex(t, bgHex), ansi256); got != want {
			t.Errorf("on %s the 256-colour selection = %s, want %s", bgHex, got, want)
		}
	}
}

// On a light terminal, the attention role (helpKeyStyle, numStyle, moreStyle)
// targets accentContrast rather than textContrast: those are short bold tokens,
// and WCAG's 4.5:1 forced them to #714b00, a near-black brown that erased the
// differentiation the colour was for. At 3:1 they land lighter, still an amber,
// while keeping the base's hue. dimStyle instead loses its foreground entirely
// on a light background, so the list's context rows render in the terminal's
// own (typically black) text rather than the dim role's grey.
func TestAttentionTargetsThreeToOne(t *testing.T) {
	useProfile(t, termenv.TrueColor)
	bg := mustHex(t, "#cee8be")
	SetBackground(bg)
	sel := selectionFor(bg)

	fourFive, _ := pick(attentionBase, textContrast, bg, sel, trueColor)
	baseH, _, _ := attentionBase.Hcl()
	for name, s := range map[string]lipgloss.Style{"helpKey": helpKeyStyle, "num": numStyle, "more": moreStyle} {
		got := fgOf(t, name, s)
		if c := worstContrast(got, bg, sel); c < accentContrast {
			t.Errorf("%sStyle on #cee8be reaches only %.2f, want %.1f", name, c, accentContrast)
		}
		if luminance(got) <= luminance(fourFive) {
			t.Errorf("%sStyle on #cee8be = %s, want lighter than the old 4.5-target result %s", name, got.Hex(), fourFive.Hex())
		}
		gotH, _, _ := got.Hcl()
		if diff := hueDiff(baseH, gotH); diff > 1 {
			t.Errorf("%sStyle on #cee8be hue %.1f is %.1f° from attentionBase's %.1f, want within 1°", name, gotH, diff, baseH)
		}
		if !s.GetBold() {
			t.Errorf("%sStyle on #cee8be should be bold", name)
		}
	}

	if got := dimStyle.GetForeground(); got != (lipgloss.NoColor{}) {
		t.Errorf("dimStyle on #cee8be foreground = %#v, want NoColor", got)
	}
	for name, s := range map[string]lipgloss.Style{"ts": tsStyle, "helpNote": helpNoteStyle, "null": nullStyle} {
		got := fgOf(t, name, s)
		if c := worstContrast(got, bg, sel); c < textContrast {
			t.Errorf("%sStyle on #cee8be reaches only %.2f, want %.1f", name, c, textContrast)
		}
	}
}

// On dark backgrounds nothing changes, including ones far from black:
// accentContrast (3:1) only ever applies when isLight(bg), so attention
// resolves at textContrast (4.5:1) exactly as it did at 62c4d1e, helpKeyStyle
// and numStyle stay non-bold (moreStyle stays bold, as it always was), and
// dimStyle keeps the dim role's foreground rather than losing it (ruling 3 is
// light-background only). The three non-black backgrounds below are where a
// prior fix-round conflated "3:1 target" with "isLight" and applied 3:1 even
// here, leaving non-bold text under 4.5:1 (e.g. #ffaf00 on #3b4252 measured
// 4.05, not the 4.54 that #ffbe5b — what 62c4d1e actually resolved to —
// reaches). The expected hexes are pinned to values measured directly against
// 62c4d1e, not recomputed here, so this test can't drift with the production
// code it's guarding.
func TestAttentionUnchangedOnDark(t *testing.T) {
	useProfile(t, termenv.TrueColor)
	want := map[string]string{
		"#000000": "#ffaf00", "#1e1e1e": "#ffaf00",
		"#3b4252": "#ffbe5b", // Nord's Polar Night background
		"#44475a": "#ffcd88", // Dracula's background
		"#6f6f6f": "#000000", // dark by the 0.179 luminance threshold, though pale grey to the eye
	}
	for bgHex, wantHex := range want {
		bg := mustHex(t, bgHex)
		SetBackground(bg)

		for name, s := range map[string]lipgloss.Style{"helpKey": helpKeyStyle, "num": numStyle, "more": moreStyle} {
			if got := fgOf(t, name, s); got.Hex() != wantHex {
				t.Errorf("on %s %sStyle = %s, want %s (62c4d1e's colour, unchanged)", bgHex, name, got.Hex(), wantHex)
			}
		}
		if helpKeyStyle.GetBold() {
			t.Errorf("on %s helpKeyStyle should not be bold", bgHex)
		}
		if numStyle.GetBold() {
			t.Errorf("on %s numStyle should not be bold", bgHex)
		}
		if !moreStyle.GetBold() {
			t.Errorf("on %s moreStyle should stay bold", bgHex)
		}
		if got, want := fgOf(t, "dim", dimStyle), fgOf(t, "helpNote", helpNoteStyle); got.Hex() != want.Hex() {
			t.Errorf("on %s dimStyle = %s, want the dim role's colour %s", bgHex, got.Hex(), want.Hex())
		}
	}
}

// On a 256-colour terminal the attention role's 3:1 target resolves to index
// 94 (#875f00) on #cee8be, an amber the cube can show, rather than 236, the
// near-black grey the old 4.5:1 target picked.
func TestAttentionANSI256ReachesThreeToOne(t *testing.T) {
	useProfile(t, termenv.ANSI256)
	bg := mustHex(t, "#cee8be")
	SetBackground(bg)
	selIdx := indexOf(t, "selStyle background", selStyle.GetBackground())
	sel := termenv.ConvertToRGB(termenv.ANSI256Color(selIdx))

	for name, s := range map[string]lipgloss.Style{"helpKey": helpKeyStyle, "num": numStyle, "more": moreStyle} {
		idx := indexOf(t, name+"Style foreground", s.GetForeground())
		if idx != 94 {
			t.Errorf("%sStyle on #cee8be (ANSI256) = index %d, want 94", name, idx)
		}
		fg := termenv.ConvertToRGB(termenv.ANSI256Color(idx))
		if c := worstContrast(fg, bg, sel); c < accentContrast {
			t.Errorf("%sStyle on #cee8be (ANSI256) reaches only %.2f, want %.1f", name, c, accentContrast)
		}
	}
}
