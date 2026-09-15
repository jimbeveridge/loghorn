package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	_ "time/tzdata" // America/Los_Angeles without relying on the host's zoneinfo

	"github.com/jimbeveridge/loghorn/internal/logfile"
)

func TestIsRegularFileTrueForARegularFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "regular")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !isRegularFile(f) {
		t.Fatalf("want true for a regular file")
	}
}

func TestIsRegularFileFalseForAPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if isRegularFile(r) {
		t.Fatalf("want false for the read end of a pipe")
	}
}

func TestRecordSinkWithNoWriterIsANoop(t *testing.T) {
	called := false
	sink := recordSink(nil, func(error) { called = true })
	sink([]byte("x"))
	if called {
		t.Fatalf("onFail must not be called when there is no log file")
	}
}

// A real Writer whose rollover cannot archive (a directory squats on the
// archive's name) fails on the first write after the fake clock crosses
// midnight; onFail must run exactly once even though the sink is called again.
func TestRecordSinkCallsOnFailOnce(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 14, 22, 0, 0, 0, loc)}
	w, err := logfile.Open(dir, clock.Now, loc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })

	if err := os.Mkdir(filepath.Join(dir, "loghorn-2026-09-14.log"), 0o755); err != nil {
		t.Fatal(err)
	}
	clock.t = time.Date(2026, 9, 15, 1, 0, 0, 0, loc)

	var failures int
	sink := recordSink(w, func(error) { failures++ })
	sink([]byte("a"))
	sink([]byte("b"))
	if failures != 1 {
		t.Fatalf("onFail called %d times, want 1", failures)
	}
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

func TestScrollbackFor(t *testing.T) {
	cases := []struct {
		name       string
		historical bool
		explicit   bool
		n          int
		want       int
	}{
		{"historical default gets the larger cap", true, false, 5000, 100000},
		{"historical with --scrollback keeps the explicit value", true, true, 777, 777},
		{"live default is unaffected", false, false, 5000, 5000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := scrollbackFor(c.historical, c.explicit, c.n); got != c.want {
				t.Fatalf("scrollbackFor(%v, %v, %d) = %d, want %d", c.historical, c.explicit, c.n, got, c.want)
			}
		})
	}
}
