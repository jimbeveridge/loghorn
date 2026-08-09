// Command clog captures GCP Cloud Run logs and focuses attention on the
// important lines. Pipe logs into stdin; run with --filter for a headless
// stdin->stdout filter.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"clog/internal/adapter"
	"clog/internal/alert"
	"clog/internal/engine"
	"clog/internal/entry"
	"clog/internal/headless"
	"clog/internal/ingest"
	"clog/internal/tui"
)

func main() {
	filterMode := flag.Bool("filter", false, "headless: print only important lines to stdout")
	contextN := flag.Int("context", 3, "leading context lines shown before each important line")
	capacity := flag.Int("scrollback", 5000, "max entries kept in memory")
	notify := flag.Bool("notify", false, "fire a desktop notification when fresh errors occur")
	notifyMaxAge := flag.Duration("notify-max-age", time.Second, "skip notifications for errors older than this (by their log timestamp)")
	notifyReset := flag.Duration("notify-reset", 15*time.Second, "end an error burst after this quiet gap, so the next error notifies again")
	flag.Parse()

	if *filterMode {
		if err := headless.Run(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "clog:", err)
			os.Exit(1)
		}
		return
	}

	ch := make(chan entry.Entry, 1024)

	var coalescer *alert.Coalescer
	var notifier alert.Notifier
	if *notify {
		coalescer = alert.NewCoalescer(*notifyMaxAge, *notifyReset, nil)
		notifier = alert.BeeepNotifier{}
	}

	// errCh carries a non-EOF stdin read error from the producer goroutine to
	// main. It's buffered so the goroutine never blocks sending it, even if
	// the TUI has already quit (e.g. via 'q') and nobody is listening yet.
	errCh := make(chan error, 1)
	go func() {
		err := ingest.Lines(os.Stdin, func(line []byte) {
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

	// clog consumes stdin for logs, so Bubble Tea can't use it for the UI:
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
	p := tea.NewProgram(tui.NewModel(ch, *capacity, *contextN), opts...)
	_, runErr := p.Run()
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "clog:", runErr)
		os.Exit(1)
	}
	// The producer may still be running if the TUI quit before stdin was
	// fully drained (e.g. the user pressed 'q'); don't block exit waiting
	// for it. Only report an error that was already available.
	select {
	case err := <-errCh:
		if err != nil {
			fmt.Fprintln(os.Stderr, "clog: input error:", err)
		}
	default:
	}
}
