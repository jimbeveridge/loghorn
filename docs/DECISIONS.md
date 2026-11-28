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

## D-001 Store logs and traces in DuckDB, in two files (superseded by D-030)

- **Date:** 2026-11-19, confirmed 2026-11-23 · **Status:** Superseded by D-030 (one file, `loghorn.duckdb`, for both signals; DuckDB stays the engine); accepted by D-011 before that · **Decided by:** user
- **Decision:** Replace the web view's in-memory record list with DuckDB:
  one file for logs, one for traces. Metrics stay discarded.
- **Why:** Faster filters and queries in the web view. The author already
  chose DuckDB as the only engine (notes #5, spikes 2 and 3).
- **Alternatives:** Keep the in-memory store (slow, about 3 GB RAM per run,
  spike 2); one DuckDB file for both signals; SQLite or ClickHouse
  (rejected by the author's spikes).
- **Consequences:** cgo builds. DuckDB lets one process write a file, so
  one loghorn process owns both files.

## D-002 Layout B: a schema per service, a table per stream, typed columns (superseded by D-025, D-030)

- **Date:** 2026-11-23 · **Status:** Superseded by D-025 (fixed columns, other fields in JSON), then D-030 (one `records` table); accepted by D-011 before that · **Decided by:** user (picked from A/B/C)
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

## D-003 Span tables by span type; the span name is a column (superseded by D-025)

- **Date:** 2026-11-23 · **Status:** Superseded by D-025 (no table per span type; the displayed kind is `_stream`, a span's name is `message`); accepted by D-011 before that · **Decided by:** Claude
- **Decision:** In the traces file, one table per span type (`db`,
  `http.server`, …, and catch-all `span`). The displayed kind (the span
  name, for unclassified spans) is a `stream` column.
- **Why:** Unclassified spans are listed under their own name today
  (`internal/record/span.go`), so one table per name could mean thousands of
  tables.
- **Alternatives:** A table per displayed kind; one spans table (the
  2026-11-19 proposal, see D-009).
- **Consequences:** The web view's kind list reads the `stream` column.

## D-004 The shared format is a set of generated views (superseded by D-025)

- **Date:** 2026-11-23 · **Status:** Superseded by D-025 (`all_records` is a plain `UNION ALL` of the table and the archives; no view per stream). D-031 later adds views per stream over `records`, for SQL. Accepted by D-011 before that · **Decided by:** Claude
- **Decision:** loghorn generates `all_records` (the columns every
  `record.Record` has) and one view per stream over both files. The web
  view reads only views.
- **Why:** `record.Record` is already the shared format every view uses
  (`internal/record/record.go`). Views keep that shape, so web code changes
  stay small.
- **Alternatives:** A third DuckDB file holding a merged copy (duplicates
  the data); merging in Go after separate queries.
- **Consequences:** Views must be rebuilt when a table or column appears.

## D-005 Schema evolution and bad values (superseded by D-025)

- **Date:** 2026-11-23 · **Status:** Superseded by D-025 (no columns added or widened; fields are converted from JSON at query time). Its rule that a bad value is NULL and stays visible in `_raw` still holds. Accepted by D-011 before that · **Decided by:** author (notes #19, #21)
- **Decision:** A new field adds a column. A changed type widens the column
  (integer → decimal → text). A value that doesn't fit is empty (NULL) in
  that column and stays visible in `_raw`.
- **Why:** Logs are never dropped or hidden.
- **Consequences:** Every table keeps `_raw`. Long text columns are
  compressed with zstd (spike 2).

## D-006 Per-repo, per-day files in `.loghorn/` (superseded by D-030)

- **Date:** 2026-11-23 · **Status:** Superseded by D-030, by way of D-026: one file, `loghorn.duckdb`, in serve's data folder, written only by `loghorn serve`; closed days go to Parquet (D-028). Accepted by D-011 before that · **Decided by:** Claude
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

## D-014 Durations stored as DOUBLE seconds (superseded by D-025)

- **Date:** 2026-11-23 · **Status:** Superseded by D-025 (durations are converted from JSON at query time); accepted before that · **Decided by:** Claude (refines the design's duration row)
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

## D-016 `query.Run` and `schema.Infer` stay as the test oracle (superseded by D-033)

- **Date:** 2026-11-23 · **Status:** Superseded by D-033 (both removed; behaviour tests state expected answers); accepted before that · **Decided by:** Claude (defers part of the design's "delete after parity")
- **Decision:** The web no longer calls `query.Run` or `schema.Infer`, but
  both stay in the code, used by the parity tests.
- **Why:** The parity tests are the proof that SQL answers match. Deleting
  the oracle would leave future SQL changes unchecked. Deleting it is a
  one-commit follow-up once the author agrees.

## D-017 Trace columns shared; JSON stored as text (superseded by D-025)

- **Date:** 2026-11-23 · **Status:** Superseded by D-025 (columns `trace_id`, `span_id`, `parent_span_id`; a span's name is `message`; `attributes` is DuckDB `JSON`); accepted before that · **Decided by:** Claude (refines `all_records`)
- **Decision:** Every table gets shared columns `_trace_id`, `_span_id`,
  `_parent_span_id` (normalized with `record.TraceRef` and
  `NormalizeSpanID`) and `_name` (a span's name). `all_records` carries them
  instead of raw `trace_id`/`span_id`/`parent_span_id`/`severity`/`message`.
  JSON fields are `VARCHAR`, not DuckDB `JSON`.
- **Why:** Logs carry trace references under `trace`/`spanId` too, and
  `trace.Build` matches normalized ids. Shared normalized columns make
  trace and aggregate lookups one indexed-style WHERE. `VARCHAR` keeps the
  JSON text exactly as today's cells show it.

## D-018 Days in doubt are rebuilt at startup, synchronously (superseded by D-027)

- **Date:** 2026-11-23 · **Status:** Superseded by D-027 (the file is filled from text once, not rebuilt per day); accepted before that, and narrowed by D-024 (runs without `-web`) · **Decided by:** Claude (simplifies the design's background rebuild)
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

## D-021 A per-day record index backs `all_records` (superseded by D-025, D-030)

- **Date:** 2026-11-23 · **Status:** Superseded by D-025 and D-030 (one `records` table, no tables per stream or day to index); accepted before that · **Decided by:** Claude (found while building)
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

## D-022 Queries read day tables directly (superseded by D-025, D-030)

- **Date:** 2026-11-23 · **Status:** Superseded by D-025 and D-030 (queries read `records` and the Parquet archives); accepted before that · **Decided by:** Claude (found while building)
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

## D-024 A writer without `-web` defers rebuilds (narrows D-018) (superseded by D-027, D-030)

- **Date:** 2026-11-24 · **Status:** Superseded by D-027 and D-030 (no `-web` flag; `loghorn serve` always imports); accepted before that · **Decided by:** Claude (review minor 10; correct if wrong)
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

## D-025 v2: one fixed table per signal, every field in JSON (superseded by D-030)

- **Date:** 2026-11-24 · **Status:** Superseded in part by D-030: its two tables became one, `records`; its columns, JSON attributes and conversion at query time were adopted with D-030. Was Proposed (v2) · **Decided by:** user (v2 spec); details Claude
- **Decision:** `logs` and `spans` tables with fixed columns: `_id`,
  `_time`, `_time_inherited`, `_source`, `_service`, `_stream`,
  `trace_id`, `span_id`, `parent_span_id`, `severity`, `message`,
  `duration_ms`, `attributes` (JSON) and `_raw`. `attributes` holds every
  normalized field, including the ones copied to columns. A field is read
  from JSON and converted at query time, per record, by the kind of JSON
  value it is, as `schema.Convert` converts it. Tallies per day, service,
  stream and path feed `schema.FromTallies`. `all_records` is a plain
  `UNION ALL` of the tables and archives.
- **Why:** The v2 spec asks for no tables per stream, no columns added at
  run time and no type-widening rewrites. Converting at query time makes a
  new field or a changed type setting work with no table change, and keeps
  every value as received, so D-011's first known difference (overrides
  needing the original text) mostly goes away.
- **Would supersede:** D-002, D-003, D-004, D-014 (durations are converted
  at query time), D-017 (columns are `trace_id`, `span_id`,
  `parent_span_id`; a span's name is `message`; JSON is DuckDB `JSON`),
  D-021, D-022.
- **Consequences:** Each query parses `attributes` once per record
  (`json_extract` with every path at once). Known differences, tested by
  `TestRelationConvertsLikeConvert`: a number read as text gives DuckDB's
  canonical spelling (`1.50` reads `1.5`, `1e3` reads `1000.0`), an
  array read as text keeps `<`, `>` and `&` where Go escapes them, and a
  string that isn't valid UTF-8 reads with U+FFFD in place of the bad
  bytes (`_raw` keeps them). All 8055 parity queries match.

## D-026 v2: `logs.duckdb` and `traces.duckdb`, owned by the lock holder (superseded by D-030)

- **Date:** 2026-11-24 · **Status:** Superseded by D-030. Was Proposed (v2) · **Decided by:** user (v2 spec); details Claude
- **Decision:** Two files, in `.loghorn/` by default or in `-db-dir`. The
  process holding `loghorn.lock` owns them. A live run without the lock
  reads the retained days' text into memory. A historical run copies the
  files' and archives' rows into memory and holds nothing open, so it
  never keeps the owner out and outlives archives the owner later
  replaces or deletes; the owner's open retries for two seconds meanwhile.
  It reads the text instead when the files aren't marked imported. `_id`
  keeps the `day<<32 | seq` encoding, so a record's archive day needs no
  column.
- **Why:** The v2 spec's single owning process. DuckDB locks a file per
  process, so a reader that kept the files open would lock a later writer
  out of DuckDB for a whole run.
- **Would supersede:** D-006. The default stays in `.loghorn/` until the
  user-local location is decided (spec open question).

## D-027 v2: the files hold the records after a one-time import (adopted under D-030)

- **Date:** 2026-11-24, mark updated 2026-11-27 · **Status:** Accepted: adopted with D-030 on this branch (2026-11-25), for Jim's review. Was Proposed (v2) · **Decided by:** Claude
- **Decision:** When `loghorn serve` opens files that lack the `imported`
  mark, it clears them (and the archive), imports the retained days'
  text, and marks them. After that the files are read, never rebuilt.
  Text files are still written and kept (D-007). The mark is a file
  beside the database, `loghorn.duckdb.imported`: serve removes it when it
  opens the files and writes it again only at a clean Close of files that
  hold every record.
- **Why:** The v2 spec wants startup to reuse persisted records rather
  than reparse text. Clearing before an import is safe because, until the
  mark is set, everything in the files came from text that still has it.
  A mark cleared at open and set only at a clean close means anything
  short of that (a crash, a failed write, a session kept in memory) makes
  the next start import the text again.
- **Supersedes:** D-018, D-024 (adapted).
- **Consequences / known gaps:** The mark is written only when every text
  file was read and no record was skipped (a full queue) or failed to
  write; skipped records are also warned about at Close. Records DuckDB
  refuses for their own data are skipped with a warning and don't stop the
  mark: importing the same text again can't store them either. A start
  after a crash imports the text again (about 20 s for 770k records).
  Whether to keep permanent text files is the spec's open question.
- **History:** As first proposed, the mark was a row in a `_meta` table in
  the database, set at import and cleared only by a clean Close that saw
  skips or failures; a writer without `-web` skipped the import (as
  D-024), leaving it to the next writer with `-web`. So a crash lost the
  queued batch from DuckDB and left the mark set, and a run that couldn't
  open the files left its records in text only. The sidecar file replaced
  the row on 2026-11-27 (review §1.3); `-web` went with D-030, and serve
  always imports. The `_meta` table is no longer created or read.

## D-028 v2: closed days archived to Parquet (adopted under D-030)

- **Date:** 2026-11-24 · **Status:** Accepted: adopted with D-030 on this branch (2026-11-25), for Jim's review; D-030 made it one file per day. Was Proposed (v2) · **Decided by:** Claude
- **Decision:** At open, and at the first record of a new day, each day
  before today moves to `duckdb-archive/<logs|traces>-YYYY-MM-DD.parquet`
  (ZSTD). *(Since D-030: one file per day for all records,
  `duckdb-archive/records-YYYY-MM-DD.parquet`.)* The export is written
  to `.tmp`, checked against its rows (count, min/max/sum of `_id`, XOR
  of hashes of `_raw` and `attributes`), synced, renamed (and the
  directory synced), and only then deleted from the file.
  Archives and tallies older than 3 days before today (the text's
  retention) are deleted.
- **Why:** The v2 spec's basic archival. One file per signal per day
  (one per day since D-030) avoids many small files. Days match the text
  files' retention.
- **Consequences:** A failure keeps the rows hot, warns once and retries
  at the next open or midnight. Rows left hot for an archived day are
  merged into it, and the view leaves out rows an archive shares with the
  tables.

## D-029 v2: `_raw` is BLOB; search reads it decoded (adopted under D-030)

- **Date:** 2026-11-24 · **Status:** Accepted: adopted with D-030 on this branch (2026-11-25), for Jim's review. Was Proposed (v2) · **Decided by:** Claude
- **Decision:** `_raw` stores the captured bytes. `Relation` adds `_text`
  (the bytes decoded as UTF-8, escaped where invalid) for search.
- **Why:** The spec asks for lossless raw records; DuckDB `VARCHAR`
  rejects invalid UTF-8.

## D-030 One `records` table in `loghorn.duckdb`, owned by `loghorn serve`

- **Date:** 2026-11-25 · **Status:** Decided by the author (review of the
  v2 spec) · **Decided by:** Jim Beveridge (review comment), confirmed on this branch
- **Decision:** Logs and spans share one table, `records`, in one file,
  `loghorn.duckdb`, with D-025's columns; `_source` tells them apart. One
  Parquet archive per closed day, `records-YYYY-MM-DD.parquet`, holds both.
  The file lives in serve's data folder, and `loghorn serve` is its only
  writer: other processes send it OpenTelemetry.
- **Why:** The author's review: one table with the common attributes as
  columns and the rest together, never a table per attribute or stream
  (no JOINs); DuckDB has no independent writers, and serve already accepts
  OTLP from several processes at once.
- **Supersedes:** D-025's two tables and D-026's two files and lock-holder
  ownership. D-028 and D-029 are unchanged. D-027 was revised on 2026-11-27: its
  imported mark is now the sidecar file `loghorn.duckdb.imported`, not
  the `_meta` table.
- **Consequences:** Each batch is one transaction in one table. Files from
  the two-table builds (`logs.duckdb`, `traces.duckdb`, `logs-` and
  `traces-` archives) are ignored; the new file is filled from text once.
- **Open:** the CLI query spec's SQL table model (schemas `logs` and
  `spans`, one table per stream with typed columns) is still to be
  provided, as views over `records`. *Provided by D-031.*

---

## D-031 `loghorn query -sql`: read-only SQL over the table model, through serve

- **Date:** 2026-11-25 · **Status:** Decided on this branch, for Jim's
  review · **Decided by:** the author asked for it; details are Claude's
  calls, listed in `docs/superpowers/specs/2026-11-25-cli-sql-design.md`
- **Decision:** The CLI query spec's table model exists as views in serve's
  session database: `logs."<stream>"`, `spans."<stream>"` and `"*"` of
  each, with `seq`, `time`, `service`, `raw` and one typed column per field
  of the stream's schema over every service, converted as the web view
  converts values (bad values NULL, durations in ms rounded to 15 digits).
  The views are rebuilt from the schema inferred at query time; the DDL only
  runs when its text changes. `loghorn query -sql` sends one query to
  serve's new `POST /api/sql`, on serve's socket only. Three layers keep it
  read-only: DuckDB's parser must read it as one query made only of
  SELECT, set-operation and CTE nodes (a data-modifying CTE parses as a
  SELECT), whose CTEs have plain names and whose tables are the model's or
  DuckDB's catalog views; it runs in a `READ ONLY` transaction on a
  connection of its own; and the engine, once its files are open, has
  external access off except for the data folder, and extension
  autoinstall and autoload off. The row cap is part of the query
  (`SELECT * FROM (…) LIMIT n+1`), since DuckDB builds the whole answer
  before the first row is read. At most 1,000 rows unless `-limit`, which
  may not exceed 100,000; 10 s timeout.
- **Why:** Only serve may open the database (D-030), and agents run many
  small queries in a row, so each must be bounded and fail with DuckDB's
  own message. Views over `records` store nothing twice (D-030).
- **Consequences:** `loghorn query -to-sql` output now runs unchanged:
  Jim's equivalence cases pass over these views (`TestToSQLOverModel`).
  `loghorn.this_run_id()` is the newest record's `loghorn.run.id` and
  `this_run_start()` that run's first record time.
- **Open:** under serve, the web view's `this-run` means "since serve
  started", while the macros mean the latest wrapped run; the two should
  agree.

## D-032 Search folds case by Unicode rules

- **Date:** 2026-11-27 · **Status:** Decided on this branch, for Jim's
  review · **Decided by:** Jim's review (§2.3) asked for a decision and
  suggested Unicode; Claude made the call
- **Decision:** A search matches case-insensitively by Unicode rules, as
  DuckDB's `lower()` folds: `istanbul` finds `İSTANBUL`, and `ÜNÏCODE` finds
  `ünïcode`. This is what the web view and `loghorn query` already do,
  since both search through SQL.
- **Why:** People search logs in their own language. Folding only ASCII
  for an ASCII term (Go's `query.Run`) misses text a reader sees as the
  same word.
- **Consequences:** `query.Run`, then kept only as a test reference
  (D-016), differed here, so `TestSearchFoldsUnicodeCase` held written
  expected values instead of comparing with it. D-033 has since removed
  `query.Run`; every behaviour test now states its expected values.

## D-033 Behaviour tests state expected answers; the Go engine is removed

- **Date:** 2026-11-27 · **Status:** Accepted · **Decided by:** the
  2026-11-27 follow-up review's plan (supersedes D-016)
- **Decision:** `query.Run`, the Go query engine, and `schema.Infer`, its
  in-memory schema inference, are deleted, with every helper only they
  used. The behaviour tests in `internal/query` run on DuckDB through
  `RunSQL` and state the answer they expect, instead of comparing with
  `query.Run`. Schema tests build tallies with `schema.Classify`, as the
  writer does, and check `schema.FromTallies`.
- **Why:** Once the web view and the CLI answer only through SQL, a
  second engine is a reference with behaviour of its own (D-032
  found one), costs upkeep on every query change, and answers tests about
  code no user runs. Written expected values check what users see.
- **Consequences:** `ToSQL` is checked against `RunSQL`: the exported SQL,
  run on the same records, must give the rows `RunSQL` gives
  (`TestToSQLDuckDB`, `TestToSQLOverModel`). `TestSpeedAtScale` times only
  the store. A change to `RunSQL` that also changes an answer must now
  change a written expected value, which a reviewer sees.

---

## Open questions

- **O-1 Scope split** — *Resolved 2026-11-23 by D-010 (one design).* Asked
  2026-11-23. Option one:
  this design covers storage plus views, and a second design moves the web
  view's queries to SQL. Option two: both in one design. Claude recommends
  the split. The speed gain needs the SQL part (notes #35).
- **O-2 Author review.** *Overtaken 2026-11-25: the author reviewed the
  v2 spec (D-030), and D-001 to D-006 are superseded.* D-001 to D-008
  haven't been reviewed by the loghorn author. They follow his notes
  except D-001's two-files-by-signal split.
- **O-3 v2 open questions** — *Mostly resolved 2026-11-25: the table
  layout, storage directory and owner by D-030 (one `records` table in
  `loghorn.duckdb` in serve's data folder, serve the only writer);
  retention and archive boundaries by D-028 (days, 3 kept). Still open:
  whether text files stay. Latency measured under the target
  (`docs/PROJECT_STATUS.md`).* Asked 2026-11-24, from the v2 spec, for
  the author: the fixed table layout; the storage directory and owner model (here: `.loghorn/`
  or `-db-dir`, the lock holder); whether text files stay after recovery
  testing; retention and archive boundaries (here: days, 3 kept); query
  latency expectations (target about 500 ms). D-025 to D-029 record what
  this branch does meanwhile.
