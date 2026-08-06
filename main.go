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
	notify := flag.Bool("notify", false, "fire coalesced desktop notifications on important lines")
	cooldown := flag.Duration("cooldown", 60*time.Second, "minimum spacing between notifications")
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
		coalescer = alert.NewCoalescer(*cooldown, nil)
		notifier = alert.BeeepNotifier{}
	}

	// errCh carries a non-EOF stdin read error from the producer goroutine to
	// main. It's buffered so the goroutine never blocks sending it, even if
	// the TUI has already quit (e.g. via 'q') and nobody is listening yet.
	errCh := make(chan error, 1)
	go func() {
		err := ingest.Lines(os.Stdin, func(line []byte) {
			e := adapter.ParseLine(line)
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

	p := tea.NewProgram(
		tui.NewModel(ch, *capacity, *contextN),
		tea.WithAltScreen(),
	)
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
