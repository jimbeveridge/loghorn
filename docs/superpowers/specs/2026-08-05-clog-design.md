# clog — Capture Log — Design Spec

**Date:** 2026-08-05
**Status:** Approved for planning
**Author:** Jim (with Claude)

---

## 1. Overview & Value Proposition

**clog** ("Capture Log") is a keyboard-first terminal UI, written in Go, that turns a
firehose of GCP Cloud Run logs into a short list of things that actually matter — live,
in the terminal, without losing the context around each one.

> When something breaks during a deploy or incident, you see the ERROR/CRITICAL/5xx the
> moment it scrolls by (no context-switch to the Logs Explorer), inspect the full LogEntry
> JSON with one keystroke, and turn "that pattern I keep searching for" into a permanent
> filter you never have to think about again.

README one-liner: *"A GCP-native log triage TUI: signal-first tailing for Cloud Run,
with query-by-example search, search-to-rule, coalesced desktop alerts, and jq-style
inspection."*

**Thesis:** *shine a light on important data.* By default "important" means **evidence of
failure**. The architecture is designed so importance can later become **derived/aggregate**
(e.g. "p99 latency of SQL queries"), without a rewrite.

**Wedge for launch:** the intersection no existing tool occupies — **opinionated about
importance** *and* **native to the GCP LogEntry schema** at the same time. The core, however,
is format-agnostic; LogEntry is the first adapter, not the architecture.

---

## 2. Values (the principles that settle later arguments)

1. **Signal over noise, always.** The default screen is the exception list, not the log.
2. **Never lose the thread.** Live-first: the tail keeps flowing; pausing to investigate must
   never drop or reorder arriving lines. Hiding a line ≠ discarding it.
3. **Simple, powerful searching.** Search is a first-class value, not an afterthought — and it
   is punctuation-light and fast to read (see §6).
4. **Zero-config useful, fully tunable.** Great defaults out of the box; every default is
   inspectable and overridable in a config file. No magic you can't see.
5. **Keyboard-native, SSH-proof.** Everything reachable by key; nothing *requires* a mouse;
   renders correctly over flaky SSH and a plain `TERM`.
6. **A good Unix citizen.** The same rules engine that powers the TUI runs headless
   (`--filter`) so clog composes with grep/jq/pagers/CI.
7. **Honest and non-destructive.** clog never mutates logs, never phones home, and flags
   malformed input rather than silently dropping it.
8. **Respect the reader's expertise.** Familiar idioms (grep `-B` context, vim nav, jq coloring,
   query-by-example). No hand-holding.
9. **Fast at firehose scale.** Thousands of lines/sec must stay responsive and bounded in memory.

---

## 3. Scope

### Delivery slices

This document is the **north star** — the full design. It ships in slices; see
`docs/ROADMAP.md` for the living, ordered backlog. Summary:

- **v0 (first shippable MVP)** — the walking skeleton that delivers the core promise end to
  end, plus the two capabilities that make it trustworthy live: stdin ingestion; LogEntry + raw
  text adapters (mixed, per-line); a **fixed, hardcoded OR'd "failures" engine** (no
  user-defined filters yet); bounded ring buffer; two-pane TUI (list + on-demand jq detail) with
  hide-plus-`N`-context and follow/pause; **headless `--filter`**; and **coalesced desktop
  alerting** on important matches. Configuration is via **CLI flags only** in v0.
- **v1 — config & query** — TOML config file; the QBE **spreadsheet query grid**; interactive
  search; **search-to-rule**; per-filter `notify` flags; importance generalized to
  **union-of-filters** (this is where the cross-field-OR decision lands: one grid = one
  conjunctive filter, a line is important if it matches *any* active filter).
- **Post-MVP horizon** — additional format adapters; **derived/aggregate importance**
  (windowed, e.g. p99 SQL latency); the **Lotus-1-2-3 text-mode spreadsheet** mode.

Everything below describes the full MVP feature set; the slices above sequence it.

### MVP (v1)

- stdin ingestion; **two auto-detected formats, mixed in one stream**: GCP LogEntry JSON and
  raw text.
- Live-first tailing with pause/follow.
- **Hide routine lines, show important ones with N leading context lines** (`grep -B`-style).
- **One row model** (`JSONPath · comparator · value(s)`) powering all five surfaces (§5).
- Query-by-example **filter grid** and **interactive search** (§6).
- **Search-to-rule**: promote a live grid into a named saved filter written to config.
- **jq-style colorized detail pane**, on demand (§9).
- **Coalesced desktop alerting** (§10).
- **Headless `--filter` mode** (§11).
- Config file (§12); single static binary; keyboard-first, mouse optional.

### Explicitly post-MVP (design must not box these out; do not build them now)

- Additional format adapters beyond LogEntry + raw text.
- **Derived/aggregate importance** (windowed aggregations, e.g. p99 SQL latency).
- **Lotus-1-2-3-style text-mode spreadsheet** over log fields, entered as a *mode shift* from
  the v1 list view. The v1 data model stays record/typed-field friendly so this drops in later.
- Alert delivery beyond desktop notifications (e.g. templates, sound tiers).

### Deliberately excluded (YAGNI)

- clog invoking `gcloud` itself — it is a clean stdin citizen; you pipe logs in.
- Any persistent store/database of past logs — clog is a live lens, not a log store
  (that is Cloud Logging's job).

---

## 4. Concepts & Data Model

**Entry** — the normalized record every layer speaks:

- `Raw []byte` — the original line, byte-for-byte (used for headless output and copy).
- `Format` — detected format (`logentry` | `rawtext`).
- `Timestamp` — normalized; best-effort for raw text.
- `Severity` — normalized to an **ordered enum** following GCP: `DEBUG < INFO < NOTICE <
  WARNING < ERROR < CRITICAL < ALERT < EMERGENCY`. Unknown/raw → a sensible default.
- **Field view** — a **JSONPath-resolvable** accessor over the entry. For LogEntry this is the
  parsed object; for raw text, `$` / `$.message` resolve to the whole line, so text comparators
  still work.
- `Important bool` + which filter(s) matched — computed by the engine.

The Entry is **typed-field-friendly** on purpose (status is an int, latency is a duration,
severity is an ordered enum), so the future spreadsheet/aggregate work has real types to
compute over rather than opaque strings.

**Adapter** — the format extension point:

```
type Adapter interface {
    Detect(line []byte) bool          // cheap check: is this my format?
    Parse(line []byte) (Entry, error) // produce a normalized Entry
}
```

- v1 adapters: `LogEntryAdapter` (GCP JSON — one JSON object per line), `RawTextAdapter`
  (fallback). Detection is **per line**, so a stream that interleaves structured platform logs
  and raw app `stdout` is handled without pre-sorting.
- Malformed/partial JSON and non-UTF8 → treated as raw text and **flagged**, never crash.
- **Multi-line raw entries** (Go/Java stack traces) are grouped into a single logical Entry
  where detectable.

### Lenient LogEntry normalization

Real producers do not reliably nest non-standard fields under `jsonPayload`. Our own backend
(`docs/backend.log`) emits pino-style JSON with `requestId`, `databaseContext`, `latency`,
`serviceName`, and even `time` sitting at the **root**, alongside canonical fields like
`severity` and `httpRequest`. We have minimal control over producers, so this is the norm, not
the exception.

clog therefore **normalizes on parse**:

- Recognize the canonical LogEntry **structural** keys at the root (`severity`, `timestamp`,
  `httpRequest`, `trace`, `spanId`, `labels`, `resource`, `logName`, `insertId`, `operation`,
  `sourceLocation`, `textPayload`, `jsonPayload`, `protoPayload`, `receiveTimestamp`).
- Treat **every other root key** as effective payload — logically merged with an explicit
  `jsonPayload` if one is present — so a field resolves the same whether the producer nested it
  or not.
- **Alias** common variants: `time` → `timestamp` (extendable).

This gives importance rules, the query grid, and correlation a **single field namespace**
regardless of where a producer placed a field. (In v0 the fixed engine reads `severity`,
`httpRequest.status`, and `message` — all present at the root in practice — so full
normalization is a v1 concern, landing with the JSONPath query grid.)

---

## 5. The Row Model & Five Surfaces (core idea)

Everything importance-, search-, and alert-related is built from **one** primitive.

**Predicate** (one grid row):

```
type Predicate struct {
    Path       string      // JSONPath, e.g. $.httpRequest.status
    Comparator Comparator  // see below
    Values     []string    // arity enforced by comparator
}
```

- **Set/text comparators** — `equals`, `contains`, `begins_with`, `ends_with`, `regex`,
  `like` — accept **multiple values, OR'd together**.
- **Inequalities** — `>`, `>=`, `<`, `<=` — accept **exactly one** value.
- The UI enforces arity as you build a row (see §6), which also teaches the model.

**Filter**:

```
type Filter struct {
    Name    string
    Rows    []Predicate   // AND'd together
    Actions Actions       // what happens on match
}

type Actions struct {
    Surface bool          // show it in the view (this is "importance")
    Notify  *NotifyConfig // if set, fire a coalesced desktop alert (§10)
}
```

Evaluation resolves each row's path against an Entry, coerces types (status→int,
latency→duration, severity→ordered enum), applies the comparator, ANDs the rows.

**One `Filter` type, five surfaces:**

| Surface | What it is |
|---|---|
| **Importance** | A named, always-on filter with `Surface: true`. The built-in "failures" set ships as default rows. |
| **Interactive search** | An ad-hoc filter you build live in the grid. |
| **Search-to-rule** | Promote your live grid into a named saved filter (append rows to config). |
| **Headless `--filter`** | Run a named filter non-interactively over stdin. |
| **Alerting** | A filter with `Notify` set; live matches fire coalesced desktop notifications. |

The `Actions` attribute future-proofs toward the analysis vision — a later action could be
"increment a counter / feed a spreadsheet cell."

---

## 6. Search UX — Query By Example (QBE)

Search is a **filter grid**. Instead of typing SQL/WHERE punctuation, you fill in cells; the
structure carries the meaning. Lineage: Zloof's Query-By-Example and the Lotus/dBASE filter row.

A filter is a stack of **rows**, each row three fields:

| Column (JSON Path) | Comparator | Value(s) |
|---|---|---|
| `$.jsonPayload.function_name` | `equals` | `fast_function` `slow_function` `big_function`  → OR |
| `$.httpRequest.status` | `>=` | `500`  (single) |

Semantics: **OR within a cell's values, AND across rows.** The example above =
`function_name IN (...) AND status >= 500`, with **no quotes, commas, or parens typed**.

**Value-entry interaction (multi-value cells):**

- Entering a value cell → **value-entry mode**.
- Type → the value builds up → **Enter commits** it as one token and opens the next slot in the
  *same* cell.
- Committed tokens render as clean chips with no punctuation.
- The **comparator determines arity**: inequalities accept exactly one value (the UI stops you
  from adding a second); set/text comparators accept many.

**Column selector = JSONPath**, which keeps search format-agnostic and handles nested LogEntry
fields naturally. Raw-text lines expose `$` / `$.message`.

Search **filters** (narrows the visible set), building on the hide-by-default view (§8). Search
is an ad-hoc `Filter`; **search-to-rule** persists it.

---

## 7. Importance & Default Rules

- **Built-in "failures" filter** ships as default rows, e.g.:
  - `$.severity` `>=` `ERROR`
  - `$.httpRequest.status` `>=` `500` (4xx surfaced at a lower weight where feasible)
  - presence of stack traces / `panic` / `exception` (text comparator over message/whole line)
- Fully overridable in config; users add/remove rows, change thresholds.
- Importance is **per-line matching in the MVP.** The rule model is intentionally shaped so a
  future predicate could be **derived** (an aggregation over a window) rather than a match —
  but no aggregation is built in v1.

---

## 8. View Behavior

- **Live-first.** Follow mode tails the stream; **pause/follow toggle**. Pausing to investigate
  never drops or reorders arriving lines (they buffer; resume is gap-free).
- **Default = hide routine lines, show important ones**, each preceded by **N leading context
  lines** (`grep -B`-style; N configurable). Important lines are the anchors; context is the
  routine lines immediately before them.
- Reveal on demand: expand context around an entry, or toggle "show everything."
- Correct behavior on terminal resize; degrades cleanly on limited terminals.
- Bounded memory: a **ring buffer** with a configurable scrollback cap; backpressure that never
  deadlocks the producer.

---

## 9. Detail Pane (inspection)

- Default layout: **full-width log list is the hero**; the detail pane is **opt-in**.
- Inspect (Enter) → a **jq-style, syntax-colored, pretty-printed** view of the full entry
  (the complete LogEntry JSON, or the raw line) slides in on the right as a split view.
- Scrollable; dismiss to reclaim full width.
- Copy/export the selected entry's JSON from the pane.

---

## 9.1 Correlated request view (investigation)

The payoff of *live-first, investigation-capable*: surface an error, then read the **whole
request's story** — including the routine rows normally hidden.

- Every entry carries a **correlation id**, extracted as the first present of a configurable
  candidate list (default: `trace`, `requestId`, `logging.googleapis.com/trace`, `spanId`),
  looked up in the normalized field namespace (root or payload).
- From any highlighted row, **one keystroke** opens a **request-scoped timeline**: all buffered
  entries sharing that correlation id, in time order, **including the unimportant rows**. Escape
  returns to the triage list.
- This is essentially free on the data side: the ring buffer already retains every row (hiding
  is a view concern, not a storage one), so correlation just re-filters what we already hold.

Example (`docs/backend.log`): highlight the `POST /api/v1/auth/login returned 401` row and pull
`requestId a673e05f-…` to see the `SELECT users` that preceded it and everything else in that
request.

## 10. Alerting & Coalescing

- A `Filter` with `Notify` set fires a **desktop notification** on live match.
- **Coalescing is mandatory** so alerts stay trustworthy during the incidents they exist for:
  - First match in a quiet window → **notify immediately**.
  - Subsequent matches within a **per-filter cooldown** → roll into a **summary count**
    (e.g. *"37 CRITICAL in the last 60s"*).
- Delivery via a cross-platform layer (`beeep`-style → `osascript`/`terminal-notifier` on
  macOS, `notify-send` on Linux).
- The coalescer takes an **injected clock** so its behavior is unit-testable without real time.

---

## 11. Headless Mode

- `clog --filter <name>` reads stdin, applies the named filter using the **same rule engine**,
  and writes **matching raw lines** to stdout. No TUI.
- One definition of "important," two front-ends (interactive + scriptable/CI).

---

## 12. Configuration

- Format: **TOML** (chosen; user had no preference).
- Location: `~/.config/clog/config.toml`, overridable by a project-local `./clog.toml`.
- Holds: default + saved filters (name, rows, actions), context `N`, colors, keybindings,
  alert cooldowns.
- **Search-to-rule** appends a named filter here.

---

## 13. Architecture & Pipeline

```
stdin
  → line splitter (bounded, big-line-safe, multi-line grouping)
  → adapter Detect/Parse  → Entry (normalized, JSONPath-resolvable)
  → engine: classify Entry against active filters (importance + alert)
  → ring buffer (bounded scrollback)
  → { TUI view (list + on-demand detail)  |  alert coalescer → desktop notifier }

clog --filter <name>:  stdin → split → parse → engine → stdout (matching raw lines)
```

Seams are chosen so each unit is independently testable:

- **Adapters** — pure `Detect`/`Parse`.
- **Comparators / engine** — pure predicate evaluation with type coercion.
- **Coalescer** — injected clock.
- **TUI** — Bubble Tea model, testable with `teatest`.

---

## 14. Tech Stack

- **TUI:** Bubble Tea + Lip Gloss + Bubbles (Charm ecosystem) — de-facto standard, has
  viewport/table/textinput, and `teatest` for TUI-level tests.
- **JSONPath:** a fast Go lib (e.g. `ojg`/`jp`).
- **Desktop notifications:** `beeep`-style cross-platform layer.
- **Config:** a TOML library.
- Single static binary; macOS + Linux; no runtime deps, no network calls, no telemetry.

(Considered and set aside: `tview` — more built-in widgets but less flexible for the future
spreadsheet layout; raw `tcell` — maximal control, much more to build.)

---

## 15. Testing Approach

Test-first (TDD). The pure units — adapters, comparators/engine, coalescer (injected clock) —
are unit-tested directly. The TUI is exercised via `teatest`. Malformed-input, mixed-stream,
multi-line-grouping, and arity-enforcement cases are explicit test targets.

---

## 16. Requirements (consolidated)

**Input & formats**
- Read a continuous stream from **stdin**; identical behavior for live tails and piped batches.
- Auto-detect and handle **LogEntry JSON + raw text, mixed in one stream**.
- Understand LogEntry natively: `severity`, `httpRequest.status/latency/requestMethod`,
  `timestamp`, `trace`/`spanId`, `resource.labels`, `jsonPayload`/`textPayload`/`message`.
- Tolerate malformed/partial JSON and non-UTF8 gracefully; flag, never crash.
- Group multi-line raw entries (stack traces) into one logical Entry where detectable.

**View & importance**
- Default: show only important lines + **N leading context lines**; hide routine runs.
- Built-in "failures" heuristics; config-overridable; live threshold tuning.
- Reveal hidden lines / context on demand; pause/follow; correct on resize.

**Search**
- QBE filter grid: `JSONPath · comparator · value(s)`; OR within a cell, AND across rows.
- Comparator sets arity (inequalities single-value; set/text multi-value OR).
- Search filters the visible set; **search-to-rule** persists to config.

**Interaction**
- Keyboard-first (vim-ish nav, Enter to inspect, `/` to search, keys to expand context /
  tune thresholds / mute / promote). Mouse optional, never required.
- jq-style colorized detail pane on inspect; copy/export selected entry.

**Alerting**
- Filter-flagged desktop notifications with **per-filter coalescing + cooldown**.

**Headless**
- `clog --filter <name>` emits matching raw lines to stdout using the same engine.

**Non-functional**
- Bounded memory under sustained high throughput (ring buffer); no producer deadlock.
- Single static Go binary; macOS/Linux; degrades on limited terminals.
- No network calls, no telemetry, non-destructive.

---

## 17. Differentiators & Competition

**Key differentiators**
1. **Importance-first, hide-with-context view** — the default *is* the triage list, with
   `-B`-style context so an error never appears naked.
2. **Search-to-rule** — find a pattern once, promote it to a persistent rule; the tool learns
   your service. Nothing in the log-viewer space does this.
3. **GCP LogEntry-native** semantics (status, severity, latency, trace) on a format-agnostic core.
4. **Dual-format, mixed-stream** ingestion without pre-sorting.
5. **jq-style inspection bound to the live tail** — one keystroke, no losing your place.
6. **Same engine, TUI or headless.**
7. **Live-first with safe investigation** (gap-free pause/resume).
8. **QBE search** — punctuation-free, fast to read, and a stepping stone to the spreadsheet vision.
9. **Coalesced desktop alerts** that stay trustworthy during incidents.

**Acknowledged competition (honest positioning)**
- `gcloud logging tail | jq` — no importance model, no UI state.
- Cloud Logging web console — powerful queries, but a context-switch, not terminal/pipe-native.
- `lnav` — excellent general log navigator, but generic and not opinionated about GCP importance.
- `toolong` — nice TUI tailer, but no importance model or LogEntry semantics.
- structured prettifiers (`humanlog`/`fblog`) — coloring, but no triage or rules.

---

## 18. Deferred / Open Questions

- Exact comparator set for v1 (is `like` worth shipping given `contains`/`begins_with`/
  `ends_with`, or defer it?).
- 4xx weighting vs. binary importance in the default "failures" filter.
- Precise multi-line-grouping heuristics per language runtime.
- Notification appearance details (title/body templates) — kept minimal for v1.
- Project repository/hosting choice (git init location, license, module path).
