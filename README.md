<img src="images/logo.svg" alt="Loghorn" width="300">

# loghorn

**Production-grade observability for your local dev loop.**

Loghorn works with the logs you already write, from plain text to structured JSON, and
grows with you into traces, latency heatmaps and request-level analysis. It runs
entirely on your machine: no account, no API keys, no cloud bill, and your logs never
leave localhost.

<!-- TODO screenshots: one per rung, same app, same failing request.
| Plain logs | Structured logs | Traces |
|---|---|---|
| ![](images/readme-plain.png) | ![](images/readme-structured.png) | ![](images/readme-traces.png) |
-->

## Start with the logs you have

You don't have to change your app to get value from Loghorn, and every step up pays
off on its own.

| Your app writes | Loghorn gives you |
|---|---|
| **Plain text** — whatever your server prints today | Every line kept per run and searchable after the terminal scrolls away; errors, panics and 5xx responses surfaced; `-filter` to see only failures |
| **Structured logs** — JSON such as GCP `LogEntry`, or OpenTelemetry logs | Filter and group by any field, count and percentiles per group, charts over time, saved queries |
| **Traces** — OpenTelemetry spans | A waterfall of every request showing where the time went, latency heatmaps where slow outliers stand out, per-operation breakdowns, and each log line attached to the request that wrote it |

<!-- TODO: add glog-style lines (time, severity, file:line, text) to the first rung once supported. -->

## Quick start

Requires Go 1.25 or newer.

```sh
go install github.com/jimbeveridge/loghorn@latest

loghorn serve                 # once, in its own terminal: the local store
loghorn run -- npm run dev    # your dev server, output unchanged, everything recorded
loghorn ui                    # open the explorer in your browser
```

From a checkout, `go build -o loghorn .` instead of `go install`.

Your command's output reaches the terminal exactly as before: colours, prompts and
`Ctrl-C` all behave normally. If your app already sends OpenTelemetry, `run` points it
at Loghorn automatically.

## Add traces to your app in under an hour

Traces answer the question logs can't: *where did the time go?* If you've heard that
OpenTelemetry, the open standard for traces, is expensive or a chore to maintain, that
reputation comes from somewhere else:

- **The cost was the vendor, not the standard.** OpenTelemetry is free and open
  source. Hosted tools bill by the gigabyte; Loghorn runs on your machine and bills
  nothing.
- **You don't hand-write spans anymore.** Standard libraries trace your HTTP handlers,
  outgoing requests and database queries automatically. Add your own span only where
  you want more detail.
- **No collector, no agent, no account.** A few lines of setup point your app at
  Loghorn on localhost, and a Claude skill writes them for you.
  <!-- TODO: skill name, where to get it, supported languages. -->
- **Nothing locks you in.** It's a vendor-neutral standard. Keep it dev-only, or send
  the same data to any production backend later.

## What you can see

- **Samples** — the records themselves, newest first, with typed filters, a time range
  and text search.
- **Table** and **Graph** — group by any fields; count, sum, average and p50/p95/p99
  per group, as a table or as lines over time.
- **Heatmap** — span durations over time, so a slow minority stands out from the
  usual; click a cell to list its spans.
- **Trace waterfall** — one request as a tree of timed bars, with its log lines, the
  critical path and self time.
- **Aggregate operation view** — every trace of one endpoint merged into a single
  tree, with p50/p95 and error counts per step.
- **`loghorn query`** — any view's data from the command line as TSV, CSV, Markdown,
  JSON or YAML, or the DuckDB SQL that reproduces it. `-sql` runs your own read-only
  SQL against the stored records.

## Reference

loghorn reads GCP `LogEntry` JSON, gcloud's YAML and JSON output, OpenTelemetry
logs and traces, and plain text, deciding per stream which it is.

### Running it

<!-- loghorn help -->
```
loghorn — capture, store and query the logs and traces of local apps

Usage: loghorn <command> [flags] [args]

Store
  serve            run the store: receive OTLP, serve the web explorer

Capture
  run -- <cmd>     run a command, passing its output through, and record it
  open <log>       open a finished log: a file, or a GitHub Actions run or job

Read
  ui               open the web explorer in your browser
  query <url>      print a web view's data as TSV, CSV, Markdown, JSON, ...
  status           show serve's state and the OTEL environment for apps
  config           show the config file in effect

Shortcuts: 'loghorn -- <cmd>' is 'loghorn run'; 'loghorn <url>' is 'loghorn query'.
Run 'loghorn help <command>' for its flags.
```
<!-- /loghorn help -->

**Start the store** once, in a terminal (it stays in the foreground) or as a login
service:

```sh
loghorn serve
```

`serve` receives OTLP/HTTP on `127.0.0.1:4318`, serves the web explorer on
`127.0.0.1:4380`, and listens on the unix socket `/tmp/loghorn.sock`, where every
other command finds it. It keeps its data in a per-user folder (see *Where data
lives*), not in the current directory, and stops cleanly on `SIGTERM` or `Ctrl-C`. It
fails at once, with a message naming the setting to change, when a port is taken,
another `serve` holds the data folder, or another answers on the socket.

`serve` refuses to start from an AI coding agent's shell (it sees `CLAUDECODE`,
`AI_AGENT` or `CODEX_SANDBOX` set): an agent would leave it holding the data folder
and ports. Start it yourself, or set `LOGHORN_ALLOW_AGENT=1`. An agent's `run` still
records, to a running `serve` or through `serve-down`.

**Wrap the producer:**

```sh
loghorn run -- npm run dev
loghorn -- npm run dev          # the same
```

`run` is a transparent wrapper. The command's stdout and stderr reach the terminal
unchanged — through a pty when stdout is a terminal, so colours, progress bars and
prompts work as they do without loghorn — and the keyboard stays the command's.
`Ctrl-C` goes straight to the command; `SIGTERM` and `SIGHUP` sent to loghorn are
forwarded to it. A command still running `-shutdown-grace` (5s) after a `Ctrl-C` or
`SIGTERM` is killed; from half a second on, a terminal shows a countdown
(`waiting for npm to exit; killing it in 3s`), and a second `Ctrl-C` kills it at
once. (An interactive command that uses `Ctrl-C` itself, such as a database shell,
is killed this way too.) loghorn exits with the command's status (127 when it can't be found). Flags go
before the command; everything from the command on is the command's.

Meanwhile every record, with escape sequences removed, goes to `serve`, under a
service name: `-service NAME`, else the command's `OTEL_SERVICE_NAME`, else the
repository's directory name and the command (`shop-npm`). `run` also points the
command's OpenTelemetry SDK at `serve` (`OTEL_EXPORTER_OTLP_*`, `OTEL_LOGS_EXPORTER`,
`OTEL_TRACES_EXPORTER`, and `loghorn.run.id` in `OTEL_RESOURCE_ATTRIBUTES`), so its own
OTLP logs and spans arrive too. Once the command's OTLP logs arrive, its stdout lines
that are JSON objects are no longer stored, since they are most likely the same events
logged twice. If `serve` stops answering mid-run, the output keeps flowing and loghorn
says how many lines were not recorded.

**Record a pipe** — the input is copied to stdout and recorded the same way:

```sh
kubectl logs -f pod | loghorn run
gcloud beta logging tail --json | loghorn run
```

A bare `loghorn` with piped input prints the help and records nothing.

**Show only the failures** — `-filter` prints just the failing records to stdout,
one per line, instead of everything; everything is still recorded. With a command the
exit status is still the command's:

```sh
loghorn run -filter -- go test ./...
```

A line is a **failure** if any of these holds:

- its severity is `ERROR` or above
- its HTTP status is 500 or above
- its message matches `panic`, `fatal`, `exception` or `traceback`, case-insensitively

**Apps that send OpenTelemetry themselves** — point them at `serve` with the
environment `loghorn status` prints:

```sh
eval "$(loghorn status -env)"
```

That sets `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`,
`OTEL_LOGS_EXPORTER=otlp`, `OTEL_TRACES_EXPORTER=otlp` and `OTEL_METRICS_EXPORTER=none`
(serve discards metrics). Set `OTEL_SERVICE_NAME` per app. Each OTLP log record's
stream is its `event.name`, else its instrumentation scope name, else `otel`; the
body, attributes, `event.name`, `otel.scope.name`, `service.name`, `trace_id`,
`span_id` and the run id are stored as fields, and `loghorn.source` is `otlp`. Spans
are stored one JSON object per line (`trace_id`, `span_id`, `parent_span_id`, `name`,
`kind`, `start`, `end`, `duration_ms`, `status`, `status_message`, `attributes`,
`events`, `links`, `service.name`, `loghorn.run.id`, `otel.resource` and the
instrumentation scope) and appear in the web view as span streams.

**When serve isn't running**, `run` still runs the command. What it records is your
choice, set in `[run] serve-down` (below) or per run with `-serve-down`:

- `record` (the default) writes the run to the data folder's `incoming/<run id>/`,
  including the command's own OTLP, and `serve` adopts it when it next starts. Apps
  sending OTLP on their own are not recorded until `serve` is back.
- `skip` records nothing.
- `fail` exits 1 without running the command, for scripts and CI that must capture
  everything.

`run` says so on stderr every time, and `loghorn status` reports the runs waiting in
`incoming/`.

**One shot, without a standing serve** — `run -serve` starts a `serve` inside `run`
for that run when none is running (same data folder, socket and ports), records into
it directly, and serves the web view while the command runs. One `Ctrl-C` stops the
command and `serve` together; a command that exits on its own leaves the web view up
for browsing until `Ctrl-C`:

```sh
loghorn run -serve -- npm test
```

While it runs, apps sending OTLP to it are recorded too. The exit status is still the
command's. It waits for `Ctrl-C` only when stdin is a terminal; from a script or CI it
stops with the run (the run stays stored for a later `serve`). An AI agent's `run`
ignores `-serve`, as `serve` itself does. With a `serve` already running, `-serve` does nothing extra; if one can't
start (a port in use, say), `run` says why and follows `serve-down`. While the one-shot
`serve` holds the data folder, a `serve` started by a login service waits its turn.

**Open a finished log** — `open` shows a file, stdin or a GitHub Actions job in a
temporary web view of its own (its URL goes to stderr; `Ctrl-C` ends it), or with
`-filter` prints its failures. It records nothing:

```sh
loghorn open capture.json
loghorn open -filter capture.json
kubectl logs pod | loghorn open -filter -
loghorn open https://github.com/owner/repo/actions/runs/43954926722/job/133607019104
loghorn open https://github.com/owner/repo/actions/runs/43954926722
loghorn open -filter -gh-job 133607019104
```

A run URL or `-gh-run` shows the run's first failed job (its first job when none
failed), listing every job on stderr so you can name another with `-gh-job`. Both go
through the GitHub CLI, so `gh` must be installed and logged in; a bare ID uses the
repository of the current directory. An old per-project day from earlier versions
(`.loghorn/loghorn-2026-11-20.log`) opens the same way.

**Look at it:**

```sh
loghorn ui                  # open the web explorer in your browser
loghorn ui -print           # just print its URL
loghorn status              # is serve running, where, and what apps need
```

### gcloud

Both shapes gcloud emits are read as-is:

```sh
gcloud beta logging tail --json | loghorn run
gcloud logging read --format=yaml | loghorn open -
```

`--json` produces a JSON array whose first line is a bare `[`, with each entry
pretty-printed across many lines; `--format=yaml` produces `---`-separated documents.
loghorn detects either from the stream's first line and groups each entry into one
record. A line that isn't JSON — a gcloud warning on a merged stderr, say — stays a
plain-text line. A tail stopped with `Ctrl-C` leaves its last entry cut off mid-key;
that partial record is kept and flagged rather than dropped.

gcloud also names `LogEntry` fields in snake_case (`json_payload`, `http_request`)
and encodes severity numerically (`200` for `INFO`, `500` for `ERROR`). Both are
accepted by default, alongside the canonical camelCase-and-string schema.

### Configuration

Settings live in `.config/loghorn/config.toml`. Every key is optional, and every
default is the behaviour loghorn has without a file at all, which is why the file is
worth creating only when a producer needs something unusual.

```toml
[input]
format       = "auto"   # auto | json | yaml | text
field-naming = "auto"   # auto | camel | snake
severity     = "auto"   # auto | string | numeric

[serve]                 # read from the user-level file only
web-port       = 4380
otlp-http-port = 4318

[run]                   # read from the user-level file only
serve-down = "record"   # record | skip | fail
```

- **`format`** — how the stream is split into records. `auto` decides from the first
  line. `json` forces JSON-array framing, which is what a tail attached *mid-stream*
  needs, having missed the opening `[`. `yaml` forces `---` documents. `text` turns
  all grouping off, one record per line.
- **`field-naming`** — `auto` accepts either spelling of a `LogEntry` field name;
  `camel` and `snake` restrict it to one.
- **`severity`** — `auto` accepts a string name or a numeric code; `string` and
  `numeric` restrict it to one.
- **`[serve]`** — the ports `serve` binds; `serve -web-port` and `-otlp-http-port`
  override them for one start. Clients ask `serve` which ports it bound, so they follow
  an override.
- **`[run] serve-down`** — what `run` does when `serve` isn't running (see above).

`serve` and `serve-down` read only the user-level file
(`~/.config/loghorn/config.toml`), never a repository's: `serve` has no project
directory, and whether a run may go unrecorded is your call. `[input]` is applied by
`run` and `open`, which read the file nearest the directory they start in.
A `display` section, which configured the terminal UI of older versions, is ignored with a warning.

#### Where the file is found

loghorn walks up from the directory it starts in, and **the first
`.config/loghorn/config.toml` it finds wins outright** — nothing is merged across
levels, so one file explains all of loghorn's configured behaviour. If the walk
reaches the root without a match, it makes one final stop at
`$XDG_CONFIG_HOME/loghorn/config.toml`, or `~/.config/loghorn/config.toml` when
`XDG_CONFIG_HOME` is unset.

That final stop is what makes personal defaults apply when you run loghorn from
outside your home directory. Note the consequence of "nearest wins" for a working
directory *inside* your home tree: once `~/.config/loghorn/config.toml` exists, the
walk finds that literal path first, so `XDG_CONFIG_HOME` no longer redirects anything.

To see which file is in effect and what it resolves to:

```sh
loghorn config
```

`.config/loghorn/` is hand-written and belongs in version control.

### Where data lives

`serve` keeps everything in a per-user data folder, whatever directory it starts in:
`~/Library/Application Support/loghorn` on macOS, `$XDG_STATE_HOME/loghorn` (default
`~/.local/state/loghorn`) on Linux, `%LOCALAPPDATA%\loghorn` on Windows. `serve -data`
or `$LOGHORN_DATA_DIR` moves it. Records go to `loghorn.log` and spans to
`loghorn.traces.jsonl`; each finished day is kept as `loghorn-YYYY-MM-DD.log` (and
`.traces.jsonl`), and days before the last three are deleted. The folder is
owner-only, because a dev server's logs can carry tokens and auth headers, and a lock
in it keeps a second `serve` out.

Inside it, `loghorn.duckdb` holds every record in one `records` table. Each finished
day moves to a Parquet file in `duckdb-archive/`. `loghorn.duckdb.imported` marks
the database as complete, so the next start does not import the text files again;
without it (after a crash, say) `serve` imports them again, and is unreachable
until that finishes.

Runs recorded while `serve` was down wait in `incoming/<run id>/` until `serve`
adopts them.

**A second instance** needs its own socket, data folder and ports, and its clients
need `$LOGHORN_SOCKET`:

```sh
loghorn serve -socket /tmp/loghorn-b.sock -data ~/lh-b -web-port 4390 -otlp-http-port 4328
LOGHORN_SOCKET=/tmp/loghorn-b.sock loghorn run -- make test
```

Earlier versions wrote `.loghorn/` in each project; those files are left where they
are, and `loghorn open` reads them.

### Web query view

`serve` serves a browser view of the stored records at `http://127.0.0.1:4380/`
(`loghorn ui` opens it), and `open` serves the same page for one finished log. The
server listens on loopback only and refuses requests for any other Host, and writes
that do not come from its own origin. The unix socket is reachable only by you
(mode 0600).

It lists the streams found in the log (database lines, HTTP lines, and so on),
and for each one a table, newest first, with typed filters, a time range and
text search. Fields get types by inference (int, float, bool, string, timestamp,
duration); a value that does not fit its type is shown raw and flagged, and is
left out of comparisons. `serve` loads the stored days when it starts.

The sidebar holds the whole query: the mode tabs, the stream, the time range,
and the filters, one row per field / operator / value (a bad value is flagged on
its own row and the other filters keep working). Three modes share it:

- **Samples** — the matching records themselves; click a row for the full
  record, with each SQL or multiline field (a stack trace, or any text with
  line breaks) in its own block above it. A multiline cell shows its first
  line and how many more there are.
  Click a column name (in the header or under *Columns*) for its
  menu: visualizer, unit and label (saved to the stream's schema at once),
  Hide, Sort ascending/descending, *Add / Move* (put a field not shown, or
  another column, right of this one; columns hidden in this query come
  first), and Move to start. The trash icon in a header hides the column.
  *A→Z* orders the columns by the last part of their path (`status` for
  `httpRequest.status`); *restore defaults* brings back the stream's default
  columns. Drag a header's edge to set its width, which is saved in the
  schema too; double-click the edge to return to the natural width.
- **Table** — one row per group. *Group by* takes one or more fields (records
  with an empty or bad value group as `(empty)`); *Metrics* always includes
  `count` and adds `sum`, `avg`, `min`, `max`, `p50`, `p95` or `p99` of a
  number field (int, float or duration).
- **Graph** — a line over time per group: the top 10 groups by count, the
  rest folded into `other`. *Plot* picks the metric drawn (`count` or one of
  the metrics); *Granularity* sets the time bucket (`1s` to `1d`), or `auto`,
  which aims for about 100 buckets and shows the size it chose. A range that
  would need more than 2,000 buckets asks for a coarser granularity. Hover
  for each group's value in a bucket.

The URL carries the query, and a favorite saves the mode with it.

Pins, favorite queries and field settings (types, display and column widths)
are saved as TOML per service, in `~/.config/loghorn/services/<service>/`
(`$XDG_CONFIG_HOME/loghorn/services/<service>/` when that is set):
`pins.toml`, `queries.toml`, and `fields-log.toml` / `fields-otel.toml` for
log and span fields. A field setting applies to every kind of its source in
that service. A record's service is its `service.namespace`, else its
`service.name` (or a Cloud Logging `resource.labels.service_name`), else
`default`; the lines of a `run` that name none take the run's service (see above).
*All* services is read only.

Earlier versions saved schemas and favorites in the repo's `.config/loghorn/`;
that is no longer read, and loghorn says so once per repo. To keep the
favorites, copy them to a service by hand. `<key>` is the escaped service key:
bytes other than letters, digits, `.`, `_` and `-` become `%XX`, and a leading
`.` becomes `%2E`.

```sh
mkdir -p ~/.config/loghorn/services/<key> && cp .config/loghorn/queries.toml ~/.config/loghorn/services/<key>/
```

Old `schemas/*.toml` are not converted: re-save their fields one by one in the
page, or paste their `[[field]]` entries into `fields-log.toml` /
`fields-otel.toml`. `.config/loghorn/config.toml` stays where it is.

**Trace waterfall** — in the web view, click a `trace_id` or `span_id` cell to see
that trace as a tree of timed bars, with its log lines, span events, serial query runs, the
critical path and a self-time table. The page URL carries the view, so it can be
shared or reloaded: `/?q=<json>&trace=<id>&span=<id>`. See `docs/features.md`.

**Aggregate operation view** — merge every trace of one operation into a single
tree. In Spans mode, click the `∑` after the name of a root span (a span with no
parent), press `Aggregate this operation` in a waterfall, or open
`/?agg=<name>&aggsvc=<service>`. Each row is a span name at one path below the
root, with the number of traces holding it, calls per trace, p50 and p95, total
and self time as shares of the operation's time, and an errors badge; `slowest`
opens the slowest trace holding that span. It aggregates the page's time range,
at most the 1000 most recent traces. See `docs/features.md`.

### Query from the command line

The web view can be queried without a browser. Copy the page URL from the address bar
and give it to `loghorn query`; it prints the same view's data, TSV by default:

```
loghorn query 'http://127.0.0.1:4380/?q={"stream":"app","service":"*","range":{"from":"all"}}'
loghorn 'http://127.0.0.1:4380/?q=…'            # the same: a URL alone is a query
loghorn query @query-url.txt -format csv
loghorn query 'http://127.0.0.1:4380/?q=…' -o errors.yaml
loghorn query -to-sql 'http://127.0.0.1:4380/?q=…'
loghorn query '/?q=…'                            # no host: the running serve, via its socket
```

- The URL (`http://127.0.0.1` or `http://localhost` only) names the view: table, graph,
  heatmap or samples, with its filters, range and service. `@file` and `@-` read a long
  URL from a file or stdin. A trace or aggregate in the URL is not exported; the main
  view is. A URL from an `open` view works while that view is up.
- `-format` is one of `tsv` (default), `csv`, `md`, `json`, `jsonl`, `yaml`, `toon` or
  `sql`. `-o FILE` writes a file instead of stdout and picks the format from the
  extension (`.csv`, `.md`, `.json`, `.jsonl`, `.yaml`, `.toon`, `.sql`).
- `-to-sql` prints DuckDB SQL that reproduces the view instead of its data.
- `-limit N` overrides the URL's row limit (samples) or top (table, graph).
- `-sql <text | @file | @->` runs one read-only DuckDB query against the running
  serve instead of a URL: `loghorn query -sql 'SELECT count(*) FROM loghorn.tables'`.
  `loghorn.tables` lists the tables and views to query (`SHOW TABLES` is not the
  way). The answer is at most 1,000 rows unless `-limit` says more, never more than
  100,000, and a query that runs longer than 10 s is stopped. DESCRIBE, SHOW and
  SUMMARIZE are accepted. Text after a trailing `;` is refused.
- Flags may come before or after the URL.
- The page's Copy button copies the same export for Samples, Table and Graph. In the
  Heatmap view it copies the span list under the heatmap; the heatmap itself is
  exported only from the command line.
- `query` never launches a command, never records anything and ignores stdin (except
  for `@-`), so it is safe to run from a script or an agent shell.

### Commands and flags

`loghorn help <command>` lists a command's flags. Flags take one dash or two.

| Command | Flags |
|---|---|
| `serve` | `-data`, `-socket`, `-web-port`, `-otlp-http-port`, `-log FILE` |
| `run` | `-filter`, `-serve`, `-service`, `-serve-down`, `-notify`, `-notify-max-age`, `-notify-reset`, `-notify-sound`, `-shutdown-grace` |
| `open` | `-filter`, `-view`, `-web-port`, `-gh-job`, `-gh-run` |
| `query` | `-o`, `-format`, `-limit`, `-to-sql`, `-sql` (at most 100,000 rows, 10 s) |
| `ui` | `-print` |
| `status` | `-env` |

Earlier versions took everything as top-level flags. Each now names its new home when
used:

| Before | Now |
|---|---|
| `loghorn -- cmd` | `loghorn run -- cmd` (or still `loghorn -- cmd`) |
| `cmd \| loghorn` | `cmd \| loghorn run` |
| `loghorn -web` | `loghorn serve`, then `loghorn ui` |
| `-web` with a command | `run` always sends to `serve` |
| `-otel` | `run` always points the command at `serve` |
| `loghorn -filter < file` | `loghorn open -filter file` |
| `-historical` | `serve` keeps the stored days; `open` reads one file |
| `-gh-job`, `-gh-run` | `loghorn open -gh-job …`, `-gh-run …`, or the URL |
| `-config` | `loghorn config` |
| `-url`, `-o`, `-format`, `-limit`, `-to-sql` | `loghorn query …` |
| `-instances` | `loghorn status` |
| `-exclusive` | gone: `run` no longer writes a log file per directory |

## Docs

`docs/ROADMAP.md` tracks what is built and what is planned. Design documents for each
feature live in `docs/superpowers/specs/`.

## License

Apache 2.0 — see [LICENSE](LICENSE).
