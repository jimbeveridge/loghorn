# gcloud multi-line input and the `.config/loghorn/config.toml` file

Date: 2026-09-18

## Problem

loghorn cannot read the two shapes `gcloud` actually produces for Cloud Run logs.

**Multi-line JSON.** `gcloud beta logging tail --json` (and `gcloud logging read
--format=json`) emit a JSON *array*: the first line is exactly `[`, each LogEntry is a
pretty-printed object spanning tens of lines, and objects are separated by `,`. A tail
interrupted with Ctrl-C never closes the array and cuts the final object mid-key.
`ingest.Records` treats a stream that does not begin `--` as one record per line, so every
such object arrives as dozens of unparseable fragments.

**Field naming.** Both `--json` and `--format=yaml` name LogEntry fields in snake_case and
encode severity as a number, not the canonical camelCase-and-string schema
`internal/adapter/logentry.go` reads:

| gcloud emits | adapter looks for |
|---|---|
| `json_payload.message` | `jsonPayload.message` |
| `http_request.status` | `httpRequest.status` |
| `text_payload` | `textPayload` |
| `receive_timestamp`, `insert_id`, `log_name`, `span_id` | camelCase equivalents |
| `severity: 200` (number) | `severity: "INFO"` (string) |

So YAML framing already works — `Records` detects the `--` prefix — but every record still
renders with severity DEFAULT, no HTTP status, and a compact-JSON dump for its message,
because no field matches. Severity colouring, importance filtering and error alerting are
all dead on real gcloud output.

Reference samples (outside this repo, 2026-09-18): `../auction/x` is a Ctrl-C'd
`gcloud beta logging tail --json` (2,820 lines, no closing `]`, final object truncated);
`../auction/y` is `--format=yaml` (4,690 lines, 110 records). Their top-level key sets are
identical, so one normalization serves both.

## Goals

- Read multi-line JSON array streams, live and from a file, including a truncated tail.
- Keep non-JSON lines inside such a stream working as plain-text records.
- Make gcloud's snake_case fields and numeric severities parse as first-class LogEntry data.
- Put the behaviour under a discoverable project config file, so a user whose producer needs
  different handling can say so without a code change.
- Keep multi-line records replayable through `.loghorn/loghorn.log` and `-historical`.

## Non-goals

- The rest of the v1 config surface from `docs/ROADMAP.md` — saved filters, context `N`,
  colours, keybindings, alert cooldowns. This change builds the config *mechanism* and fills
  in one `[input]` section; those land later as new sections.
- Full "lenient LogEntry normalization" (`docs/ROADMAP.md`, v1): promoting arbitrary root
  keys to effective payload, JSONPath field resolution. This change aliases a fixed, known
  set of gcloud spellings only.
- Multi-line raw text grouping (stack traces), still deferred.
- Mixed-format stored files. `Records` detects one framing per stream, from its first line;
  a single stored day containing both YAML and JSON records is an existing limitation and
  stays one.

## Design

### 1. Config subsystem — new package `internal/config`

**Location.** `.config/loghorn/config.toml`, TOML. One name serves both project and
user-level config, because `~/.config/loghorn/config.toml` is simply what the upward walk
below finds when it reaches the home directory — there is no second mechanism and no
separate project filename. A directory rather than a single dotfile also gives the later v1
sections room: saved filters or keybindings can become sibling files under
`.config/loghorn/` instead of having to pile into one file.

`.config/loghorn/` is not `.loghorn/`, and the distinction is the point: `.loghorn/` holds
generated logs, is created owner-only, and ignores itself (`.gitignore:43`), so config placed
there would be gitignored — while project config belongs in version control.

**Discovery.** `Load(dir string) (Config, string, error)` resolves `dir` to an absolute path
and walks toward the root, testing `.config/loghorn/config.toml` at each level. The **first
file found wins entirely** and the walk stops — one file explains all behaviour, and
`-config` can name it. A subdirectory needing different settings restates what it needs;
nothing is merged across levels.

If the walk reaches the root with no match, it makes one final stop at the user-level file:
`$XDG_CONFIG_HOME/loghorn/config.toml` when `XDG_CONFIG_HOME` is set and non-empty,
otherwise `~/.config/loghorn/config.toml`. That stop is what makes personal defaults apply
when the working directory is *outside* the home tree — run loghorn from `/opt/service` and a
pure upward walk would never pass through `~`. When the working directory is already under
`~`, the walk has already tested that path and the final stop changes nothing. The upward
walk itself always tests the literal `.config/loghorn/config.toml`, since there it is a path
relative to a project, not an XDG lookup. Cargo resolves `.cargo/config.toml` the same way:
upward from the working directory, then `$CARGO_HOME`.

With no file anywhere, built-in defaults apply. The returned string is the path actually
loaded, empty when defaults were used.

This mirrors `internal/logfile/srctree.go:25`, including its careful case: a config file that
exists but cannot be read (permissions, say) is an error, never skipped in favour of a more
distant one that might say something different.

**Schema.**

```toml
# .config/loghorn/config.toml
[input]
format       = "auto"   # auto | json | yaml | text
field-naming = "auto"   # auto | camel | snake
severity     = "auto"   # auto | string | numeric
```

- `format` — which framing `ingest.Records` uses. `auto` detects from the stream's first
  line (below). `json` forces JSON-array framing, which is what a tail attached mid-stream
  needs, having missed the opening `[`. `yaml` forces `---` document framing. `text` forces
  one record per line, disabling all grouping.
- `field-naming` — `auto` accepts either spelling of a LogEntry key; `camel` and `snake`
  restrict to one.
- `severity` — `auto` accepts a string or a number; `string` and `numeric` restrict to one.

Every key defaults to `auto`, which is the behaviour described in sections 2 and 3. A
missing section or key takes its default.

**Forward and backward compatibility.** An unknown section or key produces one warning line
on stderr naming the file and the key, then is ignored, so an older binary tolerates a newer
file. An unknown *value* for a known key is a usage error: print the file, key, offending
value and the permitted set, and exit 2 — the same treatment a bad `--theme` gets
(`main.go:113`). Malformed TOML is likewise a usage error.

**Precedence.** Command-line flags override config wherever the two overlap. No current flag
overlaps `[input]`; the rule is stated so later sections inherit it.

**Debuggability.** A new `-config` flag prints the resolved config path (or "defaults, no
file found") and the effective settings, then exits 0. "Which file am I getting?" is the
first question when discovery misbehaves, and walking up from the working directory makes
that answer non-obvious.

### 2. Multi-line JSON framing — `internal/ingest`

`Records` gains a third mode beside its existing line and YAML modes. Its signature takes
the input settings; the mode is chosen once per call, before any record is emitted.

**Detection** (`format = "auto"`) peeks a bounded prefix of the stream — 512 bytes, enough
for any framing marker — and examines the first line in it, trailing whitespace trimmed. If
that prefix holds no newline the first line is longer than any marker, so it is line mode
without further inspection. Peeking, not reading, so no input is consumed:

- exactly `[` → JSON-array mode
- begins with `--` → YAML mode (unchanged from today's two-byte peek, so no existing stream
  changes behaviour)
- anything else → line mode

An explicit `format` skips detection. `-historical` calls `Records` once per stored file, so
each file is detected independently; that stays true.

**JSON-array mode** processes one input line at a time, carrying scanner state across lines:

- A line that is empty, or is only array framing — `[`, `]`, `,`, `],` — is dropped. This is
  what ignores the leading `[`.
- A line whose trimmed text starts with `{` begins a record. Accumulation tracks brace depth
  with a scanner that respects strings and escapes, so a `{` inside `user_agent` or a SQL
  string cannot throw off the count. Scanner state (depth, in-string, escaped) persists
  across lines for the duration of a record.
- A record's bytes are the original input bytes from the start of its opening line through
  the byte where depth returns to 0, newlines included and leading indentation retained.
  `LogEntryAdapter.Detect` already trims space and `json.Unmarshal` tolerates leading
  whitespace, so the indentation costs nothing.
- The record ends at the byte where depth returns to 0. The rest of that line is fed back
  through the same state machine as if it were a fresh line, so a trailing `,` is dropped as
  array framing and a second object sharing the line still starts its own record.
- **Any other line is emitted immediately as its own record**, unchanged. That is how a
  plain-text line interleaved into the stream stays a plain-text log line.
- EOF with a record in progress emits the partial buffer; `adapter.ParseLine` fails to parse
  it and falls back to raw text flagged `Malformed`. This is the normal ending for a Ctrl-C'd
  tail, not an edge case, and emitting beats silently dropping data.
- If a record in progress exceeds 16 MiB it is emitted as-is (parsing will flag it
  `Malformed`), the scanner resets, and the reader resumes looking for the next line that
  starts a record. A long-running tail must not be able to buffer without bound because one
  object never closed.

Single-line JSON returns to depth 0 on its own line, so line-per-record producers behave
exactly as they do today.

The emitted slice stays valid only during the `emit` call, as now.

### 3. LogEntry field normalization — `internal/adapter`

`fromLogEntryObject` reads fields through a lookup that honours `field-naming`. Under `auto`
it tries the camelCase key, then the snake_case one; the two spellings cannot collide in one
document, which is why `auto` is safe as the default and why `camel` and `snake` exist only
as escape hatches.

Aliases covered, matching the sample key sets exactly:

| camelCase | snake_case |
|---|---|
| `jsonPayload` | `json_payload` |
| `textPayload` | `text_payload` |
| `httpRequest` | `http_request` |
| `receiveTimestamp` | `receive_timestamp` |
| `insertId` | `insert_id` |
| `logName` | `log_name` |
| `spanId` | `span_id` |
| `traceSampled` | `trace_sampled` |

`timestamp`, `trace`, `labels`, `resource` and `severity` are spelled the same either way.
`httpRequest.status` is also spelled `status` in both, so the nested read is unaffected even
though its siblings (`request_method`, `remote_ip`, `user_agent`) are snake_case.

Correlation-id extraction (`correlationID`) searches the same candidate list at the root and
inside the payload, and gains the snake_case payload spelling plus `span_id` alongside
`spanId`. The `y` sample carries `span_id: ''`; an empty value is skipped as today.

Severity under `severity = "auto"` keeps the existing string path and adds the numeric
`LogSeverity` ladder when the decoded value is a number:

| value | severity | value | severity |
|---|---|---|---|
| 0 | DEFAULT | 500 | ERROR |
| 100 | DEBUG | 600 | CRITICAL |
| 200 | INFO | 700 | ALERT |
| 300 | NOTICE | 800 | EMERGENCY |
| 400 | WARNING | | |

Both samples carry `severity: 200`. A number that is not exactly on the ladder maps to the
nearest defined value at or below it — 250 is INFO, 900 is EMERGENCY — so a future
intermediate code degrades sensibly rather than becoming DEFAULT. A number below 0, or one
with a fractional part, maps to DEFAULT. `normalizeYAML` already coerces YAML integers to `float64`, so JSON and YAML
present the same type here.

**API shape.** `ParseLine` today is a package-level function over package-level adapter
values (`internal/adapter/adapter.go:12`), called from `main.go`, `internal/headless`, and
about 18 test sites. Introduce `adapter.New(cfg config.Input) *Parser` with a `ParseLine`
method, and keep `adapter.ParseLine` as a package-level wrapper over a default-config
`Parser`. Existing call sites and tests then need no change.

### 4. Log-file round-trip

`internal/logfile/writer.go:161` writes `rec` followed by `'\n'`, so the log file's invariant
is one record per line. A multi-line JSON record written verbatim would break it: the stored
file's first line is `{`, not `[`, so `-historical` replay would detect line mode and shred
each record back into per-line fragments. YAML escapes this by luck — its records keep their
leading `---`, so replay re-detects YAML mode and re-groups them correctly.

Fix: the log-file sink puts every record that a replay could not reassemble onto one line
before writing. A new `ingest.CompactRecord(rec []byte) []byte` returns `rec` unchanged when
it holds no newline, and the `json.Compact` result when the record is valid JSON.

A multi-line record that is *not* valid JSON keeps its newlines only when its first line
begins `--`. That is precisely the condition a replay's own detection tests, so the records
allowed to stay multi-line are exactly the records that get re-grouped on the way back in.
Every other multi-line record has its line breaks collapsed to spaces, with all other bytes
kept. The case that forces this is the partial object `jsonArray` emits at EOF: a tail
stopped with Ctrl-C leaves an unclosed object, which is both multi-line and invalid JSON, so
passing it through verbatim would break the one-record-per-line invariant and return it from
a replay as three or four junk entries. Compacting unconditionally is not an option — it
would destroy YAML documents — and dropping the record would lose data.

`recordSink` (`main.go:437`) calls it; that covers the TUI and `--filter` alike, since both
take their sink from there.

`Entry.Raw` keeps the original pretty-printed bytes, so the detail pane, find
(`internal/tui/find.go:55`) and yank (`internal/tui/model.go:914`) all still show what
actually arrived on the wire. Only the on-disk copy is compacted.

`headless.Run` continues to write `e.Raw` to stdout, so `--filter` output stays
byte-for-byte faithful to its input, multi-line records included.

### 5. Wiring

`main.go` loads the config immediately after `flag.Parse` and the `--theme` check, before
anything is launched or written, so a config error exits 2 alongside the other usage errors.
It handles `-config` there and returns. The resolved `config.Input` is passed to
`ingest.Records` at both call sites (`main.go:292`, `internal/headless/headless.go:18` — the
latter gains a parameter) and to `adapter.New`.

Discovery starts from the working directory, which is already the anchor for `.loghorn/` and
for the source-tree refusal, so all three agree on what "this project" means.

## Testing

Table-driven unit tests per package.

`internal/config`: nearest-file-wins with `.config/loghorn/config.toml` at several depths;
the walk stopping at the first match rather than merging; a walk reaching the root with no
match falling through to the user-level file; a project file suppressing the user-level file
entirely; the user-level stop still applying when the start directory is outside the home
tree; `XDG_CONFIG_HOME` honoured for that stop when set and ignored when empty; a file that
exists but cannot be read failing rather than being skipped; every key's defaults; unknown
key and unknown section warning but parsing; unknown value and malformed TOML returning
errors naming the file and key. Tests build directory trees under `t.TempDir()` and pass an
explicit start directory and an injected home/XDG path, so nothing depends on the real
working directory or the developer's real home.

`internal/ingest`: first-line detection for each of the three modes and each explicit
`format` override; the leading `[` dropped; a multi-line object emitted as one record; braces
and brackets inside strings not disturbing depth; escaped quotes inside strings; a plain-text
line interleaved between objects emitted on its own; a truncated final object emitted;
an object sharing a line with array framing; the 16 MiB cap emitting and resetting;
single-line JSON and plain-text streams byte-identical to today's `Lines` output; and the
existing YAML tests still passing unchanged. `CompactRecord` covers single-line passthrough,
multi-line JSON compaction, and YAML passthrough.

`internal/adapter`: both spellings of every aliased key producing the same `Entry`; `camel`
and `snake` restricting correctly; numeric severity across the full ladder plus an
out-of-ladder value; string severity unchanged; correlation id from `span_id`; an empty
`span_id` skipped.

`internal/logfile` / end-to-end: a multi-line JSON record surviving record → `CompactRecord`
→ `Writer.Write` → re-read through `Records` as one equivalent record, which is the
regression this section exists to prevent.

Fixtures: trimmed excerpts of `../auction/x` and `../auction/y` committed under `testdata/`
— a handful of records each, including `x`'s truncated tail — so the real shapes are covered
without carrying 7,500 lines.

## ROADMAP changes

- The v1 bullet specifying `~/.config/loghorn/config.toml` overridable by `./loghorn.toml`
  is superseded by a single name: `.config/loghorn/config.toml`, found by walking up from the
  working directory, nearest wins entirely, with the user-level file as the walk's final
  stop rather than a separate mechanism. Its remaining content (saved filters, context `N`,
  colours, keybindings, alert cooldowns) stays v1 and becomes additional sections, or sibling
  files under `.config/loghorn/`.
- The "Lenient LogEntry normalization" bullet is narrowed, not closed: gcloud's snake_case
  aliases and numeric severity are handled here; promoting arbitrary root keys to effective
  payload and JSONPath resolution remain v1.
