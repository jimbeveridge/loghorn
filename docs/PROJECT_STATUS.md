# Project status: DuckDB storage for loghorn

Last updated: 2026-11-27 · Branch `duckdb-v2` (v2 design), built on
`duckdb-storage`

**Status values:** Planned · In progress · Completed (verified) · Failed ·
Blocked. A task is Completed only when the check named next to it has
run and passed.

## v2 design (branch `duckdb-v2`)

Spec: `docs/superpowers/specs/2026-11-23-duckdb-storage-design.md` (v2,
in review on jimbeveridge/loghorn#2). Plan:
`docs/superpowers/plans/2026-11-24-duckdb-storage-v2.md` (superseded in
part by D-030; its note says where). Storage now follows D-030: one
`records` table in one file, `loghorn.duckdb`, in serve's data folder,
written only by `loghorn serve`. D-027 to D-029 were adopted under it
(first import from text, closed days to `duckdb-archive/`, `_raw` as
BLOB). D-031 adds `loghorn query -sql`; D-032 decides search case
folding. D-025 and D-026 are superseded.

### Completed

| Work | Date | Verified by | Ref |
|---|---|---|---|
| v2 spec on the spec PR | 2026-11-24 | Pushed to the fork's `duckdb-storage-spec` | `19b583d` |
| Plan for v2 | 2026-11-24 | Self-review against the spec | `ef02aa6` |
| Tasks 1–4: fixed tables with JSON attributes, raw BLOB, two files, first import, `-db-dir` (as built then; one file and no `-db-dir` since D-030) | 2026-11-24 | New tests failed first; `go test ./...`; all 8055 parity queries (`PARITY_FULL=1`) | `8c36168`, D-025–D-027, D-029 |
| Task 5: closed days to Parquet | 2026-11-24 | Archive tests failed first; `-race` on duckstore, store | `47d97d0`, D-028 |
| Task 6: measurements at 770k records | 2026-11-24 | `LOGHORN_SPEED=1` runs below | `cedf198` |
| Verification | 2026-11-24 | `go vet ./...`; `go test ./...`; `-race` on duckstore, store, web, query, main | `cedf198` |
| Whole-branch review fixes (below) | 2026-11-24 | Each Important finding's test failed first; `go test ./...`; `-race`; all 8055 parity queries | `dc8e65a` |
| Merge upstream `main` (CLI subcommands, `loghorn serve`) | 2026-11-25 | `go vet ./...`; `go test ./...`; `-race` on duckstore, store, serve, query, web, main; all 8055 parity queries | `a21dcaf` |
| One `records` table in `loghorn.duckdb` (D-030) | 2026-11-25 | Tests changed first and failed; `go test ./...`; `-race`; all 8055 parity queries | `bda983e` |
| `loghorn query -sql` (D-031) | 2026-11-25 | Guard, view and CLI tests; `-to-sql` output equals the web view's rows (`TestToSQLOverModel`) | see "SQL from the command line" |
| The 2026-11-27 review's fixes (§1-§3, below) | 2026-11-27 | Each commit names the tests that failed first | `ac7a073`..`b3289df` |
| Post-merge fixes plan, tasks 1-6 (`docs/superpowers/plans/2026-11-27-post-merge-fixes.md`): Go query engine removed (D-033), ids stay unique and serve stays findable when DuckDB reads fail, `-sql` capped at 100,000 rows and 10 s with config locked, duration fast path equal to `ParseDuration`, docs brought up to date | 2026-11-27 | `gofmt`, `go vet ./...`, `go test -race ./...` after each task; reviews per task | `d31a3d2`..this task's commit |

### After merging upstream main (2026-11-25)

- `wrapper.go` and `replay.go` are gone upstream, and with them `-web`,
  `-db-dir` and the lock-holder ownership. `loghorn serve` now owns the
  DuckDB files, in the data folder (`internal/serve/serve.go`,
  `openStore`); if they can't open, serve falls back to a store in memory
  filled from the text, as before. `loghorn open` and `query` keep
  in-memory stores.
- serve tags a run's lines with `service.name` and `loghorn.run.id`
  before recording them, so the stored record matches the line in
  `loghorn.log`: a restart now reads the database, not the text. Two
  serve tests that keyed records by their untagged raw bytes now key them
  by message.
- `query.RunSQL` follows two upstream changes to `query.Run`: graphs echo
  `groupBy`, and error rates, averages and non-integer sums are rounded
  with `Round15`.
- Upstream's multiline detection is kept in the tallies as a `Lines` bit
  in `schema.KindSet`.
- `store.Options.DBDir` and `DeferImport` had no caller after the merge.
  They went with the read-only path in `15b6542` (review §4).

### One `records` table (2026-11-25, D-030)

Logs and spans now share one table, `records`, in `loghorn.duckdb`, and
each closed day is one Parquet archive,
`duckdb-archive/records-YYYY-MM-DD.parquet`. The measurements below were
rerun on it. Files from the two-file build are ignored: the new file
starts again from text.

### SQL from the command line (2026-11-25, D-031)

`loghorn query -sql 'SELECT …'` (or `@file`, `@-`) runs one read-only
query against the running serve and prints its rows (tsv by default; csv,
md, json, jsonl; at most 1,000 unless `-limit`). It reads the CLI query
spec's table model, now real as views over `records`: `logs."<stream>"`,
`spans."<stream>"`, `"*"` of each, and the `loghorn.this_run_*` macros.
`-to-sql` output runs unchanged: Jim's equivalence cases pass over the
views (`TestToSQLOverModel`). Design and plan:
`docs/superpowers/specs/2026-11-25-cli-sql-design.md`,
`docs/superpowers/plans/2026-11-25-cli-sql.md`.

### Review fixes (2026-11-27)

Jim's review of this branch, fixed by section. Each commit names the
tests that failed before its fix.

- §1.1 (`ac7a073`): a record DuckDB refuses, such as a time outside
  1677–2262, no longer sends the session to memory. Such a time is
  stored as NULL (`_raw` keeps it); a failed batch is tried in memory to
  find the cause, then stored row by row, skipping only refused records
  with one warning.
- §1.2 (`0c50f0d`): serve closes the store before it releases the data
  folder's lock, so a quick restart no longer falls back to memory.
- §1.3 (`cc0df89`): the imported mark is the file
  `loghorn.duckdb.imported`, removed when serve opens the files and
  written again only by a clean close of complete files. A crash or a
  session kept in memory makes the next start import the text again.
  The `_meta` table is no longer used.
- §2.1, §2.2 (`521a039`): durations convert exactly as Go's
  `ParseDuration` does, and a number too large for a float (`1e400`) is
  NULL and bad, as `schema.Convert` has it.
- §2.3, §2.4, §2.5 (`b7db2a1`): `error_rate` trims severity as
  `strings.TrimSpace` does; search folds case by Unicode rules (D-032);
  the parity data gains the edge cases the review listed.
- §3 (`b3289df`): DuckDB errors reach the client as 500s, not as 404s,
  zeros or empty lists.
- §4 (`15b6542`): the read-only path nothing called is removed
  (`DBDir`, `DeferImport`, the readers' copy).
- §4 (`ef6583b`): serve opens the store, which may import text, before it
  binds its socket and ports, so a probe meanwhile finds serve not
  running.
- §4 (`ef75169`): one snapshot per query; ids stay unique after a clock
  step; archiving invalidates the schema cache; unreadable hot days no
  longer count rows twice; an in-memory engine spills to its own temp
  folder.
- §5.3 (`24f5fd6`): `loghorn open` and serve's fallback rebuild lines
  with the configured input.
- §6: these doc updates.

### Measurements (about 770k records in one day; MacBook, arm64, 8 CPUs, 16 GB)

`LOGHORN_SPEED=1 go test -run SpeedAtScale -v -timeout 30m ./internal/store`.
Medians of 5 warm runs, through the store as the web view calls it.
Measured 2026-11-25 on the one-table build (D-030); the two-file build's
numbers from 2026-11-24 are in the last column for comparison.

| Request | `records` in `loghorn.duckdb` | After archiving to Parquet | Two-file build, hot |
|---|---|---|---|
| Samples, one stream | 19 ms | 34 ms | 21 ms |
| Samples, filter + sort | 56 ms | 97 ms | 87 ms |
| Search | 36 ms | 64 ms | 85 ms |
| Table, p95 and error rate by status | 145 ms | 153 ms | 310 ms |
| Graph by severity | 54 ms | 90 ms | 90 ms |
| Graph, all kinds | 19 ms | 29 ms | 32 ms |
| Heatmap, db spans | 50 ms | 82 ms | 86 ms |
| Kinds / schema / trace | 3 / 1 / 2 ms | 4 / 1 / 19 ms | 3 / 2 / 3 ms |

- What these numbers cover: a warm database, one day, about 770k
  records, the representative web requests in the table. They do not
  cover `loghorn query -sql`, ranges of several days (which also read the
  Parquet archives), a cold start (first query after open, or the import
  itself: about 20 s), or the in-memory fallback.
- Every representative request is under the ~500 ms target, and the one
  table is as fast or faster than two everywhere. Table p95 is still the
  slowest: the latency field is text (`"0.123s"`) parsed per record.
- Restart with 770k records stored: 20 ms (no text is reparsed).
- Archiving the day at the next day's open: 1.3 s.
- Writing: about 35k records/s.
- Disk: `loghorn.duckdb` 280 MB for the day (two files: 192 + 61 MB); the
  day's Parquet archive is 37 MB (two archives: 24 + 8 MB), ZSTD.
- Peak RSS of the benchmark process: 1.9 GB, with DuckDB capped at 1 GB
  (`memoryLimit`); the two-file build measured 1.7 GB. Spike 2 measured
  about 3 GB for the in-memory store.
- With `query.Run`'s in-memory copy of every record in the same process
  (about 3 GB beside DuckDB), the machine was under memory pressure:
  archiving took 25 s and Parquet queries up to 660 ms (two-file build).
  Real runs don't hold that copy.

### Review findings (2026-11-24, independent review of `duckdb-storage..duckdb-v2`)

As found before D-030. The historical readers (Important 1 and 2), `-web`
and the `_meta` mark are gone since: see "Review fixes (2026-11-27)".

No Critical findings. Fixed:
- Important 1: a historical reader broke when the writer later deleted an
  archive it read. Readers now copy archives into memory too
  (`TestReaderSurvivesExpiredArchive`).
- Important 2: a historical run trusted files a deferred writer had
  started, showing only that run's records. It now reads the text unless
  the files are marked imported (`TestReaderReadsMeta`,
  `TestHistoricalNeedsImportedFiles`).
- Important 3: a partial import was marked complete. The mark now needs
  every text file read (`TestPartialImportIsNotMarked`).
- Important 4: the archive's rename wasn't synced before the day's rows
  were deleted. The directory is synced now.
- Important 5: records skipped for a full queue (as during a long
  midnight archive) were lost from DuckDB with one warning. They are
  counted, warned about at Close, and clear the imported mark so the next
  `-web` run imports them from text (`TestSkippedRecordsUnmarkImport`).
- Minor: an unreadable mark no longer looks like files to start again;
  RFC 3339 strings keep nanoseconds (`tsns`, `tsoff` cases); replacing an
  archive and deleting its rows happen under one view lock; stale doc
  comments fixed; unused `FileOptions.Now` removed.

Left as known limits: below.

### Known limits and gaps

- DuckDB files don't shrink: after archiving, `loghorn.duckdb` keeps its
  size (about the largest day) and reuses the space. Compaction is
  deferred, as the spec allows.
- After a crash, or a serve that couldn't open the files and kept its
  session in memory, the next start imports the retained days' text
  again (D-027): about 20 s at 770k records. Nothing is lost, but that
  start is slower.
- Known value differences: numbers read as text take DuckDB's canonical
  spelling, and arrays read as text keep `<`, `>`, `&` unescaped (D-025).
- DuckDB's file lock is per process, so a second writer is kept out only
  across processes; tests can't check that in-process.
- DuckDB creates a `.wal` with the process umask; loghorn makes it 0600
  after each batch. In a data folder that others can read, a new WAL is
  readable by them until then. serve creates the folder owner-only (0700).
- A string that isn't valid UTF-8 is queried with U+FFFD in place of the
  bad bytes (D-025).
- Search folds case by Unicode rules (D-032).

### Next actions

1. Wait for the author's re-review of jimbeveridge/loghorn#4 (this
   branch; his review's §1 to §6 are fixed, see above) and of the spec
   in jimbeveridge/loghorn#2. Merge only #4: #2's spec is
   in it, and merging both conflicts in this file and `DECISIONS.md`.
2. Ask the author about D-031's open point: `this-run` in the web view
   under serve versus the `this_run_*` macros.
3. Run `loghorn serve` and `loghorn query -sql` by hand on a real project.

## Environment note (user's Mac)

The default SDK (`MacOSX27.0.sdk`) is newer than the Command Line Tools'
linker (26.6), so cgo links of DuckDB fail with "unknown architecture
arm64e.x1". Build and test with
`SDKROOT=/Library/Developer/CommandLineTools/SDKs/MacOSX26.sdk` until
the tools are updated. Anaconda's older `ld` is also ahead of Apple's on
`PATH`.


## First design (branch `duckdb-storage`, jimbeveridge/loghorn#3)

Historical record. The first design (per-day files, a table per stream,
the lock holder as writer, `-web` as reader) was replaced by v2 and then
D-030. `-web` and the lock-holder ownership no longer exist; items below
that name them are kept as they were, and the open items are closed.

### Completed

| Work | Date | Verified by | Ref |
|---|---|---|---|
| Repo review and improvement plan | 2026-11-18 | Code reading plus three reproductions; repo not built | chat only |
| Storage approach chosen (layout B) and first decisions recorded | 2026-11-23 | User answers in chat | `docs/DECISIONS.md` D-001…D-008 |
| Scope chosen: one design for storage and SQL queries | 2026-11-23 | User answer in chat | D-010 |
| Design approved | 2026-11-23 | User approval in chat | `docs/superpowers/specs/2026-11-23-duckdb-storage-design.md`, D-011 |
| Local build environment (Go 1.26.1 + DuckDB 2.0.1) | 2026-11-23 | `go build/vet/test ./...` pass on upstream code | D-013 |
| Implementation plan | 2026-11-23 | Self-review against the spec | `docs/superpowers/plans/2026-11-23-duckdb-storage.md`, `f398a84` |
| Task 1: schema tallies (Infer rebuilt on them) | 2026-11-23 | `go test ./internal/schema` | `8e3ff66` |
| Task 2: `internal/duckstore` engine | 2026-11-23 | `go test ./internal/duckstore` (round trip, widening from `_raw`, bad values, case clashes, conversions vs `schema.Convert`) | `03ebaf3` |
| Task 3: `query.RunSQL` with parity | 2026-11-23 | All 8055 parity queries match `query.Run` (`PARITY_FULL=1`) | `5233a0a`, D-021, D-022 |
| Task 4: web view served from DuckDB | 2026-11-23 | `go test ./...` (25 packages); `-race` on store, duckstore, web, recorder | `c2e38af`, D-023 |
| Task 5: day files, modes, rebuild, rollover, failure | 2026-11-23 | `go test ./...`; smoke run of the built binary (two runs, then `-web` alone over the files) | `25b971e` |
| `go.mod`/`go.sum` completed (`go mod tidy`) | 2026-11-24 | `go build/vet/test ./...` on the user's Mac (macOS 26.6, Go 1.27.4, cgo) | `2633b81` |
| Verification on a normal machine | 2026-11-24 | `go test ./...` (26 packages); all 8055 parity queries (`PARITY_FULL=1`, 2 m 8 s); `-race` on store, duckstore, web, recorder, query, main | `2633b81` |
| Spec sent for the author's review | 2026-11-24 | Spec-only PR open | jimbeveridge/loghorn#2 (branch `duckdb-storage-spec` on the user's fork) |
| Minor 6: `.duckdb.wal` owner-only | 2026-11-24 | `TestDayWALIsOwnerOnly` failed first | `b4fb42b` |
| Minor 1: a day-file batch is one transaction | 2026-11-24 | `TestFailedBatchLeavesNoPartialRows` failed first (records showed twice); `TestDayColumnWidensToStringInTransaction` | `7fc48f0` |
| Minors 10 and 3: plain runs defer rebuilds; `-web` binds before the store | 2026-11-24 | `TestOpenDeferredWriterBuildsNothing`, `TestLiveStoreOptionsDeferBuildWithoutWeb`, `TestWebPortCheckedBeforeRebuild` failed first | `9acef4e`, D-024 |
| Spec PR also carries `docs/DECISIONS.md` | 2026-11-24 | Pushed to the fork's `duckdb-storage-spec` | `f5f7b63` |
| Minor 2: a pair whose stamps disagree on text size is in doubt | 2026-11-24 | `TestStampsDisagreeingOnTextAreInDoubt` failed first | `b0db6c4` |
| Minor 8: SQL trims what Go trims; one parser for rebuilt rows | 2026-11-24 | Tab, newline and Unicode-space cases added to `TestRelationConvertsLikeConvert` failed first | `f375d0f` |
| Minor 9: a rebuilt day's untimed records take times from that day only | 2026-11-24 | `TestRebuiltDayTimesComeFromThatDay` failed first | `556c850` |
| Minors 4 and 5: reads wait out a rollover; late records keep a file | 2026-11-24 | `TestReadsDuringRolloverDontFail`, `TestLateDayRecordStillReachesAFile` failed first | `b7f588d` |
| Minor 7: a request waits for a lagging writer once | 2026-11-24 | `TestReadsWaitOnceWhileWriterIsBehind` failed first | `d59a987` |
| Found while verifying: a batch crossing midnight failed the day file and sent the rest of the run to memory | 2026-11-24 | `TestBatchAcrossMidnightIsWritten` failed first; day tests ×40 under `-race` beside the parity run (failed before) | `93ce5eb` |
| Verification after all fixes | 2026-11-24 | `go vet ./...`, `go test ./...`; `-race` on duckstore, store, web, main; all 8055 parity queries at `d59a987` | — |

### In progress

| Work | Started | State |
|---|---|---|
| Task 6: speed check, docs, whole-branch review | 2026-11-23 | Speed measured (below). Review done: its 4 Important findings, the Critical (`go.sum`) and all 10 Minors fixed; left: try `-web` on a real project. Closed: overtaken by v2 and D-030 (see v2 Next actions) |

### Speed at about 770k records (one day, 2-vCPU cloud sandbox)

Median of 5, DuckDB vs today's `query.Run` on the same records (the old
path's snapshot copy and schema inference are not included, so it was
slower in real use, as the author's 716 ms / 520 ms show).

| Request | DuckDB | `query.Run` |
|---|---|---|
| Samples, one stream | 20 ms | 71 ms |
| Samples, filter + sort | 23 ms | 149 ms |
| Search | 68 ms | 112 ms |
| Table, p95 and error rate by status | 26 ms | 300 ms |
| Graph by severity | 49 ms | 154 ms |
| Graph, all kinds | 128 ms | 165 ms |
| Heatmap, db spans | 32 ms | 60 ms |
| Kinds / schema / trace | 9 / 5 / 19 ms | — |

Writing: about 25k records/s in this sandbox. A burst beyond the
200k-record queue is skipped for DuckDB, marks the day dirty, and is
rebuilt from text at the next start. The logs file for the day is 121 MB.

### Review findings (2026-11-24)

Fixed, each with a test that failed first:
- A saved epoch unit (s/us/ns) on a numeric timestamp was ignored —
  `TestRunSQLTimestampUnitOverride`.
- Queries failed while a column's type changed — `TestQueriesDuringRetype`
  (queries now take a read lock the writer holds across a type change).
- A stored-days `-web` reader held today's file, so the next writer
  couldn't open DuckDB — `TestOpenStoredLeavesTodayToTheWriter`,
  `TestOpenWriterKeepsGoingWhenTodayWontOpen` (readers read today from
  text; a writer that can't open today keeps it in memory).
- Replays and text loads skipped records when the queue filled —
  `TestReplayNeverSkips` (they now wait for room).

Minor (all fixed 2026-11-24, see Completed):
1. ~~A failed batch isn't one transaction (index rows can be orphaned).~~
   Worse than noted: the batch then went whole to memory, so its rows
   showed twice.
2. ~~`ReadStamp` checks only the logs file's text size.~~
3. ~~The `-web` port check runs after a possibly long rebuild.~~
4. ~~Requests can fail briefly at midnight rollover/expiry.~~ Every store
   read now takes the lock the writer holds across rollover.
5. ~~A midnight race can send one record to memory only.~~
6. ~~`.duckdb.wal` files are not chmod 0600.~~
7. ~~Each request can wait up to 2 × 250 ms when the writer is behind.~~
8. ~~`rebuild` makes a parser per row; DuckDB `trim` differs from Go's.~~
9. ~~Untimed records at the end of a rebuilt day aren't backfilled.~~ Each
   day now takes times from its own text only; a day with no time keeps
   its records untimed.
10. ~~Startup rebuild runs even without `-web`.~~ Fixed by deferring, not
    by a background build (D-024).

### Planned (in order)

1. ~~Try `-web` on a real project.~~ Overtaken: `-web` is gone (D-030).
2. ~~After the author answers the spec PR (#2): a PR for the code, from
   the user's fork.~~ Done as #4 (v2).

### Blocked

| Item | Blocks | Needs |
|---|---|---|
| The user's account can't push to `jimbeveridge/loghorn` | Pushing branches there | Nothing now: branches go to the user's fork, PRs from there |

### Failed

None.

### Known limits (not failures)

- "All kinds" views read every stream table, so they slow down as tables
  multiply (128 ms above; D-021).
- Known differences from today: D-011's four, with #1 also covering a field
  whose type differs between services or days read together.
- The author has not reviewed this work (`docs/DECISIONS.md` O-2).

### Next actions

None: see the v2 section's Next actions.
