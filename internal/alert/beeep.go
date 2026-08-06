package alert

import "github.com/gen2brain/beeep"

// BeeepNotifier delivers desktop notifications via gen2brain/beeep
// (osascript on macOS, notify-send on Linux).
type BeeepNotifier struct{}

func (BeeepNotifier) Notify(title, body string) error {
	return beeep.Notify(title, body, "")
}
