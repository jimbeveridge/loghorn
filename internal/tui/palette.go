package tui

import (
	"math"

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
