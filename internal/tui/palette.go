package tui

import (
	"math"
	"strconv"

	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
	"github.com/muesli/termenv"
)

// Colours are chosen for the terminal loghorn is drawn on, not fixed. Each style
// has a role — a base colour tuned for a dark terminal and a contrast target — and
// SetBackground resolves every role against the terminal's actual background: keep
// the base if it already reads, otherwise walk its lightness away from the
// background, keeping its hue, until it does. Readability is judged on the colour
// the terminal will really show, which on a 256-colour terminal is the nearest
// palette index, not the colour asked for. See
// docs/superpowers/specs/2026-09-15-adaptive-palette-design.md.

const (
	// textContrast is WCAG AA for normal text.
	textContrast = 4.5
	// ruleContrast is WCAG's threshold for non-text: the detail pane's divider is a
	// line to see, not something to read.
	ruleContrast = 3.0
	// accentContrast is WCAG's large-text threshold (18pt, or 14pt bold), not the
	// normal-text 4.5:1. The attention role's styles (help keys, JSON numbers,
	// bar notes) are short tokens that don't literally qualify as large text at
	// terminal size, so this is a deliberate trade, not a strict reading of
	// WCAG: SetBackground applies it, always paired with bold, only on light
	// backgrounds, where holding attention to 4.5:1 forced a near-black brown
	// with almost no differentiation from the surrounding text — the user chose
	// a bold amber at 3:1 over that. Dark backgrounds are unaffected by this
	// exception and keep the normal-text 4.5:1 guarantee, unbolded.
	accentContrast = 3.0
	// lightLuminance is where black and white text contrast equally with a
	// background, (L+0.05)/0.05 = 1.05/(L+0.05). Above it, text should be darker.
	lightLuminance = 0.179
	// selectionShift is how far the selected row's background moves from the
	// terminal's, in CIE LCh lightness: enough to see, close enough that the
	// terminal's own foreground text still reads on it.
	selectionShift = 0.08
	// lightnessStep is the stride of the search for a readable shade.
	lightnessStep = 0.02
	// lightnessSteps is how many strides of lightnessStep it takes to cross the
	// whole lightness range, 0 to 1. Derived from lightnessStep, as an untyped
	// constant, so it can't drift out of sync if that stride ever changes.
	lightnessSteps = 1 / lightnessStep
	// selectionVisible is the least contrast the selected row's background keeps
	// with the terminal's. Truecolor selections measure about 1.17–1.35:1; 1.1
	// leaves room for a 256-colour index landing a little nearer the background,
	// while still guaranteeing the row can be seen.
	selectionVisible = 1.1
)

var (
	black = colorful.Color{R: 0, G: 0, B: 0}
	white = colorful.Color{R: 1, G: 1, B: 1}
)

// luminance is WCAG 2.x relative luminance.
func luminance(c colorful.Color) float64 {
	r, g, b := c.LinearRgb()
	return 0.2126*r + 0.7152*g + 0.0722*b
}

// contrast is the WCAG 2.x contrast ratio, from 1 (none) to 21 (black on white).
func contrast(a, b colorful.Color) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// isLight reports whether text on bg should be darker than bg.
func isLight(bg colorful.Color) bool { return luminance(bg) > lightLuminance }

// towardText is the direction lightness moves for anything drawn on bg: down on
// a light background, up on a dark one.
func towardText(bg colorful.Color) float64 {
	if isLight(bg) {
		return -1
	}
	return 1
}

// hcl is c's hue, chroma and lightness, read straight from CIE Lab rather than
// through go-colorful's own Color.Hcl (LabToHcl): that helper sets hue to 0
// whenever |a| <= 1e-4 or a and b are nearly equal — the two axes of the a-b
// plane — regardless of chroma, so a clearly chromatic colour sitting on either
// axis (a saturated navy, say) gets reported as hue 0, red. Everywhere this
// package needs a colour's own hue back out, rather than building one from a
// known hue, it must go through here instead.
func hcl(c colorful.Color) (h, chroma, l float64) {
	l, a, b := c.Lab()
	h = math.Mod(math.Atan2(b, a)*180/math.Pi+360, 360)
	chroma = math.Hypot(a, b)
	return h, chroma, l
}

// inGamut is the colour at hue h and lightness l with as much of chroma c as sRGB
// can show. Clipping each RGB channel to range instead, as Clamped does, moves the
// hue — the accent swung almost 20° on a light green terminal — so chroma gives
// way and hue and lightness hold. Twenty halvings pin chroma far finer than 8 bits
// a channel can show.
func inGamut(h, c, l float64) colorful.Color {
	if col := colorful.Hcl(h, c, l); col.IsValid() {
		return col
	}
	lo, hi := 0.0, c
	for range 20 {
		mid := (lo + hi) / 2
		if colorful.Hcl(h, mid, l).IsValid() {
			lo = mid
		} else {
			hi = mid
		}
	}
	// At chroma lo the colour is in gamut, or is a grey that floating-point error
	// has put a hair outside it; Clamped only trims that error.
	return colorful.Hcl(h, lo, l).Clamped()
}

// selectionFor is the selected row's background: bg nudged toward the text
// direction, so it stays recognisably the terminal's own colour.
func selectionFor(bg colorful.Color) colorful.Color { return nudge(bg, selectionShift) }

// nudge is bg with its CIE LCh lightness moved by shift toward the text direction.
func nudge(bg colorful.Color, shift float64) colorful.Color {
	return nudgeDir(bg, shift, towardText(bg))
}

// nudgeDir is bg with its CIE LCh lightness moved by shift in direction dir (+1
// lighter, -1 darker), rather than always toward the text direction. showSelection's
// mirrored search (see SetBackground's selection step) uses this to try the far
// side of the background from the text when the near side leaves no colour able
// to reach textContrast.
func nudgeDir(bg colorful.Color, shift, dir float64) colorful.Color {
	h, c, l := hcl(bg)
	return inGamut(h, c, clamp01(l+dir*shift))
}

// A display is what the terminal does with a colour loghorn asks for: the colour
// that actually reaches the screen, and the name to give lipgloss for it. Colours
// are judged by what is shown, so contrast holds for what the reader sees.
type display func(want colorful.Color) (shown colorful.Color, name lipgloss.Color)

// trueColor shows a colour to 8 bits a channel, named in hex.
func trueColor(want colorful.Color) (colorful.Color, lipgloss.Color) {
	hex := want.Hex()
	return hexColor(hex), lipgloss.Color(hex)
}

// ansi256 shows the nearest xterm index and names it by number. Given hex,
// lipgloss would quantise through termenv, whose grey-ramp candidate is always
// index 232, a near-black: the selected row vanished on a black terminal and
// important lines turned black on a white one. A number is used as it stands.
func ansi256(want colorful.Color) (colorful.Color, lipgloss.Color) {
	i := nearestIndex(want)
	return xterm[i-16], lipgloss.Color(strconv.Itoa(i))
}

// displayFor is the display for a lipgloss colour profile. Only 256-colour
// terminals get their own: a 16-colour terminal maps any colour to one of its
// theme's, whose shades loghorn can't know, and Ascii shows none, so for both hex
// is as good a name as any.
func displayFor(p termenv.Profile) display {
	if p == termenv.ANSI256 {
		return ansi256
	}
	return trueColor
}

// xterm is what indices 16–255 show on xterm and the terminals that copy it: a
// 6×6×6 colour cube, then a 24-step grey ramp. Index i is xterm[i-16].
var xterm = func() (table [240]colorful.Color) {
	levels := [6]float64{0, 95, 135, 175, 215, 255}
	for i := range 216 {
		table[i] = colorful.Color{R: levels[i/36] / 255, G: levels[i/6%6] / 255, B: levels[i%6] / 255}
	}
	for i := range 24 {
		v := float64(8+10*i) / 255
		table[216+i] = colorful.Color{R: v, G: v, B: v}
	}
	return table
}()

// xtermLab is xterm's entries in CIE Lab, computed once alongside the RGB
// table above. nearestIndex runs once per style per call to SetBackground but
// checks every one of these 240 entries each time; converting the fixed table
// to Lab up front means only the candidate colour's own conversion happens per
// call.
var xtermLab = func() (table [240][3]float64) {
	for i, x := range xterm {
		l, a, b := x.Lab()
		table[i] = [3]float64{l, a, b}
	}
	return table
}()

// sq is x squared, for the Lab distances below: colorful.Color.DistanceLab
// doesn't take precomputed Lab values, so its formula is inlined here against
// xtermLab.
func sq(x float64) float64 { return x * x }

// nearestIndex is the xterm index from 16 to 255 that looks most like c, by
// distance in CIE Lab. Indices 0–15 are never chosen: they are the terminal
// theme's own colours, whose shades loghorn doesn't know.
func nearestIndex(c colorful.Color) int {
	l, a, b := c.Lab()
	best, bestDist := 0, math.Inf(1)
	for i, lab := range xtermLab {
		if d := math.Sqrt(sq(l-lab[0]) + sq(a-lab[1]) + sq(b-lab[2])); d < bestDist {
			best, bestDist = i, d
		}
	}
	return 16 + best
}

// showSelection is the selection as d shows it, nudged in direction dir (+1
// lighter, -1 darker — pass towardText(bg) for today's selection, or its
// mirror to search the other side; see SetBackground's selection step). A
// 256-colour index is coarse, and the nudge can land on an index that looks
// almost like the background, which would hide the selected row; so the nudge
// goes on, a lightness step at a time, until what the terminal draws is on
// dir's side of the background and at least selectionVisible from it. The
// background is judged as itself, not as its nearest index: the terminal draws
// it exactly. lightnessSteps steps reach black or white, which contrast with
// any background on the other side of lightLuminance by more than 4:1, so the
// bound is never what ends the loop.
func showSelection(bg colorful.Color, dir float64, d display) (colorful.Color, lipgloss.Color) {
	for i := 0; ; i++ {
		shown, name := d(nudgeDir(bg, selectionShift+lightnessStep*float64(i), dir))
		onSide := (luminance(shown) < luminance(bg)) == (dir < 0)
		if onSide && contrast(shown, bg) >= selectionVisible || i == lightnessSteps {
			return shown, name
		}
	}
}

// pick resolves one role for display d, returning the colour shown and its name.
// Text is drawn both on the background and on the selected row — the status bar
// lives there — so a colour must read against both; sel is the selection as d
// shows it.
func pick(base colorful.Color, target float64, bg, sel colorful.Color, d display) (colorful.Color, lipgloss.Color) {
	worst := func(c colorful.Color) float64 { return math.Min(contrast(c, bg), contrast(c, sel)) }
	if shown, name := d(base); worst(shown) >= target {
		return shown, name
	}
	// base's hue comes from hcl, not base.Hcl(): see hcl's comment for why.
	h, c, l := hcl(base)
	dir := towardText(bg)
	prevL := l
	for i := 1; i <= lightnessSteps; i++ {
		nextL := clamp01(l + dir*lightnessStep*float64(i))
		if shown, name := d(inGamut(h, c, nextL)); worst(shown) >= target {
			return shown, name
		}
		if nextL == prevL {
			// Lightness has saturated at 0 or 1; every further step would just
			// retry the candidate already rejected above.
			break
		}
		prevL = nextL
	}
	// No shade of this hue reads: a mid-grey background leaves no room in either
	// direction. Fall back to whichever of black and white does better.
	blackShown, blackName := d(black)
	whiteShown, whiteName := d(white)
	if worst(blackShown) >= worst(whiteShown) {
		return blackShown, blackName
	}
	return whiteShown, whiteName
}

func clamp01(x float64) float64 { return math.Max(0, math.Min(1, x)) }

// blackOrWhiteReaches reports whether black or white, as d shows it, reaches
// target against both bg and sel. pick's own fallback (above) judges black and
// white the same way; selectionForBackground below uses this to decide whether
// a candidate selection leaves textContrast reachable at all.
func blackOrWhiteReaches(target float64, bg, sel colorful.Color, d display) bool {
	worst := func(c colorful.Color) float64 {
		shown, _ := d(c)
		return math.Min(contrast(shown, bg), contrast(shown, sel))
	}
	return worst(black) >= target || worst(white) >= target
}

// selectionForBackground is the selected row's background SetBackground
// resolves styles against: today's toward-text selection, unless that leaves
// every role's fallback out of reach.
//
// A mid-grey background such as #6f6f6f or #808080 nudged toward the text
// direction moves the selection *closer* to whichever of black or white the
// text would fall back to — a light background's toward-text selection is
// darker, nearer black; a dark one's is lighter, nearer white — which shrinks
// exactly the contrast pick's fallback depends on. On these backgrounds no
// role reaches textContrast against both the background and that selection,
// so every role still targeting it (attention on a light background is the
// one exception, already at accentContrast) falls back to black or white
// below target.
//
// Nudging the *other* way instead moves the selection further from that
// fallback colour, which can restore the reach the toward-text side lost, at
// the cost of a selection that sits further from the terminal's own
// background than usual. So: try today's selection first, and keep it if
// black or white already reaches textContrast against both the background and
// it — the common case, unchanged. Only when that fails is the mirrored
// selection tried, with the same stepping and the same visibility rule run in
// the other direction; it is used only if it actually gets black or white to
// target against both. If neither side does, the toward-text selection is
// kept regardless — flipping only when it helps means a background where
// nothing works looks exactly as it did before this existed.
func selectionForBackground(bg colorful.Color, d display) (colorful.Color, lipgloss.Color) {
	toward := towardText(bg)
	sel, name := showSelection(bg, toward, d)
	if blackOrWhiteReaches(textContrast, bg, sel, d) {
		return sel, name
	}
	if away, awayName := showSelection(bg, -toward, d); blackOrWhiteReaches(textContrast, bg, away, d) {
		return away, awayName
	}
	return sel, name
}

// The styles every view draws with. They are assigned only by SetBackground;
// package init resolves them for a black terminal, so code that never calls it —
// tests, and anything drawn before main does — gets the dark palette: text in the
// colours loghorn has always used, except strings, which are a fixed green rather
// than the theme's ANSI green; a slightly lighter divider; and a subtler selected
// row, derived from the background.
var (
	dimStyle, impStyle, selStyle, statusStyle, moreStyle, tsStyle lipgloss.Style
	helpHeadStyle, helpKeyStyle, helpNoteStyle                    lipgloss.Style
	dividerStyle                                                  lipgloss.Style
	keyStyle, strStyle, numStyle, boolStyle, nullStyle            lipgloss.Style
	sqlKeywordStyle, sqlTypeStyle                                 lipgloss.Style
)

// Role base colours: the 256-colour picks loghorn has always used, as hex, tuned
// for a dark terminal. Styles that shared a colour share a role.
var (
	accentBase     = hexColor("#00afff") // 39: status bar, help headings, keys
	attentionBase  = hexColor("#ffaf00") // 214: bar notes, help keys, numbers
	importantBase  = hexColor("#ff5f5f") // 203
	dimBase        = hexColor("#8a8a8a") // 245, and 244 one step away
	ruleBase       = hexColor("#585858") // 240: the detail pane's divider
	stringBase     = hexColor("#5faf5f") // 71; was ANSI 2, whose shade the terminal theme decides
	booleanBase    = hexColor("#ff87d7") // 212
	sqlKeywordBase = hexColor("#af87ff") // 141
	sqlTypeBase    = hexColor("#5fd7ff") // 81
)

func hexColor(s string) colorful.Color {
	c, err := colorful.Hex(s)
	if err != nil {
		panic(err)
	}
	return c
}

func init() { SetBackground(black) }

// SetBackground recomputes every style for a terminal whose background is bg. The
// styles are plain package variables, so call it before the program starts
// rendering. Colours are chosen for lipgloss's colour profile, which comes from
// the environment, not from asking the terminal. The background itself is never
// quantised: the terminal draws it exactly, whatever its profile.
func SetBackground(bg colorful.Color) {
	d := displayFor(lipgloss.ColorProfile())
	sel, selName := selectionForBackground(bg, d)
	fg := func(base colorful.Color, target float64) lipgloss.Style {
		_, name := pick(base, target, bg, sel, d)
		return lipgloss.NewStyle().Foreground(name)
	}
	accent := fg(accentBase, textContrast)
	dim := fg(dimBase, textContrast)

	// attention targets textContrast, WCAG's normal-text 4.5:1, except on a
	// light background, where it drops to accentContrast, 3:1, always paired
	// with bold (see accentContrast's comment) — the one case the user
	// actually asked to change. Dark backgrounds, including ones far from
	// black such as Nord's #3b4252, are unaffected.
	attention := fg(attentionBase, textContrast)
	if isLight(bg) {
		attention = fg(attentionBase, accentContrast).Bold(true)
	}

	statusStyle, helpHeadStyle, keyStyle = accent, accent.Bold(true), accent
	moreStyle = attention.Bold(true)
	helpKeyStyle, numStyle = attention, attention
	helpNoteStyle, nullStyle, tsStyle = dim, dim, dim
	if isLight(bg) {
		// The list's context rows lose dimStyle's foreground altogether on a
		// light background: the user found the dim grey washed out next to a
		// true black and wanted the terminal's own (typically black) text
		// instead, which is what an unstyled render falls back to.
		dimStyle = lipgloss.NewStyle()
	} else {
		dimStyle = dim
	}
	impStyle = fg(importantBase, textContrast).Bold(true)
	dividerStyle = fg(ruleBase, ruleContrast)
	strStyle = fg(stringBase, textContrast)
	boolStyle = fg(booleanBase, textContrast)
	sqlKeywordStyle = fg(sqlKeywordBase, textContrast)
	sqlTypeStyle = fg(sqlTypeBase, textContrast)
	selStyle = lipgloss.NewStyle().Background(selName)
}

// selBg is s with the selected row's background added, for rendering a segment
// that must keep its own foreground while joining the row's highlight. lipgloss
// always closes a Render with a full SGR reset, so nesting an already-rendered
// segment inside selStyle.Render — as the list row and status bar once did —
// cancels the background at that reset, and everything after the first segment
// goes unhighlighted. Giving each segment the background itself, and rendering
// it in one call, avoids the embedded reset altogether.
func selBg(s lipgloss.Style) lipgloss.Style { return s.Background(selStyle.GetBackground()) }
