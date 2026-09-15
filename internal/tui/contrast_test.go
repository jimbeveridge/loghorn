package tui

import (
	"math"
	"testing"

	"github.com/lucasb-eyer/go-colorful"
	"github.com/muesli/termenv"
)

func mustHex(t *testing.T, s string) colorful.Color {
	t.Helper()
	c, err := colorful.Hex(s)
	if err != nil {
		t.Fatalf("bad test colour %q: %v", s, err)
	}
	return c
}

// worstContrast is how a picked colour is judged: it must read on the background
// and on the selected row alike.
func worstContrast(c, bg, sel colorful.Color) float64 {
	return math.Min(contrast(c, bg), contrast(c, sel))
}

func TestContrastKnownPairs(t *testing.T) {
	if got := contrast(black, white); math.Abs(got-21) > 1e-9 {
		t.Errorf("black on white = %v, want 21", got)
	}
	if got := contrast(white, black); math.Abs(got-21) > 1e-9 {
		t.Errorf("contrast must be symmetric, white on black = %v", got)
	}
	green := mustHex(t, "#cee8be")
	if got := contrast(green, green); math.Abs(got-1) > 1e-9 {
		t.Errorf("a colour on itself = %v, want 1", got)
	}
	// The measured problem that motivated the palette: help keys on the user's
	// light green terminal.
	if got := contrast(mustHex(t, "#ffaf00"), green); math.Abs(got-1.398) > 0.01 {
		t.Errorf("#ffaf00 on #cee8be = %v, want about 1.40", got)
	}
}

func TestIsLight(t *testing.T) {
	for hex, want := range map[string]bool{
		"#cee8be": true, "#ffffff": true, "#fdf6e3": true,
		"#000000": false, "#1e1e1e": false,
	} {
		if got := isLight(mustHex(t, hex)); got != want {
			t.Errorf("isLight(%s) = %v, want %v", hex, got, want)
		}
	}
}

// The selected row moves toward the text direction: darker on a light terminal,
// lighter on a dark one.
func TestSelectionDirection(t *testing.T) {
	for hex, darker := range map[string]bool{"#cee8be": true, "#ffffff": true, "#000000": false, "#1e1e1e": false} {
		bg := mustHex(t, hex)
		sel := selectionFor(bg)
		if got := luminance(sel) < luminance(bg); got != darker {
			t.Errorf("selectionFor(%s) = %s; darker = %v, want %v", hex, sel.Hex(), got, darker)
		}
	}
}

// On a black terminal the base colours already read, so they come back exactly:
// a dark terminal keeps today's look.
func TestPickKeepsReadableBase(t *testing.T) {
	bg := black
	sel := selectionFor(bg)
	for _, hex := range []string{"#00afff", "#ffaf00", "#ff5f5f", "#8a8a8a", "#5faf5f", "#ff87d7", "#af87ff", "#5fd7ff"} {
		if got, _ := pick(mustHex(t, hex), textContrast, bg, sel, trueColor); got.Hex() != hex {
			t.Errorf("pick(%s) on black = %s, want it unchanged", hex, got.Hex())
		}
	}
}

// Every base colour reaches its target on real terminal backgrounds, against both
// the background and the selection shade.
func TestPickReachesTarget(t *testing.T) {
	type role struct {
		base   string
		target float64
	}
	roles := []role{
		{"#00afff", textContrast}, {"#ffaf00", textContrast}, {"#ff5f5f", textContrast},
		{"#8a8a8a", textContrast}, {"#585858", ruleContrast}, {"#5faf5f", textContrast},
		{"#ff87d7", textContrast}, {"#af87ff", textContrast}, {"#5fd7ff", textContrast},
	}
	for _, bgHex := range []string{"#cee8be", "#ffffff", "#fdf6e3", "#1e1e1e", "#000000"} {
		bg := mustHex(t, bgHex)
		sel := selectionFor(bg)
		for _, r := range roles {
			got, _ := pick(mustHex(t, r.base), r.target, bg, sel, trueColor)
			if c := worstContrast(got, bg, sel); c < r.target {
				t.Errorf("on %s, pick(%s) = %s reaches only %.2f, want %.1f", bgHex, r.base, got.Hex(), c, r.target)
			}
		}
	}
}

// hueDiff computes the angular distance between two hues in the range [0, 180].
// This handles wrap-around at 360 degrees.
func hueDiff(h1, h2 float64) float64 {
	diff := math.Abs(h1 - h2)
	if diff > 180 {
		diff = 360 - diff
	}
	return diff
}

// On a light terminal an unreadable colour moves darker, not lighter, preserving hue.
func TestPickDarkensOnLightBackground(t *testing.T) {
	bg := mustHex(t, "#cee8be")
	base := mustHex(t, "#ffaf00")
	sel := selectionFor(bg)
	got, _ := pick(base, textContrast, bg, sel, trueColor)
	if luminance(got) >= luminance(base) {
		t.Errorf("pick(#ffaf00) on #cee8be = %s, want darker than the base", got.Hex())
	}
	// Must not fall back to black or white; must search through hue space.
	if got.Hex() == black.Hex() || got.Hex() == white.Hex() {
		t.Errorf("pick(#ffaf00) on #cee8be = %s, want a shade of the base hue, not black/white", got.Hex())
	}
	// Hue must stay within a degree of the base. Chroma is reduced to stay inside
	// the sRGB gamut; clipping the channels instead would swing the hue.
	baseH, _, _ := base.Hcl()
	gotH, _, _ := got.Hcl()
	if diff := hueDiff(baseH, gotH); diff > 1 {
		t.Errorf("pick(#ffaf00) on #cee8be = %s; hue %g is %.1f degrees from base %g, want within 1°",
			got.Hex(), gotH, diff, baseH)
	}
}

// On a dark terminal an unreadable colour moves lighter, not darker, preserving hue.
func TestPickLightensOnDarkBackground(t *testing.T) {
	bg := mustHex(t, "#1e1e1e")
	base := mustHex(t, "#585858")
	sel := selectionFor(bg)
	got, _ := pick(base, ruleContrast, bg, sel, trueColor)
	if luminance(got) <= luminance(base) {
		t.Errorf("pick(#585858) on #1e1e1e = %s, want lighter than the base", got.Hex())
	}
	// Must not fall back to white; must search through hue space.
	if got.Hex() == white.Hex() {
		t.Errorf("pick(#585858) on #1e1e1e = %s, want a shade of the base hue, not white", got.Hex())
	}
}

// Mid grey has no shade of any hue that reaches 4.5:1 against both it and its
// selection; the fallback is whichever of black and white does better.
func TestPickMidGreyFallsBack(t *testing.T) {
	bg := mustHex(t, "#808080")
	sel := selectionFor(bg)
	want := black
	if worstContrast(white, bg, sel) > worstContrast(black, bg, sel) {
		want = white
	}
	for _, hex := range []string{"#00afff", "#ffaf00", "#ff5f5f", "#8a8a8a", "#5faf5f", "#ff87d7", "#af87ff", "#5fd7ff"} {
		if got, _ := pick(mustHex(t, hex), textContrast, bg, sel, trueColor); got.Hex() != want.Hex() {
			t.Errorf("pick(%s) on #808080 = %s, want fallback %s", hex, got.Hex(), want.Hex())
		}
	}
}

// hcl must return the true hue on go-colorful's own zero-hue axes: LabToHcl
// (behind Color.Hcl) sets hue to 0 whenever |a| <= 1e-4 or a and b are nearly
// equal, regardless of chroma. Both cases below are built with real chroma
// (0.2-0.3) squarely on one of those axes, so go-colorful's own accessor would
// report hue 0 for each; hcl must not.
func TestHclAxisHue(t *testing.T) {
	for _, tc := range []struct{ h, c, l float64 }{
		{270, 0.3, 0.3},  // a ~= 0: the |a| <= 1e-4 axis
		{225, 0.2, 0.25}, // a ~= b: the a≈b axis
	} {
		built := colorful.Hcl(tc.h, tc.c, tc.l)
		if quirkH, _, _ := built.Hcl(); quirkH != 0 {
			t.Fatalf("test setup: colorful.Hcl(%v,%v,%v).Hcl() = %v, want the known 0 quirk to reproduce here", tc.h, tc.c, tc.l, quirkH)
		}
		gotH, _, _ := hcl(built)
		if diff := hueDiff(tc.h, gotH); diff > 1 {
			t.Errorf("hcl(colorful.Hcl(%v, %v, %v)) hue = %.2f, want within 1° of %v", tc.h, tc.c, tc.l, gotH, tc.h)
		}
	}
}

// A colour that is exactly an xterm cube or grey-ramp entry maps to its own index,
// and nothing maps to 0–15: those are the terminal theme's colours, whose shades
// loghorn doesn't know.
func TestNearestIndex(t *testing.T) {
	for hex, want := range map[string]int{
		"#000000": 16, "#ffffff": 231, "#303030": 236, "#5faf5f": 71, "#00afff": 39,
		"#080808": 232, "#eeeeee": 255, "#ff5f5f": 203, "#8a8a8a": 245,
	} {
		if got := nearestIndex(mustHex(t, hex)); got != want {
			t.Errorf("nearestIndex(%s) = %d, want %d", hex, got, want)
		}
	}
	for i := 16; i <= 255; i++ {
		c := termenv.ConvertToRGB(termenv.ANSI256Color(i))
		if got := nearestIndex(c); got != i {
			t.Errorf("nearestIndex(%s) = %d, want its own index %d", c.Hex(), got, i)
		}
	}
	// A coarse sweep of the RGB cube, the 16 ANSI colours' usual shades among it.
	for r := 0; r <= 255; r += 17 {
		for g := 0; g <= 255; g += 17 {
			for b := 0; b <= 255; b += 17 {
				c := colorful.Color{R: float64(r) / 255, G: float64(g) / 255, B: float64(b) / 255}
				if got := nearestIndex(c); got < 16 || got > 255 {
					t.Fatalf("nearestIndex(%s) = %d, want 16–255", c.Hex(), got)
				}
			}
		}
	}
}
