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

All three live in the directory of the resolved executable
(`filepath.EvalSymlinks(os.Executable())`):

| File | Holds |
|---|---|
| `loghorn.log` | the current day — a fixed path, so `tail -F` works |
| `loghorn-YYYY-MM-DD.log` | a finished day, named by its local date |
| `loghorn.lock` | the single-writer lock; holds the owner's PID, never deleted |

In practice that directory is the root of a loghorn checkout or worktree, next to the
already-ignored `/loghorn` binary. `.gitignore` gains root-anchored `/loghorn.log`,
`/loghorn-*.log` and `/loghorn.lock` (anchored, so a new `.log` under `testdata/` is not
hidden). `git worktree remove` does not count ignored files as untracked, so it doesn't
refuse, and a worktree's logs are deleted silently along with it — verified in a
scratch repo.

Each record is written as its original bytes plus `\n`, in one unbuffered `write`.
YAML documents keep their leading `---` line, so `loghorn < loghorn-2026-09-14.log`
replays exactly what was seen. Unbuffered keeps `tail -F` current and loses nothing if
loghorn crashes; dev-server volume doesn't warrant buffering.

### Refusing to run from the loghorn source tree

Before anything else — before launching a child, and in `--filter` mode too — loghorn
walks up from the current directory to the nearest `go.mod`. If its `module` line is
`github.com/jimbeveridge/loghorn`, loghorn exits 1:

```
loghorn: refusing to run inside the loghorn source tree (/Users/jim/code/loghorn);
run it from your project's directory
```

The current directory is never meant to be loghorn's own repo, and this catches the
case that would otherwise go wrong quietly: `go run .` builds the binary into a
temporary directory that Go deletes on exit, taking the logs with it, and `go run .`
only works from inside the repo. The `module` line is read with a plain line scan;
no `golang.org/x/mod` dependency.

Checking `go.mod` rather than `git config --get remote.origin.url` needs no `git` on
`PATH`, no `origin` remote, and no matching of SSH vs HTTPS URL forms, and it still
fires in a fork, whose module path is unchanged.

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
2. **Lock.** If held: with `--exclusive` exit 1; otherwise skip the remaining steps and
   run without a log file. If taken, write the PID.
3. **Archive a leftover.** If `loghorn.log` exists and its mtime falls before local
   midnight today, move it to `loghorn-<mtime's local date>.log` — by rename, or by
   appending its contents and removing it if that archive already exists.
4. **Prune.** Delete every `loghorn-YYYY-MM-DD.log` dated before today − 3 days. Names
   that don't parse as a date are left alone.
5. **Open** `loghorn.log` with `O_APPEND|O_CREATE`. Failure here exits 1: the file is
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
- **`main.go`:** adds `--exclusive`; runs the check, resolves the executable's directory,
  and opens the writer — all before `--filter` dispatch and before `runner.Start` — then
  calls `Write` first thing in the existing ingest callback.
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
- After a write error, later writes are no-ops.
- Source-tree check: fires in a nested directory under loghorn's `go.mod`; passes under
  another module's `go.mod` and with no `go.mod` at all.
- `headless.Run` passes every record to the sink, not only important ones.

## Known limits, accepted

- A `go install`ed binary writes to `~/go/bin`, which is not a worktree and is never
  cleaned up by git. Only running from inside the repo is guarded.
- Manual testing from the repo root (`./loghorn < docs/backend.log`) is now refused; run
  it from another directory.
- Retention is 72–96 hours, not exactly 72.

## Out of scope

A size cap, configurable retention, a `--log-dir` override, several concurrent writers,
compressing archives.
