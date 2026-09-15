# Adaptive Palette Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** loghorn's TUI colours read on the terminal's actual background: each style's colour is resolved against the background colour the terminal reports (or `--theme`), keeping its hue and adjusting lightness until it reaches WCAG contrast.

**Architecture:** A new `internal/tui/palette.go` holds the contrast maths (`luminance`, `contrast`, `selectionFor`, `pick`) and owns all 17 style variables, which `SetBackground(bg)` assigns from nine colour roles plus a derived selection background. Package `init` calls `SetBackground(black)`, preserving today's dark look. `main.go` parses `--theme auto|light|dark|#rrggbb`, asks the terminal (OSC 11, via termenv) before Bubble Tea starts, and calls `tui.SetBackground`.

**Tech Stack:** Go 1.25, lipgloss v1.1.0, termenv v0.16.0, go-colorful v1.3.0 (already in `go.sum`; becomes a direct requirement).

**Spec:** `docs/superpowers/specs/2026-09-15-adaptive-palette-design.md` — read it first.

## Global Constraints

- No module is downloaded: `github.com/lucasb-eyer/go-colorful v1.3.0` is already in `go.sum` and moves from `// indirect` to a direct require. No other `go.mod` change.
- Work directly on branch `main` (the user asked for this). Don't push.
- Contrast targets: text `textContrast = 4.5`; the detail-pane divider `ruleContrast = 3.0`.
- `lightLuminance = 0.179`; `selectionShift = 0.08` (CIE LCh lightness); `lightnessStep = 0.02`, at most 50 steps.
- Base colours (hex, today's 256-colour picks): accent `#00afff` (39), attention `#ffaf00` (214), important `#ff5f5f` (203), dim `#8a8a8a` (245), rule `#585858` (240), string `#5faf5f` (71, replacing ANSI 2), boolean `#ff87d7` (212), sqlKeyword `#af87ff` (141), sqlType `#5fd7ff` (81).
- Bold stays exactly where it is today: `impStyle`, `moreStyle`, `helpHeadStyle`. Nowhere else.
- `--theme` values: `auto` (default; ask the terminal), `light` = `#ffffff`, `dark` = `#000000`, `#rrggbb` (exactly 7 characters). Any other value: stderr `loghorn: --theme must be auto, light, dark or #rrggbb`, exit 2.
- The terminal is asked only in TUI mode, and before `tea.NewProgram`.
- Comments explain *why*, in full sentences, matching the existing code's voice.
- Run `gofmt -w` on touched Go files; verify `gofmt -l .` (no output), `go vet ./...`, `go test ./...` from the repo root before each commit.
- Commits end with the trailer `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`.

## File Map

| File | Responsibility |
|---|---|
| `internal/tui/palette.go` (new) | contrast maths, roles, the 17 style variables, `SetBackground` |
| `internal/tui/contrast_test.go` (new) | tests for the maths |
| `internal/tui/palette_test.go` | existing colour test, plus `SetBackground` tests |
| `internal/tui/model.go`, `yaml.go`, `sql.go` | style declarations removed (moved to palette.go) |
| `main.go`, `main_test.go` | `--theme`, `parseTheme`, `terminalBackground`, wiring |
| `go.mod` | go-colorful direct |
| `docs/ROADMAP.md` | record the feature |

---

### Task 1: Contrast maths

**Files:**
- Create: `internal/tui/palette.go`
- Create: `internal/tui/contrast_test.go`
- Modify: `go.mod`

**Interfaces:**
- Consumes: nothing.
- Produces (package `tui`, unexported): constants `textContrast`, `ruleContrast`, `lightLuminance`, `selectionShift`, `lightnessStep`; vars `black`, `white colorful.Color`; `func luminance(c colorful.Color) float64`; `func contrast(a, b colorful.Color) float64`; `func isLight(bg colorful.Color) bool`; `func selectionFor(bg colorful.Color) colorful.Color`; `func pick(base colorful.Color, target float64, bg, sel colorful.Color) colorful.Color`. Test helper `func mustHex(t *testing.T, s string) colorful.Color` (Task 2's tests reuse it).

- [ ] **Step 1: Write the failing tests**

`internal/tui/contrast_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/ -run 'TestContrast|TestIsLight|TestSelection|TestPick' -v`
Expected: FAIL to compile — `undefined: contrast`, `undefined: black`, `undefined: pick`, etc.

- [ ] **Step 3: Implement**

`internal/tui/palette.go`:

```go
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
```

Then make go-colorful a direct requirement:

```bash
GOFLAGS=-mod=mod GOPROXY=off go mod tidy
git diff go.mod go.sum
```

Expected diff: only `github.com/lucasb-eyer/go-colorful v1.3.0` moving from the `// indirect` block into the direct `require` block. If `go mod tidy` changes anything else, or fails offline, run `git checkout go.mod go.sum` and instead edit `go.mod` by hand: delete the `github.com/lucasb-eyer/go-colorful v1.3.0 // indirect` line and add `github.com/lucasb-eyer/go-colorful v1.3.0` to the first (direct) `require` block, keeping it sorted.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/ -run 'TestContrast|TestIsLight|TestSelection|TestPick' -v`
Expected: PASS, all seven.

- [ ] **Step 5: Commit**

```bash
gofmt -l . ; go vet ./... && go test ./...
git add internal/tui/palette.go internal/tui/contrast_test.go go.mod go.sum
git commit -m "feat(tui): WCAG contrast maths for a background-aware palette

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Styles resolved from roles

**Files:**
- Modify: `internal/tui/palette.go` (append)
- Modify: `internal/tui/model.go` — delete the style declarations at lines 41-48, 551-555 and 786
- Modify: `internal/tui/yaml.go` — delete lines 13-19
- Modify: `internal/tui/sql.go` — delete lines 24-27
- Test: `internal/tui/palette_test.go` (append)

**Interfaces:**
- Consumes from Task 1: `black`, `textContrast`, `ruleContrast`, `selectionFor`, `pick`, `contrast`; test helpers `mustHex`, `worstContrast`.
- Produces: `func SetBackground(bg colorful.Color)` (exported, used by main in Task 3). The 17 package variables keep their names: `dimStyle`, `impStyle`, `selStyle`, `statusStyle`, `moreStyle`, `tsStyle`, `helpHeadStyle`, `helpKeyStyle`, `helpNoteStyle`, `dividerStyle`, `keyStyle`, `strStyle`, `numStyle`, `boolStyle`, `nullStyle`, `sqlKeywordStyle`, `sqlTypeStyle`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/palette_test.go` (add `"math"`, `"github.com/lucasb-eyer/go-colorful"` to its imports; it already imports `lipgloss`, `termenv`, `testing`):

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/ -run 'TestSetBackground|TestDefaultPalette' -v`
Expected: FAIL to compile — `undefined: SetBackground`.

- [ ] **Step 3: Implement**

Append to `internal/tui/palette.go` (add `"github.com/charmbracelet/lipgloss"` to its imports):

```go
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
```

Then delete the old declarations (they would now be duplicate definitions):

- `internal/tui/model.go`, the block:
  ```go
  var (
  	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
  	impStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
  	selStyle    = lipgloss.NewStyle().Background(lipgloss.Color("236"))
  	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
  	moreStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
  	tsStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
  )
  ```
- `internal/tui/model.go`, the block:
  ```go
  var (
  	helpHeadStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
  	helpKeyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
  	helpNoteStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
  )
  ```
- `internal/tui/model.go`, the line `var dividerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))` (keep any comment above it only if it still describes something that remains; otherwise remove it too).
- `internal/tui/yaml.go`, the block `var ( keyStyle … nullStyle … )` (five styles).
- `internal/tui/sql.go`, the block `var ( sqlKeywordStyle … sqlTypeStyle … )`.

If removing a block leaves `lipgloss` unused in a file, `go build` reports `"github.com/charmbracelet/lipgloss" imported and not used`; remove that import line then.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/ -v -run 'TestSetBackground|TestDefaultPalette|TestPaletteColours'` then `go test ./internal/tui/`
Expected: PASS, including the pre-existing `TestPaletteColours` and the whole package.

- [ ] **Step 5: Commit**

```bash
gofmt -l . ; go vet ./... && go test ./...
git add internal/tui/palette.go internal/tui/palette_test.go internal/tui/model.go internal/tui/yaml.go internal/tui/sql.go
git commit -m "feat(tui): resolve every style from a role against the background

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: `--theme` and asking the terminal

**Files:**
- Modify: `main.go`
- Test: `main_test.go` (append)
- Modify: `docs/ROADMAP.md`

**Interfaces:**
- Consumes from Task 2: `tui.SetBackground(bg colorful.Color)`.
- Consumes from termenv: `termenv.NewOutput(os.Stdout).BackgroundColor() termenv.Color`, `termenv.ConvertToRGB(termenv.Color) colorful.Color`. With no terminal reply, termenv returns `COLORFGBG`'s colour or `ANSIColor(0)` (black); with stdout not a terminal, a nil colour, which `ConvertToRGB` turns into black.
- Produces: `func parseTheme(s string) (bg colorful.Color, ask bool, err error)`, `func terminalBackground() colorful.Color`.

- [ ] **Step 1: Write the failing test**

Append to `main_test.go` (add `"github.com/lucasb-eyer/go-colorful"` to its imports if you need the type; the test below only uses `.Hex()`):

```go
func TestParseTheme(t *testing.T) {
	for _, tc := range []struct {
		in      string
		wantHex string
		wantAsk bool
	}{
		{"auto", "#000000", true},
		{"light", "#ffffff", false},
		{"dark", "#000000", false},
		{"#cee8be", "#cee8be", false},
		{"#CEE8BE", "#cee8be", false},
	} {
		bg, ask, err := parseTheme(tc.in)
		if err != nil || ask != tc.wantAsk || bg.Hex() != tc.wantHex {
			t.Errorf("parseTheme(%q) = %s, %v, %v; want %s, %v, nil", tc.in, bg.Hex(), ask, err, tc.wantHex, tc.wantAsk)
		}
	}
	for _, bad := range []string{"", "blue", "cee8be", "#cee8b", "#fff", "#cee8bz", "Light"} {
		if _, _, err := parseTheme(bad); err == nil || err.Error() != "--theme must be auto, light, dark or #rrggbb" {
			t.Errorf("parseTheme(%q) error = %v, want the usage message", bad, err)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test . -run TestParseTheme -v`
Expected: FAIL to compile — `undefined: parseTheme`.

- [ ] **Step 3: Implement**

In `main.go`:

1. Imports: add `"github.com/lucasb-eyer/go-colorful"` and `"github.com/muesli/termenv"` (keep import groups as they are: standard library, third party, then loghorn packages).

2. Flags: after the `historical` flag line, add:
   ```go
   	theme := flag.String("theme", "auto", "colours: auto (ask the terminal for its background), light, dark, or the background as #rrggbb")
   ```

3. Directly after the `flag.Visit` block that sets `explicitScrollback` (before the source-tree refusal), add:
   ```go
   	// A bad --theme is a usage error, reported before anything is launched.
   	themeBackground, askTerminal, err := parseTheme(*theme)
   	if err != nil {
   		fmt.Fprintln(os.Stderr, "loghorn:", err)
   		os.Exit(2)
   	}
   ```
   (If `err` is already declared at that scope, use `themeErr` instead of `err` for this one.)

4. In the TUI path, immediately before the comment that begins `// loghorn consumes stdin for logs, so Bubble Tea can't use it for the UI:` (i.e. before `opts := ...`), add:
   ```go
   	// Colours are resolved against the terminal's background before Bubble Tea
   	// starts. The OSC 11 reply comes back on the terminal, and once the program is
   	// reading /dev/tty it would arrive as keystrokes. --filter has returned by now;
   	// it writes no colour, so it never asks.
   	if askTerminal {
   		themeBackground = terminalBackground()
   	}
   	tui.SetBackground(themeBackground)
   ```

5. Add these functions near `executableDir`:
   ```go
   // parseTheme turns --theme into the background colours are resolved against.
   // ask reports "auto": the terminal is asked, and bg is unused.
   func parseTheme(s string) (bg colorful.Color, ask bool, err error) {
   	switch s {
   	case "auto":
   		return colorful.Color{}, true, nil
   	case "light":
   		return colorful.Color{R: 1, G: 1, B: 1}, false, nil
   	case "dark":
   		return colorful.Color{}, false, nil
   	}
   	// Exactly #rrggbb: colorful.Hex also accepts the #rgb short form, which the
   	// flag doesn't promise.
   	if len(s) == 7 && s[0] == '#' {
   		if c, err := colorful.Hex(s); err == nil {
   			return c, false, nil
   		}
   	}
   	return colorful.Color{}, false, errors.New("--theme must be auto, light, dark or #rrggbb")
   }

   // terminalBackground asks the terminal for its background colour (OSC 11). With
   // no reply termenv falls back to COLORFGBG and then to black — the dark palette
   // loghorn has always drawn — so there is no separate failure to handle.
   func terminalBackground() colorful.Color {
   	return termenv.ConvertToRGB(termenv.NewOutput(os.Stdout).BackgroundColor())
   }
   ```

6. `docs/ROADMAP.md`: add a bullet at the end of the v0.x list, directly after the "Always-on log file" bullet (before the blank line and `Toolchain note:`):
   ```markdown
   - **Colours that read on any background**: every colour was a fixed 256-colour index picked for
     a dark terminal, and on a light one they washed out — help keys measured 1.4:1 on a `#cee8be`
     background. loghorn now asks the terminal for its background (OSC 11) before the TUI starts and
     resolves each style's role against it: keep the base colour if it reaches WCAG 4.5:1 (3:1 for the
     divider) against both the background and the selected row, otherwise walk its lightness away
     from the background, keeping its hue, until it does. The selected row is the background nudged
     the same way. `--theme light|dark|#rrggbb` covers terminals that don't answer, such as tmux. On
     a black terminal the colours are unchanged. See
     [the spec](superpowers/specs/2026-09-15-adaptive-palette-design.md).
   ```

- [ ] **Step 4: Run tests and verify by hand**

```bash
gofmt -l . ; go vet ./... && go test ./...
go build -o loghorn .
REPO=$(pwd); TRY=$(mktemp -d)
(cd "$TRY" && "$REPO/loghorn" --theme=bogus --filter </dev/null); echo "exit=$?"
(cd "$TRY" && printf '{"severity":"ERROR","textPayload":"x"}\n' | "$REPO/loghorn" --theme='#cee8be' --filter); echo "exit=$?"
(cd "$TRY" && printf '{"severity":"ERROR","textPayload":"x"}\n' | "$REPO/loghorn" --theme=light --filter); echo "exit=$?"
rm -rf "$TRY"
```

Expected: first prints `loghorn: --theme must be auto, light, dark or #rrggbb` and `exit=2`; the other two print the `x` line and `exit=0`. (The TUI's colours can't be checked without a real terminal; the user checks them by eye.)

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go docs/ROADMAP.md
git commit -m "feat: --theme, and ask the terminal for its background before drawing

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```
