// Package clipboard copies text to the system clipboard by piping it to the
// platform's clipboard tool.
//
// The alternative — writing an OSC 52 escape sequence — would reach the
// clipboard over SSH too, but it goes out on stdout, which is the channel Bubble
// Tea is rendering the TUI on. Stray writes there corrupt the display.
package clipboard

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// tools lists the clipboard helpers to try per platform, in preference order.
var tools = map[string][][]string{
	"darwin":  {{"pbcopy"}},
	"windows": {{"clip"}},
	"linux": {
		{"wl-copy"}, // Wayland first: an X tool under Wayland may target the wrong session
		{"xclip", "-selection", "clipboard"},
		{"xsel", "--clipboard", "--input"},
	},
}

// tool returns the first clipboard helper present on PATH for goos.
func tool(goos string) ([]string, error) {
	candidates, ok := tools[goos]
	if !ok {
		return nil, fmt.Errorf("no clipboard tool known for %s", goos)
	}
	var names []string
	for _, argv := range candidates {
		if path, err := exec.LookPath(argv[0]); err == nil {
			return append([]string{path}, argv[1:]...), nil
		}
		names = append(names, argv[0])
	}
	return nil, fmt.Errorf("none of %s found on PATH", strings.Join(names, ", "))
}

// Copy writes text to the system clipboard.
func Copy(text string) error {
	argv, err := tool(runtime.GOOS)
	if err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%s: %s", argv[0], msg)
		}
		return fmt.Errorf("%s: %w", argv[0], err)
	}
	return nil
}
