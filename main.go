// Command loghorn captures GCP Cloud Run logs and focuses attention on the
// important lines.
//
// Prefer launching the producer:
//
//	loghorn -- npm run dev
//
// loghorn then owns the process: it captures stdout and stderr together, keeps the
// keyboard to itself, and shuts the whole process group down on quit. Piping
// (`npm run dev | loghorn`) still works and is right for files and non-interactive
// producers, but an interactive one fights loghorn for /dev/tty — both processes
// read it, so keystrokes get split between them at random.
//
// Every record is also appended to loghorn.log next to the executable; see
// package logfile.
//
// Run with --filter for a headless stdin->stdout filter.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jimbeveridge/loghorn/internal/adapter"
	"github.com/jimbeveridge/loghorn/internal/alert"
	"github.com/jimbeveridge/loghorn/internal/engine"
	"github.com/jimbeveridge/loghorn/internal/entry"
	"github.com/jimbeveridge/loghorn/internal/headless"
	"github.com/jimbeveridge/loghorn/internal/ingest"
	"github.com/jimbeveridge/loghorn/internal/logfile"
	"github.com/jimbeveridge/loghorn/internal/runner"
	"github.com/jimbeveridge/loghorn/internal/tui"
)

func usage() {
	fmt.Fprint(flag.CommandLine.Output(), `loghorn — watch logs, surface the failures.

Usage:
  loghorn [flags] -- <command> [args...]   launch the command and capture it (preferred)
  <command> | loghorn [flags]              read a pipe

Launching is preferred for an interactive producer such as a dev server: loghorn
captures its stdout and stderr together, keeps the keyboard to itself, and stops
its whole process group on quit. In a pipeline the producer still holds the
terminal, so it competes with loghorn for keystrokes and outlives it.

Every record is also appended to loghorn.log next to the loghorn executable. Each
finished day is kept as loghorn-YYYY-MM-DD.log, and days before the last three
are deleted. loghorn will not run from inside its own source tree.

Examples:
  loghorn -- npm run dev
  loghorn --scrollback 20000 -- go test ./...
  kubectl logs -f pod | loghorn

Flags:
`)
	flag.PrintDefaults()
}

// childControl adapts the runner to what the TUI needs, binding in the
// configured shutdown grace period.
type childControl struct {
	r     *runner.Runner
	grace time.Duration
}

func (c childControl) Send(p []byte) error { return c.r.Send(p) }
func (c childControl) Terminate()          { c.r.Terminate(c.grace) }
func (c childControl) Name() string        { return c.r.Name() }

func main() {
	filterMode := flag.Bool("filter", false, "headless: print only important lines to stdout")
	capacity := flag.Int("scrollback", 5000, "max entries kept in memory")
	notify := flag.Bool("notify", false, "fire a desktop notification when fresh errors occur")
	notifyMaxAge := flag.Duration("notify-max-age", time.Second, "skip notifications for errors older than this (by their log timestamp)")
	notifyReset := flag.Duration("notify-reset", 15*time.Second, "end an error burst after this quiet gap, so the next error notifies again")
	notifySound := flag.Bool("notify-sound", true, "play the system alert sound with notifications (with --notify)")
	grace := flag.Duration("shutdown-grace", 5*time.Second, "how long a launched command gets to exit on SIGTERM before SIGKILL")
	exclusive := flag.Bool("exclusive", false, "exit if another loghorn is already writing the log file, instead of running without one")
	flag.Usage = usage
	flag.Parse()

	// Refuse to run from loghorn's own source tree before anything is launched or
	// written. `go run .` only works from there, and it builds into a temporary
	// directory Go deletes on exit, which would take the log files with it.
	if cwd, err := os.Getwd(); err == nil {
		if root, ok := logfile.SourceTree(cwd); ok {
			fmt.Fprintf(os.Stderr, "loghorn: refusing to run inside the loghorn source tree (%s);\nrun it from your project's directory\n", root)
			os.Exit(1)
		}
	}

	// The log file opens before --filter dispatch and before a child is launched,
	// so --exclusive never starts a producer only to abandon it.
	logs, locked := openLogFile(*exclusive)
	if logs != nil {
		defer logs.Close()
	}

	if *filterMode {
		if locked != nil {
			fmt.Fprintf(os.Stderr, "loghorn: %v; running without a log file\n", locked)
		}
		sink := recordSink(logs, func(err error) {
			fmt.Fprintln(os.Stderr, "loghorn: log file stopped:", err)
		})
		if err := headless.Run(os.Stdin, os.Stdout, sink); err != nil {
			fmt.Fprintln(os.Stderr, "loghorn:", err)
			os.Exit(1)
		}
		return
	}

	// With a command after `--`, loghorn launches it and owns it: stdout and stderr
	// arrive together, the child is kept off the terminal so it can't steal
	// keystrokes, and quitting can shut its whole process group down. Without
	// one, read the pipe as before.
	var source io.Reader = os.Stdin
	var child *runner.Runner
	if argv := flag.Args(); len(argv) > 0 {
		r, err := runner.Start(argv)
		if err != nil {
			fmt.Fprintln(os.Stderr, "loghorn:", err)
			os.Exit(1)
		}
		child, source = r, r.Output()
		defer child.Close()
	}

	ch := make(chan entry.Entry, 1024)

	var coalescer *alert.Coalescer
	var notifier alert.Notifier
	if *notify {
		coalescer = alert.NewCoalescer(*notifyMaxAge, *notifyReset, nil)
		notifier = alert.BeeepNotifier{Sound: *notifySound}
	}

	// loghorn consumes stdin for logs, so Bubble Tea can't use it for the UI:
	// keyboard AND resize (SIGWINCH) events must come from the controlling
	// terminal. Wire /dev/tty as the program input; without it Bubble Tea tries
	// to read key/resize input from the piped stdin, which delivers neither, so
	// the TUI never learns the terminal was resized. Fall back to the default
	// if there is no controlling tty (e.g. a headless environment).
	// Mouse capture starts on so clicking selects a line; 'm' toggles it off to
	// hand text selection back to the terminal for copying.
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithMouseCellMotion()}
	if tty, err := os.Open("/dev/tty"); err == nil {
		defer tty.Close()
		opts = append(opts, tea.WithInput(tty))
	}
	model := tui.NewModel(ch, *capacity)
	if child != nil {
		model.SetChild(childControl{r: child, grace: *grace})
	}
	if locked != nil {
		model.SetLogFileOff(locked.Holder() + " has it")
	}
	p := tea.NewProgram(model, opts...)

	// logErr keeps a mid-run log file failure to print once the TUI has gone; the
	// bar marker only says that it happened. The writer reports at most one
	// failure, so the buffer of one never blocks.
	logErr := make(chan error, 1)
	sink := recordSink(logs, func(err error) {
		logErr <- err
		p.Send(tui.LogFileOff("write failed"))
	})

	// errCh carries a non-EOF read error from the producer goroutine to main.
	// It's buffered so the goroutine never blocks sending it, even if the TUI
	// has already quit (e.g. via 'q') and nobody is listening yet.
	// The goroutine starts only now because a log file failure reaches the TUI
	// through p.
	errCh := make(chan error, 1)
	go func() {
		err := ingest.Records(source, func(line []byte) {
			sink(line)
			e := adapter.ParseLine(line)
			e.Received = time.Now()
			e.Important = engine.IsImportant(e)
			if coalescer != nil && e.Important {
				if fire, title, body := coalescer.Observe(e); fire {
					_ = notifier.Notify(title, body)
				}
			}
			ch <- e
		})
		close(ch)
		errCh <- err
	}()

	if child != nil {
		// Surface an unexpected exit in the TUI instead of tearing it down — a
		// crash is exactly when the scrollback is worth reading.
		go func() {
			<-child.Exited()
			p.Send(tui.ChildExited(child.ExitCode()))
		}()
	}
	_, runErr := p.Run()
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "loghorn:", runErr)
		os.Exit(1)
	}
	// The producer may still be running if the TUI quit before stdin was
	// fully drained (e.g. the user pressed 'q'); don't block exit waiting
	// for it. Only report an error that was already available.
	select {
	case err := <-errCh:
		if err != nil {
			fmt.Fprintln(os.Stderr, "loghorn: input error:", err)
		}
	default:
	}
	select {
	case err := <-logErr:
		fmt.Fprintln(os.Stderr, "loghorn: log file stopped:", err)
	default:
	}
}

// openLogFile opens the always-on log file in the executable's directory. A lock
// held by another loghorn is returned rather than fatal, so loghorn can run on
// without a file — unless exclusive, when it exits. Any other failure exits: the
// file is always on, so an unwritable directory is a setup error.
func openLogFile(exclusive bool) (*logfile.Writer, *logfile.LockedError) {
	dir, err := executableDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "loghorn: locating the executable:", err)
		os.Exit(1)
	}
	w, err := logfile.Open(dir, time.Now, time.Local)
	var locked *logfile.LockedError
	if errors.As(err, &locked) && !exclusive {
		return nil, locked
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "loghorn:", err)
		os.Exit(1)
	}
	return w, nil
}

// executableDir is the directory of the running binary with symlinks resolved,
// so a symlinked loghorn still writes next to the real file.
func executableDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// recordSink hands each record to the log file. The writer returns only its
// first failure and ignores records after it, so onFail runs at most once.
// Without a log file the sink does nothing.
func recordSink(w *logfile.Writer, onFail func(error)) func(rec []byte) {
	if w == nil {
		return func([]byte) {}
	}
	return func(rec []byte) {
		if err := w.Write(rec); err != nil {
			onFail(err)
		}
	}
}
