package alert

import (
	"errors"
	"testing"
)

// stubBeeep swaps both beeep entry points and records which one was called.
func stubBeeep(t *testing.T) *struct {
	notified, alerted int
	title, body       string
} {
	t.Helper()
	rec := &struct {
		notified, alerted int
		title, body       string
	}{}
	origN, origA := beeepNotify, beeepAlert
	beeepNotify = func(title, message string, _ any) error {
		rec.notified++
		rec.title, rec.body = title, message
		return nil
	}
	beeepAlert = func(title, message string, _ any) error {
		rec.alerted++
		rec.title, rec.body = title, message
		return nil
	}
	t.Cleanup(func() { beeepNotify, beeepAlert = origN, origA })
	return rec
}

// With sound on, the notification goes through beeep.Alert — the variant that
// plays the system sound. A silent macOS banner auto-dismisses in a few seconds,
// so this is what makes an error alert actually noticeable.
func TestNotifierWithSoundUsesAlert(t *testing.T) {
	rec := stubBeeep(t)

	if err := (BeeepNotifier{Sound: true}).Notify("clog — error", "[ERROR] boom"); err != nil {
		t.Fatal(err)
	}
	if rec.alerted != 1 || rec.notified != 0 {
		t.Fatalf("sound should use Alert (alerted=%d notified=%d)", rec.alerted, rec.notified)
	}
	if rec.title != "clog — error" || rec.body != "[ERROR] boom" {
		t.Fatalf("title/body should pass through, got %q / %q", rec.title, rec.body)
	}
}

// With sound off it uses the silent path.
func TestNotifierWithoutSoundUsesNotify(t *testing.T) {
	rec := stubBeeep(t)

	if err := (BeeepNotifier{Sound: false}).Notify("clog — error", "[ERROR] boom"); err != nil {
		t.Fatal(err)
	}
	if rec.notified != 1 || rec.alerted != 0 {
		t.Fatalf("no sound should use Notify (alerted=%d notified=%d)", rec.alerted, rec.notified)
	}
}

// A failing desktop notifier is reported rather than swallowed.
func TestNotifierPropagatesError(t *testing.T) {
	stubBeeep(t)
	want := errors.New("no notification daemon")
	beeepAlert = func(string, string, any) error { return want }

	if got := (BeeepNotifier{Sound: true}).Notify("t", "b"); !errors.Is(got, want) {
		t.Fatalf("error should propagate, got %v", got)
	}
}
