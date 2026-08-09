# loghorn launches the producer

Date: 2026-08-08

## The bug

Quitting loghorn left `npm run dev` running, in a state that was hard to escape.

Measured cause: in `npm run dev | loghorn` only *stdout* is piped. Both processes
still have `/dev/tty` open and both read it — loghorn for its keys, the dev server
for its own shortcuts (vite `r`/`q`, nodemon `rs`). The kernel hands each byte to
whichever reader gets there first. In a harness sending ten keystrokes, **eight
were taken by the producer**.

That accounts for the whole report:

- `q` frequently never reaches loghorn.
- The dev server has been fed stray `j`/`k`/`q`/`m` keys, and since mouse capture
  landed, whole bursts of mouse escape sequences — executing whatever those mean
  to it. That is the "weird state".
- It survives loghorn's exit because node ignores `SIGPIPE`, and loghorn is a pipeline
  peer rather than a parent, so it has no way to signal it.

loghorn's own exit is clean: cooked mode, `ISIG`, mouse tracking, alt screen and
cursor are all restored (verified). The terminal is fine; the dev server is not.

None of this is fixable while loghorn is a pipeline peer — the tty is genuinely
shared. It disappears if loghorn launches the producer itself.

## Design

### Launch mode

```sh
loghorn -- npm run dev     # loghorn owns the child
npm run dev | loghorn      # unchanged, for files and non-interactive producers
```

Everything after `--` is the command (Go's `flag` package already terminates
there). With no command, loghorn reads stdin exactly as before.

### Process wiring

- **stdout and stderr share one pipe** into the existing ingest path. This also
  fixes the earlier bug where a producer's stderr bypassed the pipe and painted
  over the TUI.
- **stdin is a pty** (`creack/pty`). A plain pipe would work for the lifecycle,
  but vite, next and nodemon all gate their keypress handling on
  `process.stdin.isTTY`, so key forwarding would silently do nothing against
  exactly the tools it is for.
- **Own process group** (`Setpgid`), so the child and everything it spawns can be
  signalled together. No `Setsid`: the child stays in loghorn's session but out of
  the foreground group, so terminal input reaches only loghorn.

### Quit

| Key | Effect |
|---|---|
| `q`, `ctrl+c` | `SIGTERM` the child's process group, wait `--shutdown-grace` (default 5s), `SIGKILL` any survivor, then exit |
| `Q` | exit loghorn only, leaving the child running detached |

Termination runs inside a `tea.Cmd` so the grace period does not block the UI.

In pipeline mode there is no child and both keys simply quit.

### Child exit

If the child exits on its own, loghorn stays open and the bar reports
`child exited (1)` — a crash is exactly when you want to scroll back and read the
output. `q` and `Q` then both just quit.

### Key forwarding

`f` toggles forward mode: every keystroke is written to the child's pty until
`esc`. While armed, the bar replaces its hints with a loud banner, because `q`
goes to the child rather than quitting. Unavailable in pipeline mode.

## Testing

- The child's process group is terminated on `q`, including a grandchild it
  spawned, and `Q` leaves it alive.
- A child ignoring `SIGTERM` is `SIGKILL`ed after the grace period.
- stdout and stderr both reach the ingest path.
- Child exit is surfaced on the bar and does not close the TUI.
- Forward mode writes to the pty and `esc` leaves it.
- End to end through a pty: launch, quit, assert nothing survives.

## Known limitation

Keeping the child out of the foreground process group is what stops it taking
keystrokes — but it also means a child that reads `/dev/tty` *directly* (rather
than stdin) gets `SIGTTIN` and is stopped by the kernel, showing as state `T`.
Measured: a normal producer reading stdin runs fine (state `S`) and survives a
detached quit; one opening `/dev/tty` is suspended and then dies of `SIGHUP` when
its orphaned group is cleaned up on loghorn's exit.

This is inherent to the design, not incidental: a child that can read the
terminal is a child that can steal your keys. It affects direct-tty readers such
as password prompts (`sudo`, `ssh`, git credential helpers) in the child's
pipeline. Dev servers read `process.stdin` and are unaffected. Pipeline mode
remains available for those cases.

## Out of scope

Restarting the child (that is a process manager, and dev servers already restart
themselves), and forwarding signals other than on quit.
