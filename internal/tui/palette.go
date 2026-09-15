package tui

import (
	"math"

	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
)

// Colours are chosen for the terminal loghorn is drawn on, not fixed. Each style
// has a role — a base colour tuned for a dark terminal and a contrast target — and
// SetBackground resolves every role against the terminal's actual background: keep
// the base if it already reads, otherwise walk its lightness away from the
// background, keeping its hue, until it does. See
// docs/superpowers/specs/2026-09-15-adaptive-palette-design.md.

const (
	// textContrast is WCAG AA for normal text.
	textContrast = 4.5
	// ruleContrast is WCAG's threshold for non-text: the detail pane's divider is a
	// line to see, not something to read.
	ruleContrast = 3.0
	// lightLuminance is where black and white text contrast equally with a
	// background, (L+0.05)/0.05 = 1.05/(L+0.05). Above it, text should be darker.
	lightLuminance = 0.179
	// selectionShift is how far the selected row's background moves from the
	// terminal's, in CIE LCh lightness: enough to see, close enough that the
	// terminal's own foreground text still reads on it.
	selectionShift = 0.08
	// lightnessStep is the stride of the search for a readable shade; 50 strides
	// cover the whole lightness range.
	lightnessStep = 0.02
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

// selectionFor is the selected row's background: bg nudged toward the text
// direction, so it stays recognisably the terminal's own colour.
func selectionFor(bg colorful.Color) colorful.Color {
	h, c, l := bg.Hcl()
	return colorful.Hcl(h, c, clamp01(l+towardText(bg)*selectionShift)).Clamped()
}

// pick resolves one role. Text is drawn both on the background and on the
// selected row — the status bar lives there — so a colour must read against both.
func pick(base colorful.Color, target float64, bg, sel colorful.Color) colorful.Color {
	worst := func(c colorful.Color) float64 { return math.Min(contrast(c, bg), contrast(c, sel)) }
	if worst(base) >= target {
		return base
	}
	h, c, l := base.Hcl()
	dir := towardText(bg)
	for i := 1; i <= 50; i++ {
		cand := colorful.Hcl(h, c, clamp01(l+dir*lightnessStep*float64(i))).Clamped()
		if worst(cand) >= target {
			return cand
		}
	}
	// No shade of this hue reads: a mid-grey background leaves no room in either
	// direction. Fall back to whichever of black and white does better.
	if worst(black) >= worst(white) {
		return black
	}
	return white
}

func clamp01(x float64) float64 { return math.Max(0, math.Min(1, x)) }

// The styles every view draws with. They are assigned only by SetBackground;
// package init resolves them for a black terminal, so code that never calls it —
// tests, and anything drawn before main does — sees today's colours.
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
// rendering.
func SetBackground(bg colorful.Color) {
	sel := selectionFor(bg)
	fg := func(base colorful.Color, target float64) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(pick(base, target, bg, sel).Hex()))
	}
	accent := fg(accentBase, textContrast)
	attention := fg(attentionBase, textContrast)
	dim := fg(dimBase, textContrast)

	statusStyle, helpHeadStyle, keyStyle = accent, accent.Bold(true), accent
	moreStyle, helpKeyStyle, numStyle = attention.Bold(true), attention, attention
	dimStyle, helpNoteStyle, nullStyle, tsStyle = dim, dim, dim, dim
	impStyle = fg(importantBase, textContrast).Bold(true)
	dividerStyle = fg(ruleBase, ruleContrast)
	strStyle = fg(stringBase, textContrast)
	boolStyle = fg(booleanBase, textContrast)
	sqlKeywordStyle = fg(sqlKeywordBase, textContrast)
	sqlTypeStyle = fg(sqlTypeBase, textContrast)
	selStyle = lipgloss.NewStyle().Background(lipgloss.Color(sel.Hex()))
}
