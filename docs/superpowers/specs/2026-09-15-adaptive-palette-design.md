# loghorn picks colours that read on the terminal's background

Date: 2026-09-15

## The problem

Every colour in the TUI is a fixed 256-colour index chosen for a dark background. On a
light one they wash out. Measured on a real terminal whose background is `#cee8be` (a
light green), against the WCAG AA threshold of 4.5:1 for text:

| Colour | Used for | Contrast on `#cee8be` |
|---|---|---|
| 214 `#ffaf00` | help keys, bar notes, JSON numbers | **1.4 : 1** |
| 39 `#00afff` | status bar, help headings, JSON keys | **1.9 : 1** |
| 245 `#8a8a8a` | dim text, help notes, null | **2.6 : 1** |

The help screen's key names are the most visible symptom, but nothing on the screen
passes. The selected row is worse still: a fixed dark-grey background (236) under
default-coloured text, which on a light theme is dark on dark.

## What the terminal tells us

loghorn can ask. An OSC 11 query (`ESC ] 11 ; ? ESC \`) makes the terminal reply with
its background colour, and termenv — already a dependency, via lipgloss — sends it and
parses the reply. A probe on the same terminal reported `#cee8be`, a light background,
and a truecolor profile. termenv sends a cursor-position request (DSR) after the query as
a sentinel, so a terminal that ignores OSC 11 still answers promptly and loghorn doesn't
wait out the timeout.

## Design

### Detect once, before the TUI starts

In TUI mode only, `main.go` asks for the background after the flags are validated and
before `tea.NewProgram`: `termenv.NewOutput(os.Stdout).BackgroundColor()`, converted to
RGB. The reply arrives on the terminal loghorn draws on, not on stdin, so a piped
producer doesn't interfere. It must happen before Bubble Tea starts reading `/dev/tty`,
or the reply would be read as keystrokes.

- No reply (the terminal doesn't support OSC 11, or a multiplexer swallowed it): termenv
  falls back to the `COLORFGBG` environment variable, and failing that to black. loghorn
  uses whatever termenv returns — there is no separate "no reply" signal to act on — so an
  unanswered query yields the palette for `#000000`, which is today's look.
- `--filter` never asks: it writes no colour.

### `--theme`

`--theme auto|light|dark|#rrggbb`, default `auto`.

| Value | Background used |
|---|---|
| `auto` | the terminal's reply, else `COLORFGBG`, else `#000000` |
| `light` | `#ffffff` |
| `dark` | `#000000` |
| `#rrggbb` | exactly that colour, no query sent |

The explicit values are for terminals that don't answer — tmux and screen often don't
pass OSC 11 through. The hex form costs one parse branch and is the precise fix when you
know your background: `light` computes against white, which is lighter than `#cee8be`, so
contrast on the real background would land a little under target. Anything else exits 2
with `loghorn: --theme must be auto, light, dark or #rrggbb`.

### Roles, not colours

The 17 styles in `model.go`, `yaml.go` and `sql.go` collapse to nine foreground roles
plus the selection background. Styles that share a colour today share a role; bold stays
where it is.

| Role | Base colour (today's) | Styles |
|---|---|---|
| accent | 39 `#00afff` | `statusStyle`, `helpHeadStyle`, `keyStyle` |
| attention | 214 `#ffaf00`, bold where today | `moreStyle`, `helpKeyStyle`, `numStyle` |
| important | 203 `#ff5f5f`, bold | `impStyle` |
| dim | 245 `#8a8a8a` | `dimStyle`, `helpNoteStyle`, `nullStyle`, `tsStyle` (244 today) |
| rule | 240 `#585858` | `dividerStyle` |
| string | 2 → `#5faf5f` | `strStyle` |
| boolean | 212 `#ff87d7` | `boolStyle` |
| sqlKeyword | 141 `#af87ff` | `sqlKeywordStyle` |
| sqlType | 81 `#5fd7ff` | `sqlTypeStyle` |
| selection (background) | 236 `#303030` | `selStyle` |

`strStyle` today uses ANSI colour 2, whose shade is whatever the terminal's theme says
green is. Contrast can't be guaranteed against a colour loghorn doesn't know, so it gets a
fixed base, `#5faf5f` (256-colour 71). `tsStyle` (244) folds into dim (245): the two are
one step apart and indistinguishable.

### Choosing a colour: same hue, adjusted lightness

Each role keeps its hue and chroma; only lightness moves.

1. **Which way is readable.** The background is *light* if its relative luminance is above
   0.179 — the point where black text and white text contrast with it equally. On a light
   background colours get darker; on a dark one, lighter.
2. **Selection background.** The background's CIE LCh lightness shifted by 0.08 towards
   the text direction (darker on a light background). On `#000000` this is `#181818`, a
   little subtler than today's `#303030`; on `#cee8be` it is `#b8d1a8`, a slightly deeper
   green (both measured with a prototype of this algorithm).
3. **Each foreground role.** Start at the base colour. If its contrast against *both* the
   background and the selection background reaches the target, keep it — so on a typical
   dark terminal the colours barely change. Otherwise step LCh lightness by 0.02 in the
   readable direction, clamping chroma to the sRGB gamut, and take the first step that
   reaches the target against both. If none does, use black or white, whichever contrasts
   more.
4. **Targets.** 4.5:1 for every text role. 3:1 for `rule`, which is a line, not text
   (the WCAG non-text threshold).

Checking against the selection background as well matters because the status bar and the
selected row are drawn on it: the bar's own text is `accent`, and a selected important
line is `important` on the selection colour.

Contrast is WCAG 2.x: `(L1 + 0.05) / (L2 + 0.05)` of sRGB relative luminances. LCh
arithmetic uses `github.com/lucasb-eyer/go-colorful`, already in `go.sum` as lipgloss's
dependency; it moves from indirect to a direct `require`, with no new module downloaded.

### Where the palette lives

A new `internal/tui/palette.go` owns the roles, the contrast function and the lightness
search, and exposes one entry point:

```go
// SetBackground recomputes every style for a terminal whose background is bg.
func SetBackground(bg colorful.Color)
```

It reassigns the existing package-level style variables, so no rendering code changes.
`main.go` calls it once, before `tui.NewModel`; nothing renders concurrently at that
point. The package's own initialisation calls it with `#000000`, so tests and any code
that never calls `SetBackground` get the dark palette they get today. The 17 style
variables move into `palette.go`, so one file declares and assigns them all.

The colours are hex values; lipgloss already degrades hex to the nearest 256- or
16-colour index for terminals with a smaller profile.

## Testing

- **Contrast function:** known pairs — black on white is 21:1, a colour on itself is 1:1,
  and `#ffaf00` on `#cee8be` is 1.4:1 to two significant figures.
- **Every role reaches its target** against both the background and its selection
  background, for each of `#cee8be`, `#ffffff`, `#fdf6e3` (Solarized light), `#1e1e1e` and
  `#000000`. A table test over roles × backgrounds, checking chosen colours by contrast
  rather than rendered escape codes.
- **Mid grey falls back correctly:** on `#808080` no colour reaches 4.5:1 against both the
  background and its darker selection shade (black manages 5.3:1 and 4.0:1). The test
  asserts each text role is whichever of black and white contrasts more, not that it meets
  the target.
- **Dark terminals keep their look:** on `#000000`, every role whose base colour already
  meets the target is returned unchanged — measured, that is every role except `rule`.
- **Direction:** the selection background on `#cee8be` is darker than `#cee8be`; on
  `#000000` it is lighter.
- **`--theme` parsing:** `auto`, `light`, `dark`, `#cee8be`, and rejections (`blue`,
  `#cee8b`, `cee8be`). Put the parser in a small helper so it is testable.
- **Existing tests:** `palette_test.go` keeps its purpose (every style renders a colour)
  across the new roles. Any test that asserts a specific 256-colour escape code is updated
  to the new values.
- **Manual:** on the `#cee8be` terminal, the help screen, the status bar, a selected
  important row, and a JSON/YAML/SQL detail pane all read clearly; the same with
  `--theme dark` on a dark terminal looks as it does today.

## Known limits, accepted

- The contrast guarantee holds only for truecolor and 256-colour terminals. On a
  16-colour terminal lipgloss maps each colour to one of the terminal's own 16, whose
  shades loghorn doesn't know.
- The background is read once. Switching the terminal between light and dark themes while
  loghorn runs keeps the palette it started with; restart it.
- Inside tmux or screen the query often gets no reply, and loghorn falls back to the dark
  palette. Pass `--theme`.
- A mid-grey background, around `#808080`, has no colour that reaches 4.5:1 against both
  it and the selection shade; every text role falls back to black, at about 4:1 on a
  selected row.
- Default-coloured text — the body of every list line — is the terminal's own foreground
  and is never recoloured. The selection background is kept close to the real background
  so that text stays readable on it.

## Out of scope

User-configurable colours per role, re-detecting the background mid-run, and a
separate palette for 16-colour terminals.
