package alert

import (
	"strings"
	"testing"
	"time"

	"clog/internal/entry"
)

func TestCoalescerFiresThenSuppressesThenSummarizes(t *testing.T) {
	now := time.Unix(0, 0)
	clock := func() time.Time { return now }
	c := NewCoalescer(60*time.Second, clock)

	e := entry.Entry{Severity: entry.SevError, Message: "boom"}

	// First important event fires immediately.
	fire, _, body := c.Observe(e)
	if !fire {
		t.Fatalf("first event should fire")
	}
	if strings.Contains(body, "events since last alert") {
		t.Fatalf("first fire should be a single-event body, got %q", body)
	}

	// Two more within the window: suppressed.
	now = now.Add(10 * time.Second)
	if fire, _, _ := c.Observe(e); fire {
		t.Fatalf("event within cooldown should be suppressed")
	}
	now = now.Add(10 * time.Second)
	if fire, _, _ := c.Observe(e); fire {
		t.Fatalf("event within cooldown should be suppressed")
	}

	// After the window: fires a summary counting the suppressed events.
	now = now.Add(60 * time.Second)
	fire, _, body = c.Observe(e)
	if !fire {
		t.Fatalf("event after cooldown should fire")
	}
	if !strings.Contains(body, "events since last alert") {
		t.Fatalf("post-cooldown fire should summarize, got %q", body)
	}
}
