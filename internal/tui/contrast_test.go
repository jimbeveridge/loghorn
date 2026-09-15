package tui

import (
	"math"
	"testing"

	"github.com/lucasb-eyer/go-colorful"
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
		if got := pick(mustHex(t, hex), textContrast, bg, sel).Hex(); got != hex {
			t.Errorf("pick(%s) on black = %s, want it unchanged", hex, got)
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
			got := pick(mustHex(t, r.base), r.target, bg, sel)
			if c := worstContrast(got, bg, sel); c < r.target {
				t.Errorf("on %s, pick(%s) = %s reaches only %.2f, want %.1f", bgHex, r.base, got.Hex(), c, r.target)
			}
		}
	}
}

// On a light terminal an unreadable colour moves darker, not lighter.
func TestPickDarkensOnLightBackground(t *testing.T) {
	bg := mustHex(t, "#cee8be")
	base := mustHex(t, "#ffaf00")
	got := pick(base, textContrast, bg, selectionFor(bg))
	if luminance(got) >= luminance(base) {
		t.Errorf("pick(#ffaf00) on #cee8be = %s, want darker than the base", got.Hex())
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
	for _, hex := range []string{"#00afff", "#ffaf00", "#8a8a8a"} {
		if got := pick(mustHex(t, hex), textContrast, bg, sel); got.Hex() != want.Hex() {
			t.Errorf("pick(%s) on #808080 = %s, want fallback %s", hex, got.Hex(), want.Hex())
		}
	}
}
