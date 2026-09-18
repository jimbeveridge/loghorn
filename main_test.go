package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // America/Los_Angeles without relying on the host's zoneinfo

	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/logfile"
)

func TestParseTheme(t *testing.T) {
	for _, tc := range []struct {
		in      string
		wantHex string // ignored when wantAsk is true: bg is documented unused then.
		wantAsk bool
	}{
		{"auto", "#000000", true},
		{"light", "#ffffff", false},
		{"dark", "#000000", false},
		{"#cee8be", "#cee8be", false},
		{"#CEE8BE", "#cee8be", false},
	} {
		bg, ask, err := parseTheme(tc.in)
		if err != nil || ask != tc.wantAsk || (!tc.wantAsk && bg.Hex() != tc.wantHex) {
			t.Errorf("parseTheme(%q) = %s, %v, %v; want %s, %v, nil", tc.in, bg.Hex(), ask, err, tc.wantHex, tc.wantAsk)
		}
	}
	for _, bad := range []string{"", "blue", "cee8be", "#cee8b", "#fff", "#cee8bz", "Light"} {
		if _, _, err := parseTheme(bad); err == nil || err.Error() != "--theme must be auto, light, dark or #rrggbb" {
			t.Errorf("parseTheme(%q) error = %v, want the usage message", bad, err)
		}
	}
}

// logDir is where the log file lives and -historical reads from: .loghorn
// under the directory loghorn was started in, one per project.
func TestLogDir(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := logDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cwd, ".loghorn"); got != want {
		t.Fatalf("logDir() = %q, want %q", got, want)
	}
}

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

// recordSink must compact a record on its way to the log file: the file holds
// one record per line, and -historical replay depends on it. Asserted here
// because nothing else did — removing the CompactRecord call from recordSink
// left the whole suite passing.
func TestRecordSinkCompactsMultiLineRecords(t *testing.T) {
	dir := t.TempDir()
	w, err := logfile.Open(dir, time.Now, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var failures int
	sink := recordSink(w, func(error) { failures++ })
	sink([]byte("{\n  \"severity\": 500,\n  \"insert_id\": \"a1\"\n}"))
	// A YAML document is the one record allowed to stay multi-line, because
	// replay re-detects its framing and re-groups it.
	sink([]byte("---\nseverity: 500\nmessage: keeps its lines"))
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if failures != 0 {
		t.Fatalf("onFail ran %d times, want 0", failures)
	}

	b, err := os.ReadFile(filepath.Join(dir, "loghorn.log"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, `{"severity":500,"insert_id":"a1"}`+"\n") {
		t.Errorf("the multi-line JSON record was not compacted onto one line:\n%q", got)
	}
	if !strings.Contains(got, "---\nseverity: 500\nmessage: keeps its lines\n") {
		t.Errorf("the YAML document should keep its newlines:\n%q", got)
	}
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

// main resolves its config with config.Load(cwd): the working directory
// anchors it, the same as .loghorn/ and the source-tree refusal, so all three
// agree on what "this project" means. This pins that call's contract —
// coverage of the walk itself belongs to package config.
func TestConfigForUsesWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, config.RelPath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("[input]\nformat = \"json\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, path, err := config.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if path != p || cfg.Input.Format != config.FormatJSON {
		t.Fatalf("Load(%q) = %q, %q; want %q, json", dir, path, cfg.Input.Format, p)
	}
}

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
