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
RGB. The reply arrives on the terminal loghorn draws on, not on stdin, so loghorn's own
stdin — the pipe carrying logs — doesn't interfere. It must happen before Bubble Tea starts
reading `/dev/tty`, or the reply would be read as keystrokes.

- No reply (the terminal doesn't support OSC 11), or no query at all (inside tmux or screen,
  below): termenv
  falls back to the `COLORFGBG` environment variable, and failing that to black. loghorn
  uses whatever termenv returns — there is no separate "no reply" signal to act on — so an
  unanswered query yields the palette for `#000000`: text in its usual colours except
  strings, which use a fixed green; a slightly lighter divider; and a subtler selected row.
- `--filter` never asks: it writes no colour.

### `--theme`

`--theme auto|light|dark|#rrggbb`, default `auto`.

| Value | Background used |
|---|---|
| `auto` | the terminal's reply, else `COLORFGBG`, else `#000000` |
| `light` | `#ffffff` |
| `dark` | `#000000` |
| `#rrggbb` | exactly that colour, no query sent |

The explicit values are for terminals that don't answer, and for tmux and screen: termenv
doesn't send the query at all when `TERM` starts with `screen` or `tmux`, so loghorn falls
back to black there. The hex form costs one parse branch and is the precise fix when you
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
| attention | 214 `#ffaf00`; 4.5:1 like every other role, except on a light background, where it targets 3:1 (WCAG's large-text threshold, applied as a deliberate trade — see Targets below) and `helpKeyStyle`/`numStyle` go bold to go with it; `moreStyle` is always bold | `moreStyle`, `helpKeyStyle`, `numStyle` |
| important | 203 `#ff5f5f`, bold | `impStyle` |
| dim | 245 `#8a8a8a` | `helpNoteStyle`, `nullStyle`, `tsStyle` (244 today), and `dimStyle` on dark backgrounds only — on a light background `dimStyle` carries no foreground at all (see Targets below) |
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

Each role keeps its hue; lightness moves, and chroma gives way only where sRGB can't show the
colour at that lightness.

1. **Which way is readable.** The background is *light* if its relative luminance is above
   0.179 — the point where black text and white text contrast with it equally. On a light
   background colours get darker; on a dark one, lighter.
2. **Selection background.** The background's CIE LCh lightness shifted by 0.08 towards
   the text direction (darker on a light background). On `#000000` this is `#181818`, a
   little subtler than today's `#303030`; on `#cee8be` it is `#b8d1a8`, a slightly deeper
   green (both measured with a prototype of this algorithm). Hue and chroma for this shift,
   and everywhere below that a colour's own hue is read back out, come straight from Lab —
   `atan2(b, a)`, `hypot(a, b)` — rather than through go-colorful's own Hcl accessor, which
   sets hue to 0 whenever `a` is within 1e-4 of zero or `a` and `b` are nearly equal,
   regardless of chroma. That axis is not just greys: a saturated navy background such as
   `#003357` sits on it, and reading its hue the naive way once sent the selected row to
   maroon instead of a darker navy.

   On a mid-grey background — `#6f6f6f` and `#808080` are the measured cases — the
   toward-text selection can leave *no* colour able to reach `textContrast` against both the
   background and it: the selection sits between the background and whichever of black or
   white the fallback would use, which only narrows the contrast that fallback depends on.
   So the toward-text selection is tried first, and kept if black or white (as the terminal
   will actually show them) already reaches `textContrast` against both the background and
   it — the ordinary case, unchanged. Only when neither does is the selection nudged the
   *other* way instead — away from the text direction, with the same stepping and the same
   visibility rule (on the far side of the background, `selectionVisible` from it) — and that
   selection is used if it gets black or white to `textContrast` against both. Moving away
   from the text direction moves the selection away from the fallback colour too, which is
   what restores the reach the toward-text side lost, at the cost of a selection sitting
   further from the terminal's own background than usual. If neither direction reaches the
   target, the toward-text selection is kept regardless: flipping only when it actually helps
   means a background where nothing works looks exactly as it did before this rule existed.
3. **Each foreground role.** Start at the base colour. If its contrast against *both* the
   background and the selection background reaches the target, keep it — so on a typical
   dark terminal the colours barely change. Otherwise step LCh lightness by 0.02 in the
   readable direction and take the first step that reaches the target against both. A step
   outside the sRGB gamut has its chroma reduced, by bisection at the same hue and
   lightness, until it fits; clipping each RGB channel instead would move the hue (the
   accent swung 19° on `#cee8be`). If no step reaches the target, use black or white,
   whichever contrasts more.
4. **Targets.** 4.5:1 for every text role but one. 3:1 for `rule`, which is a line, not
   text (the WCAG non-text threshold). `attention` (`moreStyle`, `helpKeyStyle`,
   `numStyle`) targets 4.5:1 too, with one exception: **only when `isLight(bg)`**,
   it drops to `accentContrast`, 3:1, and `helpKeyStyle`/`numStyle` become bold to
   go with it (`moreStyle` is always bold). WCAG's 3:1 is the
   threshold for *large* text — 18pt, or 14pt bold — and attention's tokens are
   short, so bold help keys and numbers at terminal size don't strictly qualify;
   this is a deliberate trade the user asked for, not a strict reading of WCAG,
   because holding them to 4.5:1 forced `#714b00`, a near-black brown, on the
   user's `#cee8be` terminal, leaving almost no differentiation between the
   accent and the surrounding text. At 3:1, bold, it is `#986600`, a visibly
   lighter amber (`#875f00`, ANSI256 index 94, on a 256-colour terminal). The 3:1
   target is gated on `isLight(bg)`, not on whether the base colour happens to
   already clear 3:1: on a dark background — including ones far from black, such
   as Nord's `#3b4252` — attention still targets textContrast, 4.5:1, exactly like
   every other role, so it resolves there to `#ffbe5b`, not the base `#ffaf00`,
   and `helpKeyStyle`/`numStyle` stay non-bold. Separately, on a light background
   `dimStyle` — the log list's non-failure rows — is left with no foreground at
   all, `lipgloss.NewStyle()`, so those rows render in the terminal's own
   (typically black) text rather than the dim role's grey: the user found the dim
   grey list lines washed out next to a true black and asked for "a strong black"
   there. `helpNoteStyle`, `nullStyle` and `tsStyle` keep the dim role's colour on
   every background; only the list's context rows change, and only on a light one.
5. **Judge what the terminal shows.** On a truecolor terminal that is the colour rounded to
   8 bits a channel. On a 256-colour terminal it is the nearest xterm index from 16 to 255
   by CIE Lab distance — 0–15 are the theme's own colours, which loghorn doesn't know — so
   the selection background and every candidate are quantised before their contrast is
   judged. The selection's nudge continues in 0.02 steps until the colour shown is on the
   text side of the real background and contrasts with it by at least 1.1:1. Truecolor
   selections measure about 1.17–1.35:1, so 1.1 allows for quantisation while keeping the
   row visible. On `#cee8be` the first nudge already qualifies, 151 `#afd7af` at 1.21:1; on
   `#d7ffaf`, itself index 193, the first nudge already lands on a different one, 150. The
   background is never quantised: the terminal draws it exactly, so visibility is judged
   against it, not against its nearest index.

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
that never calls `SetBackground` get the dark palette: text colours as before except
strings, a slightly lighter divider and a subtler selected row. The 17 style
variables move into `palette.go`, so one file declares and assigns them all.

The palette is resolved for lipgloss's colour profile, which lipgloss reads from the
environment (`TERM`, `COLORTERM`), not by asking the terminal. Truecolor terminals get hex.
256-colour terminals get index numbers from loghorn's own quantiser, which lipgloss passes
through untouched: given hex, lipgloss v1.1 quantises through termenv v0.16's
`hexToANSI256Color`, whose grey-ramp candidate is computed from cube indices instead of
channel values and so is always 232 `#080808` — the selected row nearly vanished on black,
and important lines turned near-black on white. 16-colour and no-colour profiles get hex,
which lipgloss maps to the theme's own colours or drops. A small `display` function — the
colour actually shown, and the name lipgloss is given for it — carries the difference, so
the search has no profile branches of its own.

## Testing

- **Contrast function:** known pairs — black on white is 21:1, a colour on itself is 1:1,
  and `#ffaf00` on `#cee8be` is 1.4:1 to two significant figures.
- **Every role reaches its target** against both the background and its selection
  background, for each of `#cee8be`, `#ffffff`, `#fdf6e3` (Solarized light), `#1e1e1e` and
  `#000000`. A table test over roles × backgrounds, checking chosen colours by contrast
  rather than rendered escape codes. `attention`'s target is 3:1 only on a light
  background (4.5 everywhere else, like every other role), and on a light
  background `dimStyle` is skipped there — it carries no foreground to check — and
  verified separately to be `lipgloss.NoColor{}`.
- **Attention hits 3:1, not 4.5, and is bold, only on light backgrounds:** on `#cee8be`,
  `helpKeyStyle`/`numStyle`/`moreStyle` reach 3:1 against both the background and the
  selection, are lighter than the 4.5:1 result (`#714b00`), keep the base's hue
  within 1°, and are bold; on `#cee8be` under ANSI256 they resolve to index 94.
  On `#000000`/`#1e1e1e` they equal today's `#ffaf00` and `helpKeyStyle`/`numStyle`
  are not bold. This also holds on dark backgrounds far from black — `#3b4252`
  (Nord) and `#44475a` (Dracula) — each pinned to the colour 4.5:1 resolves to
  there (`#ffbe5b`, `#ffcd88`), not recomputed, so the test can't drift with the
  code it guards (`TestAttentionUnchangedOnDark`).
- **Hue holds:** in truecolor, on `#cee8be` and `#ffffff`, every chromatic role (accent,
  attention, important, string, boolean, sqlKeyword, sqlType) resolves within 1° of its base
  hue. Near-grey results are skipped: their hue is undefined.
- **256 colours:** the quantiser maps every xterm colour from 16 to 255 to its own index
  (`#000000`→16, `#ffffff`→231, `#303030`→236, `#5faf5f`→71, `#00afff`→39) and never
  returns 0–15. Under the ANSI256 profile, on `#000000`, `#1e1e1e`, `#ffffff`, `#cee8be`,
  `#fdf6e3` and `#d7ffaf`, every style names an index from 16 to 255 whose colour reaches its
  target against the background and the selection's index, and the selection's colour is on
  the text side of the background at 1.1:1 or more. The selection stops at the first nudge
  that qualifies: 151 on `#cee8be`, 234 on `#000000`, 254 on `#ffffff`. Tests that care
  about the profile set it and restore it, so they don't depend on the environment.
- **Every style is wired to its role:** at package init each of the 15 text styles is its
  role's base colour, and the divider is lightened from its base to reach 3:1.
- **Mid grey falls back correctly:** `pick`'s own fallback is exercised directly, against a
  toward-text selection: on `#808080` no shade of any hue reaches 4.5:1 against both the
  background and that selection (black manages 5.3:1 and 4.0:1), so the test asserts each
  text role is whichever of black and white contrasts more, not that it meets the target
  (`TestPickMidGreyFallsBack`).
- **Mid grey reaches target via the flip:** `SetBackground` itself avoids the case above. On
  `#6f6f6f`, `#808080` and `#767676`, in both colour profiles, every role reaches its target
  against both the background and the selection `SetBackground` actually resolves, the
  divider reaches 3:1, and the selection stays at least `selectionVisible` from the
  background (`TestMidGreyReachesTargetViaFlip`). `#6f6f6f` is dark by the 0.179 luminance
  threshold though pale to the eye; `#767676` sits close enough above the threshold to be
  worth checking, though its toward-text selection already reached the target before the
  flip existed, so it stays unchanged by it.
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
  `--theme dark` on a dark terminal shows text colours as before, a subtler selected row,
  a slightly lighter divider, and fixed green strings.

## Known limits, accepted

- The contrast guarantee holds for truecolor and 256-colour terminals — on 256 colours
  because loghorn quantises each colour itself and judges the index. It does not hold on a
  16-colour terminal, where lipgloss maps each colour to one of the terminal's own 16, whose
  shades loghorn doesn't know.
- On 256 colours hue is only approximate, and roles can share an index. The cube has no
  chromatic levels between 0 and 95 a channel, so where text must be dark there are few
  shades to choose from: accent and sqlType are both 24 on `#ffffff` and `#fdf6e3`.
  Attention no longer shares accent's 236 on `#cee8be` — its 3:1 target (not 4.5, see
  Targets) resolves to 94, a genuine amber — but accent itself is still 236 there.
- The background is read once. Switching the terminal between light and dark themes while
  loghorn runs keeps the palette it started with; restart it.
- Inside tmux or screen termenv doesn't send the query at all — it skips it whenever `TERM`
  starts with `screen` or `tmux` — so loghorn falls back to the dark palette. Pass `--theme`.
- A terminal that answers neither OSC 11 nor the cursor-position request stalls startup for
  termenv's 5-second timeout. So does an interactive piped producer reading the same
  terminal, which can swallow the reply. `--theme` skips the query.
- A mid-grey background, around `#808080`, is classified light (its luminance clears
  `lightLuminance`). Its toward-text selection alone would leave every role still
  targeting 4.5:1 with no colour that reaches it against both the background and that
  selection, falling back to black at about 4:1 on a selected row; `#6f6f6f`, dark by the
  same threshold though pale to the eye, has the matching problem in the other direction.
  The selection step's flip (above) catches both: on each, black or white reaches 4.5:1
  against the background and the *flipped* selection, so `SetBackground` uses that
  selection instead and every role reaches its target there — see
  `TestMidGreyReachesTargetViaFlip`. Near `lightLuminance` — `#767676`, say, just above the
  threshold — a background classified light this way can itself have a light terminal
  foreground; loghorn has no way to know what the terminal's own text colour actually is,
  so `dimStyle`'s "use the terminal's own foreground" fallback can't be guaranteed readable
  right at that boundary, unlike every colour loghorn picks itself. That caveat is
  unaffected by the flip: it concerns the one role that isn't loghorn's own colour choice.
- On saturated dark-blue backgrounds such as `#000044`, the 256-colour selection is much
  heavier than usual, index 61 at about 3.45:1, because the cube has few dark blues to nudge
  through; text still meets its targets.
- The list has two row kinds, `RowImportant` and `RowContext`; both are styled on a dark
  background (`impStyle`, `dimStyle`). On a light background `RowContext` rows — the
  non-failure rows — instead render as the terminal's own foreground, uncoloured
  (`dimStyle` carries no colour there; see Targets); `RowImportant` rows stay styled with
  `impStyle` on every background. The selection background is kept close to the real background
  so that text stays readable on it.

## Out of scope

User-configurable colours per role, re-detecting the background mid-run, and a
separate palette for 16-colour terminals.
