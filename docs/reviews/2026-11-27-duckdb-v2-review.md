# Review: `duckdb-v2` (DuckDB storage)

Branch `duckdb-v2` at `1f3aede`, against `main` at `b412968` (the merge base).
That is 42 files and about 6.4k added lines. `go build`, `go vet` and the tests for
`internal/duckstore`, `internal/store`, `internal/serve`, `internal/query` and
`internal/web` pass.

How this review was done:

- Two independent code reviews: the storage layer, and the query path.
- Every important finding was confirmed with a throwaway probe test in a copy of the
  branch, unless it is marked *unverified*.
- The design was checked against decisions Jim has stated.

## What this review takes as settled

- **One table.** Logs and spans share one `records` table in `loghorn.duckdb`,
  owned by `loghorn serve` and told apart by `_source` (D-030). Docs that describe
  separate `logs`/`spans` schemas, or `logs.duckdb`/`traces.duckdb`, are out of
  date, including the table contract in main's `2026-11-22-cli-query-design.md`.
- **Hot rows vs archive.** Hot rows keep custom fields in the JSON `attributes`
  column. Archived Parquet days are meant to store attributes as separate typed
  columns, and the SQL view will cross the two.
- **Performance.** The target for the first release is **500 ms for every kind of
  result**. The ~770k-record measurements (worst case is the Table view at about
  150 ms) are inside it.
- **Re-parsing `_raw`.** Rows are rebuilt from `_raw` for display as a stopgap
  while the DuckDB schema is still being decided. That is not a defect in itself.
- **Waiting on the schema.** The schema and the SQL surface are still open, so
  this review does not judge the column layout. It lists the questions the schema
  has to answer (§5).
- **Handed off.** The trace-aggregate view scans everything because the service
  and time range never reach the store (`AggregateRecords(name)`,
  `internal/store/store.go:406`, called from `internal/web/server.go:592`). That
  fix belongs to the UI and has gone to the `fix-trace-aggregate-view` session.

## 1. Data-loss bugs: fix now

These are independent of the schema decision.

### 1.1 Critical: one out-of-range timestamp breaks storage for days

`internal/duckstore/writer.go:21-35`, and `:132` (`_time TIMESTAMP_NS`).

`TIMESTAMP_NS` only covers the years 1677 to 2262. Take a record with
`"timestamp":"3000-01-01T00:00:00Z"`, or a `9999-12-31` sentinel:

1. `AppendRow` fails, and the whole appender batch fails with it (up to 20,000
   records).
2. The error is treated as a file failure. `failed=true`, and the rest of the
   session goes to the in-memory store only.
3. The retry into memory fails the same way, so the bad record and the rest of
   its batch vanish from the view.
4. At Close, `imported=0` is set. The next start wipes the files and re-imports
   the text, which still contains the bad line. The import fails again and the
   mark is never set.
5. Every restart for the retention window (about 4 days) re-imports into memory
   only.

The probe confirmed this: a good record plus a far-future record left
`failed=true`, the good record was lost, and `imported="0"` after reopening.

Fix:

- Clamp an out-of-range `_time` in `row()`, or store it as NULL. The original
  value stays in `_raw`.
- More generally, tell per-row data errors apart from I/O errors before switching
  the session to memory.

### 1.2 Important: serve releases the folder lock before DuckDB is closed

`internal/serve/serve.go:204`:
`s.closeErr = errors.Join(s.logs.Close(), s.st.Close())`.

Go evaluates the arguments left to right. So `logs.Close()` releases the
data-folder flock first, and only then does `st.Close()` drain the write queue
(up to an hour through `Flush`) and checkpoint.

The failure: a serve that restarts in that window gets the folder lock, but its
`ATTACH` loses to DuckDB's per-process file lock. It falls back to a memory store
without saying so. That session's records never reach DuckDB, and because
`imported` is still `1`, they are never imported later.

This breaks the CLAUDE.md rule that the lock holder owns the data folder. Fix:
close the store before the log writer.

### 1.3 Important: gaps between DuckDB and the text are never filled

`internal/store/open.go:105-134`, `internal/serve/serve.go:256-269`.

The `imported` mark is cleared only by a clean Close that saw skips or failures.
DuckDB ends up short of the text while the mark still reads `1` when:

- serve crashes or is killed while up to 200k records are queued;
- a session falls back to memory because `openWriter` failed (§1.2, or a
  user's `duckdb` CLI holding the file).

Now that serve is a long-lived daemon, these gaps become permanent from the
user's point of view. D-027 mentions the crash case but not the fallback case.

Fix:

- Keep a per-day high-water mark (the highest `_id`, or the byte offset into the
  text) and check it at startup.
- Or clear the mark at open and set it only at a clean Close.

A second pass (`/code-review high`) found a further case: when a file write
fails, `closeFiles` tries to set `imported=0` in the same broken file
(`internal/duckstore/files.go:206`). If that write fails too, the mark stays at
`1`. Wherever the staleness marker ends up, it has to live somewhere that can
still be written when the DuckDB file can't, for example a sidecar file in the
data folder.

## 2. Conversion bugs: fix before `query.Run` stops being the reference

`RunSQL` is supposed to answer exactly as `query.Run` does. Each of these was
found with a probe that runs the same records through both. Each will survive any
schema choice, because it lives in the SQL conversion helpers.

### 2.1 Important: durations come out one rounding step away from Go

`internal/duckstore/sqltype.go:124-129` (`durationSQL`), reached through
`internal/query/sqlrun.go:208-212`.

SQL computes `N * 1e-3` (or `1e-6`, `1e-9`). Go uses
`time.ParseDuration(s).Seconds()`, which works in whole nanoseconds.

- 719 of the 5,000 `Nms` strings differ, and 4,148 of 15,000 across
  ms/us/ns.
- `"5us"` gives `4.9999999999999996e-06` in SQL and `5e-06` in Go, so the filter
  `d = 5us` matches in `Run` and not in `RunSQL`.
- Compound values (`"0.1s0.2s"`) differ too.
- Range comparisons at an exact boundary and graph group labels are affected the
  same way.

Fix: parse to whole nanoseconds, then divide once.

### 2.2 Minor: a non-finite number counts as a valid value

`internal/duckstore/sqltype.go:56-57`, `internal/duckstore/read.go:84`.

DuckDB reads JSON `1e400` as DOUBLE infinity, while Go's `Convert` marks it Bad.
In the probe, with values `{1e400, 2.5, 1.5}`:

| Query | `Run` | `RunSQL` |
|---|---|---|
| Filter `f > 2` | 1 match | 2 matches |
| `sum(f)` | 4 | empty |
| Sort | bad value last | bad value first |

One such record blanks every sum and average in its group. Fix: add an
`isfinite` check.

### 2.3 Minor: search folds case differently

`internal/query/sqlrun.go:151-153` and `internal/query/run.go:380`.

DuckDB's `lower()` follows Unicode rules, while Go's `containsFold` folds only
ASCII for an ASCII search term. Searching `istanbul` finds `İSTANBUL` in SQL but
not in Go. Decide which is right and write it down. The Unicode behaviour is
probably the better one.

### 2.4 Important: `error_rate` trims only spaces in SQL

`internal/query/sqlrun.go:355`. Found by the `/code-review high` pass.

- SQL uses `upper(trim(sev))`, and DuckDB's one-argument `trim` removes only
  spaces.
- Go's `isErrorSeverity` uses `strings.TrimSpace`, which also strips tabs,
  newlines and Unicode spaces.
- A severity of `"ERROR\n"` or `"\tERROR"` therefore counts as an error in `Run`
  but not in `RunSQL`. Table `error_rate`, and ranking by it, differ.

Fix: use duckstore's existing `trimSQL`, which strips what Go strips.

A related nit: `durationSQL` and `epochFloatSQL`
(`internal/duckstore/sqltype.go:125`) write bare `1.0`, `60.0` and `3600.0`
literals. In DuckDB these are DECIMAL, which CLAUDE.md says must be cast to
DOUBLE. The results are exact today only because the CASE mixes them with a
DOUBLE `1e-9`. Fix this together with §2.1.

### 2.5 Why the parity tests missed these

`internal/query/parity_test.go:367, 396-454`:

- By default the tests run every 10th of the 8,055 queries.
- `knownLossy` skips every documented difference.
- The generated data uses only durations that happen to round alike, has no
  non-finite numbers, and no non-ASCII case folding beyond ü and ï.

Before `query.Run` is deleted, its role as reference should become written
expected values with generated edge cases for each type. Otherwise the SQL
behaviour becomes the spec by default, bugs included. Main's `-to-sql` tests
(`tosql_duckdb_test.go`, `tosql_views_test.go`) also compare against `Run` and
need the same treatment.

Nothing in production calls `query.Run` or `schema.Infer` any more. They are
test references only (D-016), so removing them is mechanically easy once the
tests are replaced.

## 3. Hidden errors

`internal/store/store.go:204-249, 372-401`, `internal/web/server.go:493`.

- `Record`, `SpanRecords`, `TraceRecords` and `AggregateRecords` discard their
  DuckDB error (`recs, _ :=`). A broken Parquet archive or an I/O error therefore
  shows up as a 404 such as "no record with that seq".
- `Info`, `Streams` and `Kinds` return zeros or empty lists on error.
- Every `Store.Query` error is returned as 400 Bad Request, including DuckDB
  failures and cancelled requests. Before this branch, `Run` only failed
  validation.

Fix: return the errors, and map engine failures to 5xx.

## 4. Minor

- **Temp directory** (`internal/duckstore/engine.go:106,124`). DuckDB's
  `temp_directory` is `.tmp`, relative to serve's working directory. Large sorts
  spill into whatever repo serve was started from, or fail under a supervisor
  whose working directory is `/`. Fix: put it under the data folder.
- **Archiving on the writer goroutine** (`internal/duckstore/writer.go:22`,
  `internal/duckstore/archive.go:146-186`). A day is exported, validated and
  deleted while holding `wmu`. On a busy day, live ingest can overflow the 200k
  queue, which clears `imported` and forces a full re-import on the next start.
  *Unverified.*
- **A day that never archives** (`internal/duckstore/archive.go:290-308`). If
  archiving keeps failing, that day's hot rows are never deleted, so the file
  grows without bound. Its `_fields` tallies still expire, so the schema stops
  showing fields that visible rows still have.
- **Rows can show twice** (`internal/duckstore/archive.go:116-131`). If the
  `hotDays` query fails, it returns an empty set. An archive that overlaps hot
  rows after a crash then counts twice in `all_records`.
- **Silent ports at startup** (`internal/serve/serve.go:126-175`, `:152`). This
  is more serious than minor.
  - The ports are bound but not served while `openStore` imports up to 4 days of
    text, and a full import also runs on any start after the mark is cleared.
  - At about 35k records/s, 770k records take about 20 s.
  - `sock.Probe` gives up after 2 s with an error other than "not running", so
    `loghorn run` and `status` report serve as broken during an upgrade's first
    start.
  - Fix: serve first and import in the background (reads already wait on
    `Sync`), or import before binding.
- **Schema cache after midnight** (`internal/duckstore/writer.go:22`). `write`
  bumps `version` before the deferred midnight `expireArchives` deletes tallies.
  A schema read in between caches the expired day's fields until the next write.
  Fix: bump `version` again after archiving.
- **`_id` after a clock step** (`internal/store/open.go:151-156`). Only today's
  sequence is seeded. If the clock was ahead in an earlier run, ids for that later
  day can collide. *Unverified.*
- **No snapshot within a request** (`internal/query/sqlrun.go`). A request sends 2
  to 4 statements through the connection pool with no transaction. During live
  ingest, counts, pages, graph groups and heatmap totals can disagree. Fix: one
  connection and a read-only transaction per request. *Unverified.*
- **Shutdown and adoption.** Nothing drains or checkpoints on SIGTERM, beyond
  what Close does. Records serve adopts from `run`'s `incoming/` folder have no
  documented path into `records`, and adopting one for an already-archived day is
  untested.
- **Dead code.** The read-only path (`copyAndDetach`, `copyArchives`, copying the
  meta) and the `DBDir`/`DeferImport` options have no production caller. Their
  comments still describe `-web` and runs that own the files
  (`internal/store/open.go:35-40`, `internal/duckstore/files.go:17-22`). Delete
  them, or say who the reader is meant to be.
- **Queue limit counts records, not bytes.** 200k queued records with `Fields`
  maps and `Raw` can reach hundreds of MB outside DuckDB's 1 GB limit.

## 5. Design concerns to settle with the schema

1. **Two sources of truth, and an all-or-nothing reconcile.** The text files stay
   authoritative (D-007), and DuckDB is reconciled with them by a single
   `imported` bit followed by a full wipe and re-import. §1.1, §1.2, §1.3 and the
   archiving item in §4 all trace back to this. Either reconcile per day,
   incrementally, or stop treating text as the source of truth.
2. **The conversion rules exist three times:** `schema.Convert` (Go), the SQL
   helpers (`conv`, `jsonConv`, `durationSQL`), and `query.Run`. The planned
   archive with typed columns adds a fourth place, the code that writes those
   columns. One definition, for example SQL macros used by both the archiver and
   the views, would keep hot rows and archived rows consistent, and is what lets
   `query.Run` go.
3. **Filters and display read different sources.** Filters read the stored
   `attributes`, while display re-parses `_raw` with serve's *current* input
   config, not the run's. The probe confirmed it: a run with `severity=string`
   stores `DEFAULT` and displays `ERROR`. That is acceptable as a stopgap, but the
   final schema should display rows from the stored fields.

   The `/code-review high` pass found that some of this is a plain bug rather
   than a stopgap, because the engine itself is built with the wrong config:
   - `loghorn open` builds its store with `store.New`, whose Input is zero, while
     its recorder normalizes with `cfg.Input` (`helpers.go:77`).
   - serve's fallback does the same, then preloads with `o.Config.Input`
     (`internal/serve/serve.go:267`).

   Passing the real Input to `NewWithInput` in both places is a one-line fix each,
   and is worth making now.
4. **Questions the schema has to answer:**
   - one view, or one view per stream, given that the same field name can have
     different types in different streams;
   - how dotted paths become column names, and what happens when a field collides
     with `_time`, `_source` and the other fixed columns;
   - how one type per field is chosen across days and streams, when hot rows hold
     JSON and archived days hold typed columns (Parquet columns can be missing or
     typed differently from one day to the next);
   - how the archiver validates an export once `attributes` is split into columns
     (today it compares an XOR hash of the JSON);
   - whether the text files stay a source of truth (item 1).
5. **SQL from the CLI.** DuckDB lets only one process open the file, so
   `loghorn query -sql` must go through serve or query copied archives. Running
   arbitrary SQL inside serve needs:
   - a read-only connection;
   - outside file access turned off except for the archive folder;
   - a timeout and a memory limit, so one bad query can't stall ingest.

## 6. Docs

- **The spec** (`docs/superpowers/specs/2026-11-23-duckdb-storage-design.md`)
  still calls itself a first-pass proposal and lists items as "Confirm with
  Jim" that D-030 records as decided.
- **Plan v2** (`docs/superpowers/plans/2026-11-24-duckdb-storage-v2.md`)
  describes `logs.duckdb`/`traces.duckdb`, per-signal Parquet files, `-db-dir`,
  the lock holder, and `wrapper.go`/`replay.go`.
- **Plan v1** is fully superseded, but not marked as such.
- **`docs/DECISIONS.md`**: D-001 to D-006, D-014, D-017 and D-021/22 are not marked
  superseded. D-025's heading says it is superseded, but its Status still says
  Proposed.
- **`docs/PROJECT_STATUS.md` and `docs/AGENT_HANDOFF.md`** still name the
  removed `-web` and `-db-dir` flags (about 15 places), name `logs.duckdb`, and
  say "D-025 to D-029 proposed". The handoff never mentions D-030.
- **The archive folder** has three names: `duckdb-archive/` (D-028), `archive/`
  (plan v2), and main's notes, which use a layout per stream.
- **Main's cli-query spec**: its `logs`/`spans` table contract is out of date now
  that there is one table.

## 7. What looks sound

- **No SQL injection.** Values are bound parameters. Field paths go through
  `lit(jsonPointer(...))`, which doubles quotes, and operators come from fixed
  sets.
- **Archive order:** write `.tmp`, validate, fsync, rename, fsync the directory,
  then delete under `schemaMu`, with overlap handled after a crash.
- **Midnight rollover and a batch that crosses midnight.**
- **Query bounds match `Run`:** inclusive `Range.To`; heatmap cells (`from <=
  time < to`, `lo` inclusive, `hi` exclusive, no `lo` for the lowest band); bucket
  starts computed in Go and joined with ASOF; DECIMAL literals cast to DOUBLE.
- **`*` handling:** the service `*` and the stream `*` are handled separately in
  `Relation`.
- **One query path for the CLI and the web view:** `cmd_query.go` posts to serve's
  `/api/query`, so the two can't drift.
- **Falling back when DuckDB won't open** uses an in-memory DuckDB, not a second
  engine.
- **The WAL** is chmod 0600, and day numbering is consistent between local time
  and UTC.

## Suggested order

1. Fix §1.1 and §1.2 now. Both are small and both lose data.
2. Fix §2.1, §2.2 and §2.4, and decide §2.3. Pass the real Input in `open` and
   in the serve fallback (§5.3), and serve before importing (§4).
3. Fix §3 (hidden errors).
4. Settle the schema (§5), then write the SQL spec. That spec should include the
   §1.3 reconcile decision, retargeting `-to-sql`, and retiring `query.Run` and
   `schema.Infer` once their tests have written expected values.
5. Clean up the docs (§6) as part of the schema work.
