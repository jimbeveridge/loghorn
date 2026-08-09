package alert

import (
	"testing"
	"time"

	"loghorn/internal/entry"
)

// A fresh error fires once; consecutive errors within the burst are suppressed;
// after a resetIdle-long quiet gap the next error fires again.
func TestCoalescerOnePerBurst(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	c := NewCoalescer(1*time.Second, 15*time.Second, clock)

	// Fresh error = its own timestamp is within maxAge of now.
	fresh := func() entry.Entry {
		return entry.Entry{Severity: entry.SevError, Message: "boom", Timestamp: now}
	}

	if fire, _, _ := c.Observe(fresh()); !fire {
		t.Fatalf("first error of a burst should fire")
	}
	// More errors within 15s of each other: same burst, suppressed.
	now = now.Add(5 * time.Second)
	if fire, _, _ := c.Observe(fresh()); fire {
		t.Fatalf("error within the burst should be suppressed")
	}
	now = now.Add(5 * time.Second)
	if fire, _, _ := c.Observe(fresh()); fire {
		t.Fatalf("error within the burst should be suppressed")
	}
	// 15s+ with no errors ends the burst; the next error fires.
	now = now.Add(15 * time.Second)
	if fire, _, body := c.Observe(fresh()); !fire {
		t.Fatalf("error after a 15s quiet gap should fire again")
	} else if body == "" {
		t.Fatalf("a firing notification needs a body")
	}
}

// A steady stream of errors closer together than resetIdle stays one burst
// (only the first fires), even across many events.
func TestCoalescerSteadyStreamStaysOneBurst(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	c := NewCoalescer(1*time.Second, 15*time.Second, clock)

	fires := 0
	for i := 0; i < 20; i++ {
		if fire, _, _ := c.Observe(entry.Entry{Severity: entry.SevError, Timestamp: now}); fire {
			fires++
		}
		now = now.Add(10 * time.Second) // always < 15s apart
	}
	if fires != 1 {
		t.Fatalf("a steady <15s-apart stream should notify once, fired %d times", fires)
	}
}

// Stale errors (old log timestamp) are gated out — replaying an old file must
// not raise notifications, and must not start a burst.
func TestCoalescerGatesStaleErrors(t *testing.T) {
	now := time.Unix(10000, 0)
	clock := func() time.Time { return now }
	c := NewCoalescer(1*time.Second, 15*time.Second, clock)

	old := entry.Entry{Severity: entry.SevError, Message: "old", Timestamp: now.Add(-1 * time.Hour)}
	if fire, _, _ := c.Observe(old); fire {
		t.Fatalf("an error older than maxAge should not fire")
	}
	// A subsequently fresh error still counts as the first of its burst.
	if fire, _, _ := c.Observe(entry.Entry{Severity: entry.SevError, Timestamp: now}); !fire {
		t.Fatalf("a fresh error should fire even after gated stale ones")
	}
}

// An entry with no parseable timestamp is treated as live (can't prove stale).
func TestCoalescerNoTimestampIsLive(t *testing.T) {
	now := time.Unix(1000, 0)
	c := NewCoalescer(1*time.Second, 15*time.Second, func() time.Time { return now })
	if fire, _, _ := c.Observe(entry.Entry{Severity: entry.SevError, Message: "x"}); !fire {
		t.Fatalf("an error with no timestamp should fire (treated as live)")
	}
}
