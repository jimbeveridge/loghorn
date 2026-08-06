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

// Coalescer decides when an important event should raise a notification. It is
// event-driven (no timers): a trailing burst is summarized on the next event
// after the cooldown elapses.
type Coalescer struct {
	cooldown   time.Duration
	now        func() time.Time
	started    bool
	windowOpen time.Time
	suppressed int
}

func NewCoalescer(cooldown time.Duration, now func() time.Time) *Coalescer {
	if now == nil {
		now = time.Now
	}
	return &Coalescer{cooldown: cooldown, now: now}
}

// Observe is called once per important entry. It returns whether to fire a
// notification now, plus the title and body to show.
func (c *Coalescer) Observe(e entry.Entry) (bool, string, string) {
	t := c.now()
	if !c.started || t.Sub(c.windowOpen) >= c.cooldown {
		title := "clog: important log activity"
		var body string
		if c.suppressed > 0 {
			body = fmt.Sprintf("%d events since last alert", c.suppressed+1)
		} else {
			body = single(e)
		}
		c.started = true
		c.windowOpen = t
		c.suppressed = 0
		return true, title, body
	}
	c.suppressed++
	return false, "", ""
}

func single(e entry.Entry) string {
	m := e.Message
	if len(m) > 120 {
		m = m[:120] + "…"
	}
	return fmt.Sprintf("[%s] %s", e.Severity, m)
}
