# clog Roadmap

Living, ordered backlog of delivery slices. The durable design is in
[docs/superpowers/specs/2026-08-05-clog-design.md](superpowers/specs/2026-08-05-clog-design.md);
this file is the sequence and can be reordered as priorities shift.

Each slice gets its own dated spec (when its detail firms up) and its own implementation plan
under `docs/superpowers/plans/`, built one slice at a time.

Status legend: **next** · planned · later · done

---

## v0 — walking skeleton + live trust  ·  **done**

The core promise end to end — *pipe logs in, see the important lines with context, inspect the
JSON* — plus the two things that make it trustworthy on a live tail (alerts, scriptability).

- stdin ingestion; line splitter (bounded, big-line-safe). One physical line → one entry in
  v0; multi-line raw stack-trace grouping is deferred to v1.
- Two format adapters, auto-detected **per line**, mixed in one stream: **GCP LogEntry JSON**
  and **raw text**. Malformed JSON / non-UTF8 → treated as raw and flagged, never crash.
- Normalized `Entry` (raw bytes, format, timestamp, ordered-enum severity, and **typed
  accessors** — severity, HTTP status, message). General JSONPath resolution arrives with the
  query grid in v1.
- **Fixed, hardcoded "failures" engine** (OR'd in code): `severity >= ERROR` OR
  `httpRequest.status >= 500` OR text matches `panic|fatal|exception|traceback`.
  *(No user-defined filters in v0 — this sidesteps the cross-field-OR decision until v1.)*
- Bounded **ring buffer** (configurable scrollback cap); no producer deadlock.
- **TUI** (Bubble Tea): follow/pause tail showing **only important lines + `N` leading context
  lines** (dimmed); vim-ish nav; **Enter** opens an on-demand right pane with **jq-style
  colorized pretty-printed JSON** (or the raw line); copy selected entry.
- **Headless `clog --filter`**: same engine, no UI — emit important raw lines to stdout.
- **Coalesced desktop alerting**: opt-in; notify on important matches with a per-run cooldown
  and rolling summary count (injected-clock-testable coalescer). macOS + Linux.
- Configuration via **CLI flags only** (e.g. `--context N`, `--filter`, `--notify`). No config
  file yet. (Notification flags were refined post-v0 — see v0.x below.)

Deferred out of v0: the query grid, interactive search, search-to-rule, a config file.
(Mouse support was deferred out of v0 and has since landed — see v0.x below.)

## v0.x — post-v0 polish  ·  done

Shipped after v0 in response to real use:

- **Scrollable detail pane** (`bubbles/viewport`): long LogEntry JSON no longer overflows the
  screen — it scrolls vertically, with an `all` / `NN%` indicator. (This was the final review's
  top v1 item; done early.)
- **Detail pane overlays the list**: the list is always laid out at the full terminal width, and
  the pane is composited on top of the right-hand columns, behind a vertical rule and a one-space
  margin that run its full height. Opening it covers text rather than
  re-flowing the list into a narrower column and re-truncating every line, so what you were
  reading stays exactly where it was. Hit-testing reads the same full-width layout the renderer
  drew, which also fixed clicks landing on the wrong row once the pane stopped being half-width.
- **Detail pane sized to its content**: the pane is as wide as the entry's longest line rather
  than a fixed half-screen, capped at terminal width − 20 so the list keeps a readable column.
  Measured ANSI-aware on the unwrapped render, recomputed per entry and on resize. Lines wider
  than the cap are **clipped, not folded**: one log line stays one line, so the structure of the
  JSON and of a stack trace survives, where folding turned every long value into a ragged block.
  The trade is that the far end of an over-wide value is off-screen; horizontal scrolling would
  be the fix if that starts to bite.
- **Reliable keyboard + resize when piping**: because stdin is the log stream, the TUI reads
  key and resize events from `/dev/tty`; the layout reflows live on terminal resize.
- **Unseen-content indicator**: the status bar shows `▲N` for older lines above the window, and
  while held `▼N of M waiting` for what is piling up behind the shade (see below).
- **Windowshade follow model**: the status bar is the shade's pull handle. The cursor is a
  position in `[0, len(rows)]`, and the extra slot past the last row *is* the bar — resting
  there means live, so `follow` is derived rather than a flag kept in sync. Moving up off the
  handle pulls the shade down: the window is pinned where it stands, so arriving rows pile up
  below it instead of scrolling the view (the old paused mode only stopped the cursor, not the
  scrolling). `j`/`down` from the newest row, `G`, `space` or clicking the bar grab it back.
- **Liveness at a glance**: the bar carries a total ingest count — climbing even when nothing
  passes the display filter — and a braille spinner that advances one frame per ingested line,
  so it moves exactly when data flows and freezes solid when the pipe stops. No ticker, no
  idle repaints.
- **Per-line ingest time**: each list line is prefaced with the wall-clock time clog received
  it, `HH:MM:SS.mmm` (no date).
- **Mouse support** (on by default): click to select, double-click to open the detail pane,
  wheel to walk the list or scroll the pane. `m` toggles capture, handing text selection back
  to the terminal so a line can be copied. One `listLines` layout backs both rendering and
  hit-testing, so a click cannot land on a row other than the one under the cursor.
- **Stacking view filters**: `a` toggles all-lines / failures-only, `c` pins the view to the
  selected line's correlation id. They compose — both on shows the failures within that one
  request. Pinning uses `Entry.CorrelationID` (`trace` → `requestId` →
  `logging.googleapis.com/trace` → `logging.googleapis.com/spanId` → `spanId`) rather than a bare
  `spanId`, because real logs carry whichever of those the producer emits: `docs/backend.log` has
  only `requestId`, and Cloud Run uses the fully-qualified span key. Active filters are named on
  the bar; the default says nothing. A line carrying no id cannot anchor the pin, so `c` declines
  and says why instead of emptying the screen.
- **Key reference behind `?`**: the status bar keeps only the keys reached for constantly —
  `j/k`, `space`, `enter`, `q` — plus `?`, so the rest stay discoverable. `m`, `f`, `Q`, `g`
  and `G` live in a scrollable help overlay that layers over whatever you were reading. Mouse
  capture is reported on the bar only when it is *off*, the surprising state. `enter` closes
  the detail pane as well as opening it, so undoing it doesn't mean crossing the keyboard.
- **Frame fits the terminal**: Bubble Tea drops the *top* of an oversized frame, so an overlong
  one wiped the list. Multi-line messages are flattened to the single line the list gives them,
  `⋯` gap markers are budgeted as the lines they are, and the trailing newline is gone.
- **Stack traces read as stack traces**: newlines inside JSON string values print as real line
  breaks in the detail pane instead of literal `\n`.
- **clog launches the producer** (`clog -- npm run dev`): in a pipeline only stdout is piped, so
  both processes still read `/dev/tty` and the kernel splits keystrokes between them — measured
  at 8 of 10 keys going to the producer, which is why `q` often missed and npm ended up acting
  on stray keys and mouse escapes, then outlived clog (node ignores `SIGPIPE`). Launching fixes
  it at the root: stdout+stderr share one pipe, stdin is a pty (so the child's own shortcuts
  still work, reachable via `f`), and the child runs in its own process group. `q` stops that
  group (SIGTERM, `--shutdown-grace`, then SIGKILL); `Q` detaches and leaves it running. If the
  child dies on its own the TUI stays up and the bar says so, since that is when the scrollback
  matters most. Piping still works for files and non-interactive producers.
- **Refined error notifications** (`--notify`): fire once per error burst — a burst ends after
  a quiet gap (`--notify-reset`, default 15s) so the next error notifies again — and skip
  errors whose own log timestamp is older than `--notify-max-age` (default 1s), so replaying an
  old file stays quiet. The LogEntry adapter now also reads the pino-style `time` field as a
  timestamp alias (pulled forward from v1 to support the freshness gate).
- **Notification sound** (`--notify-sound`, on by default): macOS shows `osascript` notifications
  under Script Editor's alert style, which is "Banners" out of the box — they auto-dismiss after
  a few seconds, so a silent one is easy to miss entirely. The sound is the only part clog
  controls; making them persist means setting Script Editor (or terminal-notifier, which beeep
  prefers when it is on `PATH`) to "Alerts" in System Settings › Notifications.

Toolchain note: the effective Go floor is **Go 1.24**, not the 1.22 the original plan targeted —
bubbletea's transitive deps (`colorprofile`, `x/ansi`, `x/cellbuf`) require it, and `go mod
tidy` raises the `go` directive automatically.

## v1 — config & query-by-example  ·  planned

- **Multi-line raw grouping** (stack traces) and general **JSONPath field resolution** (needed
  by the query grid), both deferred out of v0.
- **Lenient LogEntry normalization** — real producers don't reliably nest non-standard fields
  under `jsonPayload`; our own backend puts `requestId`, `databaseContext`, `latency`, and even
  `time` at the **root** (see `docs/backend.log`). Normalize on parse: recognize canonical
  LogEntry structural keys at the root, treat every other root key as effective payload, and
  alias variants (`time`→`timestamp`). Gives search + correlation one field namespace.
- **Correlated request view** — from any highlighted row, one keystroke shows the *complete*
  timeline for that row's correlation id (`requestId`/`trace`), **including the routine rows
  normally hidden** — "shine a light on the error, then read the whole request's story."
  Enabled by the ring already retaining every row; needs (a) correlation-id extraction (first
  present of a configurable candidate list: default `trace`, `requestId`,
  `logging.googleapis.com/trace`, `spanId`) and (b) a request-scoped timeline view.
- TOML config file (`~/.config/clog/config.toml`, overridable by `./clog.toml`): default +
  saved filters, context `N`, colors, keybindings, alert cooldowns.
- **QBE spreadsheet query grid**: one column per predicate — header = field (JSONPath), row 2 =
  comparator, rows 3+ = zero-or-more values. **AND across columns, OR down a column**, zero
  values = unary comparator.
- **Interactive search** = building an ad-hoc filter in the grid; **search-to-rule** = promote
  it to a named saved filter in config.
- Importance generalized to **union-of-filters**: one grid = one conjunctive filter; a line is
  important if it matches **any** active filter (DNF). The built-in "failures" set becomes
  several simple filters instead of hardcoded code.
- Per-filter `notify` action flags (alerting driven by named filters).

## Post-MVP horizon  ·  later

- Additional format adapters beyond LogEntry + raw text (the `Adapter` interface is the seam).
- **Derived / aggregate importance** — windowed aggregations as predicates (e.g. p99 latency of
  SQL queries), not just per-line matches.
- **Lotus-1-2-3-style text-mode spreadsheet** over log fields, entered as a mode shift from the
  list view. Query grid columns and data grid columns share semantics.
- Alert delivery beyond desktop notifications (templates, sound/severity tiers).
