package alert

import "github.com/gen2brain/beeep"

// beeepNotify and beeepAlert are swappable so tests can observe which one a
// notifier chose without talking to the desktop.
var (
	beeepNotify = beeep.Notify
	beeepAlert  = beeep.Alert
)

// BeeepNotifier delivers desktop notifications via gen2brain/beeep
// (terminal-notifier or osascript on macOS, notify-send on Linux).
type BeeepNotifier struct {
	// Sound plays the system alert sound alongside the notification (a beep on
	// Linux). It is on by default because macOS shows osascript notifications
	// under Script Editor's alert style, which is "Banners" out of the box —
	// they auto-dismiss after a few seconds, so a silent one is easy to miss
	// entirely, which defeats the point of alerting on errors.
	//
	// The persistent alternative is a system setting loghorn cannot reach: set
	// Script Editor (or terminal-notifier, if installed — beeep prefers it) to
	// "Alerts" in System Settings › Notifications.
	Sound bool
}

func (n BeeepNotifier) Notify(title, body string) error {
	if n.Sound {
		return beeepAlert(title, body, "")
	}
	return beeepNotify(title, body, "")
}
