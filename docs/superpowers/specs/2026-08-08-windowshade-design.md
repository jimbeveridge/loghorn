# The status bar as a windowshade pull

Date: 2026-08-08

## Goal

Replace follow/pause with a physical metaphor: the status bar at the bottom is
the shade's pull handle. The cursor resting on it means output is live. Move the
cursor up off the handle and the shade comes down — output stops and the view
holds still. Grab the handle again and you snap back to live.

## Problems with the current model

`follow` is a flag maintained alongside `selected`, and the two have to be kept
consistent at every keystroke — which is where the pause bugs came from (down at
the bottom pausing when there was nowhere to go).

Worse, pausing does not actually stop the display. `listWindow` derives the
window from the tail, so a paused view still scrolls as rows arrive; only the
cursor stops following. A shade that has been pulled down should hold the view
completely still.

## Design

### Cursor model

`selected` becomes a position in `[0, len(rows)]`. The extra position past the
last row — `len(rows)` — *is* the bar. Everything derives from it:

```
onShade() == selected >= len(rows)      // on the handle: live
```

There is no `follow` field. `j`/`k` fall out for free: down from the newest row
lands on the handle and goes live, up from the handle lands on the newest row and
freezes. A new row arriving while on the handle bumps `selected` to the new
`len(rows)`, so the cursor stays on the handle.

### The window actually freezes

`listWindow` gains an explicit `top` anchor:

- **Live:** `top` is recomputed from the tail each frame, so the window rides the
  newest row.
- **Held:** `top` is frozen at the value it had when the cursor left the handle.
  New rows append below the window and nothing on screen moves.

Moving the cursor while held scrolls `top` only as far as needed to keep the
cursor visible. The gap-marker line budgeting is unchanged, so the frame-height
invariant still holds.

### Counters

```
 ▶ clog · LIVE ⠹ · 8,431 lines · 12 shown · j/k · spc hold · enter open · m mouse:on · q quit
   clog · HELD ⠹ · 8,431 lines · 12 shown · ▼12 of 503 waiting · j/k · spc live · …
```

`8,431 lines` is total ingest since startup — always climbing, the proof the pipe
is alive even when nothing passes the display filter.

`▼12 of 503 waiting` means "12 display rows will appear when you let go, out of
503 raw lines read while held". Both figures measure from the moment the shade
came down, so they are directly comparable: the ratio shows how much of the
stream is being filtered out. This replaces the old `▼N new` indicator. `▲N`,
for content above the window, stays.

### Liveness pulse

A braille spinner on the bar advances **one frame per ingested line**. A
time-based pulse would need a ticker to turn itself off and would repaint while
idle; a per-line spinner moves exactly when data flows and freezes solid the
instant it stops, with no timer and no idle repaints. A fast stream spins fast, a
trickle ticks slowly, a dead pipe stops dead.

It also appears on the detail-pane bar, so ingest stays visible while reading an
entry.

### Grabbing and releasing

Grab (go live): `j`/`down` from the newest row, `G`, `space`, or clicking the
bar. Release (freeze): move the cursor up off the bar — that is the only way.

`FOLLOW`/`PAUSED` become `LIVE`/`HELD`. The bar carries the `▶` cursor and the
selected-row highlight while it holds the cursor, so "where is the cursor" has
one consistent answer.

## Testing

- Held: the visible window does not move as rows arrive, and the frame still
  fits the terminal height.
- Live: the window rides the tail.
- Down from the newest row goes live; up from the bar freezes at the newest row.
- Clicking the bar grabs; clicking a row freezes.
- Waiting counters measure from the freeze and reset on release.
- The spinner advances per ingested line and is static otherwise.

## Out of scope

Dragging the bar with a held mouse button, and a rate/throughput figure.
