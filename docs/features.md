# Web view features (Scuba parity)

A checklist of the controls in Facebook's original Scuba query sidebar, mapped
to loghorn's web view (`loghorn serve`, then `loghorn ui`). Use it to pick what to build next and
to record what's done.

Status: **done** · **partial** · **todo** · **n/a** (doesn't fit loghorn)

When an item ships, change its status and add the commit or plan that delivered it.

---

## Header

| Scuba control | What it does in Scuba | loghorn | Notes |
|---|---|---|---|
| Dataset name (`Pagestats`) | Shows the table being queried | **done** | The stream picker in the sidebar. |
| ✎ rename / edit dataset | Edits the dataset's metadata | **todo** | Could edit a stream's display name and description in the schema TOML. |
| Show dataset description | Expands a prose description of the table | **todo** | Needs a `description` field in the stream schema. |
| View-type dropdown (`Time Series`) | Picks Samples / Table / Time Series / Pie / Distribution, etc. | **partial** | Samples, Table and Graph tabs. The graph draws lines or stacked areas (Chart; stacked only for count or sum). Missing: distribution (histogram), pie. |

## Actions bar

| Scuba control | What it does | loghorn | Notes |
|---|---|---|---|
| Save | Saves the query by name | **done** | The heart in the header saves; the sidebar's Favorites tab lists them (`/api/favorites`), stored as TOML. |
| FBUrl (short link) | Copies a shareable link to this query | **partial** | The URL query string already holds the full state (`history.replaceState`). Missing: a "copy link" button. A short link isn't needed for a loopback server. |
| Export | Downloads results as CSV / TSV | **todo** | Export the current Samples or Table view as CSV or JSON Lines. |
| Reset | Clears the query back to defaults | **todo** | Columns have "restore defaults", but there's no reset for the whole query. |
| Dive! | Runs the query | **done** | Enter in Search and the Refresh button. Most controls also run the query when changed. |

## Sidebar tabs

| Scuba control | What it does | loghorn | Notes |
|---|---|---|---|
| View Settings | The query controls | **done** | The sidebar. |
| Fields | Lists the dataset's columns with types and descriptions | **partial** | The Schema… dialog shows path, inferred type, type, unit, visualizer, label, hidden and count. It's a dialog, not a sidebar tab. |
| Discuss | Comments attached to the dataset | **n/a** | Single-user, local tool. |

## View settings

The sidebar uses Scuba's layout: labels right-aligned in a fixed column, so every box starts at the same x.

| Scuba control | What it does | loghorn | Notes |
|---|---|---|---|
| Start | Start of the time range (relative or absolute) | **done** | From: this run / 15m / 1h / 24h / all / custom. |
| End | End of the time range | **done** | To: now / custom. |
| Compare | Overlays the same query shifted back in time (e.g. −7 days) | **todo** | Time-shift comparison series on the graph and a delta column in the table. For logs, "previous run" is probably a more useful shift than a fixed duration. |
| Min Samples | Hides groups with fewer than N rows | **todo** | Server-side `HAVING count >= N` in the table and graph. |
| Group By | Group-by fields | **done** | Several fields allowed (Table and Graph). |
| Order By | Sort groups by a metric | **done** | Table and Graph: count or any metric, ↓ desc / ↑ asc. Groups with no value sort last. Ranks groups before the limit and picks the graph's top 10. (Samples still sorts by clicking a column.) |
| Aggregate | Avg / Sum / Min / Max / percentiles | **done** | count, sum, avg, min, max, p50, p95, p99. Possible additions: count-distinct, p90, p99.9, stddev. |
| Limit | Number of rows or groups | **done** | **Top** (5 … 1000, default 10) for Table and Graph, ranked by Order by. The graph draws at most 50 groups and folds the rest into "other". The footer says when groups were cut. Samples still has no row-limit control. |
| Axes (Linked / Separate) | Whether several metrics share a y-axis | **todo** | The graph plots one metric (`plot`). Plotting several metrics needs a choice between shared and separate axes. |
| Granularity | Time bucket size | **done** | auto, 1s … 1d. |
| Opsfeed Overlay | Draws event markers (deploys, incidents) on the graph | **todo** | For loghorn this could mark child restarts, run boundaries, or records matching a saved filter (e.g. panics). |

## Filters

| Scuba control | What it does | loghorn | Notes |
|---|---|---|---|
| Filter rows: field · op · values | AND of typed predicates | **done** | Inline triples with ops chosen by type: `= != < <= > >=`, `in`, `not-in`, `contains`, `empty`, `not-empty`. Each triple is checked as you edit it. |
| Multi-value tokens (`/home.php ×`) | Several values per filter, shown as removable chips | **done** | `in` / `not-in` values. |
| + New Filter | Adds a row | **done** | |
| Field autocomplete | Suggests field names | **done** | `field-paths` datalist. |
| Value suggestions | Suggests common values for the field | **todo** | Top-N distinct values from the store, ranked by count. |
| OR across filters | Scuba ANDs its filters; OR goes inside `in` | **todo** | Only if needed. The ROADMAP marks cross-field OR as a deliberate decision. |
| Regex op | | **todo** | Not in Scuba's basic sidebar, but useful for logs. |

## Comparison filters

| Scuba control | What it does | loghorn | Notes |
|---|---|---|---|
| Comparison Filters | A second filter set drawn against the first (A vs B) | **todo** | E.g. `status >= 500` vs everything, or one route vs another. Shares the series/delta plumbing with Compare. |
| Remove Comparison | Drops the B side | **todo** | Comes with the item above. |

## Trace waterfall

A trace shown as a tree of timed bars. Design and the deferred list (flame graph, span links, auto-refresh and the rest) are in `docs/superpowers/specs/2026-11-21-trace-waterfall-design.md`.

- **Opening.** Click a `trace_id` or `span_id` cell in the table, or visit a deep link: `/?q=<json>&trace=<id>&span=<id>`. `trace` alone opens the trace; `span` alone finds the trace that holds that span, and selects it. Both spellings of a Cloud Trace ID (`projects/<p>/traces/<id>`) and upper-case hex are accepted. Close (or Esc) goes back to the table.
- **Tree.** Spans, their span events, the log lines that carry the trace ID, and a placeholder for each parent that is not in the data. Bars are coloured by `span.type` (db, http, rpc, messaging, gen_ai, other). Click a row for its record below the grid.
- **Span events.** Each event is a ◆ row under its span, in time order with the span's logs, and a tick on the span's bar. An `exception` event is red and shows `exception.type: exception.message`. Click one for its time, span and attributes. In the table, `span.event_count` gives each span's number of events.
- **Logs & events toggle.** `Logs & events: in tree` / `Logs & events: hidden`; hides log and event rows (the bar ticks stay); remembered in the browser.
- **Find.** Matches span names and, with logs and events shown, log messages, event names and exception messages; Enter and Shift+Enter step through them.
- **Serial runs.** Three or more sibling database queries that ran one after another get a chip, `6 serial queries, 48 ms`, and a faint band over their rows.
- **Critical path.** The `Critical path` button dims the bars that are not on it. Off by default.
- **Self time.** The table below the grid lists each span name with its count, total time, self time (time not spent in a child) and self time as a share of the trace. Click a header to sort, again to reverse; click a name to find it in the tree.

## Aggregate operation view

Every trace of one operation merged into one tree. Design and the deferred list (flame graph, trace diff, keyboard navigation, entry from grouped views and the rest) are in `docs/superpowers/specs/2026-11-21-trace-aggregate-design.md`.

- **Opening.** In Spans mode, `∑` after the name of a root span in the table; `Aggregate this operation` in the waterfall; or a deep link `/?q=<json>&agg=<name>&aggsvc=<service>`. Close (or Esc) goes back to where you came from.
- **Range and cap.** Aggregates the page's range, the 1000 most recent traces. The header says `N of M traces (capped, most recent)` when it cuts.
- **Rows.** One row per span name at each path below the root: traces holding it, calls per trace, p50, p95, total and self time as shares of the operation, errors, and a bar.
- **Slowest.** `slowest <duration>` opens the slowest trace holding that span, with the span selected; Back returns to the aggregate.
- **Serial chip.** `2.3 serial queries/trace, 15.0%` on a span whose database children run one after another: queries per trace, and their share of the operation's time.
- **Outside spans.** Spans of a trace that are not under the operation root (missing parents) are counted in the header, never merged.

## Beyond the screenshot

These loghorn features have no counterpart in the picture. They're here so the list is complete.

| Feature | loghorn | Notes |
|---|---|---|
| Free-text search across all fields | **done** | The Search box. |
| Expand a row to see the raw record | **done** | Click a Samples row (`/api/record`). |
| Column add, move, A→Z, width, crop/wrap; saved to the schema | **done** | |
| Visualizers (links, durations, timestamps, bad-type flags) | **done** | |
| Schema editor (type, unit, label, hidden) | **done** | |
| Loopback/Host guard | **done** | A token for `-web` is proposed and still open. |
| Live auto-refresh while the child runs | **todo** | Refresh is manual today. |

---

## Suggested next

Ordered by value for the effort. Reorder freely.

1. **Min Samples.** Small and server-side; makes Table and Graph easier to read. (Top is done.)
2. **Reset + copy link.** UI-only.
3. **Export (CSV / JSONL)** of the current result.
4. **Value suggestions** in filter triples.
5. **Compare (time-shift / previous run)** and then **Comparison filters**. These are the biggest pieces: they need a second result set and a graph that can show two of them.
6. **Event overlay** (child restarts, matched failures) on the graph.
7. **More views:** distribution/histogram (stacked area is done).
