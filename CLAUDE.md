# loghorn — notes for agents

## Startup ordering

- `run` (`cmd_run.go`): validate or bind anything that can fail with
  `os.Exit` (flags, config, the serve probe, the incoming folder and its
  receiver) **before** launching the child command. `os.Exit` skips deferred
  cleanup, and the child runs in its own process group, so an exit after
  launch leaves it running.
- `serve` (`internal/serve`): take the data folder's lock, then open the
  store (which may import the text files first), then bind the socket and
  both ports, and only then serve. Consequence: serve is unreachable while
  the import runs, so clients see no socket until it ends.
- A failure in `serve` undoes what came before it, so a supervisor can
  restart serve cleanly. serve never forks.

## Commands and serve

- Each command is a `cmdX(args, cliEnv) int` in `cmd_x.go` with its own
  `flag.FlagSet` (`newFlagSet`, `parseCmd` in `dispatch.go`). `run` parses
  flags only before the command; the others accept them anywhere. A removed
  top-level flag gets a message from `migrations` in `dispatch.go`.
- `topHelp` in `dispatch.go` is copied into README.md between the
  `<!-- loghorn help -->` markers; `TestReadmeUsageMatchesHelp` keeps them
  equal, so edit both.
- Clients find serve only through its socket (`sock.DefaultPath()`:
  `$LOGHORN_SOCKET`, else `/tmp/loghorn.sock`) and ask `/api/session` for its
  ports. Never assume 4380/4318 in a client.
- Tests must never reach a real serve or the real data folder: set
  `LOGHORN_SOCKET` and `LOGHORN_DATA_DIR` (the e2e helpers `loghornCmd` and
  `newServeEnv` do), and make socket paths under `os.MkdirTemp("/tmp", …)`:
  macOS `t.TempDir()` paths overflow sun_path (103 bytes).
- `serve` refuses to start (and `run -serve` is ignored) under an AI agent
  (`CLAUDECODE`, `AI_AGENT`, `CODEX_SANDBOX` set; see `agent.go`), and the
  tests run under one. Binary tests must append `noAgent` to the command's
  environment, as `loghornCmd` and `serveEnv.environ` do; a hand smoke test
  of `serve` needs `LOGHORN_ALLOW_AGENT=1`.
- The web handler maps an empty query service to `default`, and records from
  `run` carry the run's service, so a hand-written `/?q=` that should see
  them needs `"service":"*"` (or the service).

## Web view (`internal/web`)

- The page (`internal/web/static/`) is embedded with `go:embed`. After editing
  any `app.js`, `app.css` or `index.html`, rebuild the binary (or re-run
  `go run ./internal/web/demo <logfile>`) before checking it in a browser.
- Keep the `[hidden]{display:none!important}` rule in `app.css`: a
  `display:flex` rule otherwise overrides the `hidden` attribute and hidden
  panels show.
- The Host guard reads the listener port from `http.LocalAddrContextKey`.
  Handler tests must use `httptest.NewServer`; a request built with
  `httptest.NewRequest` and passed to `ServeHTTP` has no local address and
  gets 403.
- Number values in test records must be `json.Number`: `schema.Convert`
  rejects plain `float64`/`int64` as the wrong type.
- Search (`#run-query`) is the sidebar's default button: `wireSubmit` runs
  it on Enter in any sidebar input, after the field's own handlers. It does
  not cancel the key, because Enter is what fires `change` on Rows, a
  relative From or a field name; a field that commits on Enter itself must
  `preventDefault` it (as the filter value does) or it validates twice.
- A button inside a control that redraws on its input's blur/change must
  `preventDefault` on `mousedown`, or the redraw swallows the click.
- The server requires `plot` and `order` to be count or one of `aggregates`,
  so any edit of the metrics list in the page must also reconcile
  `state.plot` and `state.order` (see `syncAggregates`).
- Sidebar controls are `.sb-field` rows: a `.sb-l` label in the fixed label
  column, then a `.sb-body`. A new control must use that row shape (with
  `data-views` on the row) or its box won't line up with the others.
- `go:embed` can't reach the repo's `images/`, so
  `internal/web/static/favicon.svg` is a copy of `images/favicon.svg`. Edit
  both, or copy after editing.
- Before checking a page in the browser, make sure the server you start is
  the one listening: a demo left on the port makes the new one exit with
  "address already in use" while the browser keeps showing the old build.
  Find it with `lsof -nP -iTCP:<port> -sTCP:LISTEN` and kill it by PID.
- Saved TOML: BurntSushi/toml's `omitempty` keeps a zero int; add `omitzero`
  to any optional int field (see `schema.Field.Width`).
- Schema edits from the page queue one after another; each must re-apply its
  patch when it runs, because the previous save's reload replaces
  `schemaData` (see `saveFieldFormat`).
- Field settings in `fields-<source>.toml` apply to every kind of that source,
  so a field save must send only what the user changed. Send `type` and
  `visualizer` only when the user set them; otherwise a width or label edit
  freezes one kind's inferred type for all kinds.
- Every async loader in `app.js` (`loadKinds`, `loadPins`, `loadSchema`,
  `runQuery`) takes a sequence number and drops its answer when a newer call
  superseded it, and its callers bail out on a superseded load. A new loader
  must do the same, or a slow answer for the previous service or range
  overwrites the current state.
- Pins, favorites and field settings belong to the selected service. Read
  and write them for `state.service` only. Under All (`*`) they are
  read-only, and the page skips per-service writes.
- Tests and demos must not touch the real `~/.config/loghorn`. Point `HOME`
  at a temp dir (`svcconf` puts config under `$HOME/.config/loghorn` when
  `XDG_CONFIG_HOME` is unset).
- A loader's success path calls `clearError` (`runQuery`, `openTrace`,
  `openAggregate`), which wipes a banner another loader just showed. Run a
  deep-link load after the table query, as `restoreTraceFromURL` does.
- In Spans mode a stream is a span kind (`http.server`, a span name or type;
  see `/api/kinds`), not `app`. A check that leaves the stream on a log
  stream sees an empty table.
- A browser check of a view with no button yet can call it directly:
  `await import('/static/agg.js')` (or `trace.js`) in Playwright's evaluate
  shares the page's module instance.
- In Spans mode the stream `*` (`ALL_KINDS`, `record.AllStreams`) is every
  span kind at once; the Heatmap view selects it on entry. It is not the
  service `*` (All services), and pins never accept it.
- A heatmap cell's list narrows with `Query.Cell` (`from <= time < to`,
  `lo <= duration_ms < hi` on the raw number), not with range + filters:
  `Range.To` is inclusive, so a span on a bucket edge would land in two
  cells, and a filter on `duration_ms` is validated against the inferred
  type (an `int` field rejects a float edge). Clip `from` to the result's
  resolved `from`, use the next bucket's start as `to`, and omit `lo` for
  the lowest band (it holds zero and negative durations). Never send `cell`
  on the heatmap request itself: it narrows every view.
- The trace and aggregate views stack, and the URL names only the top one;
  the stack is `history.state.crumbs` (`nav.js`). A view that writes history
  must use `navState(crumb, push)` for the state and call `syncNav()` after
  any show, hide or URL write, or the breadcrumb, back labels and sidebar
  lock go stale. Close through `nav.back()`, never `history.back()` directly.
- The demo loads spans with `-spans FILE` (lines as in serve's
  `loghorn.traces.jsonl`); the Spans stream list stays empty until a
  service is selected.
- Table and graph groups that tie order by count descending, then by the
  group key's text: a timestamp group sorts by its RFC 3339 string, not by
  time.

## Go modules

After `go get` of a module with generated protobuf code (e.g.
`go.opentelemetry.io/proto/otlp`), run `go mod tidy`: `go get` alone leaves
go.sum without the entries for its transitive grpc/grpc-gateway imports, and
the build fails on a clean checkout.

To find dead code, run
`golangci-lint run --no-config --default=none -E unused --tests=false ./internal/...`
(it lists unexported functions only tests use), and repeat until it prints
nothing. When deleting code, grep the comments for its names too: comments in
`tosql.go` and `sqlrun.go` once defined SQL behaviour by naming the Go
functions they mirrored.

## Config lookups that walk up the tree

A helper that searches upward for a repo's `.config/loghorn/` must stop at the
git root (`.git` may be a file in a worktree), and outside git must not walk
above its start directory, or it picks up the user-level `~/.config/loghorn`.
Test it with no `.git` and with `HOME` as an ancestor of the start directory
(see `schema.FindConfigDir`).

## DuckDB from tests and tools

Unset `AI_AGENT` in the environment of any `duckdb` process loghorn runs:
DuckDB's agent mode changes the `-json` output (see
`internal/query/tosql_duckdb_test.go`). A DuckDB decimal literal such as
`0.95` is a DECIMAL; cast it to DOUBLE wherever the SQL must match Go's
float64 math.

- Generated SQL re-evaluates an expression at every use: `trim(x,
  schema.Space)` costs about 90 ms per million rows per use, and binding it
  through a one-element list lambda is slower still. Put a cheap test on the
  raw value before trimming (see `durationSQL`).
- The engine's config is locked at the end of `harden()`
  (`lock_configuration`). Tests can't `SET` anything (TimeZone etc.) on
  `e.DB()`, and any new setting must be applied inside `harden()`.
- `Engine.warned` (`warnOnce`/`warnKeyed`) has no lock of its own and is used
  only on the writer under `wmu`. Don't call it from `Store.add` (under
  `s.mu`); use `Engine.Warn` with a once flag on the store side.
- The test hooks `failWrites` and `rejectRow` return one fixed error text for
  every row. Calling `e.write` directly under `e.wmu` gives a test
  deterministic batches.
- `writeRows` gives up on a batch as "DuckDB is failing" only when its first
  two rows (or its only row) each fail with the batch's own error text. A bad
  row makes its whole batch fail with that row's error, so one failing row
  proves nothing.
