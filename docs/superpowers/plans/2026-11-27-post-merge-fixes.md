# Post-merge fixes for the DuckDB storage work: implementation plan

> **For agentic workers:** execute with the `orchestrating-plans` skill. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the follow-up review's findings, and replace the "DuckDB matches
`query.Run`" parity testing with tests that state the expected answers and run
on the DuckDB engine the product uses.

**Architecture:** The web view, `open`, `query` and `-sql` all run on DuckDB
(`query.RunSQL`, `internal/duckstore`). `query.Run` (Go, in memory) has no
caller outside tests. Tasks 1–2 move its behaviour tests to `RunSQL` and then
delete it. Tasks 3–5 are independent fixes. Task 6 updates the docs.

**Tech Stack:** Go 1.x, DuckDB via `github.com/duckdb/duckdb-go` (cgo).

**Spec:** `docs/reviews/2026-11-27-duckdb-v2-followup-review.md`. Background:
`docs/reviews/2026-11-27-duckdb-v2-review.md`, `docs/DECISIONS.md` (D-016,
D-030–D-032), `docs/superpowers/specs/2026-11-25-cli-sql-design.md`.

## Global Constraints

- Work on `main` and commit each task directly to it: no branch, no merge commit, no push.
- loghorn is public. Never write a private app's identifiers into code, tests, docs or commit messages. Before each commit, run `git diff --cached | grep -inE 'auction|anvil|nworth|\btaa\b'`; it must print nothing.
- Follow `/Users/jim/code/loghorn/CLAUDE.md`. In particular:
  - Tests set `LOGHORN_SOCKET`, `LOGHORN_DATA_DIR` and `HOME` to temp paths.
  - Socket dirs come from `os.MkdirTemp("/tmp", …)`.
  - Binary tests append `noAgent`.
  - Unset `AI_AGENT` for `duckdb` processes.
  - Number values in records are `json.Number`.
- Every task ends green with what CI runs: `gofmt -l .` prints nothing, `go vet ./...` passes, and `go test -race ./...` passes.
- End every commit message with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BwT7CJ73nwPq8FqGjRaqCX
  ```
- **Out of scope** (these wait for owner decisions; don't touch them):
  - D-031's SQL surface and the 2026-11-26 tables draft.
  - What "this run" means under serve.
  - Keeping the in-memory fallback.
  - Importing before binding, and signals during that import.
  - Retention.
  - The earlier review's archiving and queue items (§4: archiving on the writer goroutine, a day that never archives, the queue limit, no drain on SIGTERM).

## Review Focus

1. **A behaviour test changed its expected value during the move to DuckDB.** That must be a deliberate, justified change, recorded in the commit message, never a silent edit to make a test pass.
2. **Deleting `query.Run` must not delete code that `RunSQL`, `ToSQL`, `OpsFor` or the web view still use.** The build catches calls but not behaviour, such as a shared helper whose rounding moved.
3. **`-sql` with a trailing `;`, a trailing comment, `-limit` above the cap, or a CTE that shadows an internal table must be capped or refused**, never run unbounded or allowed through.
4. **`/api/session` must answer 200 with serve's identity when counting records fails.** `sock.Probe` depends on it.
5. **The duration fast path must give exactly what `schema.ParseDuration` gives**, including for overflow (NULL), `0`, a leading `+` or `-`, and units `µs`/`μs`.

---

### Task 1: Move the query behaviour tests to DuckDB and drop the parity test

**Model: opus. Review: full.**

**Files:**
- Create: `internal/query/backend_test.go`. Move `backend`, `newBackend` and `(backend).schema` here from `parity_test.go`.
- Modify: `internal/query/run_test.go:38-45`. The `run()` helper calls `RunSQL` on a backend built from `recs`.
- Modify: `internal/query/run_test.go:160`, plus any other direct `Run(` call in a behaviour test file (aggregate, graph, heatmap, order, top, round, query tests).
- Modify: `internal/query/parity_test.go`. Remove `TestRunSQLMatchesRun` and the helpers only it uses (`parityQueries`, `operands`, `knownLossy`, `compareJSON`, `parityStreams`, …). Keep `BenchmarkRunSQLSmall`, `TestSearchFoldsUnicodeCase`, `TestErrorRateTrimsLikeGo` and `TestRunSQLTimestampUnitOverride`. Rewrite the last two so they assert written values rather than `Run`'s answer.
- Create: `internal/query/edge_test.go`, holding written expected values for the parity data's edge cases.

**Interfaces:**
- Produces:
  - `func newBackend(t *testing.T, recs []record.Record) backend` (test-only, package `query_test` or `query`, whichever the moved code was in; keep it where the behaviour tests can call it).
  - `run(t, recs, s, q) Result`, with the same signature, now on `RunSQL`.
  - After this task, no test outside `tosql_*_test.go` and `internal/store/bench_test.go` calls `query.Run`.

- [ ] **Step 1: Repoint `run()`.**
  - `run()` builds `newBackend(t, recs)` and calls `RunSQL(context.Background(), b, s, q, t0, t0.Add(-time.Hour))`.
  - The behaviour tests build schemas by hand with `Stream: "a"`. Make sure the records reach the same stream and source in the backend (stamp missing `Stream`/`Source`/`Service` in the helper if needed), so the tests still exercise the same rows.
  - If the package split (`query` vs `query_test`) blocks the move, put the helper where both can use it.
- [ ] **Step 2: Run `go test ./internal/query/` and sort the failures.**
  - For each one, decide which answer is right. The written expected value is the spec unless a decision overrides it: D-032 (Unicode case folding) is the known one.
  - Fix `sqlrun.go` when DuckDB is wrong. Change the expected value only when a decision overrides it.
  - List every changed expected value, and why, in the commit message.
- [ ] **Step 3: Write `edge_test.go`.**
  - Use the records from `parityLines()`, moved into `edge_test.go` so they outlive `parity_test.go`.
  - Write one table test per edge class, with exact expected rows or values (compute each by hand or from the Go stdlib function that defines it, e.g. `time.ParseDuration`, never from `query.Run`):
    - (a) an out-of-range timestamp (NULL time, still returned);
    - (b) infinite and huge numbers in sum/avg/min/max;
    - (c) string durations: fractional, multi-part and overflowing ones, filtered and sorted;
    - (d) a graph across the America/New_York DST change (2026-03-08) with bucket starts;
    - (e) a field with mixed int, float and string values, filtered as each type;
    - (f) saved-type overrides: the parity test's two `overrides` sets;
    - (g) a missing field: filters with `exists` and with `missing`;
    - (h) inferred schemas: the expected `schema.Schema` for two parity streams, from `backend.schema`.
- [ ] **Step 4: Delete `TestRunSQLMatchesRun` and its helpers.** Rewrite `TestErrorRateTrimsLikeGo` and `TestRunSQLTimestampUnitOverride` with written expected values.
- [ ] **Step 5: Verify.** Run `go test -race ./internal/query/ ./internal/store/`. Expected: PASS. Also `grep -n 'query.Run(\|[^L]Run(recs' internal/query/*_test.go` must match only in `tosql_*_test.go`.
- [ ] **Step 6: Commit** with the message `test(query): behaviour tests run on DuckDB; written edge cases replace the parity test`.

### Task 2: Remove `query.Run` and the Go-only engine

**Model: opus. Review: full.**

**Files:**
- Modify:
  - `internal/query/run.go`: delete `Run` and every helper that has no remaining caller.
  - `aggregate.go`, `graph.go`, `heatmap.go`, `top.go`, `round.go`: likewise.
- Modify: `internal/query/tosql_test.go`, `tosql_views_test.go`, `tosql_model_test.go`, `tosql_duckdb_test.go`. Compare `ToSQL`'s SQL output against `RunSQL`'s result on the same records (newBackend), not against `Run`. This keeps the check that matters: the exported SQL gives what the UI shows.
- Modify: `internal/store/bench_test.go:110`. Drop the `query.Run` measurement.
- Modify: `internal/schema`. If `schema.Infer` has no caller outside tests after Task 1, delete it and its Go-only tally code. Keep anything the DuckDB inference path (`TallyType`, `Classify`, `KindSet`, `Lines`) or the web uses.
- Modify: `docs/DECISIONS.md`. Mark D-016 superseded by a new decision **D-033 "Behaviour tests state expected answers; the Go engine is removed"**, dated 2026-11-27. Under Consequences, say that `ToSQL` is checked against `RunSQL`. Update D-032's Consequences line about `query.Run`.
- Modify: `docs/AGENT_HANDOFF.md:48` and `docs/PROJECT_STATUS.md`, wherever they call `query.Run` the oracle.

**Interfaces:**
- Consumes: `newBackend` from Task 1.
- Produces: no `query.Run` and no `schema.Infer` (if they were removed). `RunSQL`, `ToSQL`, `OpsFor`, `Round15` and `Validate` are unchanged.

- [ ] **Step 1: Retarget the four `tosql_*` tests** to `RunSQL`, then run them. Expected: PASS. Fix `ToSQL` where it differs, and note it in the commit.
- [ ] **Step 2: Delete `Run`, then repeatedly remove functions that have no caller.**
  - Check each function with `grep -rn '\bName\b' --include='*.go' .` before deleting it. Callers in `_test.go` files count only when the test checks behaviour that still exists.
  - Use `go build ./... && go vet ./...` after each round.
- [ ] **Step 3: Remove `schema.Infer`** if it is now unused, as above.
- [ ] **Step 4: Update the docs** as listed above.
- [ ] **Step 5: Verify** with `gofmt -l .`, `go vet ./...` and `go test -race ./...`. Expected: clean, PASS. Also `grep -rn 'query\.Run(' --include='*.go' .` must print nothing.
- [ ] **Step 6: Commit** with the message `refactor(query): remove the Go query engine; ToSQL is checked against RunSQL`.

### Task 3: Keep serve findable and store ids safe when DuckDB reads fail

**Model: opus. Review: full.**

**Files:**
- Modify: `internal/web/server.go:253-266`, `sessionJSON`. When `Store.Info()` errors, answer 200 with every identity field (pid, ports, socket, data dir, version, mode, run start), zero counts, and a new field `Error string \`json:"error,omitempty"\`` holding the message.
- Modify: `internal/duckstore/read.go:175-191`. Change the signatures to `func (e *Engine) MaxSeq(day int) (int, error)` and `func (e *Engine) Count() (int, error)`.
- Modify: `internal/store/open.go:71,120,130`, callers of `Count`.
  - In `openWriter`, a `Count` error fails the open (return the error). Never import on top of rows it couldn't count.
  - In `importText`, a `Count` error reports the import as not complete.
- Modify: `internal/store/store.go:149-153`, a caller of `MaxSeq`. On error, warn through the engine's warn function and leave `s.seq[day]` unset, so the next record asks again. Use a fallback counter for this record that can't collide with a stored id: the highest id seen this session for that day.
- Modify: `internal/store/store.go:~177`. Set `s.last = r.Time` only when `r.Time` is within the range the writer stores. Reuse the bound check that `writer.go:139-147` applies, exported as a small helper in duckstore if needed. Untimed records after an out-of-range one inherit the previous valid time.
- Modify: `internal/duckstore/writer.go:26-58`, `write`/`writeRows`.
  - When the file part fails a batch and memory refuses it too, and the first single-row retry fails with the same error text as the batch, stop retrying that batch row by row.
  - Then warn once per distinct error (deduplicate warnings for the session), and count the records as skipped.
- Test: `internal/web/errors_test.go`, `internal/store/store_test.go`, `internal/duckstore/engine_test.go`.

- [ ] **Step 1: Write the failing tests.**
  - `TestSessionAnswersWhenCountFails`: use `httptest.NewServer` and make `Info()` fail (e.g. remove a day's archive Parquet, or a store test hook). GET `/api/session` gives 200, `pid != 0`, `port != 0`, and `error != ""`.
  - `TestSeqSurvivesMaxSeqError`: ids stay unique when `MaxSeq` fails.
  - `TestUntimedAfterOutOfRangeKeepsValidTime`: a record at year 3000, then an untimed one. The untimed one's stored `_time` equals the last valid time, not NULL.
  - `TestRowRetryStopsOnSameError`: use `rejectRow`/`failWrites` hooks that fail every write. One warning, not one per batch, and no per-row loop over the whole batch.
- [ ] **Step 2: Run them.** Run `go test ./internal/web/ ./internal/store/ ./internal/duckstore/ -run 'Session|Seq|Untimed|RowRetry'`. Expected: FAIL.
- [ ] **Step 3: Implement** as listed under Files.
- [ ] **Step 4: Verify.** Run `go test -race ./internal/... .`. Expected: PASS, including `sock.Probe` users in the root package tests.
- [ ] **Step 5: Commit** with the message `fix(serve,store): serve stays findable and ids stay unique when DuckDB reads fail`.

### Task 4: Bound and tighten `-sql`

**Model: opus. Review: full.**

**Files:**
- Modify: `internal/store/sql.go`.
  - `SQLTimeout` becomes `10 * time.Second`.
  - Before wrapping, strip trailing whitespace and `;` (repeatedly).
  - If the wrapped query fails `CheckReadOnly`, return the error `the query must be one SELECT; remove trailing text after it`. Never run the unwrapped text.
- Modify: `internal/web/sql.go`. Add `const MaxSQLLimit = 100000`. A limit above it gets 400 `limit must be at most 100000`.
- Modify: `cmd_query.go:277`. Check the same bound client-side, with a usage error that has the same text and uses `web.MaxSQLLimit`.
- Modify: `internal/duckstore/guard.go:65-131`. Treat a CTE name as defined only within the WITH clause's own query node and its children, not across the whole statement. A `BASE_TABLE` reference outside that scope is checked against the model's allowed schemas as usual.
- Modify: `internal/duckstore/engine.go:123-150`, `harden()`. Run `SET lock_configuration = true` last. First confirm that nothing sets configuration after `harden` (`grep -n 'SET ' internal/duckstore/*.go`); move any later SET before it.
- Modify: the `-sql` help text in `cmd_query.go:32-60`. Discovery is `SELECT * FROM loghorn.tables` and `DESCRIBE logs."<stream>"`, not `SHOW TABLES`.
- Test: `internal/store/sql_test.go`, `internal/duckstore/guard_test.go`, `internal/web/sql_test.go`, `cmd_query_sql_test.go`, `internal/duckstore/engine_test.go`.

- [ ] **Step 1: Write the failing tests.**
  - `TestSQLTrailingSemicolonKeepsCap`: `SELECT range FROM range(1000000);` with max 10 gives 11 rows read at most (the store reads max+1), and `Truncated` is reported as today.
  - `TestSQLTrailingTextRefused`: `SELECT 1; SELECT 2` gives an error.
  - `TestSQLLimitAboveMax`: POST limit 100001 gives 400 with the text above.
  - `TestGuardCTEScope`:
    - `SELECT * FROM (WITH records AS (SELECT 1) SELECT 1), records` is refused.
    - `WITH a AS (SELECT 1) SELECT * FROM a` is allowed.
    - `WITH a AS (SELECT 1) SELECT * FROM (SELECT * FROM a)` is allowed.
    - `WITH a AS (SELECT 1), b AS (SELECT * FROM a) SELECT * FROM b` is allowed.
  - `TestConfigLockedAfterHarden`: `SET memory_limit='2GB'` on `e.DB()` errors.
  - CLI: `-sql 'SELECT 1' -limit 100001` exits with a usage error.
- [ ] **Step 2: Run them.** Run `go test ./internal/store/ ./internal/duckstore/ ./internal/web/ . -run 'SQL|Guard|ConfigLocked'`. Expected: the new ones FAIL.
- [ ] **Step 3: Implement** as listed under Files.
- [ ] **Step 4: Verify.** Run `go test -race ./...`. Expected: PASS.
- [ ] **Step 5: Commit** with the message `fix(sql): cap -sql's rows and time, scope CTE names, lock DuckDB's config`.

### Task 5: Fast path for single-unit durations

**Model: opus. Review: full.**

**Files:**
- Modify: `internal/duckstore/sqltype.go:130-148`, `durationSQL`.
  - Before the general case, add a branch: when `s` fully matches `[-+]?[0-9]{1,19}(ns|us|µs|μs|ms|s|m|h)`, compute `d = CAST(digits AS HUGEINT) * unit`.
  - If `d` is within the same signed limit as the general path, return the same `secs` expression (negated for a leading `-`). Otherwise return NULL.
  - Bare numbers and `0`/`+0`/`-0` keep their existing branches.
- Test: `internal/duckstore/sqltype_test.go`, `internal/duckstore/bench_test.go` (create it if there is none).

- [ ] **Step 1: Write the failing benchmark and the tests.**
  - `TestDurationSQLMatchesGo`: for each string, the result equals `schema.ParseDuration` (value and validity). Cases:
    - a table of edge strings: `0`, `+0`, `-0`, `1ns`, `-1ns`, `+12ms`, `9223372036854775807ns`, `9223372036854775808ns` (invalid), `2562047h` (valid), `2562048h` (invalid), `12µs`, `12μs`, `1.5s`, `1h2m`, `12`, `1e3`, `ms`, ``;
    - 50,000 generated single-unit strings from a seeded `math/rand` (seed 1).
  - `BenchmarkDurationSQL`: 1,000,000 rows of single-unit integer strings in an in-memory engine; time `SELECT sum(<durationSQL>)`. Record its result before and after in the commit message. Target: at least 2× faster than before.
- [ ] **Step 2: Run them.** Run `go test ./internal/duckstore/ -run DurationSQLMatchesGo` (expected: PASS before the change, establishing the reference) and `go test ./internal/duckstore/ -run '^$' -bench DurationSQL -benchtime 3x`.
- [ ] **Step 3: Implement the fast path.**
- [ ] **Step 4: Verify.**
  - Run the same test (expected: PASS) and the same benchmark (expected: at least 2× faster).
  - Run `go test -race ./internal/duckstore/ ./internal/query/ ./internal/store/`. Expected: PASS.
- [ ] **Step 5: Commit** with the message `perf(duckstore): fast path for single-unit durations, exact as Go`.

### Task 6: Bring the docs in line with what shipped

**Model: sonnet. Review: none (docs only).**

**Files:**
- `README.md`:
  - Add `-sql` to the feature list and the Query section, and add it to the `query` row of the flags table (with the 100,000-row cap and 10 s timeout from Task 4).
  - In "Where data lives", add `loghorn.duckdb`, `duckdb-archive/` and `loghorn.duckdb.imported`.
  - If the `topHelp` text in `dispatch.go` changes, change both so `TestReadmeUsageMatchesHelp` passes.
- `docs/features.md:4,112`: `loghorn -web` becomes `loghorn serve` / `loghorn ui`.
- `docs/superpowers/specs/2026-11-23-duckdb-storage-design.md:52`: add a note that D-031 overrides it for the SQL surface (per-stream views).
- `docs/DECISIONS.md` D-030 "Supersedes": D-027 was revised on 2026-11-27 (sidecar mark). It must no longer say D-027 is unchanged.
- `docs/superpowers/specs/2026-11-19-architecture-working-notes.md` #5 (lines ~86-90): mark it superseded by D-030 (one `records` table).
- `docs/PROJECT_STATUS.md`:
  - Line 34: change "this commit" to `bda983e`.
  - Add Completed rows for D-031 (`-sql`), the 2026-11-27 fixes and this plan's tasks.
  - State plainly what the 500 ms measurements cover (warm, one day, 770k records) and what they don't: `-sql`, multi-day ranges, cold start, memory fallback.
- `docs/superpowers/specs/2026-11-25-cli-sql-design.md`:
  - Read-only guard: add `loghorn` to the allowed schemas.
  - Say that DESCRIBE, SHOW and SUMMARIZE serialize as SELECT.
  - Discovery is `loghorn.tables`, not `SHOW TABLES`.
  - Add the row cap and timeout.
- `CLAUDE.md`, under Startup ordering (serve): the order is lock, then open the store (which may import the text), then bind the socket and ports. Add the consequence: serve is unreachable during the import.
- `docs/superpowers/plans/2026-11-23-cli-subcommands.md:15`: replace the named apps with "never write a private application's identifiers…", and drop the grep for those names.

- [ ] **Step 1: Make the edits.**
- [ ] **Step 2: Verify.**
  - `go test . -run TestReadmeUsageMatchesHelp`: PASS.
  - `git diff | grep -inE 'auction|anvil|nworth|\btaa\b'` prints nothing. The plan line removal is the only diff line that may match, and only as a deletion.
- [ ] **Step 3: Commit** with the message `docs: README, features, specs and status match what shipped`.

## Final review

After Task 6, run a whole-branch Opus review of `f98ff3c..HEAD` against this plan and the follow-up review.
