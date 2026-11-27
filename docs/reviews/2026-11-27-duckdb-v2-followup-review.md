# Follow-up review: `duckdb-v2` after the fixes (PR #4)

Commits `a21dcaf..8772c8e` (everything since 2026-11-25), fast-forwarded into `main`
on 2026-11-27. This review follows `2026-11-27-duckdb-v2-review.md`, which covered
the branch up to `1f3aede`.

## Coverage

| Commits | Content | Depth |
|---|---|---|
| `0cf1323`..`1f3aede` | serve owns the DuckDB files; one `records` table | No new bug hunt. Earlier findings re-checked against the final code. |
| `259249a`..`38f0cac` | `loghorn query -sql` | Full review, with probes. |
| `ac7a073`..`8772c8e` | fixes for the earlier review | Full review: each fix checked, plus a search for new bugs. |
| `a21dcaf` | merge of main into the branch | Conflict resolution checked: nothing from main was lost. It also adds about 18 lines to `internal/query/sqlrun.go` (`metric()`: Round15 for `error_rate`, `avg` and non-integer `sum`; graphs echo `GroupBy`). Those lines look correct. |

`go build ./...`, `go vet ./...` and `go test ./...` pass at `8772c8e`, and so does
`PARITY_FULL=1 TestRunSQLMatchesRun`. The diff and the commit messages contain no
private application names.

## Earlier findings

| Finding | Status |
|---|---|
| 1.1 Out-of-range timestamp | Fixed (`ac7a073`). The time is stored as NULL, and only a bad row is skipped. |
| 1.2 Lock released before DuckDB is closed | Fixed (`0c50f0d`) |
| 1.3 Gaps between DuckDB and the text | Fixed (`cc0df89`). A sidecar mark is written only after a clean CHECKPOINT. |
| 2.1 Duration rounding | Fixed (`521a039`), but slower (new finding 5) |
| 2.2 Non-finite numbers; DECIMAL literals | Fixed (`521a039`) |
| 2.3 Case folding | Kept on purpose and recorded as D-032 |
| 2.4 `error_rate` trimming | Fixed (`b7db2a1`) |
| 2.5 Parity coverage | Partly fixed. Only search and `error_rate` have written expected values. |
| 3 Hidden errors | Fixed (`b3289df`), which causes new finding 1 |
| 4 Spill folder, silent ports, `config.Input`, double count, schema cache, `_id`, snapshot | Fixed |
| 4 Archiving on the writer goroutine; a day that never archives; queue limit counted in records not bytes; no drain on SIGTERM | Not fixed |

## New findings

1. **Important: when `Store.Info()` fails, clients can't find serve.**
   `internal/web/server.go:253` returns 500 from `/api/session` when `Store.Info()`
   fails, for example when a day's archive Parquet file is missing. `sock.Probe`
   then reports that serve isn't answering, so `run`, `status`, `query` and `open`
   all treat it as down. Fix: answer with serve's identity whatever happens, and
   report the count error separately.
2. **Important: a trailing `;` disables `-sql`'s row cap** (`internal/store/sql.go:51-57`).
   The `LIMIT` wrapper can't hold a statement that ends in `;`, so the query runs
   without it and DuckDB builds the whole answer. Measured: 532 ms against 5 ms.
3. **Important: `-limit` has no upper bound** (`internal/web/sql.go:51`,
   `cmd_query.go:277`). Rows are collected as `[]any` and the export is built in a
   buffer, outside DuckDB's `memory_limit`. A huge `-limit` can run serve out of
   memory, and `max+1` overflows at `MaxInt`.
4. **Important: one slow `-sql` query blocks readers for up to 30 s.** The query
   holds `schemaMu.RLock` for its whole run. An archive waiting on the write lock
   then queues every later reader, including the web view.
5. **Important for the 500 ms target: exact duration conversion is about 2.4×
   slower.** About 370 ms over 770k string durations, against 155 ms before, on an
   18-core machine. Most values are a single unit such as `12ms`, so a fast path
   for that case would recover most of the cost.
6. Minor:
   - A crash leads to a ~20 s re-import before serve binds, and a signal during the
     import kills it.
   - The guard collects CTE names without regard to scope, which lets a query read
     the internal `records` (read-only, inside the store).
   - The cli-sql spec offers `SHOW TABLES` for discovery, but it lists tables the
     guard refuses.
   - `lock_configuration` is not set.
   - `Count()` and `MaxSeq()` return 0 on error.
   - A record with a NULL `_time` passes its out-of-range time on to the untimed
     records that follow it.
   - When DuckDB's instance is broken, every batch is retried row by row.

## Compared with the decisions, the vision and the specs

- **D-031 against the 2026-11-26 tables draft.** What ships: `logs."<stream>"` and
  `spans."<stream>"` views, `loghorn.this_run_*` macros, `loghorn.tables`. The draft
  calls for flat conceptual tables, no loghorn-specific macros and a `traces` table.
  Converging is a large rewrite. Owner decision needed.
- **500 ms.** The measurements cover warm queries over one day. Not measured:
  `-sql`, multi-day ranges, cold starts, the memory fallback and the crash
  re-import.
- **"This run".** The web view uses serve's start time (`serve.go:279`); the `-sql`
  macros use the latest run.
- **The in-memory fallback** has no decision of its own.
- **Opening the store before binding** isn't described in CLAUDE.md's startup rule.
- **Stale docs.**
  - README: no `-sql`, and no DuckDB files under "where data lives".
  - `features.md` still says `-web`.
  - Line 52 of the storage spec contradicts D-031.
  - D-030 says D-027 is unchanged, but D-027 was rewritten.
  - The architecture notes (#5) still propose typed columns per stream.
  - `PROJECT_STATUS.md:34` cites "this commit".
  - The cli-sql spec's list of allowed schemas omits `loghorn`.
- **Leak (pre-existing).** `docs/superpowers/plans/2026-11-23-cli-subcommands.md:15`
  names private applications.

## Testing direction

The UI now runs only on DuckDB. `query.Run` has no non-test caller; it is the
reference for `TestRunSQLMatchesRun` and the `-to-sql` tests. Matching it proves
only that the two engines agree. Next steps:

1. Move the behaviour tests with written expected values (about 80, all through
   `run()` in `internal/query/run_test.go`) to `RunSQL`.
2. Turn the parity edge cases into written expected values.
3. Then remove the parity test, `query.Run` and code that only it uses.

## Owner decisions

1. Keep, freeze or replace D-031's SQL surface in light of the tables draft.
2. What "this run" means under serve.
3. Keep the in-memory fallback, or fail and let a supervisor restart serve. Can a
   slow import delay binding?
4. Whether the 500 ms target covers `-sql`, multi-day ranges and cold starts.
5. Retention by days (D-028), or by run count plus size (architecture notes).
