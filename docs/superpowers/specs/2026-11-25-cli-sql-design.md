# SQL from the command line (`loghorn query -sql`) — design

Date: 2026-11-25 | Branch: `duckdb-v2` | Builds on: `2026-11-22-cli-query-design.md` (Jim's table model) and `2026-11-23-duckdb-storage-design.md` (D-030)

## Intent

An LLM agent debugging an app should be able to ask Loghorn questions in SQL, one command at a time, and read the answer as plain text: what failed, how often, which service, how slow. It runs many small queries in a row, so each one has to be quick, bounded in size, and explain its mistakes in words the agent can act on. Jim's CLI query spec already fixes the table model (`logs."app"`, `spans."*"`, `seq`/`time`/`service`/`raw`, one typed column per field, two `this_run_*` macros) and the flag (`-sql 'SELECT …'`, `-sql @file`, `-sql @-`). This design makes that table model real on top of the `records` table and wires the flag.

## Goals

- `loghorn query -sql 'SELECT …'` (and `@file`, `@-`) runs against the running `loghorn serve` and prints the rows as TSV by default, or CSV, Markdown, JSON or JSON lines with `-format` / `-o`.
- The table model in `query.SQLHeader` exists exactly as written there, so SQL printed by `loghorn query -to-sql` runs unchanged and returns the rows the web view shows.
- An agent can discover the tables and columns with SQL alone (`SELECT * FROM loghorn.tables` (not `SHOW TABLES`), `DESCRIBE logs."app"`, `information_schema.columns`).
- Output is bounded: at most 1,000 rows unless `-limit` says otherwise, with a note on stderr when rows were cut.
- Errors are DuckDB's own message, prefixed with `loghorn:`, so the agent can fix its SQL.

## Non-goals

- No writes of any kind through `-sql`, and no SQL from the web page.
- No new storage: the table model is views over `records` and the Parquet archives; nothing is copied.
- YAML, TOON and SQL output formats for `-sql`.
- Per-service tables or per-service saved field settings in the model: a stream's table uses the stream's schema over every service, as the web does for service `*`.

## Anti-goals

- `-sql` must not be able to change or delete stored records, attach other databases, install extensions, or read or write files.
- A stale table model (a field that exists but has no column) is a failure even if the query runs.

## Approaches

1. **Views rebuilt on each request (chosen).** Before a `-sql` query, serve lists the streams, infers each stream's schema (cached by the store until data changes), and creates `logs."<stream>"` / `spans."<stream>"` views whose typed columns reuse the conversion SQL the web view already uses (`duckstore.Relation`, which matches `schema.Convert`, bad values as NULL). The DDL is only re-run when its text changes. Always current, no invalidation logic, nothing stored twice.
2. **Views rebuilt when the data version changes.** Same views, built from the writer's side after each batch. Saves a few milliseconds per query but couples the writer to schema inference and still needs the per-request path for saved settings. Not worth it.
3. **Materialized typed tables per stream.** Fast to query, but doubles storage, needs migration when a type changes, and contradicts D-030. Rejected.

Where the SQL runs: only `loghorn serve` may open `loghorn.duckdb` (D-030), so the CLI sends the SQL to serve's new `POST /api/sql` on serve's unix socket, the same way a host-less `loghorn query` URL works. The endpoint refuses TCP requests: the page never needs it, and a browser cannot reach the socket.

## Design

**Endpoint.** `POST /api/sql` with `{"sql": "...", "format": "tsv", "limit": 1000}`, socket only. It answers the rows in the export format, or a JSON `{"error": ...}` with status 400. A cut result sets `X-Loghorn-Note: showing the first N rows; add LIMIT or pass -limit`. The query runs under the request context with a 30 s timeout.

**Read-only guard.** DuckDB's own parser decides: `json_serialize_sql(sql)` must succeed (it accepts only queries: `SELECT`, `WITH`, `FROM`-first, `DESCRIBE`, `SHOW`, `SUMMARIZE`, set operations) and hold exactly one statement. The serialized tree is then walked: every table function must be on a short list (`range`, `generate_series`, `unnest`, `json_each`, `json_tree`, `duckdb_*` catalog functions), and every table reference must be a CTE name or sit in the `logs`, `spans`, `loghorn`, `information_schema` or `pg_catalog` schemas. `DESCRIBE`, `SHOW` and `SUMMARIZE` serialize as `SELECT`, so the guard treats them as queries. That blocks `read_text`, `read_csv`, `query()`, and `FROM 'file.csv'` replacement scans. The check runs on a separate connection before the query.

**Table model.** Schemas `memory.logs`, `memory.spans`, `memory.loghorn`. For each stream from `Store.Streams()` and for `*` of each source, one view:

`SELECT _id AS seq, timezone('UTC', _time) AS "time", _service AS service, _text AS raw, <vi> AS "<path>", …` over `Relation(source, "*", stream, fields)`, where fields are `schema.Effective(inferred, nil).Fields`. Durations are converted to milliseconds rounded to 15 significant digits (`query.Round15`); timestamps become TIMESTAMPTZ. A field named `seq`, `time`, `service` or `raw` (any case) gets no column, as `SQLHeader` says.

**Macros.** `loghorn.this_run_start()` and `loghorn.this_run_id()` describe the most recent run: the `loghorn.run.id` of the newest record that has one, and the time of that run's first record. With no run yet, the start is serve's start and the id is NULL. *Open for Jim:* the web view's `this-run` under serve means "since serve started"; the macros follow Jim's spec (a wrapped command's run) instead.

**CLI.** `loghorn query -sql <text | @file | @->`; with `-sql` there is no URL argument. `-format`/`-o` pick csv, tsv (default), md, json or jsonl; `-limit N` caps rows (default 1,000, at most 100,000), and a query is stopped after 10 s. Values print as the export package prints them: NULL empty in TSV/CSV, timestamps RFC 3339 UTC, lists and structs as compact JSON.

## Testing

- Guard: accepted and refused statements, one test each.
- Model: views and column types for a fixture with logs and spans; bad values NULL; durations in ms; `*` tables.
- Equivalence: for a set of web queries, the rows of `ToSQL(q)` run through `-sql` equal the rows `RunSQL` returns (same `seq` order, same group values).
- Endpoint: socket accepted, TCP refused, row cap and note, error text.
- CLI: `-sql` text, `@file`, `@-`, formats, no URL with `-sql`.
- An agent trial: an LLM uses only `loghorn help query` and `-sql` to answer questions about a realistic data set.

## Left to the builder

The exact allow-list of table functions, how DuckDB values (HUGEINT, DECIMAL, INTERVAL, LIST, STRUCT, MAP, UUID, BLOB) map to export values, and the help text.
