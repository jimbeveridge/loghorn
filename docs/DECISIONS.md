# Decisions

Technical decisions for the DuckDB storage work, newest last. Earlier
architecture decisions live in
`docs/superpowers/specs/2026-11-19-architecture-working-notes.md` (cited
below as "notes #N") and are not repeated here.

**Rules for this file:** never edit an entry's decision after it is
accepted. To change one, add a new entry and mark the old one
`Superseded by D-NNN`.

**Status values:** Proposed · Accepted (provisional, until the design doc
is approved) · Accepted · Superseded · Rejected.

**Decided by:** *user*, the person directing this work; *author*, the
loghorn maintainer, via the notes; *Claude*, a proposal the user accepted
or has not yet answered.

---

## D-001 Store logs and traces in DuckDB, in two files

- **Date:** 2026-11-19, confirmed 2026-11-23 · **Status:** Accepted (provisional) · **Decided by:** user
- **Decision:** Replace the web view's in-memory record list with DuckDB:
  one file for logs, one for traces. Metrics stay discarded.
- **Why:** Faster filters and queries in the web view. The author already
  chose DuckDB as the only engine (notes #5, spikes 2 and 3).
- **Alternatives:** Keep the in-memory store (slow, about 3 GB RAM per run,
  spike 2); one DuckDB file for both signals; SQLite or ClickHouse
  (rejected by the author's spikes).
- **Consequences:** cgo builds. DuckDB lets one process write a file, so
  one loghorn process owns both files.

## D-002 Layout B: a schema per service, a table per stream, typed columns

- **Date:** 2026-11-23 · **Status:** Accepted (provisional) · **Decided by:** user (picked from A/B/C)
- **Decision:** Inside each file, a schema per service key and a table per
  stream, with one typed column per field, named by its dotted path.
- **Why:** It matches notes #16 ("database = service") and is the layout
  spike 2 measured at 1–17 ms on about 1 GB.
- **Alternatives:** A: one table per signal, attributes in one JSON column
  (fixed shape, but JSON parsing on every filter; speed not measured).
  C: A plus promoted typed columns (most moving parts).
- **Consequences:** Tables and columns are created as data arrives.
  Cross-stream reads need generated views (D-004). Type changes need rules
  (D-005).

## D-003 Span tables by span type; the span name is a column

- **Date:** 2026-11-23 · **Status:** Accepted (provisional) · **Decided by:** Claude
- **Decision:** In the traces file, one table per span type (`db`,
  `http.server`, …, and catch-all `span`). The displayed kind (the span
  name, for unclassified spans) is a `stream` column.
- **Why:** Unclassified spans are listed under their own name today
  (`internal/record/span.go`), so one table per name could mean thousands of
  tables.
- **Alternatives:** A table per displayed kind; one spans table (the
  2026-11-19 proposal, see D-009).
- **Consequences:** The web view's kind list reads the `stream` column.

## D-004 The shared format is a set of generated views

- **Date:** 2026-11-23 · **Status:** Accepted (provisional) · **Decided by:** Claude
- **Decision:** loghorn generates `all_records` (the columns every
  `record.Record` has) and one view per stream over both files. The web
  view reads only views.
- **Why:** `record.Record` is already the shared format every view uses
  (`internal/record/record.go`). Views keep that shape, so web code changes
  stay small.
- **Alternatives:** A third DuckDB file holding a merged copy (duplicates
  the data); merging in Go after separate queries.
- **Consequences:** Views must be rebuilt when a table or column appears.

## D-005 Schema evolution and bad values

- **Date:** 2026-11-23 · **Status:** Accepted (provisional) · **Decided by:** author (notes #19, #21)
- **Decision:** A new field adds a column. A changed type widens the column
  (integer → decimal → text). A value that doesn't fit is empty (NULL) in
  that column and stays visible in `_raw`.
- **Why:** Logs are never dropped or hidden.
- **Consequences:** Every table keeps `_raw`. Long text columns are
  compressed with zstd (spike 2).

## D-006 Per-repo, per-day files in `.loghorn/`

- **Date:** 2026-11-23 · **Status:** Accepted (provisional) · **Decided by:** Claude
- **Decision:** `.loghorn/logs-YYYY-MM-DD.duckdb` and
  `.loghorn/traces-YYYY-MM-DD.duckdb`, written by the run that holds
  `loghorn.lock`, rotated at midnight, kept 3 days like the text files.
- **Why:** It fits today's code and lock. DuckDB files don't shrink after
  DELETE, so expiry drops whole days. The author also chose a file per day
  (notes #39).
- **Alternatives:** A central store owned by a new `loghorn serve` (notes
  #4, #20): the long-term direction, but a separate project.
- **Consequences:** `serve` can take the same layout over later. Queries
  spanning days read several files.

## D-007 Text files stay the source of truth; no ingestion changes

- **Date:** 2026-11-23 · **Status:** Accepted (provisional) · **Decided by:** Claude
- **Decision:** `loghorn.log` and `loghorn.traces.jsonl` keep being
  written. Records enter DuckDB where they enter the in-memory store today
  (`store.Add`), not in the OTLP receiver. DuckDB can be rebuilt from the
  text files.
- **Why:** Someone else is working on ingestion (anti-goal in
  `docs/superpowers/specs/2026-11-20-span-streams-design.md`). It also gives
  a safe fallback.
- **Consequences:** Disk holds both forms for now (notes open question 6).

## D-008 Batched background writes

- **Date:** 2026-11-23 · **Status:** Accepted (provisional) · **Decided by:** Claude
- **Decision:** Rows are appended in batches in the background, never one
  per request.
- **Why:** Spike 2 saw DuckDB flushes stall for up to 0.5 s.
- **Consequences:** Newly arrived records appear in queries after a short
  delay. The exact batch size and delay are left to the design.

## D-009 Single spans table (2026-11-19 proposal)

- **Date:** 2026-11-19 · **Status:** Superseded by D-002 and D-003 · **Decided by:** Claude
- **Decision:** One `spans` table per service, shared columns with logs,
  an `all_records` view.
- **Why superseded:** The web view now sorts spans into streams by span
  type (commits `808cd5b`, `1dd0d54`), so spans follow the per-stream
  layout.
- **Note:** The proposal was a demo SQL file shared in chat and was never
  added to the repo.

## D-010 One design covers storage and moving the web view's queries to SQL

- **Date:** 2026-11-23 · **Status:** Accepted (provisional) · **Decided by:** user (resolves O-1)
- **Decision:** A single design and plan covers writing to DuckDB (D-001
  to D-008) and running the web view's queries as SQL against the views
  (D-004).
- **Why:** The speed gain arrives in one step.
- **Alternatives:** Split into two designs, storage first (Claude's
  recommendation): each half testable sooner, but no speed gain until the
  second.
- **Consequences:** A larger design and plan. Nothing can be checked end
  to end until both halves are built.

## D-011 Design approved, with its known differences accepted

- **Date:** 2026-11-23 · **Status:** Accepted · **Decided by:** user approved Claude's calls; Claude chose the known-differences option under the user's "take aggressive decisions"
- **Decision:** `docs/superpowers/specs/2026-11-23-duckdb-storage-design.md`
  is approved, including its "Claude's calls" list. The four "Known
  differences" from today's answers are accepted. No text copy of each
  field is stored.
- **Why:** The differences are rare (type overrides needing original
  text; present-but-null fields in per-interval default columns), already
  happen today (rebuilt days), or are invisible (last digit of sums). A text
  copy would roughly double storage and slow writes.
- **Alternatives:** Store a text copy of every field (exact parity).
- **Consequences:** D-001 to D-008 and D-010 move from provisional to
  accepted, as refined by the design doc. In particular, D-003's displayed
  kind lives in a `_stream` column, not `stream`.

## D-012 Native execution, one final review

- **Date:** 2026-11-23 · **Status:** Accepted · **Decided by:** user ("complete the whole thing by your own")
- **Decision:** Claude implements every plan task in this session, tests
  first, then one fresh reviewer checks the whole branch.
- **Alternatives:** A fresh agent per task, plus a reviewer per task (more
  thorough, slower).

## D-013 Local build setup for this session (not committed)

- **Date:** 2026-11-23 · **Status:** Accepted · **Decided by:** Claude
- **Decision:** The cloud session can't reach the Go module proxy, Go
  downloads, or GitHub releases. So it builds Go 1.26.1 from its GitHub
  source, points Go at module copies cloned from GitHub mirrors through a
  `go.work` outside the repo, and links DuckDB 2.0.1's shared library
  (from npm `@duckdb/node-bindings-linux-x64@2.0.1-r.1`) with the driver's
  `duckdb_use_lib` build tag.
- **Consequences:** The committed `go.mod` requires the DuckDB driver, but
  `go.sum` can't be completed here. **Before merging, run `go mod tidy` on a
  normal machine** and run the tests without the build tag.

## D-014 Durations stored as DOUBLE seconds

- **Date:** 2026-11-23 · **Status:** Accepted · **Decided by:** Claude (refines the design's duration row)
- **Decision:** A duration column is `DOUBLE` holding
  `schema.Convert(v, duration).Num`, in seconds.
- **Why:** The design kept durations as text because "the saved unit can
  change". Reading `schema.Convert` showed the unit is display-only for
  durations: a number is taken as is and a string is parsed by
  `ParseDuration`, whatever the unit. So the converted number never changes,
  and filters and percentiles avoid parsing text on every query.
- **Alternatives:** VARCHAR as received, parsed at query time (slower, same
  answers).
- **Consequences:** The text as received stays in `_raw`.

## D-015 Requests wait briefly for queued writes

- **Date:** 2026-11-23 · **Status:** Accepted · **Decided by:** Claude
- **Decision:** A read request waits up to 250 ms for records added before
  it to be committed, then answers with what is committed.
- **Why:** Today a record is visible the moment it is added. Without the
  wait, a page refresh or a test could miss records still in the queue.
  Writers never wait on readers, so the child and the receiver are not
  slowed.
- **Alternatives:** Answer from committed data only (stale by one batch).

## D-016 `query.Run` and `schema.Infer` stay as the test oracle

- **Date:** 2026-11-23 · **Status:** Accepted · **Decided by:** Claude (defers part of the design's "delete after parity")
- **Decision:** The web no longer calls `query.Run` or `schema.Infer`, but
  both stay in the code, used by the parity tests.
- **Why:** The parity tests are the proof that SQL answers match. Deleting
  the oracle would leave future SQL changes unchecked. Deleting it is a
  one-commit follow-up once the author agrees.

## D-017 Trace columns shared; JSON stored as text

- **Date:** 2026-11-23 · **Status:** Accepted · **Decided by:** Claude (refines `all_records`)
- **Decision:** Every table gets shared columns `_trace_id`, `_span_id`,
  `_parent_span_id` (normalized with `record.TraceRef` and
  `NormalizeSpanID`) and `_name` (a span's name). `all_records` carries them
  instead of raw `trace_id`/`span_id`/`parent_span_id`/`severity`/`message`.
  JSON fields are `VARCHAR`, not DuckDB `JSON`.
- **Why:** Logs carry trace references under `trace`/`spanId` too, and
  `trace.Build` matches normalized ids. Shared normalized columns make
  trace and aggregate lookups one indexed-style WHERE. `VARCHAR` keeps the
  JSON text exactly as today's cells show it.

## D-018 Days in doubt are rebuilt at startup, synchronously

- **Date:** 2026-11-23 · **Status:** Accepted; narrowed by D-024 (runs without `-web`) · **Decided by:** Claude (simplifies the design's background rebuild)
- **Decision:** At startup the lock holder rebuilds each retained day whose
  pair is missing or in doubt, before records flow, into a temporary file
  then renamed. Other processes load such days from text into memory.
  "In doubt" = not closed cleanly, marked dirty, or the day's text files
  have a different total size from the one recorded at the last clean
  close.
- **Why:** One code path, no switching a day from memory to file
  mid-session. The cost is paid once per day in doubt and is no more than
  today's preload, which every `-web` run pays. Sizes are a sturdier
  fingerprint than modification times.
- **Consequences:** A first run after upgrading pauses while it builds up to
  four days; loghorn prints a line while it does.

## D-019 Where the code lives

- **Date:** 2026-11-23 · **Status:** Accepted · **Decided by:** Claude (builder's choice in the design)
- **Decision:** New package `internal/duckstore` owns DuckDB. `internal/store`
  stays the façade its callers use. SQL execution lives in
  `internal/query/sqlrun.go`, next to the helpers whose answers it must
  match.

## D-020 Untimed leading records appear at end of input

- **Date:** 2026-11-23 · **Status:** Superseded by D-023 · **Decided by:** Claude
- **Decision:** A record with no time and no earlier record waits until a
  timed record arrives (and takes its time, as today) or the input ends.
- **Why:** A stored `_time` never changes after it is written. Today such
  records show with no time until backfilled; live runs never have them.

## D-021 A per-day record index backs `all_records`

- **Date:** 2026-11-23 · **Status:** Accepted · **Decided by:** Claude (found while building)
- **Decision:** Each day database keeps `_loghorn._records`: every record's
  shared columns (not `_bad` or `_raw`) and its table's position.
  `all_records` reads these indexes, not every stream table. Lookups that
  need the line (`/api/record`, traces) find the table there, then read
  `_raw` from it.
- **Why:** DuckDB pays about 0.3 ms per table in a `UNION ALL`, measured
  here (50 tables 14 ms, 400 tables 120 ms). With a table per stream,
  service and day, a union over every table would miss the ~50 ms target
  for kinds, streams and traces.
- **Consequences:** A second, narrow append per record. "All kinds" queries
  still read every stream table of the source, so they slow down as tables
  multiply.

## D-022 Queries read day tables directly

- **Date:** 2026-11-23 · **Status:** Accepted · **Decided by:** Claude (found while building)
- **Decision:** The relation a query reads is built from the same model as
  the views, but selects from each day table directly, converting each
  column straight to the effective type. The views stay for people and
  tools reading the databases.
- **Why:** Binding a wide view cost more than the query itself in tests,
  and converting once (table type → effective type) is closer to
  `schema.Convert` than converting twice through the view's widest type.

## D-023 Untimed leading records are written at once and backfilled (supersedes D-020)

- **Date:** 2026-11-23 · **Status:** Accepted · **Decided by:** Claude
- **Decision:** A record with no time and no earlier record is written
  right away with no time. When the first timed record arrives, the writer
  sets those records' `_time` to its time, in queue order.
- **Why:** D-020 hid such records until a timed one arrived. Tests showed
  what that costs: a replay of untimed lines showed nothing in the web
  view. This keeps today's behaviour exactly; the update touches only the
  leading records of a session.
- **Supersedes:** D-020.

## D-024 A writer without `-web` defers rebuilds (narrows D-018)

- **Date:** 2026-11-24 · **Status:** Accepted · **Decided by:** Claude (review minor 10; correct if wrong)
- **Decision:** A lock holder running without `-web` rebuilds no day at
  startup. It still writes today's records to today's pair, and marks
  today dirty, so the next writer with `-web` rebuilds it from text.
  Earlier days in doubt are left for that writer too. With `-web`, D-018
  is unchanged. The `-web` port is bound before the store opens, so a busy
  port fails before any rebuild.
- **Why:** Nothing reads the day files in a run without `-web`, yet D-018
  made every such run wait for rebuilds (about 44 s for 600k lines)
  before the child started. The text files stay the source of truth
  (D-007), so deferring loses nothing. This is simpler than the design's
  background rebuild: a day still never switches from memory to file
  mid-session.
- **Consequences:** The first `-web` run after plain runs on a day in doubt
  pays the rebuild. A run without `-web` writes today to a pair that is
  rebuilt later anyway.

---

## Open questions

- **O-1 Scope split** — *Resolved 2026-11-23 by D-010 (one design).* Asked
  2026-11-23. Option one:
  this design covers storage plus views, and a second design moves the web
  view's queries to SQL. Option two: both in one design. Claude recommends
  the split. The speed gain needs the SQL part (notes #35).
- **O-2 Author review.** D-001 to D-008 haven't been reviewed by the loghorn
  author. They follow his notes except D-001's two-files-by-signal split.
