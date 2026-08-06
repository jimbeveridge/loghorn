// Package alert coalesces important events into throttled desktop notifications.
package alert

import (
	"fmt"
	"time"

	"clog/internal/entry"
)

// Notifier delivers a desktop notification. Implementations live elsewhere so
// the coalescer stays pure and testable.
type Notifier interface {
	Notify(title, body string) error
}

// Coalescer decides when an important event should raise a desktop
// notification. Two rules keep alerts useful during real tailing:
//
//   - Freshness gate: an event whose own log timestamp is older than maxAge is
//     ignored — we're probably replaying an old log file, not seeing it live.
//     Events with no timestamp are treated as live (we can't prove otherwise).
//   - One-per-burst: the first error of a burst fires; further errors are
//     suppressed until a resetIdle-long gap with no errors ends the burst, so
//     the next error fires again.
//
// It is event-driven (no timers) and takes an injected clock for testing.
type Coalescer struct {
	maxAge    time.Duration
	resetIdle time.Duration
	now       func() time.Time
	lastFresh time.Time // wall-clock of the last non-gated error observed
	hasFresh  bool
}

// NewCoalescer builds a coalescer. maxAge gates stale events by their log
// timestamp; resetIdle is the quiet gap that ends an error burst. A nil clock
// uses time.Now.
func NewCoalescer(maxAge, resetIdle time.Duration, now func() time.Time) *Coalescer {
	if now == nil {
		now = time.Now
	}
	return &Coalescer{maxAge: maxAge, resetIdle: resetIdle, now: now}
}

// Observe is called once per important entry. It returns whether to fire a
// notification now, plus the title and body to show.
func (c *Coalescer) Observe(e entry.Entry) (bool, string, string) {
	now := c.now()

	// Freshness gate: skip errors already stale by their own clock.
	if !e.Timestamp.IsZero() && now.Sub(e.Timestamp) > c.maxAge {
		return false, "", ""
	}

	// One-per-burst: fire only when starting a new burst (the first fresh error,
	// or the first after a resetIdle-long quiet gap). Every fresh error extends
	// the burst, so a steady stream stays a single notification.
	fire := !c.hasFresh || now.Sub(c.lastFresh) >= c.resetIdle
	c.lastFresh = now
	c.hasFresh = true
	if !fire {
		return false, "", ""
	}
	return true, "clog — error", single(e)
}

func single(e entry.Entry) string {
	m := e.Message
	if len(m) > 120 {
		m = m[:120] + "…"
	}
	return fmt.Sprintf("[%s] %s", e.Severity, m)
}
