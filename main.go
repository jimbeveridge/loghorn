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

	go func() {
		_ = ingest.Lines(os.Stdin, func(line []byte) {
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
	}()

	p := tea.NewProgram(
		tui.NewModel(ch, *capacity, *contextN),
		tea.WithAltScreen(),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "clog:", err)
		os.Exit(1)
	}
}
