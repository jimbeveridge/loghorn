# loghorn keeps a log file

Date: 2026-09-15

## The need

loghorn only holds its scrollback in memory, so whatever the dev server said is gone
the moment loghorn exits — or the moment the ring evicts it. loghorn should echo every
record it receives to a file, always, and expire old lines so the file cannot grow
without bound.

This runs on a devserver, where anything older than 72 hours is safe to delete. That is
a floor, not a target: a line younger than 72 hours must survive, and an older one may
linger a while.

## Design

### Whole days, never partial files

A log file can only be appended to; dropping lines from the front means rewriting it.
So expiry works on whole files: one file per local calendar day, and an old day is
deleted by unlinking it. Lines are never parsed for their age — a day's file holds
exactly the records that *arrived* that day, which is the only age that is reliable
(raw text often has no timestamp, and a replayed file carries old ones).

Keeping **today plus the three previous calendar days** is the smallest whole-day
rule that honours the floor. A day's file becomes deletable only once its *newest*
line is 72 hours old, which is the moment the fourth day starts. Worst case a line
lives about 96 hours. Using calendar dates rather than hour arithmetic keeps DST days
(23 or 25 hours) from mattering.

### Files

All three live in a `.loghorn/` folder in the directory loghorn was started from
(`filepath.Join(cwd, ".loghorn")`, `cwd` from `os.Getwd()` at startup), not next to
the executable. One binary serves every project on the machine, so keying the
directory off the executable meant every loghorn anywhere shared a single lock —
reported by the user running loghorn from two projects at once. Keying it off the
start directory instead gives each project its own logs and its own lock.

| File | Holds |
|---|---|
| `loghorn.log` | the current day — a fixed path, so `tail -F` works |
| `loghorn-YYYY-MM-DD.log` | a finished day, named by its local date |
| `loghorn.lock` | the single-writer lock; holds the owner's PID, never deleted |

`logfile.Open` creates `.loghorn/` itself (`os.MkdirAll(dir, 0o700)`, owner-only like
the files it holds) before taking the lock, and writes `.loghorn/.gitignore` containing
exactly `*\n` — the same trick `.pytest_cache` uses to ignore itself — the first time it
sees the directory (`O_EXCL`, so an existing `.gitignore`, whether loghorn's own from an
earlier run or a user's edit, is left untouched rather than overwritten). That's enough
for a project's own `git status` to stay clean without editing the project's own
`.gitignore` to mention `.loghorn/`.

All three are created `0600` (owner read/write only, via `OpenFile`'s mode argument, not a
separate `chmod`). The log can hold days of dev-server output — tokens, auth headers — that
other accounts on the box have no business reading; the lock file gets the same mode simply
so every file this package creates is uniformly owner-only. Archiving keeps the mode: renaming
onto a new archive name carries the source file's mode, and appending onto an existing archive
opens that archive rather than creating it, so its own mode is untouched.

Each record is written as its original bytes plus `\n`, in one unbuffered `write` — "original
bytes" means as `ingest.Records` defines a record: it strips the line's trailing newline and, for
CRLF input, the `\r` before it; the writer then adds back a single `\n`. So `loghorn <
loghorn-2026-09-14.log` replays a record identical to what produced it — ingest strips the same
`\r`/`\n` on the way back in, so nothing about the replay is lossy relative to the first pass.
YAML documents keep their leading `---` line. Unbuffered keeps `tail -F` current and loses nothing
if loghorn crashes; dev-server volume doesn't warrant buffering.

### Refusing to run from the loghorn source tree

Before anything else — before launching a child, and in `--filter` mode too — loghorn
walks up from the current directory to the nearest `go.mod`. If its `module` line is
`github.com/jimbeveridge/loghorn`, loghorn exits 1:

```
loghorn: refusing to run inside the loghorn source tree (/Users/jim/code/loghorn);
run it from your project's directory
```

loghorn is meant to be run from the project it watches, and loghorn's own source tree
is never that project — the check catches the mistake of running it from its own repo
by accident. (Its older rationale — that `go run .` builds into a temporary directory
Go deletes on exit, taking the logs with it — no longer applies now that logs live in
the start directory rather than next to the executable; the check is kept anyway,
unchanged, because the source tree still isn't a sensible place to run loghorn from.)
The `module` line is read with a plain line scan; no `golang.org/x/mod` dependency.

Checking `go.mod` rather than `git config --get remote.origin.url` needs no `git` on
`PATH`, no `origin` remote, and no matching of SSH vs HTTPS URL forms, and it still
fires in a fork, whose module path is unchanged.

### Replaying a file is not recorded

Measured: `loghorn --filter < loghorn.log`, run from outside the repo so the source-tree
check doesn't intervene, reads the log file while appending every record it reads back to
that same file — so it never reaches EOF. Three lines became 357,965 lines (1.6 MB) in one
second. Replaying an archive (`loghorn < loghorn-2026-09-14.log`) doesn't loop, since that
file isn't the one being written to, but it does copy the whole day into today's file, which
is just as unwanted.

The fix is not to open the log file at all when a run is a replay: no command was given
after `--` (so `flag.Args()` is empty) *and* stdin is a regular file (`os.Stdin.Stat()` —
stdin has no path, so this is an fstat on the open descriptor, not `os.Stat` on a name —
then `Mode().IsRegular()`; a `Stat` error counts as not regular, so an unidentifiable stdin is
still recorded rather than silently dropped). A file already on disk is already recorded —
writing it again adds nothing — while a pipe or a launched command's output only exists once,
so those are still recorded. No log file being opened means no lock is taken, nothing is
written, the TUI's bar carries no marker, and neither mode prints anything: not recording a
replay is the unsurprising case, not a failure. This applies in both the TUI and `--filter`;
`--exclusive` is moot when no log file is opened at all. The source-tree refusal above still
runs first, unchanged.

### Reading stored logs (`-historical`)

`loghorn -historical` reads the files this package writes back into the normal TUI (or
`--filter`) instead of watching live input:

- **Source.** Every `loghorn-YYYY-MM-DD.log` archive in `.loghorn/` in the current
  directory, oldest first, then `loghorn.log` if it exists — `logfile.HistoricalFiles(dir)`.
  Names that don't parse as one of those two shapes (`loghorn.lock`, `.gitignore`, a stray
  `other.log`, a hand-renamed `loghorn-junk.log`) are ignored.
- **Stops at the end.** No following of `loghorn.log` after the stored lines are read — each
  file is a plain `*os.File`, so `ingest.Records` reaching EOF ends that file and the loop
  moves to the next, with nothing left running once the last one is read.
- **Read-only.** `-historical` never calls `openLogFile`: no lock is taken, nothing is written,
  the TUI carries no marker, and no message is printed. It therefore works while another
  loghorn is recording, and `--exclusive` has no effect on it. In particular it never creates
  `.loghorn/` — `HistoricalFiles` on a missing directory simply reports no stored files
  (`loghorn: no stored logs in <dir>`), rather than `Open`'s `os.MkdirAll`, which only the
  always-on log file's own `openLogFile` path reaches.
- **Scrollback.** If `--scrollback` was not passed explicitly (`flag.Visit`), the ring's
  capacity defaults to 100,000 instead of the live default of 5,000: three or four stored days
  of typical output usually exceed 5,000 lines, and 100,000 keeps memory bounded without
  silently evicting the oldest of them.
- **Guards**, after the source-tree refusal: a command after `--` (`-historical` is a reader,
  not a launcher) or stdin that isn't a character device (piped or redirected — it would
  compete with the stored files) each exit 2; no stored files at all exits 1.
- **Per-file format detection.** `ingest.Records` detects YAML vs line format by peeking the
  start of its stream, so it runs once per file, in order, rather than over one concatenated
  stream — different days may come from different producers. `--filter` mode likewise calls
  `headless.Run` once per file, with a nil sink.
- **Opening.** Every listed file is opened before any is read, so a concurrent midnight rename
  by a recording loghorn can't make a file disappear mid-listing — an open descriptor survives
  the rename. A file that vanished between listing and opening (pruned in that window) is
  skipped; any other open error is fatal.

### Single writer

loghorn takes `flock(LOCK_EX|LOCK_NB)` on `loghorn.lock` and holds the descriptor for
its lifetime. The kernel drops the lock when the process exits or crashes, so there is
never a stale lock to clean up, and the file is never deleted (deleting a lock file
races the next locker).

Having taken the lock, loghorn truncates the file and writes its PID. The PID is purely
for the message a second instance prints — it is never used to decide who holds the
lock. A PID-file lock ("is that PID still alive?") breaks when a crashed owner's PID is
reused by an unrelated process, and races when two instances start together; `flock`
has neither problem.

Only one loghorn is expected at a time. Without the lock, a second one would corrupt the
first at midnight: both roll over, and one ends up writing to an unlinked file — a whole
day of records silently discarded while its TUI looks fine. If a second one starts
anyway:

- **By default** it runs normally **without a log file** and says so, naming the owner
  (see Failures).
- **With `--exclusive`** it exits 1 instead:

  ```
  loghorn: another loghorn (pid 48213) is writing logs in /Users/jim/code/loghorn-wt
  ```

The lock is taken before a child is launched, so `--exclusive` never starts
`npm run dev` only to abandon it.

### Startup

1. **Source-tree check** (above).
2. **Create `.loghorn/`** (`os.MkdirAll(dir, 0o700)`) and write its self-ignoring
   `.gitignore` if one isn't already there. Either failing exits 1, unwritable-start-dir
   included.
3. **Lock.** If held: with `--exclusive` exit 1; otherwise skip the remaining steps and
   run without a log file. If taken, write the PID.
4. **Archive a leftover.** If `loghorn.log` exists and its mtime falls before local
   midnight today, move it to `loghorn-<mtime's local date>.log` — by rename, or by
   appending its contents and removing it if that archive already exists.
5. **Prune.** Delete every `loghorn-YYYY-MM-DD.log` dated before today − 3 days. Names
   that don't parse as a date are left alone.
6. **Open** `loghorn.log` with `O_APPEND|O_CREATE`. Failure here exits 1: the file is
   always on, so an unwritable directory is a setup error, not something to run
   through.

**Why the mtime is trustworthy.** While loghorn runs, it archives `loghorn.log` itself at
midnight. So a `loghorn.log` found at startup contains only records from the day of its
last write — which is its mtime. The only way to fool it is to `touch` or hand-edit the
file, and the cost is an archive misdated by a day. An mtime in the future (clock skew)
is not before today's midnight, so the file is kept as today's.

### Midnight

The writer tracks `day`, the local date the current file covers, and the next local
midnight after it. On each record, before writing:

- If now is at or past that midnight: close `loghorn.log`, archive it as
  `loghorn-<day>.log` (same rename-or-append rule), prune, reopen, and advance `day` to
  today's date.
- Then write the record.

Rolling over *before* the write is what keeps a record whole and on the right side of
midnight. The archive is named by `day`, not by yesterday's date, so a file opened on
the 14th that sees nothing until the 16th is still archived as the 14th — which again
agrees with its mtime.

The writer checks only on a record. If nothing arrives after midnight, nothing rotates
until the next record or the next startup, and the startup mtime rule covers that.

### Failures

A write or rotation error mid-run (disk full, directory removed) stops the log file for
the rest of the run and is reported once; loghorn keeps running. The TUI should not die
over its side log.

- **TUI:** a persistent `no log file` marker on the status bar, in the same spirit as
  the mouse-capture-off marker: the bar names the surprising state and says nothing in
  the normal one. It is not a one-shot `notice`, which the next keystroke would erase.
  The ingest goroutine delivers it with `p.Send`, as `ChildExited` does. The lock-held
  case shows the same marker with the owner, `no log file (pid 48213 has it)`.
- **`--filter`:** one line on stderr; stdout stays the filtered stream. Lock held:
  `loghorn: another loghorn (pid 48213) is writing logs in <dir>; running without a log file`.

If the holder's PID can't be read (it is between taking the lock and writing it), the
messages drop the PID rather than fail.

## Structure

- **`internal/logfile`** (new): a `Writer` that owns the lock, both rotations and pruning.
  `Open(dir string, clock func() time.Time, loc *time.Location) (*Writer, error)`,
  `Write(rec []byte) error` (after the first error, a no-op that returns nil), `Close()`.
  `Open` reports a held lock as a distinct error type carrying the holder's PID (0 if
  unreadable), so the caller can choose to run on without a file or exit. The injected
  clock and location make every date decision testable.
- **Source-tree check:** a small function taking the starting directory, so tests can
  point it at a temp tree.
- **`main.go`:** adds `--exclusive`; runs the check, resolves `.loghorn/` under the current
  directory, and opens the writer — all before `--filter` dispatch and before
  `runner.Start` — then calls `Write` first thing in the existing ingest callback.
- **`headless.Run`:** gains a record-sink parameter it calls for every record, important
  or not. Tee-ing the raw input bytes instead would let a midnight rollover split a
  multi-line YAML record across two files.
- **`internal/tui`:** a message and bar marker for the log file being off.

## Testing

All in `t.TempDir()`, with a fake clock and `America/Los_Angeles`:

- A leftover `loghorn.log` with a yesterday mtime (`os.Chtimes`) is archived under that
  date; one with today's mtime is appended to.
- Archiving onto an existing archive appends rather than overwrites.
- A clock crossing midnight mid-stream: the record before lands in the archive, the
  record after in the new `loghorn.log`.
- A gap of several days archives under the file's own `day`.
- Pruning keeps today and the three previous dates, deletes older, ignores unparseable
  names.
- A DST-change day rotates at local midnight.
- `Open` writes its PID to `loghorn.lock`; a second `Open` on the same directory reports
  the lock as held with that PID, and the lock is free again after the first `Close`.
- `Open` on a not-yet-existing directory creates it `0700` and writes a self-ignoring
  `.gitignore` (`*\n`); an existing `.gitignore` is left untouched.
- After a write error, later writes are no-ops.
- Source-tree check: fires in a nested directory under loghorn's `go.mod`; passes under
  another module's `go.mod` and with no `go.mod` at all.
- `headless.Run` passes every record to the sink, not only important ones.

## Known limits, accepted

- Manual testing from the repo root (`./loghorn < docs/backend.log`) is now refused; run
  it from another directory.
- Retention is 72–96 hours, not exactly 72.
- loghorn needs a writable start directory; an unwritable one exits at startup. The log
  file is always on and there is no `--log-dir` to redirect it, so this is a hard stop,
  not something to run through.
- If the host's clock is wrong in a way that puts it days ahead — a VM started before its
  first NTP sync, say — startup pruning trusts that clock and can delete archives that are
  not actually old.
- Input replayed from a file (`loghorn < file`) is not recorded, per the rule above; only
  pipes and launched commands are.
- `-historical`'s per-line ingest-time column shows when the line was replayed, not when it
  originally arrived — the stored files don't record arrival time. The record's own timestamp,
  where the producer included one, is still shown in the detail pane.
- `-historical` reads `.loghorn/` in the current directory — the directory it's run from,
  same as the always-on log file, not wherever an earlier recording loghorn happened to run
  from.

## Out of scope

A size cap, configurable retention, a `--log-dir` override, several concurrent writers,
compressing archives.
