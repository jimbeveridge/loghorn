# loghorn re-architecture — working notes

Date: 2026-11-19 (discussion 2026-11-18 → 19)
Status: **working notes, not a spec.** Records the decisions made so far, the
proposals still awaiting a decision, and the open questions and
investigations. A spec (or several) will be written from this once the
storage-engine spike reports and the open questions below are answered.

Supersedes parts of `2026-11-18-sql-query-design.md` (see "Related documents").

## How to read this

Each decision is tagged:

- **[user]** — the user decided or explicitly agreed.
- **[proposed]** — Claude proposed it; the user has not confirmed or rejected
  it. Treat as the current default, open to change.
- **[superseded]** — decided earlier, since overtaken; kept so nobody revives
  it by accident.

## Where this started

The original request: convert SQL queries to and from the web view's URL so
an LLM can work with the query, support `DESCRIBE table` and
`SHOW CREATE DATABASE db` (which needs field types), render samples and
tables to stdout from the CLI, and decide for an incoming SQL URL whether the
UI controls can show it or raw SQL must run. Embedding
`dolthub/go-mysql-server` was suggested so LLMs aren't confused by a small
SQL subset.

The discussion grew into a re-architecture of loghorn: drop the TUI, add a
long-running `loghorn serve` that receives and stores logs, and make SQL the
LLM's interface to it.

## Audience and goals

- **Primary audience [user]:** a developer tracking what happens on their own
  desktop — local test instances of frontend, backend and workers, and backend
  tests, especially integration and E2E.
- **Capture everything an LLM starts [user]:** every time an LLM starts a
  backend or frontend, its logs are captured.
- **LLM investigation [user]:** an LLM uses a query as the *starting point of
  a distributed-systems incident investigation*, then runs its own follow-up
  queries. Its job is not to hand back a URL.
- **People [user]:** use the web explorer. Its speed matters (see the 1.1×
  bar below).
- **Scale [user]:** the only reason to "scale" is very large local logs. One
  integration-test run of the app under test produces ~0.5–1 GB of logs.

## Decisions

### Architecture

1. **Drop the TUI [user].** "A bit of a failed experiment." It is ~7,100 of
   ~18,000 non-blank Go lines (tests included) in `internal/tui`.
2. **cgo is allowed [user].** Lifts the earlier "no cgo" constraint, which
   ruled out DuckDB and chDB.
3. **Stay in Go [proposed].** Embedded engines (DuckDB, chDB) are native
   libraries reached by FFI from any language, so switching languages buys
   nothing; ingest, adapters, query engine, schema inference and the web
   server (~6,000 non-test lines) carry over. The user raised switching
   languages for embedded ClickHouse; this was Claude's reply, not confirmed.
4. **`loghorn serve` [proposed]:** one long-running process that receives
   all input, is the database's single writer, expires data, runs scheduled
   pulls, evaluates alerts, and serves an HTTP API to the web explorer and the
   CLI. Embedded engines (DuckDB, chDB) allow only one writing process, which
   is why clients go through `serve`.

   ```
   apps ──OTLP──▶ ┐
   -- <cmd> ─────▶ ├─ loghorn serve ── embedded engine ── runs, expiry, sources
   imports ──────▶ ┘        │
                            ├── HTTP ── web explorer (people)
                            └── HTTP ── loghorn -sql / -url / DESCRIBE (LLMs)
   ```

5. **[superseded by D-030: one `records` table in one DuckDB file; no separate modes] Easy mode vs scaling mode [user idea, reframed].** The user proposed:
   - *easy mode*: loghorn is the OTEL receiver, stores into SQLite on the fly,
     expires data; SQL goes to SQLite; a few hundred MB.
   - *scaling mode*: OTEL Collector → ClickHouse (Docker or another machine);
     SQL goes to ClickHouse.

   Refined in discussion:
   - **[proposed]** loghorn stays the receiver in both modes (no Collector
     needed); only storage differs. Apps never change when switching.
   - **[proposed]** loghorn owns its storage schema in both (typed columns per
     stream), rather than the ClickHouse exporter's generic `otel_logs` table
     with attribute maps. (Earlier, before the audience was clarified, Claude
     recommended matching the exporter schema so loghorn could sit on top of
     ClickStack/SigNoz; that argument no longer applies — see superseded.)
   - **[user] DuckDB is the only engine; there is no separate scaling
     mode.** Spike 2 showed one embedded DuckDB handles data at the scale of the app under test
     (1–17 ms for browser and investigation queries on a ~1 GB, two-run
     dataset) and spike 3 showed the hot + Parquet layout keeps that. Costs
     accepted: ~+58 MB binary, cgo builds per platform. The SQL dialect is
     DuckDB's (close to Postgres); loghorn adds `SHOW`/`DESCRIBE` forms and
     helper functions where DuckDB lacks them.

6. **Runs are first-class [proposed].** Every record carries a run id (a test
   run, a dev session, an imported GitHub job). Queries usually target one
   run (`run = 'latest'`, `SHOW RUNS`). Replaces the old "this run = process
   start" notion. Retention for runs is by count per suite plus a size cap,
   not by days (1 GB/run × 4 days of runs is too much).

### Capture (sources)

7. **Three kinds of source [proposed]:**

   | kind | examples | ends? | how |
   |---|---|---|---|
   | push | OTLP, `loghorn -- <cmd>` | while the app runs | apps send to `serve` |
   | import | GitHub Actions job/run, a file | yes | pulled once |
   | follow | gcloud; later kubectl, AWS | never | `serve` keeps pulling from a saved position |

8. **OTLP receiver [user direction, proposed detail].** Repos' dev/test
   config sets `OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:<port>`, so
   apps report to loghorn however they were started (by an LLM, a person, a
   test harness). Exporters already tolerate the receiver being down; loghorn
   should report gaps (e.g. "nothing from `app-backend` in the last run").
9. **`loghorn -- <cmd>` becomes a transparent wrapper [proposed]** for apps
   that only write to stdout (e.g. pino): stdout/stderr pass through
   unchanged, exit code kept, output captured on the side. An LLM sees normal
   output. A Claude Code `PreToolUse` hook could wrap dev/test commands
   automatically so capture doesn't depend on the LLM remembering [proposed].

   **The wrapper ships to `serve` as OTLP [user asked; detail proposed].**
   `loghorn -- <cmd>` becomes a small OTLP client:
   1. runs the child with `internal/runner`;
   2. passes stdout/stderr through unchanged and keeps the exit code;
   3. parses each line with the existing adapters and normaliser (pino,
      LogEntry, YAML, plain text) into canonical records;
   4. converts each to an OTEL log record: timestamp, severity, the raw line
      as body, parsed fields as attributes, `service.name` and run id as
      resource attributes;
   5. sends batches to `serve` over OTLP/HTTP, so `serve` has one way in and
      the wrapper could target any OTLP endpoint.

   Needed changes and decisions:
   - **`service.name`:** a flag (`-service app-backend`), a field in the
     line (pino `name`, Cloud Logging `serviceContext`), or repo name +
     command as a fallback. Run id: open question 4.
   - **Runner:** today it uses a pty (merges stdout/stderr; the child thinks
     it's on a terminal and emits colour codes, which must be stripped) and
     its own process group (`Setpgid`, so terminal Ctrl-C doesn't reach the
     child). The wrapper uses plain pipes when its own stdout isn't a
     terminal (e.g. run by an LLM) and a pty when it is; it forwards signals
     or leaves the child in the terminal's process group.
   - **`serve` down:** never block or fail the child. A bounded buffer that
     drops when full, a stderr warning ("loghorn serve unreachable; logs not
     recorded"), and optionally a local spill file `serve` imports later.
   - **Unstructured lines** (stack traces, npm output) become body-only
     records with inferred severity and inherited effective time, as the
     normaliser does today.
   - New code: OTLP encoding (OTEL Go SDK or protobuf types), pass-through,
     signal forwarding, buffering. The rest is reuse.
10. **GitHub Actions import [user].** Logs are written once over a short
    period; import once. The existing `internal/ghjob` package becomes the
    importer; a job becomes a run (run id, job, attempt as attributes)
    [proposed].
11. **gcloud follow source [user].** Logs never end, so `serve` pulls on a
    schedule:
    - **[user]** Poll hourly; poll every minute while the user is querying
      that source's data — but **only queries whose window reaches "now"**
      speed it up. Queries over old data don't.
    - **[user]** When loghorn can't tell whether a query reaches now, inspect
      the max timestamp of the result.
    - **[proposed]** How "reaches now" is decided, in order:
      1. Browser queries: `range.to` is `now`/empty.
      2. CLI SQL: the `_time` bound pushed to a follow-source table scan,
         read from the engine's query plan; no upper bound, or one at/after
         the source's newest record, reaches now. A `-now` pinned in the past
         doesn't.
      3. Else the result's max `_time`/bucket, compared with the source's
         newest stored record (not the clock — a lagging source's newest data
         is an hour old).
      4. Else (no time column, or an empty result — e.g. "any errors in the
         last 5 min?" → 0 rows) assume it reaches now.
    - **[proposed]** Active means a qualifying query in the last ~15 min, per
      source. The first qualifying query after an idle period triggers an
      immediate pull but doesn't wait for it; its footer shows the lag
      (`gcloud:prod current to 07:41:10 (55m behind; refresh started)`).
      `-fresh` makes the CLI wait.
    - **[proposed]** Each pull re-reads an overlap window (Cloud Logging
      entries become readable late) and dedupes on `insertId`. After sleep or
      a `serve` restart, catch up from the saved position in pages, bounded by
      retention. Intervals are per-source settings. A follow source requires
      a filter (resource type, service, min severity) and may have a volume
      cap. Failing pulls (expired gcloud credentials) are visible, never
      silent. Reuses the existing gcloud input config and YAML adapter.
12. **Generic sources [proposed]:** a *poll command* run on a schedule with
    the saved position substituted (`aws logs filter-log-events
    --start-time {cursor}`) and a *follow command* supervised and restarted
    from the saved position (`kubectl logs -f --since-time {cursor}`). Covers
    most tools without per-provider code.
13. **Idempotent ingest [proposed]:** every source has a natural key
    (`insertId` for gcloud; job id + line number for GitHub), so re-importing
    is a no-op.
14. **Freshness is visible [proposed]:** `SHOW SOURCES` (kind, newest record,
    last successful pull, last error) and a footer line on any query that
    touches a follow source and reaches past its newest data. Without it,
    "no errors in the last 5 minutes" could mean the pull is broken.
15. **Source config [proposed]:** TOML, `.config/loghorn/sources.toml`, with
    CLI commands to add, list and pause sources.

### Storage, naming and schemas

16. **Database = `service.name` [user]** ("perfect"); **table = stream**
    (`labels.schema`) [proposed]. Records without `service.name` go to a
    default database. Cross-service joins are plain SQL
    (`checkout.http_request JOIN payments.http_request USING (trace_id)`).
    `SHOW CREATE DATABASE <service>` returns every table of that service.
17. **A cross-stream/cross-service table [proposed]** of the fields every
    record has plus OTEL's standard ones (`_time`, run, service, stream,
    severity, message, `trace_id`, `span_id`, `_raw`) for "what happened
    around 11:41?" and "everything for this trace". Named `all_records` in the
    SQL spec, `otel.logs` earlier; name open.
18. **Saved schemas are per service [user]** ("perfect"), not per repo. A
    repo can still declare schemas for the services it owns. Where they live
    once data is central is open.
19. **Explicit schema evolution [user]** (Meta experience: type conflicts over
    time forced well-defined schemas with explicit evolution). Detail
    [proposed]: inference for undeclared fields; a saved schema is a contract;
    violating data is still stored (never drop logs) and flagged; allowed
    changes are add field, widen type (int→float, anything→string), retire
    field; narrowing is explicit. Deferred to the central-store work; the SQL
    interface only needs `DESCRIBE` to report declared vs inferred types and
    violation counts.
20. **Central store [user direction]:** "moving towards a single directory on
    the machine where all OTEL data is written"; may replace per-repo
    `.loghorn/` directories. Problem raised by the user: different apps may
    have different schemas for same-named streams — answered by decision 16.
21. **Never hide data [carried over]:** a value that doesn't parse as its
    column's type is NULL in that column (so `status >= 500` never matches
    text), counted by `DESCRIBE`, and kept in `_raw`.

39. **Hot DuckDB + daily Parquet archive [user].** Today's data lives in a
    DuckDB file; each day is converted to Parquet (several small days may be
    merged into one file). A view per stream unions the hot table with
    `read_parquet(..., union_by_name = true)`. **[user]** No special handling
    for late data in v1: at 02:00, `serve` makes sure imports have finished,
    then converts. **[proposed]** Details: a fresh hot file per day, so no
    DuckDB file outlives a day (DuckDB files don't shrink after DELETE);
    one Parquet file per stream per day in
    `archive/service=…/stream=…/day=…/`, sorted by `_time`, zstd; the
    conversion archives every row in the hot file by its own day, so a row
    that arrives late lands as an extra file in its day's folder with no
    special code; write to a temp name, rename, then delete the source (safe
    to redo thanks to natural keys); a missed 02:00 (laptop asleep) runs at
    the next wake; expiry deletes day folders, so retention is by day, not by
    run. Measured by spike 3 (fast enough). **[proposed, from spike 3]**
    The hot branch of each view adds `CAST(_time AS DATE) AS day`; loghorn
    adds a `day BETWEEN` predicate derived from every query's time range (a
    `_time` filter alone still opens every file); conversion writes the
    canonical schema types (a type that differs between days otherwise
    becomes VARCHAR and breaks aggregates); keep DuckDB's default row-group
    size; force zstd on long-text columns in the hot file too; instead of
    merging tiny days into one day folder (which breaks `day` pruning), roll
    old or small data up into `month=` partitions, since the number of
    folders is what grows per-query cost.

40. **Remote ClickHouse, executed by ClickHouse [user, future].** A later
    feature: loghorn can query data held in a ClickHouse server, and
    ClickHouse runs the whole query (no copying data into DuckDB).
    **[proposed]** Shape:
    - A configured ClickHouse connection appears as a database (e.g.
      `ch_staging`). A query whose tables all belong to it is sent to
      ClickHouse unchanged over its HTTP interface; everything else runs in
      DuckDB. DuckDB lists a query's tables without running it, so routing
      needs no SQL parser of loghorn's own.
    - The LLM writes ClickHouse SQL for those databases; `SHOW DATABASES`
      and `-sql-help` state each database's dialect. `SHOW`/`DESCRIBE` map
      to ClickHouse's own `DESCRIBE TABLE`, `SHOW CREATE TABLE` and
      `system.columns`.
    - For the browser on remote data, `Query` → SQL needs a ClickHouse
      dialect (`toStartOfInterval`, `quantileExactHigh(0.95)`,
      `positionCaseInsensitive`, backtick quoting); spike 2 already wrote and
      verified those forms. A per-connection mapping turns field paths into
      column expressions when the remote schema differs (e.g. the OTEL
      exporter's `LogAttributes['http.status']`).
    - Queries mixing local and remote tables are rejected in v1 with a clear
      message (cross-engine joins need a query planner). Data that must be
      joined locally comes in through a poll source instead (decision 12:
      `clickhouse client --query "… WHERE ts > {cursor} FORMAT
      JSONEachRow"`).
    - Rejected alternative: DuckDB pulling a remote result via
      `read_parquet('http://…?query=…FORMAT Parquet')`, because DuckDB can't
      push filters into the remote query.
    - **Now [proposed]:** the SQL generator, helper functions and type
      mapping sit behind a per-dialect interface with DuckDB as the only
      implementation, so adding ClickHouse later is a second dialect, a
      router and a connection config — not a restructuring of `serve`. The
      Parquet archive is engine-neutral (ClickHouse reads Parquet).

41. **Interim `-otel` on the subprocess path [user].** Before `serve`
    exists, `loghorn -otel -- <cmd>` binds an OTLP/HTTP receiver, sets the
    child's OTEL environment (logs endpoint, protocol, and
    `loghorn.run.id` in `OTEL_RESOURCE_ATTRIBUTES`), runs the child on the
    pty as today, and stores OTLP log records as JSON lines in `.loghorn/`
    [user: keep `.loghorn/` for the first pass]. JSON stdout lines aren't
    stored once OTLP records arrive; non-JSON stdout still is [user]. Stream
    = `event.name` → instrumentation scope → `otel` [Claude's call, per OTEL
    practice; user delegated]. Spec:
    `2026-11-19-otel-receiver-design.md`, which also removes the TUI
    [user] and makes `loghorn -- <cmd>` a transparent wrapper. Motivation:
    the app under test is adding OTEL now.

### SQL interface (CLI, for LLMs)

22. **The CLI is the LLM's main surface [user].** People copy the browser URL
    and paste it into the LLM's terminal, so the CLI accepts URLs.
23. **Flags [proposed, from the SQL spec]:** `-sql '<SQL>'`, `-sql-file`,
    `-url '<URL>'` (`?q=<json>` or `?sql=`), `-to-sql '<URL>'` (name [user]; print
    the SQL, run nothing — **the LLM can ask for a query's SQL [user]**),
    `-now <RFC 3339>`, `-format text|json|tsv|csv`, `-max-rows` (default 200),
    `-max-cell` (default 200 chars), `-sql-help` (a one-page guide for LLMs),
    `-fresh`. With `serve`, these become calls to its HTTP API.
24. **Arbitrary SQL runs in the CLI only [user]** ("an okay simplification
    for now"; arbitrary SQL in the browser raises questions such as a query
    with no time condition).
25. **Time [user]:** relative values at the start of an investigation
    (`1h`, latest run), held fixed while it is in progress. **[proposed]**
    `-now` pins "now"; the CLI prints the "now" it used so the LLM can pin it.
    Relative ranges stay relative in emitted SQL (`_time >= ago('1h')`).
26. **No time condition = all stored data [proposed]**, as SQL would; the
    footer says so ("no time condition: covers all stored data, X .. Y"). No
    hidden default window.
27. **Output [proposed]:** cheap in tokens and explicit about what was cut:
    row cap, cell cap with `…[+N chars]`, a footer with shown/total rows, the
    time span covered, the "now" used, and source freshness. Errors suggest
    the fix (`unknown column "http.status"; did you mean
    "httpRequest.status"?`).
28. **Discovery [user asked; detail proposed]:** `SHOW DATABASES`,
    `SHOW TABLES` (rows, time span), `DESCRIBE` (types, unit,
    declared/inferred, null rate, distinct count, top values, violations),
    `SHOW CREATE TABLE`, `SHOW CREATE DATABASE`, plus `SHOW RUNS` and
    `SHOW SOURCES`. loghorn answers these itself if the engine lacks them.
29. **Helper functions [proposed]:** `lh_now()`, `ago(d)`, `bucket(t, size)`,
    `search(text, s)`, `percentile`/`p50`/`p95`/`p99` (matching the web view
    exactly), `duration(s)`; `this_run_start()` likely becomes run-based.
    Implemented in whichever engine is chosen.
30. **No reading SQL from stdin [proposed]:** loghorn records piped stdin to
    the project log, so long queries use `-sql-file`.
31. **Read-only SQL [proposed]:** user SQL can't write, attach or load
    extensions.

### Browser

32. **SQL in the browser is only converted, never executed [user].** A
    `?sql=` URL is converted to the web view's `Query`; if it converts, the
    controls show it; if not, the browser shows an error. The browser keeps
    writing `?q=` URLs [proposed].
33. **Browser latency bar [user]:** originally ≤ ~1.1× of today's native
    engine (2× too slow). With native retired (decision 35) the bar becomes
    absolute; spike 3 measured < 30 ms for the browser views over 7 days of
    data at the scale of the app under test [proposed target: keep them under ~50 ms].
34. **One converter in Go [proposed]:** `Query` ↔ SQL. Display settings SQL
    can't carry go in a trailing `-- loghorn: view=graph chart=stacked
    bucket=auto` comment. Converting SQL → `Query` accepts the shapes the
    converter emits plus natural edits (reordered or added conditions, case,
    quoting); anything else is rejected naming the blocker. Round-trip test:
    Query → SQL → Query is identity, and (while `query.Run` still exists to
    compare against) DuckDB results equal native results.
35. **Browser queries run through DuckDB; `query.Run` is retired [user].**
    The browser's `Query` is converted to SQL and executed by DuckDB via
    `serve`: one set of semantics for people and LLMs. Native was both slower
    at the app's scale (table 716 ms, graph 520 ms vs DuckDB 5 ms) and held
    ~3 GB of RAM per run. `?q=` URLs and favorites keep working because
    `Query` remains the browser's model.
36a. **Full-text search speed is acceptable [user]:** ~100–300 ms over 1–7
    days in every layout; no index or special plan in v1.
36b. **Table ↔ trace navigation [user, 2026-11-20]:** the goal is moving
    easily from a table of "all spans of a type" (a span stream such as
    `db` or `http.server`) to the trace hierarchy one row belongs to, shown
    as a tree and/or a flame graph, and back to the table. A trace view
    needs *every* span of the trace, across streams, joined by `trace_id`
    and `parent_span_id`. That is a cross-stream fetch by `trace_id`, which
    could be its own lookup. Whether general queries (e.g. table mode) also
    need to span streams is **open**, not decided. The record path must stop
    depending on `labels.schema`: today log lines with neither
    `labels.schema` nor `httpRequest` are dropped. Span streams
    (`2026-11-20-span-streams-design.md`) are the interim step: every span
    gets a stream (catch-all `span`), and `trace_id`, `span_id` and
    `parent_span_id` are kept on every span record. Requires a major
    query-engine update; not started.
36c. **The stream list adapts to the interval [user, 2026-11-21].** The
    interval-aware list below replaces the fixed per-stream list, which
    had two problems:
    - Most kinds of span had no stream of their own. Sweep, token refresh,
      boot, timer and similar internal spans all fell into the catch-all
      `span`.
    - Its counts covered the whole store, not the selected interval.

    The design:
    - **What the list shows:** the top 10 kinds of span in the selected
      interval. A kind is the attribute classification (`db`,
      `http.server`, …); for spans the classification leaves unclassified,
      it is the span `name`. Each row shows its count in the interval.
    - **More:** the rest are one click away, with a type-to-filter box.
    - **Pins:** pinned kinds always show first. A pinned kind with nothing
      in the interval still shows, greyed with `0`.
    - **Values within a kind:** the list never lists the values inside one
      kind, such as each database table. That is the job of Filters or
      a group-by in Table view.
    - **Clicking the label** chooses what the list is grouped by. Only
      low-cardinality fields are offered.
    - **Columns:** they come from a saved query (a favorite) when there is
      one. Otherwise they are inferred from the records that match in the
      interval.
    - **Formatting:** saved schemas keep only formatting (field types and
      how each is shown).
36d. **Config is keyed by service, not by repo [user, 2026-11-21].**
    loghorn normally runs globally, so a repo's `.config/loghorn/` is not
    a meaningful home for pins, favorites or formatting.
    - **The key:** `service.namespace` when it is set, else `service.name`.
    - **Location:** user-level `~/.config/loghorn/services/<key>/`, with
      the key escaped for the filesystem.
    - **Records with no service** (child stdout, piped input) get their key
      from the child's `OTEL_SERVICE_NAME`, or from the GCP resource labels
      for GCP logs, or fall into a `default` bucket [proposed].
    - **Open:** whether the repo `.config/loghorn/` stays as a shared,
      checked-in override; whether logs and spans share one list or the
      page switches between them; what pins show when several services
      are in the interval.

### Alerts and notifications

36. **Alerts move out of the TUI into the web app [user]**, which asks the
    browser for permission to show desktop notifications.
37. **Detail [proposed]:** `serve` pushes alert events to open pages (SSE);
    pages show clickable notifications that open the explorer on the
    offending query. Notifications only work while a loghorn tab is open (Web
    Push would route through the browser vendor's servers), so when no page is
    connected `serve` sends a native notification via the existing `beeep`
    code (`internal/alert`) with the URL in the text. Each alert goes out
    once. Alerts become saved queries plus a condition ("count > 0 in the
    latest `app-backend` run"), evaluated by `serve` as data arrives.

### Fate of other existing features

38. **[proposed]** `-gh-job`/`-gh-run` viewers → importers (decision 10);
    `-historical` → the database replaces it; headless `-filter` → the
    transparent wrapper; SQL formatting in the Detail pane (`internal/sqlfmt`)
    → possibly the web view's expanded row; `-scrollback` and TUI keys go.

## Superseded or rejected

- **go-mysql-server [superseded]:** rejected by spike 1 (57 MB, 165 modules,
  289 s to load 500k rows into its tables, 248 s REGEXP, no percentile, wrong
  bucket counts in one form).
- **Pure-Go-only engines [superseded]:** cgo is now allowed.
- **SQLite as the engine, and an easy/scaling split with ClickHouse or chDB
  [rejected]:** SQLite missed the browser bar at the app's scale (table 1.27×,
  graph 2.2× native); ClickHouse/chDB were as fast as DuckDB but need a
  180–360 MB runtime and awkward ingest. DuckDB alone covers the workload.
- **The native engine `query.Run` [superseded]:** retired in favour of
  DuckDB (decision 35).
- **SQL cache file `.loghorn/sql-cache.db` rebuilt from `.loghorn/` text
  files [superseded]:** with `serve` storing records as they arrive, there is
  nothing to copy.
- **CLI reads the current directory's `.loghorn/` [superseded]:** the CLI
  talks to `serve`.
- **Run-start marker in `.loghorn/` for `this-run` [superseded]:** runs are
  first-class.
- **Matching the ClickHouse exporter schema so loghorn can sit on top of
  SigNoz/ClickStack [superseded]:** loghorn's audience is a single developer
  desktop; it is the receiver and owns its schema. (Facts kept for
  reference: ClickStack uses the standard exporter's `otel_logs`; SigNoz uses
  its own schema; reading someone else's ClickHouse may be read-only, so
  helpers would have to expand client-side.)
- **A raw-SQL results view or auto-graph in the browser [superseded]** by
  decision 32.
- **A graph in CLI output [rejected]:** LLMs read a time-series table more
  accurately than a rendered chart; graphs are for people in the browser.

## Investigations

### Spike 1 — SQL engines on synthetic data (done)

Branch `worktree-agent-a566f5b8576d37f38` (worktree
`.claude/worktrees/agent-a566f5b8576d37f38`), package `internal/sqlspike/`,
commit b9d73a8. Synthetic HTTP-style records from two services; Apple M5 Pro.

| query, 500k records | native | SQLite | SQLite + indexes | GMS in place | GMS own tables |
|---|---|---|---|---|---|
| samples | 26 ms | 21 (0.8×) | 5.7 (0.22×) | 127 (4.8×) | 26 (1.0×) |
| table | 93 ms | 187 (2.0×) | 118 (1.3×) | 122 (1.3×) | 91 (0.98×) |
| graph | 103 ms | 117 (1.14×) | 215 (2.1×) | 271 (2.6×) | 240 (2.3×) |

- All engines agreed with native on the three UI queries (SQLite's built-in
  p95 differed up to ~6%; a window-function rewrite matched exactly).
- SQLite ran join, before/after, `COUNT(DISTINCT)`, window, JSON and REGEXP
  queries in 15–450 ms at 500k. Load: 0.9 s / 500k pre-parsed records (2.0 s
  with indexes). +5.2 MB binary; needs `go 1.26`.
- The 0.9 s excluded parsing log files; parse cost was unmeasured.

### Spike 2 — storage engines on real app logs (done)

Branch `worktree-agent-a19de797595a4473b` (worktree
`.claude/worktrees/agent-a19de797595a4473b`), package
`internal/enginespike/`, commit 53acc4c (merges spike 1). Rerun commands in
`internal/enginespike/run.sh` and the spike report; downloads (ClickHouse
binary, libchdb.so) and the 14 GB of test databases are in this session's
scratch directory and will not survive a cleanup.

**Data.** `(a local scratch directory outside the repo)`
(read-only, outside the repo; never pipe it into `loghorn`):
`integration-full-info.log` (564 MB, 773k lines, one full
integration run at info level) and `integration-full.log` (235 KB, same run
at default level). Locally written lines are flat pino JSON with the
stream in `logging.googleapis.com/labels.schema`; in Cloud Logging the same
fields arrive under `jsonPayload` with well-known ones promoted. loghorn's
normaliser maps both shapes to the same canonical fields, so the results
apply to either source. 772,654
records in six streams (db 722k, api 36k, server 12k, email, worker, auth).
**The whole run spans 103 seconds**, so graph queries used 1-second buckets.
Run B = run A shifted +2 h with ids suffixed; 1.55M rows, ~1.13 GB raw.
One table per stream with typed columns from `schema.Infer` (db 19 cols,
api 59, server 149) plus `run`, `_seq`, `_time`, `_raw`.

**Ingest parse cost** (run A via `adapter.ParseLine` → `record.Normalize` →
`store.Add`): 8.7 s single-threaded (64 MB/s); 1.4 s with parsing split over
18 workers and an in-order `store.Add`. Native in-memory records cost ~4 KB
each: 3 GB heap per run, 6.1 GB for two.

**Query p50, ms, run B** (native is single-threaded; DuckDB uses all cores
unless noted):

| | Q1 samples | Q2 table | Q3 graph | Q4 join | Q5 A vs B | Q6 distinct | Q7 window | Q8 text |
|---|---|---|---|---|---|---|---|---|
| native | 27–139 | 716 | 520 | – | – | – | – | 437 |
| SQLite | 0.1–11 | 913 (1.27×) | 1143 (2.2×) | 42 | 73 | 681 | 17 | 717 |
| DuckDB | 1.1–1.2 | 4.8 | 5.1 | 4.9 | 2.2 | 3.8 | 1.2 | 80 |
| DuckDB, 1 thread | 0.7–1.1 | 16.9 | 13.1 | 4.4 | 1.7 | 10.8 | 1.0 | 415 |
| chDB, bulk | 2.5–9.2 | 8.0 | 9.6 | 9.0 | 3.3 | 7.7 | 1.6 | 52 |
| ClickHouse, bulk | 2.7–5.7 | 7.5 | 9.3 | 8.8 | 3.0 | 7.9 | 1.6 | 43 |

All engines agreed with native on Q1–Q3 and Q8 (same rows, groups, counts,
averages, exact p95). Q4–Q7 spot-checked DuckDB against ClickHouse.

| | ingest, 1k batches | bulk | disk, 2 runs | binary |
|---|---|---|---|---|
| SQLite | 78–200k rows/s | 139k | 2.1 GB | +5.2 MB |
| DuckDB | 75k (worst batch 503 ms) | 360k | 2.0–2.4 GB; ~0.23 GB with zstd | +57.8 MB |
| chDB | 45k | 127k | 0.20 GB | +358 MB runtime library |
| ClickHouse server | 4.5k sync, 48k async | 213k | 0.10–0.20 GB | +7.8 MB client, 184 MB server, ~475 MB RSS |

Gotchas: DuckDB stores long strings (`_raw`, SQL text) uncompressed unless
told (zstd on those columns, measured once: ~10× smaller disk, Q2 8→13 ms);
DuckDB allows one process to open the file, so all clients go through
`serve`; DuckDB appender flushes/checkpoints stalled up to 0.5 s and must
stay off the request path; SQLite's (run,_time) index turns scans into
random lookups (why Q2/Q3 lose to native); chDB panics at init if its
library is missing; ClickHouse needs async inserts or buffering; dotted
column names need quoting in every dialect (consider friendly-name views).
Load, disk and parse numbers are single measurements on a busy 18-core,
48 GB laptop.

**Spike recommendation:** DuckDB for `serve`, one engine (no scaling mode),
and the browser runs through it, retiring `query.Run` (native is now both
slower and ~3 GB of RAM per run). Needs zstd on long-text columns and
background flushing. Not yet accepted by the user.

### Spike 3 — hot DuckDB + daily Parquet archive (done)

Branch `worktree-agent-a2442f6d37a0e29bb` (worktree
`.claude/worktrees/agent-a2442f6d37a0e29bb`), package
`internal/enginespike/` (`multiday.go`, `cmd/multiday`, `multiday.sh`),
commit 93129e9. Rerun: `sh internal/enginespike/multiday.sh <scratch> &&
sh internal/enginespike/multiday.sh <scratch> clean` (~8 GB peak, ~4 min).
All generated data was deleted.

The app's run replayed as 7 days (772,654 rows/day, 5.4M total). A = all
7 days in one DuckDB file; B = today hot in DuckDB + 6 days of Parquet via
per-stream `UNION ALL BY NAME` views; Bday = B plus an automatic `day`
predicate; C = Parquet only. p50 ms of 7 warm runs (page cache warm; true
cold not measured). B and C matched A on all 226 result sets.

| query | today A / B / Bday | past day A / B / Bday / C | 7 days A / B / Bday |
|---|---|---|---|
| Q1 samples | 1.9 / 5.5 / 4.6 | 2.2 / 5.7 / 6.0 / 5.8 | 4.7 / 6.2 / 6.9 |
| Q2 table | 6.1 / 8.1 / 7.9 | 5.6 / 7.4 / 7.1 / 8.2 | 14.7 / 17.9 / 19.2 |
| Q3 graph | 6.2 / 11.4 / 11.5 | 5.8 / 10.5 / 10.3 / 10.7 | 13.3 / 17.8 / 17.9 |
| Q4 join | 5.3 / 10.8 / 10.5 | 5.4 / 12.4 / 11.9 / 11.2 | 8.5 / 14.7 / 15.3 |
| Q6 distinct | 4.8 / 22.1 / 20.1 | 4.8 / 22.2 / 19.5 / 16.8 | 11.0 / 25.6 / 28.8 |
| Q8 full text | 94 / 159 / 143 | 95 / 116 / 114 / 113 | 292 / 308 / 335 |
| today vs yesterday | | | 2.4 / 5.6 / 4.7 |
| count by stream by day | | | 5.3 / 21.9 / 23.6 |

- Every browser query except full text stays under 30 ms. The archive adds
  ~2.5 ms per stream touched (globbing folders, opening file footers).
- Conversion: 0.39 s and 27 MB per day (sorted, zstd). Merging 3 small days:
  0.36 s. Disk: A 345 MB with zstd forced on long text (6.9 GB without);
  B 210 MB.
- Pruning: `_time` alone prunes row groups and whole branches but still
  opens every file (90 of 90); a `day` predicate opens only the matching
  one. One past day as folders grow, without → with `day`: 6 folders
  3.7 → 2.8 ms, 90 folders 19 → 10 ms, 365 folders 69 → 33 ms (17.6 ms
  without `union_by_name`). `union_by_name` reads every file's schema per
  query.
- 16k-row groups were slower except for full text (116 → ~60 ms on one past
  day).
- Gotchas: the hot table has no `day` column, so a `day` filter silently
  drops today's rows unless the view adds it; types differing across days
  become VARCHAR under `union_by_name` (`avg` fails, `max` compares
  strings), and without it a missing column fails the glob; merged
  multi-day files break `day` pruning; no other process can open the hot
  file while `serve` holds it, but Parquet files can be read from any
  process.
- Full-text search (Q8) is ~100–300 ms in every layout, native included;
  it needs its own plan (newest rows first with a `LIMIT` and a time window,
  or an index).

## Open questions

1. ~~Engine~~ — resolved: DuckDB only (decision 5).
2. ~~Browser execution path~~ — resolved: through DuckDB (decision 35).
3. ~~SQL dialect~~ — resolved: DuckDB's.
4. **Run identity:** how a run gets its id — an env var set by whatever
   starts the tests, derived from `-- <cmd>`, or an OTEL resource attribute
   emitted by the test framework.
5. **Retention numbers:** runs kept per suite, size cap, age per follow
   source.
6. **`.loghorn/` text files:** keep alongside the database (a greppable plain
   record) or drop?
7. **`serve` lifecycle:** started manually, by launchd, or by the first client
   that needs it; what the CLI does when it isn't running.
8. **Central store location** and where per-service saved schemas live
   (repo `.config/loghorn/` vs user-level).
9. **stdout-only apps:** transparent wrapper, an OTEL log transport in the
   app, and/or a `PreToolUse` hook — which to support first.
10. **Alerts design** (decision 37) unconfirmed.
11. **Name** of the cross-stream table (`all_records` vs `otel.logs`).
12. **Security:** `serve` listens on loopback (web, API, OTLP); the earlier
    question of an unauthenticated `-web` on 127.0.0.1 (token proposed) still
    applies.
13. ~~Full-text search speed~~ — resolved: acceptable (decision 36a).

## Related documents and artifacts

- `docs/superpowers/specs/2026-11-18-sql-query-design.md` — the SQL interface
  spec written before the re-architecture. Still valid: naming, columns and
  types, helper functions, `Query` ↔ SQL conversion, the browser's `?sql=`
  handling, CLI flags and output, discovery statements, `-sql-help`.
  Superseded: "Where the CLI's data comes from", "The cache", the run-start
  marker, the SQLite dialect and SQLite-specific type mapping (now DuckDB,
  which has real timestamp, interval and JSON types), "Approaches
  considered" (now spikes 2 and 3), and "browser stays native" (reversed:
  decision 35).
- `docs/superpowers/specs/2026-11-17-web-query-design.md`,
  `2026-11-17-web-explore-design.md` — the current web view and `Query`
  model.
- Code: `internal/query` (`Query`, `query.Run`), `internal/schema` (types,
  inference, saved schemas), `internal/ingest`, `internal/adapter`,
  `internal/record`, `internal/store`, `internal/web`, `internal/ghjob`,
  `internal/alert`, `internal/tui` (to be removed).

## Next steps

1. Spikes 1–3 are done; engine, browser path, dialect and full-text search
   are settled.
2. Answer the remaining open questions (4–12) with the user, and confirm or
   reject the [proposed] decisions.
3. Write the architecture spec (likely split: `serve` + storage + runs;
   sources; SQL interface; browser; alerts; TUI removal), then plans.
