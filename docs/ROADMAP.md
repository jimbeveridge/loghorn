# clog Roadmap

Living, ordered backlog of delivery slices. The durable design is in
[docs/superpowers/specs/2026-08-05-clog-design.md](superpowers/specs/2026-08-05-clog-design.md);
this file is the sequence and can be reordered as priorities shift.

Each slice gets its own dated spec (when its detail firms up) and its own implementation plan
under `docs/superpowers/plans/`, built one slice at a time.

Status legend: **next** · planned · later · done

---

## v0 — walking skeleton + live trust  ·  **next**

The core promise end to end — *pipe logs in, see the important lines with context, inspect the
JSON* — plus the two things that make it trustworthy on a live tail (alerts, scriptability).

- stdin ingestion; line splitter (bounded, big-line-safe; group multi-line raw stack traces).
- Two format adapters, auto-detected **per line**, mixed in one stream: **GCP LogEntry JSON**
  and **raw text**. Malformed JSON / non-UTF8 → treated as raw and flagged, never crash.
- Normalized `Entry` (raw bytes, format, timestamp, ordered-enum severity, JSONPath field view).
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
- Configuration via **CLI flags only** (e.g. `--context N`, `--filter`, `--notify`,
  `--cooldown`). No config file yet.

Deferred out of v0: the query grid, interactive search, search-to-rule, a config file, mouse.

## v1 — config & query-by-example  ·  planned

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
