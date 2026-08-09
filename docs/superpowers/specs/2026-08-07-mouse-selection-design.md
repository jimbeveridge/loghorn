# Mouse selection in the clog TUI

Date: 2026-08-07

## Goal

Select a list line by clicking it, without giving up the ability to select and
copy text in the terminal.

## Background

Enabling mouse tracking makes the terminal forward click events to the program
instead of handling them itself, which disables native text selection. For a log
viewer that is a real cost: copying a log line is a common thing to want. Bubble
Tea can switch tracking on and off at runtime, so this is a toggle rather than a
fixed trade.

## Design

### Line to row mapping

The list draws `⋯` gap markers on their own lines, so a screen line index is not
a display row index. `listLines` lays out the visible list once, pairing each
rendered line with the row it shows (`-1` for a gap marker). Both the renderer
and mouse hit-testing read it, so a click cannot land on a different row than
the one under the cursor — including under the gap-line budgeting that keeps the
frame inside the terminal height.

### Events

| Event | List mode | Detail open |
|---|---|---|
| Left click on a row | select it | select it and re-target the detail pane |
| Left click on a gap marker, empty space, or the status bar | no-op | no-op |
| Left click in the detail pane's half | — | no-op (the wheel works there) |
| Double-click a row | open the detail pane | — |
| Wheel up/down | move the selection one row | scroll the detail viewport |

Clicks, the wheel and `j`/`k` all move the cursor through `moveSelection`, which
clamps to the ends and re-derives follow as "following iff parked on the newest
row". One scroll model, not a separate mouse one.

### Double-click timing

`tea.MouseMsg` carries no timestamp, so the model holds `lastClickRow`,
`lastClickAt`, and a `now func() time.Time` defaulting to `time.Now`. Two clicks
on the same row within 500ms count as a double-click. The injected clock keeps
the behaviour testable without sleeps.

### Toggle

`m` toggles mouse capture from anywhere (list or detail), returning
`tea.EnableMouseCellMotion` or `tea.DisableMouse`. The status bar shows
`m mouse:on|off` so the current state is visible — that is how you know to press
`m` before selecting text to copy. The program starts with capture on
(`tea.WithMouseCellMotion`).

### Status bar

The bar has to absorb a new hint without growing, so the hints shorten: the key
name carries the meaning where the verb was redundant.

```
 clog · FOLLOW · 1234 lines · 8 shown · j/k · spc pause · enter open · m mouse:on · q quit
 clog · detail 45% · j/k scroll · spc page · esc close · q quit
```

Bubble Tea truncates the line to the terminal width, and state is rendered
before hints, so a narrow window loses hints rather than state.

## Testing

`tea.MouseMsg` flows through `Update` like any other message, so everything is
testable headlessly: click at a Y coordinate and assert selection and follow
state; click a gap-marker line and assert nothing moved; two clicks with a
stubbed clock and assert the detail pane opened; wheel with the detail pane open
and assert `YOffset` moved while the selection did not.

## Out of scope

Hover highlighting (needs all-motion tracking, which is noisier), drag to select
a range, and right-click menus.
