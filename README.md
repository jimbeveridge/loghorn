# loghorn

Watch a dev server's logs and surface the failures.

loghorn reads a log stream, decides which lines matter, and shows those. Everything
else stays in the scrollback, one keystroke away — so a request that failed is
readable in context rather than lost in ten thousand routine lines.

A line is a **failure** if any of these holds:

- its severity is `ERROR` or above
- its HTTP status is 500 or above
- its message matches `panic`, `fatal`, `exception` or `traceback`, case-insensitively

It reads GCP `LogEntry` JSON, gcloud's YAML and JSON output, and plain text, deciding
per stream which it is.

## Install

Requires Go 1.25 or newer.

```sh
go install github.com/jimbeveridge/loghorn@latest
```

Or from a checkout:

```sh
go build -o loghorn .
```

## Running it

**Launch the producer — the preferred way:**

```sh
loghorn -- npm run dev
```

loghorn owns the process: it captures stdout and stderr together, keeps the keyboard
to itself, and stops the whole process group on quit.

**Read a pipe** — right for files and non-interactive producers:

```sh
kubectl logs -f pod | loghorn
gcloud beta logging tail --json | loghorn
```

An interactive producer in a pipeline fights loghorn for `/dev/tty`, so both read it
and keystrokes get split at random. Launch it instead.

**Replay what was stored:**

```sh
loghorn -historical
```

**Headless, for a script or CI** — prints only the failures to stdout, byte-for-byte
as they arrived:

```sh
loghorn --filter < capture.json
```

## Keys

| Key | |
|---|---|
| `j` / `k` | move a line |
| `ctrl+d` / `ctrl+u` | half page |
| `g` / `G` | oldest / newest |
| `space` | hold / release the live tail |
| `a` | all lines / failures only |
| `c` | pin this line's request; again releases |
| `/` *text* `enter` | find; `/` then `enter` clears |
| `enter` | open the detail pane |
| `y` | copy the entry to the clipboard |
| `f` | send keys to a launched child until `esc` |
| `q` | quit (`Q` quits but leaves the child running) |
| `?` | the full list, in the app |

## gcloud

Both shapes gcloud emits are read as-is:

```sh
gcloud beta logging tail --json | loghorn
gcloud logging read --format=yaml | loghorn
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

## Configuration

Settings live in `.config/loghorn/config.toml`. Every key is optional, and every
default is the behaviour loghorn has without a file at all, which is why the file is
worth creating only when a producer needs something unusual.

```toml
[input]
format       = "auto"   # auto | json | yaml | text
field-naming = "auto"   # auto | camel | snake
severity     = "auto"   # auto | string | numeric

[display]
format-sql          = true    # lay SQL out a clause to a line
unquote-identifiers = false   # drop redundant SQL identifier quoting
keyword-case        = "preserve"   # preserve | upper | lower
```

- **`format`** — how the stream is split into records. `auto` decides from the first
  line. `json` forces JSON-array framing, which is what a tail attached *mid-stream*
  needs, having missed the opening `[`. `yaml` forces `---` documents. `text` turns
  all grouping off, one record per line.
- **`field-naming`** — `auto` accepts either spelling of a `LogEntry` field name;
  `camel` and `snake` restrict it to one.
- **`severity`** — `auto` accepts a string name or a numeric code; `string` and
  `numeric` restrict it to one.
- **`format-sql`** — lays a logged statement out a clause and a select item to a line
  before the pane shows it. On by default. Turn it off to see statements exactly as they
  were logged, which for a generated statement means one long line; that also skips the
  formatter, which costs about 100ms the first time it sees a given statement.
- **`unquote-identifiers`** — ORMs quote every identifier they generate, so a logged
  statement arrives as ``select `id`, `user_id` from `grants` `` and reads as more
  punctuation than SQL. With this on, the detail pane drops the quotes that carry no
  meaning and shows `select id, user_id from grants`. Quotes that *do* carry meaning
  stay: a reserved word (`` `order` ``), a name with a space in it, a name starting
  with a digit. The SQL lexer decides which is which, so there is no keyword list to
  fall out of date. Only the pane is rewritten — the stored log file and the entry's
  raw bytes keep the statement exactly as logged — but note that yank copies what the
  pane shows, so with this on you are copying the unquoted form.
- **`keyword-case`** — how SQL keywords are cased in the detail pane. `preserve` shows
  them as the statement wrote them; `upper` gives you `SELECT ... FROM ... WHERE`, which
  is the usual way to tell keywords from the columns around them, and `lower` the
  reverse. Only keywords are recased — a quoted identifier and the contents of a string
  literal are left alone. An unrecognised value is rejected when the file loads rather
  than passed through, because sql-formatter answers one by silently deleting every
  keyword from the statement. This is the formatter's own setting, so it does nothing
  with `format-sql = false`; `unquote-identifiers` still applies either way.

### Where the file is found

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
loghorn -config
```

`.config/loghorn/` is hand-written and belongs in version control. It is not
`.loghorn/`, which holds generated logs and ignores itself.

## The log file

Every record loghorn receives is appended to `.loghorn/loghorn.log` in the directory
it starts in, so each project gets its own logs and its own lock. Each finished day is
kept as `loghorn-YYYY-MM-DD.log`, and days before the last three are deleted. The
directory is created owner-only and ignores itself, because a dev server's logs can
carry tokens and auth headers.

Input read from a file (`loghorn < file`) is not recorded — it is already on disk.
loghorn refuses to run from inside its own source tree.

## Flags

| Flag | Default | |
|---|---|---|
| `--filter` | off | headless: print only the failures to stdout |
| `-historical` | off | read the stored files instead of live input |
| `-config` | off | print the config file in effect, then exit |
| `--scrollback` | `5000` | entries kept in memory |
| `--notify` | off | desktop notification on fresh errors |
| `--notify-max-age` | `1s` | skip notifications for errors older than this |
| `--notify-reset` | `15s` | quiet gap that ends an error burst |
| `--notify-sound` | on | play the system alert sound with notifications |
| `--theme` | `auto` | `auto`, `light`, `dark`, or a background as `#rrggbb` |
| `--exclusive` | off | exit rather than run without the log file if another loghorn holds it |
| `--shutdown-grace` | `5s` | how long a launched command gets on SIGTERM before SIGKILL |

One dash or two makes no difference — `-filter` and `--filter` are the same flag.
`loghorn --help` is authoritative.

## Docs

`docs/ROADMAP.md` tracks what is built and what is planned. Design documents for each
feature live in `docs/superpowers/specs/`.

## License

Apache 2.0 — see [LICENSE](LICENSE).
