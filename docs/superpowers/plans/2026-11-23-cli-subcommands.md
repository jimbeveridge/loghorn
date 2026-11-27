# CLI Subcommands and Always-On serve — Implementation Plan

> **For agentic workers:** executed inline with `superpowers:executing-plans` (the user's choice for this plan), one fresh Opus reviewer on the whole branch at the end. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace loghorn's flag soup with subcommands (`serve`, `run`, `open`, `query`, `status`, `ui`, `config`, `help`), and make `serve` the one always-on OTLP receiver and web server, with `run` as its client.

**Architecture:** A dispatcher in `main` routes `os.Args[1]` to one `flag.FlagSet` per command. A new `internal/serve` package owns the long-running process: data-folder lock, `/tmp/loghorn.sock`, web (4380) and OTLP/HTTP (4318) listeners, today's file storage moved into the per-user data folder, and a router that records each `run`'s lines through a recorder with that run's input settings and service. `run` ships framed lines to `serve` as OTLP logs (scope `loghorn.run`) via `internal/shipper`, or, with `serve` down, follows the `serve-down` setting (`record` into `<data>/incoming/<run id>/`, `skip`, `fail`). `serve` adopts incoming runs.

**Tech Stack:** Go (module `github.com/jimbeveridge/loghorn`), stdlib `flag`/`net/http`, `go.opentelemetry.io/proto/otlp` (already a dependency), `golang.org/x/sys/windows` (already indirect) for the Windows lock.

**Spec:** `docs/superpowers/specs/2026-11-23-cli-subcommands-design.md` — read the sections each task names.

## Global Constraints

- loghorn is public: never write a private application's identifiers (its name, paths, service names or cloud project ids) into code, tests, docs or commit messages.
- This runs in the worktree `.claude/worktrees/cli-subcommands`: no heredocs in Bash, run each git command on its own (no `&&`/`;` chains with git), never set `XDG_CONFIG_HOME`. Write scripts with the Write tool.
- Tests and demos never touch the real `~/.config/loghorn`, the real data folder or `/tmp/loghorn.sock`: set `HOME`, `LOGHORN_DATA_DIR` and `LOGHORN_SOCKET` to temp paths. Socket paths must fit 103 bytes: make socket dirs with `os.MkdirTemp("/tmp", "lh")` (macOS `t.TempDir()` paths are too long), and remove them in `t.Cleanup`.
- Never pipe input into a `loghorn` binary while probing by hand; use `< file`.
- Web handler tests use `httptest.NewServer`; number values in test records are `json.Number`.
- Startup ordering (CLAUDE.md): everything that can `os.Exit` is validated or bound before a child is launched; `serve` binds every listener before it serves.
- Flags stay single-dash Go style; one `flag.FlagSet` per command with `flag.ContinueOnError`; `-h` exits 0, a bad flag exits 2.
- Defaults: web port `4380`, OTLP/HTTP port `4318`, socket `/tmp/loghorn.sock` (Windows `%LOCALAPPDATA%\loghorn\loghorn.sock`), data folder macOS `~/Library/Application Support/loghorn`, Linux `$XDG_STATE_HOME/loghorn` else `~/.local/state/loghorn`, Windows `%LOCALAPPDATA%\loghorn`. Env overrides: `LOGHORN_SOCKET`, `LOGHORN_DATA_DIR`.
- Exit codes: 0 ok; 2 usage (bad flag, bad config, bad arguments); 1 runtime failure; `run` returns the child's status (127 not found).
- New code under `internal/serve`, `internal/sock`, `internal/datadir`, `internal/incoming`, `internal/shipper`, `internal/browser`, `internal/logfile` uses no Unix-only syscalls outside `_unix.go` files.
- Commit messages end with:
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` and
  `Claude-Session: https://claude.ai/code/session_01BwT7CMQmjqqUEBt9UTEnkd`

## Review Focus

1. Two `run`s recording to one `serve` at once: each run's lines are parsed with its own `[input]` settings and stored under its own service; one run's child sending OTLP never makes `serve` drop the other run's JSON stdout lines. — test in Task 3.
2. `serve` going away mid-run: the child keeps running and its output keeps passing through; `run` never blocks on a full buffer, warns once, and reports how many lines were dropped. — tests in Task 2 (shipper) and Task 5 (e2e).
3. A stale `/tmp/loghorn.sock` after `kill -9` lets the next `serve` start; a live one makes the second `serve` exit 1 before it writes anything. — tests in Tasks 1 and 3.
4. Incoming runs: a folder still locked by a running `run` is never adopted; an unlocked one is adopted exactly once and deleted; its records keep the run's service. — tests in Task 6.
5. Argument boundaries: `loghorn run npm test -filter` passes `-filter` to npm; `loghorn query URL -o f.csv` parses `-o`; a bare `loghorn` with piped stdin records nothing. — tests in Task 4.

---

### Task 1: Foundations — data folder, socket, config sections, portable lock, session fields

Spec sections: "Data folder", "Ports and the socket", "OS-service requirements" (5), "When `serve` is down" (the setting).

**Files:**
- Create: `internal/datadir/datadir.go`, `internal/datadir/datadir_test.go`
- Create: `internal/sock/sock.go`, `internal/sock/sock_test.go`
- Create: `internal/logfile/lock_unix.go`, `internal/logfile/lock_windows.go` (move the flock code out of `lock.go`; `lock.go` keeps `LockedError`, `readPID`, `acquire`'s portable part)
- Modify: `internal/logfile/lock.go`, `internal/logfile/lock_test.go`
- Modify: `internal/config/config.go`, `internal/config/config_test.go`
- Modify: `internal/web/server.go` (Options, sessionJSON), `internal/web/socket_test.go` (session test)

**Interfaces:**
- Produces:
  - `datadir.Dir() (string, error)` — `$LOGHORN_DATA_DIR` if set, else `forOS(runtime.GOOS, os.Getenv, home)`.
  - `datadir.forOS(goos string, getenv func(string) string, home string) string` (unexported, tested).
  - `sock.DefaultPath() string` — `$LOGHORN_SOCKET`, else `/tmp/loghorn.sock` (Windows: `%LOCALAPPDATA%\loghorn\loghorn.sock`).
  - `sock.Listen(path string) (net.Listener, error)` — returns `*sock.LiveError{Path string}` when something answers on path; removes a socket file that refuses connections; refuses a path over 103 bytes; chmods the socket 0600.
  - `sock.Client(path string) *http.Client` (moved from `internal/instances`, same behaviour; `sock.ResponseTimeout` var).
  - `sock.Probe(path string) (sock.Session, error)` — GET `http://loghorn/api/session` on the socket, 2 s budget; `errors.Is(err, sock.ErrNotRunning)` when nothing answers (missing file, refused).
  - `type sock.Session struct { PID int "pid"; Started time.Time "started"; Version string "version"; DataDir string "dataDir"; Socket string "socket"; Port int "port"; OTLPPort int "otlpPort" }` (JSON names in quotes) with `WebURL() string` → `http://127.0.0.1:<Port>/` and `OTLPEndpoint() string` → `http://127.0.0.1:<OTLPPort>`.
  - `web.Options` gains `DataDir, Socket, Version string; OTLPPort int`; `sessionJSON` gains `version`, `dataDir`, `socket`, `otlpPort`.
  - `config.Config` gains `Serve ServeConfig \`toml:"serve"\`` and `Run RunConfig \`toml:"run"\``; `ServeConfig{ OTLPHTTPPort int \`toml:"otlp-http-port,omitzero"\`; WebPort int \`toml:"web-port,omitzero"\` }`; `RunConfig{ ServeDown string \`toml:"serve-down,omitempty"\` }`.
  - `config.DefaultWebPort = 4380`, `config.DefaultOTLPHTTPPort = 4318`; `(ServeConfig) Resolved() (web, otlp int)` applies defaults for zero.
  - `config.ServeDownRecord/Skip/Fail = "record"/"skip"/"fail"`; `(RunConfig) Mode() string` → `record` when empty.
  - `config.LoadUser() (Config, string, error)` — only the user-level file (`$XDG_CONFIG_HOME/loghorn/config.toml` or `~/.config/loghorn/config.toml`), never a repo file; defaults when absent.
  - `logfile.TryLock(dir string) (release func(), err error)` — `*LockedError` when held; does not create dir.

- [ ] **Step 1: Write the failing tests**
  - `TestForOS`: `darwin`, home `/Users/u` → `/Users/u/Library/Application Support/loghorn`; `linux` with `XDG_STATE_HOME=/s` → `/s/loghorn`; `linux` without → `/home/u/.local/state/loghorn`; `windows` with `LOCALAPPDATA=C:\L` → `filepath.Join("C:\\L","loghorn")`.
  - `TestDirEnvOverride`: `LOGHORN_DATA_DIR=/x` → `/x`.
  - `TestDefaultPath`: env set → env; unset (non-windows) → `/tmp/loghorn.sock`.
  - `TestListenStaleSocket`: create a unix listener at path, close it without unlinking (`(*net.UnixListener).SetUnlinkOnClose(false)`), then `Listen(path)` succeeds.
  - `TestListenLiveSocket`: a serving listener at path → `Listen` returns `*LiveError`.
  - `TestListenTooLong`: 120-byte path → error mentioning `103`.
  - `TestProbeNotRunning`: missing path → `errors.Is(err, ErrNotRunning)`.
  - `TestProbeSession`: `httptest`-style server on a unix listener answering `{"pid":7,"port":4380,"otlpPort":4318,"dataDir":"/d"}` → `Session` fields, `WebURL()=="http://127.0.0.1:4380/"`, `OTLPEndpoint()=="http://127.0.0.1:4318"`.
  - `TestServeAndRunSections`: a user config with `[serve] web-port = 5000` and `[run] serve-down = "fail"` parses; `Resolved()` gives `(5000, 4318)`; `serve-down = "maybe"` is an error naming the allowed values; `web-port = 70000` is an error.
  - `TestLoadUserIgnoresRepo`: a repo `.config/loghorn/config.toml` in the cwd and a user file under `HOME` → `LoadUser` returns the user file's path.
  - `TestTryLock`: `logfile.Open(dir)` held → `TryLock(dir)` returns `*LockedError`; after `Close` → succeeds, and `release()` frees it.
  - Extend the existing `/api/session` test with `Version`, `DataDir`, `Socket`, `OTLPPort`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/datadir/ ./internal/sock/ ./internal/config/ ./internal/logfile/ ./internal/web/ 2>&1 | tail -30`
Expected: FAIL — packages `datadir`, `sock` don't exist; undefined `LoadUser`, `TryLock`, `ServeConfig`.

- [ ] **Step 3: Implement**
  - `datadir`, `sock` as above. `sock.Listen`: `os.Lstat(path)`; if present, `net.DialTimeout("unix", path, 500ms)`; success → close conn, return `&LiveError{path}`; failure → `os.Remove(path)`. Then `net.Listen("unix", path)`, `os.Chmod(path, 0o600)`. `LiveError.Error()`: `"another loghorn serve is answering on <path>"`.
  - Split `lock.go`: `acquire` calls `lockFile(f *os.File) error` (returns `errWouldBlock` when held) implemented with `syscall.Flock(LOCK_EX|LOCK_NB)` in `lock_unix.go` (`//go:build !windows`) and `windows.LockFileEx(LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY)` in `lock_windows.go`. `TryLock` opens `<dir>/loghorn.lock` without creating `dir` and reuses `acquire`.
  - Config: add the sections, validation (`serve-down` in the three values; ports 0–65535), `LoadUser` via `Loader{XDG, Home}.userLevel()` then `parse`. `Config.String()` keeps printing whatever it printed; update its test expectation if the new sections appear.
  - Web: thread the new Options into `sessionJSON`.

- [ ] **Step 4: Run to verify they pass, plus the Windows build of the touched packages**

Run: `go test ./internal/datadir/ ./internal/sock/ ./internal/config/ ./internal/logfile/ ./internal/web/ 2>&1 | tail -15; GOOS=windows go build ./internal/datadir/ ./internal/sock/ ./internal/logfile/ ./internal/config/`
Expected: all `ok`; the Windows build prints nothing.

- [ ] **Step 5: Commit**

```
git add internal/datadir internal/sock internal/logfile internal/config internal/web
git commit -m "feat: data folder, serve socket, [serve]/[run] config and a portable lock"
```

---

### Task 2: Run-line transport — `loghorn.run` scope in the receiver, the shipper, recorder service

Spec sections: "`run` as a client".

**Files:**
- Modify: `internal/otlp/receiver.go`, `internal/otlp/receiver_test.go`
- Create: `internal/shipper/shipper.go`, `internal/shipper/shipper_test.go`
- Modify: `internal/recorder/recorder.go`, `internal/recorder/recorder_test.go`

**Interfaces:**
- Consumes: `config.Input` (JSON-encodable as-is).
- Produces:
  - `otlp.RunScope = "loghorn.run"`; `type otlp.RunLine struct { Service, RunID string; Input []byte; Line []byte }`.
  - `(*otlp.Receiver).SetRunLines(f func(otlp.RunLine))` — call before `Serve`. When set, every ScopeLogs whose scope name is `RunScope` is removed from the request before `MapLogs`, and each of its log records with a string body becomes one `RunLine` (resource attributes `service.name`, `loghorn.run.id`, `loghorn.input`), delivered in request order before the mapped logs, under the same delivery lock as `logs`. Unset: those records map like any other.
  - `shipper.New(endpoint string, r shipper.Resource, onError func(error)) *shipper.Shipper`; `type shipper.Resource struct { Service, RunID string; Input config.Input }`.
  - `(*Shipper).Line(rec []byte)` — copies rec, never blocks; drops when its 4096-line queue is full.
  - `(*Shipper).Close(timeout time.Duration) (dropped int)` — flushes what is queued, waits at most timeout, returns lines dropped (queue full or failed batches).
  - `recorder.Options.Service string` — set on each stored record whose `Service` is empty, before `Store.Add`.
  - `(*recorder.Recorder).MarkOTLP()` — sets the OTLP-seen flag without storing anything.

- [ ] **Step 1: Write the failing tests**
  - `TestReceiverRunScopeDiverted`: a protobuf request with two ResourceLogs — one with scope `loghorn.run`, resource `service.name=api`, `loghorn.run.id=r1`, `loghorn.input={"format":"auto"}` and bodies `{"msg":"a"}`, `plain b`; one ordinary — with `SetRunLines` set: the run callback gets exactly two `RunLine`s in order with those fields; `logs` gets only the ordinary record.
  - `TestReceiverRunScopeUnsetMapsNormally`: same request without `SetRunLines` → `logs` gets three lines.
  - `TestShipperSendsBatches`: an `httptest.Server` decoding `/v1/logs` protobuf; 1000 `Line` calls then `Close(2s)` → the server saw 1000 records in order, scope `loghorn.run`, resource attrs `service.name`, `loghorn.run.id`, and `loghorn.input` equal to `json.Marshal(Input)`; `dropped==0`.
  - `TestShipperNeverBlocks`: endpoint is a closed port; 10000 `Line` calls return within 1 s total; `Close(500ms)` returns `dropped==10000`; `onError` called exactly once.
  - `TestRecorderServiceFillsEmpty`: a record with no service field gets `Service=="api"`; one whose line names its own service keeps it.
  - `TestRecorderMarkOTLP`: with `DedupeAfterOTLP`, after `MarkOTLP()` a JSON line is not stored, a plain line is.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/otlp/ ./internal/shipper/ ./internal/recorder/ 2>&1 | tail -20`
Expected: FAIL — undefined `SetRunLines`, `RunLine`, package `shipper`, `Options.Service`, `MarkOTLP`.

- [ ] **Step 3: Implement**
  - Shipper: one goroutine drains a `chan []byte` (cap 4096); a batch is sent at 512 lines or 200 ms since its first line, as `application/x-protobuf` POST to `endpoint+"/v1/logs"` with a 5 s client timeout; each record has `TimeUnixNano` = the `Line` call time and a string body. Failed batches add to `dropped`; the first failure calls `onError`.
  - Receiver: partition in the logs handler before mapping; `RunLine.Line` is the body string's bytes.

- [ ] **Step 4: Run to verify they pass**

Run: `go test -race ./internal/otlp/ ./internal/shipper/ ./internal/recorder/ 2>&1 | tail -10`
Expected: all `ok`.

- [ ] **Step 5: Commit**

```
git add internal/otlp internal/shipper internal/recorder
git commit -m "feat: ship a run's lines to serve as OTLP logs in the loghorn.run scope"
```

---

### Task 3: `internal/serve` — the always-on process

Spec sections: "`serve`" (all subsections), "OS-service requirements" 1–6.

**Files:**
- Create: `internal/serve/serve.go`, `internal/serve/router.go`, `internal/serve/serve_test.go`

**Interfaces:**
- Consumes: Task 1 (`sock.Listen`, `config.Config`, `web.Options` fields), Task 2 (`SetRunLines`, `RunLine`, `recorder.Options.Service`, `MarkOTLP`), existing `logfile.Open`, `Writer.Companion`, `store.New/Preload`, `web.New/Listen`, `otlp.NewReceiver`, `record.NormalizeSpan`, `ingest.CompactRecord`.
- Produces:
  - `type serve.Options struct { DataDir, Socket string; WebPort, OTLPPort int; Config config.Config; ConfigBase, Version string; Log io.Writer; Now func() time.Time; AdoptEvery, SocketCheckEvery time.Duration }` — ports are used as given (0 = ephemeral, for tests); zero durations mean 1 min and 5 min.
  - `serve.Start(o Options) (*Server, error)` — order: `logfile.Open(DataDir)` (lock; `*logfile.LockedError` returned as is) → `sock.Listen(Socket)` (`*sock.LiveError` as is) → `web.Listen(WebPort)` → OTLP `net.Listen("tcp", 127.0.0.1:OTLPPort)` (a bind failure on either port returns `*serve.PortError{Addr, Which string}` where Which is `"web"` or `"otlp-http"`) → span companion writer → store `ModeLive` preloaded from DataDir → serve web on TCP and socket, receiver on OTLP → start the socket check. Any failure undoes what came before it.
  - `(*Server).Session() sock.Session`, `(*Server).Close() error` — drain the receiver (500 ms idle, 3 s max), close servers, close writers, remove the socket file (only if it is still ours), release the lock. Safe twice.
  - `(*Server).adoptHook` — a `func()` field Task 6 fills; `Start` calls it once before serving and every `AdoptEvery`.
  - Router (unexported): `runLine(otlp.RunLine)` → per-run recorder keyed by RunID, created with `Input` decoded from `RunLine.Input` (serve's `Config.Input` if empty or invalid), `Service`, `Live: true`, `DedupeAfterOTLP: true`, the shared log sink and store; `otlpLog(line)` → stored through the general recorder; if the line's `loghorn.run.id` names a run, that run's recorder (or, if it doesn't exist yet, a pending mark applied on creation) gets `MarkOTLP()`. Runs idle for 10 min are dropped on the `AdoptEvery` tick.

- [ ] **Step 1: Write the failing tests** (each with `HOME` and DataDir in temp dirs, socket under `os.MkdirTemp("/tmp","lh")`, ports 0)
  - `TestStartServesSessionAndWeb`: `Start` → `sock.Probe(socket)` returns the bound ports and DataDir; GET `WebURL()+"api/streams"` is 200.
  - `TestRunLinesUseRunInput`: deliver (through a real OTLP POST from a `shipper`) run `r1` service `api` and run `r2` service `web`, each with a JSON line and a plain line; the store has four records with services `api`,`api`,`web`,`web`; `loghorn.log` in DataDir holds the four lines.
  - `TestOTLPDedupePerRun`: run `r1` gets an OTLP log with resource `loghorn.run.id=r1`, then a JSON stdout line from `r1` (dropped) and a JSON line from `r2` (kept).
  - `TestSecondServeFailsOnLock` and `TestSecondServeFailsOnLiveSocket` (different DataDir, same socket) → `*logfile.LockedError`, `*sock.LiveError`; in both, the first serve still answers.
  - `TestPortInUse`: occupy a TCP port, pass it as `OTLPPort` → `*PortError{Which:"otlp-http"}`; the lock is released and the socket file is gone afterwards.
  - `TestCloseRemovesSocketAndFlushes`: after `Close`, the socket path doesn't exist, `TryLock(DataDir)` succeeds, and every delivered line is in `loghorn.log`.
  - `TestSocketRecreated`: with `SocketCheckEvery: 50ms`, remove the socket file; within 1 s `Probe` succeeds again.
  - `TestStartFromRoot`: `os.Chdir("/")` for the test (restore after) — `Start` succeeds and writes nothing outside DataDir.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/serve/ 2>&1 | tail -20`
Expected: FAIL — package `serve` doesn't exist.

- [ ] **Step 3: Implement** — `serve.go` (Options, Start, Close, Session, socket check), `router.go` (run recorders, OTLP routing, idle eviction). The log sink compacts multi-line records with `ingest.CompactRecord` as `recordSink` does today; the span sink writes the companion and adds `record.NormalizeSpan` results to the store (move `spanSink` logic here; `main` keeps using it until Task 5 deletes its copy). `Version` comes from `Options.Version`.

- [ ] **Step 4: Run to verify they pass**

Run: `go test -race ./internal/serve/ 2>&1 | tail -10; GOOS=windows go build ./internal/serve/`
Expected: `ok`; the Windows build prints nothing.

- [ ] **Step 5: Commit**

```
git add internal/serve
git commit -m "feat: internal/serve, the always-on receiver and web server"
```

---

### Task 4: Dispatcher, help, migration errors, `serve` and `config` commands

Spec sections: "Commands", "Top-level help", "Dispatch", "Flag placement", "Migration error", "Flag mapping", "OS-service requirements" 3, 4, 6.

**Files:**
- Create: `dispatch.go`, `dispatch_test.go`, `cmd_serve.go`, `cmd_serve_test.go`
- Modify: `main.go` (main becomes `os.Exit(dispatch(os.Args[1:], stdEnv()))`; `parseFlags`, `usage`, the global `flag` use and `opts` parsing go; `opts` stays as the parameter struct for the run/open/query functions), `mode.go` (drop `mode.check`, `detectInput`'s replay flags, `removedFlagError`; keep only what `run`/`open` still call), `wrapper.go`, `replay.go`, `query_cli.go` (callers adapt to `opts` built by the new FlagSets)
- Modify: `e2e_test.go`, `mode_test.go`, `main_test.go` (new invocation forms); delete `otel_e2e_test.go`'s tests of `-otel` (Task 5 replaces them)
- Modify: `README.md` (usage block between `<!-- loghorn help -->` and `<!-- /loghorn help -->`)

**Interfaces:**
- Consumes: Task 1 (`config.LoadUser`, `Resolved`, `datadir.Dir`, `sock.DefaultPath`), Task 3 (`serve.Start`, errors).
- Produces:
  - `dispatch(args []string, env cliEnv) int`; `type cliEnv struct { stdin *os.File; stdout, stderr io.Writer }`.
  - `topHelp` (const, the spec's text, with `ui` under Read).
  - `parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error)` — flags anywhere; `--` ends flags; returns positionals.
  - `migrationMessage(flag string) (string, bool)` — one entry per removed top-level flag: `web`, `web-port`, `otel`, `filter`, `notify`, `notify-max-age`, `notify-reset`, `notify-sound`, `shutdown-grace`, `exclusive`, `historical`, `gh-job`, `gh-run`, `config`, `url`, `o`, `format`, `limit`, `to-sql`, `instances`, `scrollback`, `theme`. Text pattern: `-web was removed: run 'loghorn serve' for the web view; 'loghorn run' sends to it, and 'loghorn open <log>' shows a finished log`; `-filter is now a flag of 'loghorn run' and 'loghorn open'`; `-instances was removed: 'loghorn status' shows the running serve`; `-scrollback`/`-theme` keep "was removed along with the terminal UI".
  - Commands: `serve`, `run`, `open`, `query`, `status`, `ui`, `config`, `help`. In this task `run`, `open` and `query` call today's `runCommand`/`runPipe`, `runReplay` and `runQuery` through FlagSets with the new flag names (run: `-filter -notify -notify-max-age -notify-reset -notify-sound -shutdown-grace`; open: `-filter -view -web-port -gh-job -gh-run` + positional; query: `-o -format -limit -to-sql` + positional URL); `status` and `ui` print `loghorn: <name> is not built yet` and return 2 until Task 8.
  - `cmdServe(args []string, env cliEnv) int` — flags `-data` (default `datadir.Dir()`), `-socket` (default `sock.DefaultPath()`), `-web-port`, `-otlp-http-port` (default `-1` = take config/default), `-log FILE` (append; default stderr). Order: flags → `config.LoadUser` (exit 2 on error) → `serve.Start` → print `loghorn serve: web <url>  otlp <endpoint>  socket <path>  data <dir>` → wait for SIGINT/SIGTERM → `Close` → 0. Error messages (exit 1): `*PortError` → `loghorn serve: 127.0.0.1:<port> is in use; choose another <web|OTLP> port with -<flag> or [serve] <key> in <config path or ~/.config/loghorn/config.toml>`; `*LockedError` → `loghorn serve: <err>`; `*LiveError` → `loghorn serve: another serve is answering on <path>; stop it, or give this one its own -socket, -data and ports`.
  - `config` prints what `-config` printed.

- [ ] **Step 1: Write the failing tests** (`dispatch_test.go` drives `dispatch` with buffers; `cmd_serve_test.go` runs the built binary)
  - `TestNoArgsPrintsHelpExit2` for stdin a terminal-less `/dev/null`, a pipe with data, and a regular file: stderr starts with `loghorn — capture`, exit 2, and nothing is written under `HOME` or `LOGHORN_DATA_DIR`; with a pipe or file the hint line `to record piped input use 'loghorn run'; to view a file use 'loghorn open'` appears.
  - `TestHelpExit0`: `-h`, `--help`, `help` → stdout is `topHelp`, exit 0; `help run` → mentions `-shutdown-grace`, exit 0.
  - `TestUnknownCommand`: `npm test` → `unknown command "npm"; to run a command use 'loghorn run -- npm …'`, exit 2.
  - `TestMigrationMessages`: table over every flag in the list → exit 2 and the exact message; `-web=true` and `--web` work too.
  - `TestParseInterspersed`: `[URL -o f.csv]` → positionals `[URL]`, `o=f.csv`; `[-o f -- -x]` → `[-x]`.
  - `TestRunPassesChildFlags`: e2e `loghorn run sh -c 'echo $0' -filter` prints `-filter`.
  - `TestShortcuts`: `loghorn -- echo hi` behaves as `run`; `loghorn http://127.0.0.1:1/?q=` behaves as `query` (connection error, exit 1).
  - `TestReadmeUsageMatchesHelp`: the README block equals `topHelp`.
  - `TestServeCommand` (binary): start `serve` with temp `-data`, `-socket`, `-web-port 0 -otlp-http-port 0`, HOME temp; wait for the socket; SIGTERM → exit 0 and the socket file is gone.
  - `TestServePortInUseMessage`: occupy a port, pass it as `-otlp-http-port` → exit 1 within 5 s, stderr contains `-otlp-http-port` and `[serve] otlp-http-port`.
  - `TestServeBadConfigExit2`: user config with `[run] serve-down = "x"` → exit 2.
  - Update the existing e2e tests to `run`/`open` forms (`loghorn -filter -- …` → `loghorn run -filter -- …`; `-web|-filter < file` → `open -filter file`); `TestReplaysNeedWebOrFilter` is replaced by `TestOpenNeedsOneSource`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test . 2>&1 | tail -30`
Expected: FAIL — undefined `dispatch`, `topHelp`, `parseInterspersed`, `migrationMessage`, `cmdServe`.

- [ ] **Step 3: Implement** — `dispatch.go`, `cmd_serve.go`, and the FlagSet adapters; README block.

- [ ] **Step 4: Run to verify they pass**

Run: `go test -race . 2>&1 | tail -15; go vet ./...`
Expected: `ok`; vet silent.

- [ ] **Step 5: Commit**

```
git add -A .
git commit -m "feat: loghorn subcommands with serve, config and migration errors"
```

---

### Task 5: `loghorn run` as a client of `serve`

Spec sections: "`run` as a client", "When `serve` is down" (the probe only), "Flag mapping" (`-web`, `-otel`, `-exclusive` rows).

**Files:**
- Create: `cmd_run.go`, `cmd_run_test.go`, `service_name.go`, `service_name_test.go`
- Modify/Delete: `wrapper.go` (its command and pipe paths move into `cmd_run.go`; `startLiveWeb`, `openRecordingLog`, `openLogFile`, `logDir`, the `-otel` branch and `spanSink`/`recordSink` copies that `serve` now owns are deleted; `startWeb` stays for `open`), `main.go`, `main_test.go`, `childservice_test.go`
- Modify: `e2e_test.go` (tests that read `.loghorn/loghorn.log` now read a test serve's DataDir)

**Interfaces:**
- Consumes: Task 1 (`sock.Probe`, `Session.OTLPEndpoint`, `WebURL`), Task 2 (`shipper`), existing `otlp.ChildEnv`, `otlp.NewRunID`, `wrap.Run`, `wrap.Pipe`, `recorder`.
- Produces:
  - `cmdRun(args []string, env cliEnv) int` — flags `-filter -notify -notify-max-age -notify-reset -notify-sound -shutdown-grace -service NAME -serve-down MODE`; a command (with or without `--`) or a piped/redirected stdin; neither → usage error exit 2 `loghorn run needs a command (loghorn run -- <command>) or piped input`.
  - `runService(flagValue string, environ []string, cwd string, args []string) string` — flag, else last `OTEL_SERVICE_NAME`, else `<repo dir name>-<base of args[0]>` (repo dir = nearest ancestor with a `.git` file or directory, stopping at the filesystem root; none → cwd), or just `<dir name>` for a pipe (`args` empty).
  - `type runTarget struct { sink func([]byte); childEnv []string; finish func() }` — what Task 6's down path also returns.
  - `connectServe(sess sock.Session, svc, runID string, in config.Input, stderr io.Writer) runTarget` — shipper sink; child env `otlp.ChildEnv(os.Environ(), sess.OTLPEndpoint(), runID)`; prints `loghorn: recording to serve as <svc> (run <id>); web view at <url>`; `finish` closes the shipper (3 s) and warns `loghorn: serve stopped answering; <n> lines were not recorded` when `dropped > 0`.

- [ ] **Step 1: Write the failing tests**
  - `TestRunService` table: flag wins; env `OTEL_SERVICE_NAME=a` then `=b` → `b`; repo dir `proj` (a `.git` dir two levels up) with `npm` → `proj-npm`; `.git` as a file counts; no repo → cwd base; pipe → dir name only.
  - e2e `TestRunRecordsToServe`: start a test serve (helper `startTestServe(t) (sock, dataDir string)` in `cmd_run_test.go`, reused by later tasks); `loghorn run -- sh -c 'echo {"msg":"x","level":"error"}; echo plain; exit 3'` with `LOGHORN_SOCKET` set → stdout has both lines, exit 3; within 2 s DataDir's `loghorn.log` has both lines; `/api/streams` over the socket counts them; stderr names the web URL.
  - e2e `TestRunChildOTLPReachesServe`: the child (a `go test` helper process, as `otel_e2e_test.go` did) posts one span and one log to `$OTEL_EXPORTER_OTLP_ENDPOINT` → both in DataDir (`loghorn.traces.jsonl`, `loghorn.log`).
  - e2e `TestRunPipeToServe`: `loghorn run < file` → lines recorded, stdout copies them.
  - e2e `TestRunServeDiesMidRun`: child prints a line, sleeps 1 s, prints 2000 lines; the test kills serve during the sleep → child output complete on stdout, exit 0, stderr warns once about unrecorded lines.
  - e2e `TestRunFilterAndNotifyStillLocal`: `-filter` with serve up prints only the error line.

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'TestRun' . 2>&1 | tail -20`
Expected: FAIL — undefined `runService`, `cmdRun`, `connectServe`.

- [ ] **Step 3: Implement.** `cmdRun` probes `sock.DefaultPath()` before launching anything. Until Task 6, a down serve prints the spec's "isn't running" line and runs the child unrecorded (the `skip` behaviour). The local recorder has `Sink: target.sink`, no store, `Input: cfg.Input` (repo-or-user config from `config.Load(cwd)`), `Alert: notifier(...)`.

- [ ] **Step 4: Run to verify they pass**

Run: `go test -race . 2>&1 | tail -15`
Expected: `ok`.

- [ ] **Step 5: Commit**

```
git add -A .
git commit -m "feat: loghorn run sends its records and its child's OTLP to serve"
```

---

### Task 6: `serve-down` — record into incoming, skip, fail; `serve` adopts

Spec sections: "When `serve` is down".

**Files:**
- Create: `internal/incoming/incoming.go`, `internal/incoming/incoming_test.go`
- Modify: `cmd_run.go`, `cmd_run_test.go`, `internal/serve/serve.go` (fill `adoptHook`), `internal/serve/serve_test.go`

**Interfaces:**
- Consumes: Task 1 (`datadir.Dir`, `config.LoadUser`, `RunConfig.Mode`, `logfile.TryLock`), Task 3 (`adoptHook`, router), Task 5 (`runTarget`).
- Produces:
  - `type incoming.Meta struct { RunID, Service string; Input config.Input; Started time.Time }` stored as `run.json`.
  - `incoming.Create(dataDir string, m Meta) (logs, spans *logfile.Writer, err error)` — `<dataDir>/incoming/<RunID>/`, `run.json`, then `logfile.Open` (holds the lock until `logs.Close`).
  - `type incoming.Pending struct { Dir string; Meta Meta; Locked bool }`; `incoming.List(dataDir string) ([]Pending, error)` (oldest `Started` first; a folder without a readable `run.json` is listed with zero Meta and adopted with serve's defaults).
  - `incoming.Adopt(p Pending, line func(rec []byte), span func(line []byte)) error` — takes `TryLock` (returns `*LockedError` if held), reads the folder's log files with `ingest.Stored` and its span files line by line, calls the callbacks in order, then removes the folder.
  - `connectIncoming(dataDir string, m incoming.Meta, cfg config.Config, stderr io.Writer) (runTarget, error)` in `cmd_run.go` — writer sink, child OTLP receiver on `otlp.Listen()` (today's `-otel` path, `DedupeAfterOTLP` on the local recorder), the spec's warning line.
  - Mode resolution in `cmdRun`: `-serve-down` (validated: record|skip|fail, else exit 2) → `LoadUser().Run.Mode()`.

- [ ] **Step 1: Write the failing tests**
  - `TestCreateListAdopt`: Create (held) → `List` shows `Locked:true`; `Adopt` → `*LockedError`; Close → `Locked:false`; Adopt → callbacks see every line in order, folder gone; second `List` empty.
  - `TestAdoptMissingMeta`: a folder with logs but no `run.json` is adopted.
  - serve `TestServeAdoptsIncoming`: Create+write+Close two runs (`svc a`, `svc b`) before `Start` → after Start the store has their records with services `a`/`b`, DataDir's `loghorn.log` contains them, `incoming/` is empty; a third run still held during Start is adopted on a later tick (`AdoptEvery: 50ms`) once closed.
  - e2e `TestServeDownRecord`: no serve, `LOGHORN_DATA_DIR` temp → `loghorn run -- echo hi` exits 0, prints `hi`, stderr has `recording this run to`, and `incoming/<id>/loghorn.log` contains `hi`; then starting serve adopts it.
  - e2e `TestServeDownSkip`: `-serve-down skip` → no `incoming/`; stderr warns.
  - e2e `TestServeDownFail`: `-serve-down fail` → exit 1, the child never ran (a marker file it would create is absent).
  - e2e `TestServeDownFromUserConfig`: user config `[run] serve-down = "skip"` → skip behaviour.
  - e2e `TestServeDownTwoRuns`: two concurrent `run`s with serve down write two folders.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/incoming/ ./internal/serve/ 2>&1 | tail -15; go test -run 'TestServeDown' . 2>&1 | tail -15`
Expected: FAIL — package `incoming` missing; serve doesn't adopt; run doesn't record when serve is down.

- [ ] **Step 3: Implement.** Adoption records each line through a recorder built from the folder's Meta (`Live: false`, `Service: Meta.Service`, the shared log sink and store) and each span through the span sink; `serve` logs `loghorn serve: adopted run <id> (<service>, <n> records) recorded while serve was down`.

- [ ] **Step 4: Run to verify they pass**

Run: `go test -race ./internal/incoming/ ./internal/serve/ . 2>&1 | tail -15`
Expected: all `ok`.

- [ ] **Step 5: Commit**

```
git add -A .
git commit -m "feat: serve-down setting; run records to incoming and serve adopts it"
```

---

### Task 7: `loghorn open`

Spec sections: "`open`", "Flag mapping" (file, `-historical`, GitHub rows).

**Files:**
- Create: `cmd_open.go`, `cmd_open_test.go`
- Delete/Modify: `replay.go` (moves into `cmd_open.go`), `mode.go` (whatever remains of input kinds), `wrapper.go` (`startWeb` loses its socket: `serveSocket` deleted)
- Modify: `e2e_test.go`

**Interfaces:**
- Produces:
  - `cmdOpen(args []string, env cliEnv) int` — flags `-filter`, `-view` (with `-filter`, also serve the view), `-web-port` (default 0), `-gh-job`, `-gh-run`; positional `PATH`, `-`, or `https://github.com/…/actions/runs/N[/job/M]`.
  - `openSource(pos, ghJob, ghRun string) (src openSrc, err error)`; `type openSrc struct { kind string /* "file","stdin","gh-job","gh-run" */; value string }` — a GitHub URL containing `/job/` is a job, else a run; exactly one source or a usage error (`loghorn open needs one log: a file, -, a GitHub Actions URL, -gh-job ID or -gh-run ID`).
  - Behaviour: without `-filter` (or with `-view`), the temporary web view as today (`startWeb`, store mode `ModeHistorical` for files and stdin, `ModeGHJob` for GitHub), its URL on stderr, served until SIGINT/SIGTERM; never writes the log or the data folder.

- [ ] **Step 1: Write the failing tests**
  - `TestOpenSource` table: `log.txt`; `-`; job URL; run URL; `-gh-job 5`; `-gh-run 6`; two sources → error; none → error.
  - e2e `TestOpenFileFilter`: `open -filter f.log` prints only the error record, exit 0, nothing under HOME/data dir.
  - e2e `TestOpenStdinFilterNotRecorded`: `open -filter - < f.log` same.
  - e2e `TestOpenFileServesView`: `open f.log` prints `web view at http://127.0.0.1:` and `/api/streams` counts the records; SIGTERM → exit 0.
  - e2e `TestOpenHasNoSocket`: while `open` serves, no new file appears in the socket dir and `LOGHORN_SOCKET` path stays absent.

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'TestOpen' . 2>&1 | tail -15`
Expected: FAIL — undefined `openSource`, `cmdOpen`.

- [ ] **Step 3: Implement** — move `runReplay`/`fetchGHLog`/`pickRunJob` into `cmd_open.go` driven by `openSrc`; delete `-historical` support, `openHistoricalFiles`, `refuseOtherInput`, `webStoreMode`'s historical branch.

- [ ] **Step 4: Run to verify they pass**

Run: `go test -race . 2>&1 | tail -10`
Expected: `ok`.

- [ ] **Step 5: Commit**

```
git add -A .
git commit -m "feat: loghorn open shows a finished log from a file, stdin or GitHub Actions"
```

---

### Task 8: `loghorn query`, `status` and `ui`

Spec sections: "`loghorn ui`", "`loghorn status`", "When `serve` is down" (client messages), "Interaction with the CLI query plan".

**Files:**
- Create: `cmd_query.go` (from `query_cli.go`), `cmd_status.go`, `cmd_ui.go`, `internal/browser/browser.go`, `internal/browser/browser_test.go`, `cmd_status_test.go`, `cmd_ui_test.go`
- Delete: `query_cli.go`, `internal/instances/` (both files), `internal/web/socket.go` and `socket_test.go`'s `SocketDir`/`ListenSocket` tests (the session test moved in Task 1 stays, in a renamed file if needed)
- Modify: query tests (`query_cli_test.go` → `cmd_query_test.go`)

**Interfaces:**
- Consumes: Task 1 (`sock.Probe`, `Client`, `DefaultPath`, `Session`), Task 6 (`incoming.List`), `config.LoadUser`.
- Produces:
  - `cmdQuery(args []string, env cliEnv) int` — flags `-o -format -limit -to-sql`; one positional: a loopback URL (TCP to that port), a host-less `/?q=…` (through `sock.DefaultPath()`), `@file` or `@-` (resolved first, then the same rules). Messages and exit codes otherwise as today; "no live instance" becomes the spec's "isn't running" message.
  - `notRunningMessage(path string) string` — `loghorn serve isn't running (no answer on <path>); start it with 'loghorn serve' or install it with 'loghorn service install'`.
  - `otelEnv(endpoint string) string` — exactly the spec's block (comment line, five `export` lines, the `OTEL_SERVICE_NAME` comment); `otelExports(endpoint)` — the five `export` lines only.
  - `cmdStatus(args []string, env cliEnv) int` — flag `-env`. Up: `serve: running (pid N, since <time>, version V)`, `data: <dir>`, `socket: <path>`, `web: <url>`, `otlp/http: <endpoint>`, incoming summary if any (`incoming: 3 runs recorded while serve was down, oldest 08:12`), blank line, `otelEnv`. Down: `loghorn: ` + `notRunningMessage`, the incoming summary (data folder from `datadir.Dir()`), then `otelEnv` from `LoadUser().Serve.Resolved()`, exit 1. `-env`: only `otelExports`, exit 0 up or down.
  - `browser.Open(url string) error` — `open URL` (darwin), `xdg-open URL` (others), `rundll32 url.dll,FileProtocolHandler URL` (windows); starts the command and does not wait. Built via `browser.command(goos, url string) *exec.Cmd` (tested).
  - `cmdUI(args []string, env cliEnv) int` — flag `-print`; package var `openBrowser = browser.Open` for tests. Up: `-print` → URL on stdout, exit 0; otherwise URL on stderr, `openBrowser(url)`; error → `loghorn: can't open a browser (<err>); open <url> yourself`, exit 1. Down → `notRunningMessage`, exit 1.

- [ ] **Step 1: Write the failing tests**
  - Existing query tests rewritten for `loghorn query …` against a test serve (`startTestServe`), including host-less `/?q=` via the socket, `@file`, `@-`, `-o out.csv`, flags after the URL.
  - `TestQueryServeDown`: host-less URL with no serve → exit 1 and `notRunningMessage`.
  - `TestCommand`: `command("darwin", u).Args == ["open", u]`; linux `["xdg-open", u]`; windows `["rundll32", "url.dll,FileProtocolHandler", u]`.
  - `TestStatusUp`: test serve with one incoming folder left locked by a held `incoming.Create` → output contains `serve: running`, the web URL, `incoming: 1 run`, and `export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:<otlp port>`; exit 0.
  - `TestStatusDown`: no serve, user config `[serve] otlp-http-port = 4999` → exit 1, `isn't running`, `export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4999`.
  - `TestStatusEnv`: `-env` up and down → exactly the five export lines, exit 0.
  - `TestOtelEnvText`: equals the spec block for endpoint `http://127.0.0.1:4318`.
  - `TestUI`: fake `openBrowser` records the URL equal to the session's `WebURL()`; `-print` prints it and the fake is not called; a failing fake → exit 1 and the message; serve down → exit 1.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/browser/ . 2>&1 | tail -20`
Expected: FAIL — undefined `cmdQuery`, `cmdStatus`, `cmdUI`, `otelEnv`, package `browser`.

- [ ] **Step 3: Implement**, remove the Task 4 "not built yet" stubs, delete `internal/instances` and `web.SocketDir`/`ListenSocket`.

- [ ] **Step 4: Run to verify they pass**

Run: `go test -race ./... 2>&1 | tail -30; go vet ./...`
Expected: every package `ok`; vet silent.

- [ ] **Step 5: Commit**

```
git add -A .
git commit -m "feat: loghorn query, status (with the OTEL environment) and ui"
```

---

### Task 9: Docs, leftovers and the Windows check

Spec sections: all, as the README's source; "Testing".

**Files:**
- Modify: `README.md` (usage, quick start: `loghorn serve` then `loghorn run -- …`, `loghorn ui`, `eval "$(loghorn status -env)"`, the `serve-down` setting, the data folder, the socket and ports, migration table from the spec's Flag mapping), `CLAUDE.md` (rewrite the notes that mention `-web`, `-otel`, `.loghorn/` under cwd, per-instance sockets; add: tests set `LOGHORN_SOCKET`/`LOGHORN_DATA_DIR` and use `/tmp` socket dirs; `serve` binds before it serves), `main.go` package comment, `internal/web/demo/main.go` if it references removed APIs
- Delete: any function left unused (`go vet` and `staticcheck`-style `grep` for references), `mode.go` if empty

- [ ] **Step 1: Find leftovers**

Run: `grep -rn -e '-web\b' -e '-otel' -e 'historical' -e 'SocketDir' -e 'instances\.' -e '\.loghorn/' --include='*.go' --include='*.md' . | grep -v -e '^./docs/superpowers/' | cut -c1-160`
Expected: only intended mentions (migration messages, the README migration table, `open`'s note about old per-repo days).

- [ ] **Step 2: Fix the docs and delete dead code**; keep `TestReadmeUsageMatchesHelp` green.

- [ ] **Step 3: Full verification**

Run: `go build ./... ; go vet ./... ; go test -race ./... 2>&1 | tail -30 ; GOOS=windows go build ./internal/serve/ ./internal/sock/ ./internal/datadir/ ./internal/incoming/ ./internal/shipper/ ./internal/browser/ ./internal/logfile/ ./internal/config/`
Expected: no build or vet output; every package `ok`; the Windows build prints nothing.

- [ ] **Step 4: Manual smoke test** (HOME, `LOGHORN_DATA_DIR`, `LOGHORN_SOCKET` in a scratch dir; ports 0 via flags): build, start `serve`, `run -- sh -c 'echo hi'`, `status`, `status -env`, `ui -print`, `query '/?q=…'` for the default view, stop `serve` with SIGTERM, `run -- echo down` (records to incoming), restart `serve`, `status` shows no incoming. Record the transcript summary in the ledger.

- [ ] **Step 5: Commit**

```
git add -A .
git commit -m "docs: subcommands, serve and run in README and CLAUDE.md; drop dead code"
```
