# gcloud Input and Config File Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let loghorn read `gcloud beta logging tail --json` multi-line JSON and gcloud's snake_case, numeric-severity LogEntry fields, with the behaviour overridable from a `.config/loghorn/config.toml` file found by walking up from the working directory.

**Architecture:** A new `internal/config` package resolves one TOML file and hands an `Input` value to two existing seams. `internal/ingest` gains a third framing mode — a brace-depth state machine that turns a JSON array of pretty-printed objects into one record each, passing non-JSON lines through untouched. `internal/adapter` gains a configured `Parser` that accepts either spelling of a LogEntry field name and either encoding of a severity. A compaction step on the log-file sink keeps the stored file's one-record-per-line invariant, so `-historical` replay still works.

**Tech Stack:** Go 1.25, `github.com/BurntSushi/toml` (new), `gopkg.in/yaml.v3` (existing), standard `encoding/json`.

**Spec:** `docs/superpowers/specs/2026-09-18-gcloud-input-config-design.md`

## Global Constraints

- Go 1.25.0 (`go.mod`). `slices`, `strings.Builder` and `math` are all available from the standard library.
- Exactly one new dependency: `github.com/BurntSushi/toml` at `v1.6.0`. Add no others.
- Every config key's default is the string `"auto"`, and `auto` must reproduce today's behaviour plus the new gcloud handling. A zero-value `config.Input` (all fields `""`) must behave as `auto` everywhere, so existing constructions like `LogEntryAdapter{}` keep working.
- `adapter.ParseLine(line []byte) entry.Entry` keeps its exact current signature. About 18 call sites across `internal/tui` depend on it and must not be touched.
- `go test ./...` must pass at the end of every task. The baseline is green as of `34d3ad6`.
- `gofmt -l .` must print nothing.
- Match the codebase's comment style: comments explain *why* a choice was made, not what the line does. Read the neighbouring comments in any file you modify before adding your own.
- Never commit the working tree's pre-existing modifications to `internal/tui/model.go`, `internal/tui/detail_width_test.go` or `internal/tui/detail_wrap_test.go`. They are unrelated in-progress work. Stage only the files each task's commit step names.

---

### Task 1: The `internal/config` package

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `config.RelPath = ".config/loghorn/config.toml"`
  - `type Format string`, constants `FormatAuto`/`FormatJSON`/`FormatYAML`/`FormatText` with values `"auto"`/`"json"`/`"yaml"`/`"text"`
  - `type FieldNaming string`, constants `NamingAuto`/`NamingCamel`/`NamingSnake` with values `"auto"`/`"camel"`/`"snake"`
  - `type SeverityMode string`, constants `SeverityAuto`/`SeverityString`/`SeverityNumeric` with values `"auto"`/`"string"`/`"numeric"`
  - `type Input struct { Format Format; FieldNaming FieldNaming; Severity SeverityMode }`
  - `type Config struct { Input Input }` with method `String() string`
  - `func Default() Config`
  - `type Loader struct { Dir, XDG, Home string; Warn func(string) }` with method `Load() (Config, string, error)`
  - `func Load(dir string) (Config, string, error)`

- [ ] **Step 1: Add the TOML dependency**

```bash
go get github.com/BurntSushi/toml@v1.6.0
```

Expected: `go.mod` gains `github.com/BurntSushi/toml v1.6.0` in the direct require block.

- [ ] **Step 2: Write the failing tests**

Create `internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig creates dir/.config/loghorn/config.toml holding body.
func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, RelPath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// loaderAt points both the walk and the user-level stop at a temporary tree,
// so no test reads the developer's real home or working directory.
func loaderAt(dir, home string) Loader {
	return Loader{Dir: dir, Home: home}
}

func TestDefaultsWhenNoFileAnywhere(t *testing.T) {
	root := t.TempDir()
	cfg, path, err := loaderAt(root, filepath.Join(root, "nohome")).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty when no file was found", path)
	}
	if cfg != Default() {
		t.Errorf("cfg = %+v, want Default() = %+v", cfg, Default())
	}
}

func TestNearestFileWins(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, root, "[input]\nformat = \"yaml\"\n")
	want := writeConfig(t, filepath.Join(root, "a"), "[input]\nformat = \"json\"\n")

	cfg, path, err := loaderAt(deep, filepath.Join(root, "nohome")).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if path != want {
		t.Errorf("path = %q, want the nearest file %q", path, want)
	}
	if cfg.Input.Format != FormatJSON {
		t.Errorf("Format = %q, want json from the nearest file", cfg.Input.Format)
	}
}

// The walk stops at the first file; a farther file contributes nothing, not
// even for a key the nearer one leaves out.
func TestNoMergeAcrossLevels(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, root, "[input]\nseverity = \"numeric\"\n")
	writeConfig(t, sub, "[input]\nformat = \"json\"\n")

	cfg, _, err := loaderAt(sub, filepath.Join(root, "nohome")).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Input.Severity != SeverityAuto {
		t.Errorf("Severity = %q, want auto: the root file must not be merged in", cfg.Input.Severity)
	}
}

func TestFallsBackToUserLevelFile(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	want := writeConfig(t, home, "[input]\nfield-naming = \"snake\"\n")
	start := filepath.Join(root, "work")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, path, err := loaderAt(start, home).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if path != want {
		t.Errorf("path = %q, want the user-level file %q", path, want)
	}
	if cfg.Input.FieldNaming != NamingSnake {
		t.Errorf("FieldNaming = %q, want snake", cfg.Input.FieldNaming)
	}
}

func TestProjectFileSuppressesUserLevelFile(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	writeConfig(t, home, "[input]\nseverity = \"numeric\"\n")
	work := filepath.Join(root, "work")
	want := writeConfig(t, work, "[input]\nformat = \"json\"\n")

	cfg, path, err := loaderAt(work, home).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if path != want {
		t.Errorf("path = %q, want the project file %q", path, want)
	}
	if cfg.Input.Severity != SeverityAuto {
		t.Errorf("Severity = %q, want auto: a project file suppresses the user-level file entirely", cfg.Input.Severity)
	}
}

// Started outside the home tree, a pure upward walk would never pass through
// the home directory, so personal defaults would silently stop applying.
func TestUserLevelFileAppliesFromOutsideHomeTree(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "[input]\nformat = \"text\"\n")
	outside := t.TempDir() // a sibling temp dir, not under home

	cfg, path, err := loaderAt(outside, home).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if path == "" || cfg.Input.Format != FormatText {
		t.Errorf("got path %q format %q, want the user-level file to apply from outside the home tree", path, cfg.Input.Format)
	}
}

func TestXDGConfigHomeOverridesHomeForUserLevelStop(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	writeConfig(t, home, "[input]\nformat = \"yaml\"\n")

	xdg := filepath.Join(root, "xdg")
	p := filepath.Join(xdg, "loghorn", "config.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("[input]\nformat = \"json\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	start := filepath.Join(root, "work")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, path, err := Loader{Dir: start, XDG: xdg, Home: home}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if path != p {
		t.Errorf("path = %q, want the XDG file %q", path, p)
	}
	if cfg.Input.Format != FormatJSON {
		t.Errorf("Format = %q, want json from the XDG file", cfg.Input.Format)
	}

	// An empty XDG_CONFIG_HOME is the same as unset, so home is used.
	cfg, path, err = Loader{Dir: start, XDG: "", Home: home}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Input.Format != FormatYAML {
		t.Errorf("Format = %q with empty XDG, want yaml from home (path %q)", cfg.Input.Format, path)
	}
}

// The nearest file decides. One that exists but can't be read must fail the
// run, never be skipped in favour of a more distant file that might disagree.
func TestUnreadableFileIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a 0o000 file, so this case can't be provoked")
	}
	root := t.TempDir()
	p := writeConfig(t, root, "[input]\nformat = \"json\"\n")
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(p, 0o644) })

	if _, _, err := loaderAt(root, filepath.Join(root, "nohome")).Load(); err == nil {
		t.Fatal("Load should fail on a config file that exists but cannot be read")
	}
}

// An unknown key warns and is ignored, so an older binary tolerates a file
// written for a newer one.
func TestUnknownKeyWarnsAndParses(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "[input]\nformat = \"json\"\nfuture-key = 3\n\n[filters]\nname = \"x\"\n")

	var warnings []string
	l := loaderAt(root, filepath.Join(root, "nohome"))
	l.Warn = func(msg string) { warnings = append(warnings, msg) }
	cfg, _, err := l.Load()
	if err != nil {
		t.Fatalf("an unknown key must not fail the load: %v", err)
	}
	if cfg.Input.Format != FormatJSON {
		t.Errorf("Format = %q, want json: known keys still apply", cfg.Input.Format)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"input.future-key", "filters.name"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings missing %q:\n%s", want, joined)
		}
	}
}

// An unknown value can't be ignored: the user asked for behaviour this binary
// doesn't have, and guessing which they meant would be worse than stopping.
func TestUnknownValueIsAnError(t *testing.T) {
	for _, body := range []string{
		"[input]\nformat = \"jsonl\"\n",
		"[input]\nfield-naming = \"kebab\"\n",
		"[input]\nseverity = \"number\"\n",
	} {
		root := t.TempDir()
		p := writeConfig(t, root, body)
		_, _, err := loaderAt(root, filepath.Join(root, "nohome")).Load()
		if err == nil {
			t.Errorf("%q should be rejected", body)
			continue
		}
		if !strings.Contains(err.Error(), p) {
			t.Errorf("error should name the file %q: %v", p, err)
		}
	}
}

func TestMalformedTOMLIsAnError(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "[input\nformat = json\n")
	if _, _, err := loaderAt(root, filepath.Join(root, "nohome")).Load(); err == nil {
		t.Fatal("malformed TOML should be rejected")
	}
}

func TestStringRendersEffectiveSettings(t *testing.T) {
	got := Default().String()
	for _, want := range []string{"[input]", `format`, `"auto"`, "field-naming", "severity"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() missing %q:\n%s", want, got)
		}
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/config/`
Expected: FAIL — the package has no non-test Go files, so the build fails with undefined identifiers (`RelPath`, `Loader`, `Default`, …).

- [ ] **Step 4: Write the implementation**

Create `internal/config/config.go`:

```go
// Package config reads loghorn's configuration file.
//
// The file is .config/loghorn/config.toml. One name serves both project and
// user-level configuration, because ~/.config/loghorn/config.toml is simply
// what the upward walk finds on reaching the home directory — there is no
// second mechanism and no separate project filename. The first file found
// wins entirely and nothing is merged across levels, so one file explains all
// of loghorn's configured behaviour and -config can name it.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// RelPath is the config file's path relative to each directory the walk tests.
const RelPath = ".config/loghorn/config.toml"

// Format selects how ingest frames a byte stream into records.
type Format string

const (
	FormatAuto Format = "auto"
	FormatJSON Format = "json"
	FormatYAML Format = "yaml"
	FormatText Format = "text"
)

// FieldNaming selects which spelling of a LogEntry field name is accepted.
type FieldNaming string

const (
	NamingAuto  FieldNaming = "auto"
	NamingCamel FieldNaming = "camel"
	NamingSnake FieldNaming = "snake"
)

// SeverityMode selects which encoding of a severity value is accepted.
type SeverityMode string

const (
	SeverityAuto    SeverityMode = "auto"
	SeverityString  SeverityMode = "string"
	SeverityNumeric SeverityMode = "numeric"
)

// Input is the [input] section: how a producer's bytes become entries.
type Input struct {
	Format      Format       `toml:"format"`
	FieldNaming FieldNaming  `toml:"field-naming"`
	Severity    SeverityMode `toml:"severity"`
}

// Config is the whole file. Later sections (saved filters, keybindings) become
// sibling fields here.
type Config struct {
	Input Input `toml:"input"`
}

// Default is the configuration used when no file is found: every key "auto".
func Default() Config {
	return Config{Input: Input{
		Format:      FormatAuto,
		FieldNaming: NamingAuto,
		Severity:    SeverityAuto,
	}}
}

// String renders the effective settings the way the file would spell them, for
// -config.
func (c Config) String() string {
	return fmt.Sprintf("[input]\nformat       = %q\nfield-naming = %q\nseverity     = %q\n",
		string(c.Input.Format), string(c.Input.FieldNaming), string(c.Input.Severity))
}

// Loader resolves the config file. Its fields exist so tests can aim the walk
// and the user-level stop at a temporary tree rather than the real working
// directory and the developer's own home.
type Loader struct {
	Dir  string       // directory the upward walk starts from
	XDG  string       // XDG_CONFIG_HOME; empty means unset
	Home string       // home directory; empty means unknown
	Warn func(string) // one call per unknown setting; nil discards them
}

// Load reads the configuration that applies to dir. The returned string is the
// path actually read, empty when no file was found and defaults were used.
func Load(dir string) (Config, string, error) {
	home, _ := os.UserHomeDir()
	return Loader{
		Dir:  dir,
		XDG:  os.Getenv("XDG_CONFIG_HOME"),
		Home: home,
		Warn: func(msg string) { fmt.Fprintln(os.Stderr, "loghorn:", msg) },
	}.Load()
}

// Load resolves and parses the configuration. See the package comment.
func (l Loader) Load() (Config, string, error) {
	path, data, err := l.find()
	if err != nil {
		return Config{}, "", err
	}
	if path == "" {
		return Default(), "", nil
	}
	cfg, err := l.parse(path, data)
	if err != nil {
		return Config{}, "", err
	}
	return cfg, path, nil
}

// find walks up from l.Dir returning the first config file's path and contents,
// then falls through to the user-level file. A file that exists but can't be
// read is an error rather than a miss: the nearest file decides, so one made
// unreadable by permissions must not be skipped in favour of a more distant
// one that might say something different. This mirrors logfile.SourceTree.
func (l Loader) find() (string, []byte, error) {
	dir, err := filepath.Abs(l.Dir)
	if err != nil {
		return "", nil, err
	}
	for {
		p := filepath.Join(dir, RelPath)
		switch data, err := os.ReadFile(p); {
		case err == nil:
			return p, data, nil
		case !errors.Is(err, fs.ErrNotExist):
			return "", nil, fmt.Errorf("%s: %w", p, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return l.userLevel()
		}
		dir = parent
	}
}

// userLevel is the walk's final stop. Without it, personal defaults would
// apply only when the working directory happened to sit under the home
// directory: started from /opt/service the walk never passes through it. When
// the working directory is already under home, the walk has tested this path
// already and this stop changes nothing.
func (l Loader) userLevel() (string, []byte, error) {
	var p string
	switch {
	case l.XDG != "":
		p = filepath.Join(l.XDG, "loghorn", "config.toml")
	case l.Home != "":
		p = filepath.Join(l.Home, ".config", "loghorn", "config.toml")
	default:
		return "", nil, nil
	}
	switch data, err := os.ReadFile(p); {
	case err == nil:
		return p, data, nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", nil, fmt.Errorf("%s: %w", p, err)
	}
	return "", nil, nil
}

// parse decodes one file over the defaults, so an absent key keeps its
// default, then warns about settings this binary doesn't know and rejects
// values it can't honour.
func (l Loader) parse(path string, data []byte) (Config, error) {
	cfg := Default()
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	// An unknown section or key warns rather than fails, so an older binary
	// tolerates a file written for a newer one.
	for _, key := range md.Undecoded() {
		l.warn(fmt.Sprintf("%s: unknown setting %q, ignored", path, key.String()))
	}
	if err := cfg.validate(path); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (l Loader) warn(msg string) {
	if l.Warn != nil {
		l.Warn(msg)
	}
}

// validate rejects a value outside a key's permitted set. Unlike an unknown
// key, an unknown value can't be ignored: the user asked for behaviour this
// binary doesn't have, and guessing which of the permitted values they meant
// would be worse than stopping.
func (c Config) validate(path string) error {
	for _, k := range []struct {
		key     string
		got     string
		allowed []string
	}{
		{"input.format", string(c.Input.Format), []string{"auto", "json", "yaml", "text"}},
		{"input.field-naming", string(c.Input.FieldNaming), []string{"auto", "camel", "snake"}},
		{"input.severity", string(c.Input.Severity), []string{"auto", "string", "numeric"}},
	} {
		if !slices.Contains(k.allowed, k.got) {
			return fmt.Errorf("%s: %s = %q is not one of %s",
				path, k.key, k.got, strings.Join(k.allowed, ", "))
		}
	}
	return nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS for all eleven tests.

- [ ] **Step 6: Check formatting and the whole suite**

Run: `gofmt -l . && go test ./...`
Expected: `gofmt -l` prints nothing; every package passes.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): read .config/loghorn/config.toml, nearest wins

One name serves project and user-level config: ~/.config/loghorn/config.toml
is what the upward walk finds on reaching home. The walk makes a final stop
there anyway, since started from outside the home tree it would otherwise
never pass through it and personal defaults would silently stop applying.

An unknown key warns so an older binary tolerates a newer file; an unknown
value fails, because guessing which permitted value was meant is worse than
stopping."
```

---

### Task 2: JSON-array framing in `internal/ingest`

**Files:**
- Modify: `internal/ingest/reader.go` (`Records`, lines 33-48)
- Create: `internal/ingest/jsonarray.go`
- Modify: `internal/ingest/reader_test.go` (`collectRecords`, line 43)
- Create: `internal/ingest/jsonarray_test.go`
- Modify: `internal/headless/headless.go:18`, `main.go:292` — call-site updates only, to keep the tree compiling. Task 5 does the real wiring.

**Interfaces:**
- Consumes: `config.Format`, `config.FormatAuto`, `config.FormatJSON`, `config.FormatYAML`, `config.FormatText` from Task 1.
- Produces: `func Records(r io.Reader, format config.Format, emit func(rec []byte)) error` — a signature change from today's `Records(r, emit)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/ingest/jsonarray_test.go`:

```go
package ingest

import (
	"strings"
	"testing"

	"github.com/jimbeveridge/loghorn/internal/config"
)

// collectAs is collectRecords with an explicit framing, for the cases that
// test an override rather than detection.
func collectAs(t *testing.T, format config.Format, input string) []string {
	t.Helper()
	var got []string
	err := Records(strings.NewReader(input), format, func(rec []byte) {
		got = append(got, string(rec))
	})
	if err != nil {
		t.Fatalf("Records error: %v", err)
	}
	return got
}

// The shape gcloud beta logging tail --json produces: a bare "[" opens the
// stream and each entry is pretty-printed across many lines.
const tailStream = `[
  {
    "insert_id": "a1",
    "severity": 200
  },
  {
    "insert_id": "a2",
    "severity": 500
  }
]
`

func TestDetectsJSONArrayFromLeadingBracket(t *testing.T) {
	got := collectRecords(t, tailStream)
	want := []string{
		"{\n    \"insert_id\": \"a1\",\n    \"severity\": 200\n  }",
		"{\n    \"insert_id\": \"a2\",\n    \"severity\": 500\n  }",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d:\n%q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d:\ngot:  %q\nwant: %q", i, got[i], want[i])
		}
	}
}

// Detection picks each framing from the stream's first line.
func TestDetectionByFirstLine(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		wantFirst   string
		wantCount   int
	}{
		{"json array", "[\n  {\"a\":1}\n]\n", `{"a":1}`, 1},
		{"yaml documents", "---\na: 1\n---\na: 2\n", "---\na: 1", 2},
		{"one json object per line", "{\"a\":1}\n{\"a\":2}\n", `{"a":1}`, 2},
		{"plain text", "hello\nworld\n", "hello", 2},
		// A first line longer than the peek can't be a framing marker.
		{"very long first line", strings.Repeat("Z", 1000) + "\nx\n", strings.Repeat("Z", 1000), 2},
		// A stream that is nothing but "---", with no newline at all.
		{"bare yaml separator", "---", "---", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := collectRecords(t, tc.input)
			if len(got) != tc.wantCount {
				t.Fatalf("got %d records, want %d:\n%q", len(got), tc.wantCount, got)
			}
			if got[0] != tc.wantFirst {
				t.Errorf("first record:\ngot:  %q\nwant: %q", got[0], tc.wantFirst)
			}
		})
	}
}

// An explicit format skips detection, which is what a tail attached mid-stream
// needs: it missed the opening "[".
func TestExplicitFormatSkipsDetection(t *testing.T) {
	midStream := "  {\n    \"insert_id\": \"a1\"\n  },\n"
	got := collectAs(t, config.FormatJSON, midStream)
	if len(got) != 1 || got[0] != "{\n    \"insert_id\": \"a1\"\n  }" {
		t.Fatalf("forced json framing should group the object: %q", got)
	}

	// Forced text framing disables all grouping, even for a "[" stream.
	got = collectAs(t, config.FormatText, tailStream)
	if len(got) != 10 {
		t.Fatalf("forced text framing should emit every line: got %d records\n%q", len(got), got)
	}

	// Forced yaml framing groups on "---" even without a leading separator.
	got = collectAs(t, config.FormatYAML, "a: 1\n---\na: 2\n")
	if len(got) != 2 {
		t.Fatalf("forced yaml framing should split on ---: %q", got)
	}
}

// A brace inside a string must not move the depth, or a user_agent or a SQL
// statement would swallow the rest of the stream.
func TestBracesInsideStringsDoNotAffectDepth(t *testing.T) {
	input := `[
  {
    "user_agent": "Mozilla/5.0 {not-a-brace} [nor-this]",
    "quoted": "he said \"hi {x}\" and left",
    "backslash": "ends with a backslash \\",
    "severity": 200
  }
]
`
	got := collectRecords(t, input)
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1:\n%q", len(got), got)
	}
	for _, want := range []string{"not-a-brace", `\"hi {x}\"`, "severity"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("record lost %q:\n%s", want, got[0])
		}
	}
}

// Nested objects and arrays close in the right order.
func TestNestedObjectsGroupAsOneRecord(t *testing.T) {
	input := `[
  {
    "resource": {
      "labels": {
        "service_name": "taa-backend"
      },
      "type": "cloud_run_revision"
    },
    "error_groups": [],
    "severity": 200
  }
]
`
	got := collectRecords(t, input)
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1:\n%q", len(got), got)
	}
	if !strings.Contains(got[0], "cloud_run_revision") || !strings.Contains(got[0], `"severity": 200`) {
		t.Errorf("record truncated at a nested close:\n%s", got[0])
	}
}

// A line that isn't JSON stays a plain-text log line, even inside a JSON
// array stream — gcloud's own warnings arrive that way when stderr is merged.
func TestPlainTextLinesPassThrough(t *testing.T) {
	input := `[
  {
    "insert_id": "a1"
  },
WARNING: some gcloud notice
  {
    "insert_id": "a2"
  }
`
	got := collectRecords(t, input)
	if len(got) != 3 {
		t.Fatalf("got %d records, want 3:\n%q", len(got), got)
	}
	if got[1] != "WARNING: some gcloud notice" {
		t.Errorf("plain-text record = %q, want it passed through unchanged", got[1])
	}
}

// A tail stopped with Ctrl-C leaves the array unclosed and the last object cut
// off mid-key. Emit it rather than drop it; the adapter flags it Malformed.
func TestTruncatedFinalObjectIsEmitted(t *testing.T) {
	input := `[
  {
    "insert_id": "a1"
  },
  {
    "insert_id": "a2",
    "http_request": {
      "referer": "`
	got := collectRecords(t, input)
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2:\n%q", len(got), got)
	}
	if !strings.Contains(got[1], "a2") || strings.HasSuffix(got[1], "\n") {
		t.Errorf("truncated record = %q, want the partial object with no trailing newline", got[1])
	}
}

// An object sharing its line with array punctuation still becomes its own
// record, and a second object on one line is not lost.
func TestObjectsSharingALine(t *testing.T) {
	got := collectRecords(t, "[\n  {\"a\":1}, {\"a\":2}\n]\n")
	want := []string{`{"a":1}`, `{"a":2}`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBlankAndPunctuationLinesAreDropped(t *testing.T) {
	got := collectRecords(t, "[\n\n  {\"a\":1}\n  ,\n\n]\n")
	if len(got) != 1 || got[0] != `{"a":1}` {
		t.Fatalf("got %q, want one record", got)
	}
}

// A live tail must not buffer without limit because one object never closed.
func TestOversizeRecordIsEmittedAndScannerResets(t *testing.T) {
	old := maxRecord
	maxRecord = 64
	t.Cleanup(func() { maxRecord = old })

	input := "[\n  {\n" + strings.Repeat(`    "k": "vvvvvvvvvv",`+"\n", 20) + "  {\"a\":1}\n"
	got := collectRecords(t, input)
	if len(got) < 2 {
		t.Fatalf("got %d records, want the oversize buffer plus a later one:\n%q", len(got), got)
	}
	if got[len(got)-1] != `{"a":1}` {
		t.Errorf("last record = %q, want the scanner to have reset and found the next object", got[len(got)-1])
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/ingest/`
Expected: FAIL — `Records` takes two arguments, `maxRecord` is undefined, and `collectRecords` passes no format.

- [ ] **Step 3: Update `Records` and its two call sites**

In `internal/ingest/reader.go`, replace the `Records` function and its doc comment (lines 33-48) with:

```go
// Records reads r to EOF, invoking emit once per log record.
//
// Producers frame records three ways, and format decides which applies.
// config.FormatAuto detects it from the stream's first line:
//
//	"["  a JSON array — gcloud beta logging tail --json, where each record is
//	     a pretty-printed object spanning many lines
//	"--" YAML documents — gcloud logging read --format=yaml, one record per
//	     document with "---" between them
//	else one record per line: plain text, or one JSON object per line
//
// An explicit format skips detection, which is what a tail attached
// mid-stream needs: it missed the opening "[".
//
// The slice passed to emit is only valid during the call; emit must copy it
// to retain it.
func Records(r io.Reader, format config.Format, emit func(rec []byte)) error {
	br := bufio.NewReader(r)
	if format == config.FormatAuto {
		format = detect(br)
	}
	switch format {
	case config.FormatJSON:
		return jsonArray(br, emit)
	case config.FormatYAML:
		return yamlDocuments(br, emit)
	default:
		return lines(br, emit)
	}
}

// detectPeek bounds how much of the stream detection examines. Every framing
// marker is a line of one to three bytes, so a longer first line can't be
// one, and a peek must stay well inside bufio's buffer anyway.
const detectPeek = 512

// detect reports the framing the stream's first line indicates, without
// consuming any of it.
func detect(br *bufio.Reader) config.Format {
	head, err := br.Peek(detectPeek)
	if len(head) == 0 {
		return config.FormatText
	}
	end := bytes.IndexByte(head, '\n')
	if end < 0 {
		if !errors.Is(err, io.EOF) {
			// The first line runs past the peek, so it is no marker.
			return config.FormatText
		}
		end = len(head) // the whole stream is one unterminated line
	}
	switch first := bytes.TrimRight(head[:end], " \t\r"); {
	case bytes.Equal(first, []byte("[")):
		return config.FormatJSON
	case bytes.HasPrefix(first, []byte("--")):
		return config.FormatYAML
	}
	return config.FormatText
}
```

Add `"github.com/jimbeveridge/loghorn/internal/config"` to that file's import block.

In `internal/headless/headless.go:18`, change `ingest.Records(r, func(rec []byte) {` to `ingest.Records(r, config.FormatAuto, func(rec []byte) {` and add the `config` import. In `main.go:292`, make the same change: `err = ingest.Records(src, config.FormatAuto, func(line []byte) {` and add the import. Both become real wiring in Task 5.

- [ ] **Step 4: Write the framing state machine**

Create `internal/ingest/jsonarray.go`:

```go
package ingest

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// maxRecord bounds one in-progress JSON record. A live tail must not be able
// to buffer without limit because a single object never closed, so at this
// size the buffer is emitted as-is — parsing will flag it Malformed — and the
// scanner resets to look for the next object. A var, not a const, so a test
// can provoke the limit without building a 16MB stream.
var maxRecord = 16 << 20

// jsonArray frames a JSON array stream: array punctuation between objects is
// dropped, each object becomes one record however many lines it spans, and any
// other line is emitted as its own record so a plain-text line interleaved
// into the stream stays a plain-text log line.
func jsonArray(br *bufio.Reader, emit func(rec []byte)) error {
	var s objScanner
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			s.feed(trimEOL(line), emit)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				// An object still open at EOF is the normal ending for a tail
				// stopped with Ctrl-C, not an edge case: emit the partial
				// record rather than silently dropping data.
				s.reset(emit)
				return nil
			}
			return err
		}
	}
}

// objScanner accumulates one JSON object at a time. Depth, string and escape
// state persist across lines, since a record spans many of them.
type objScanner struct {
	buf     []byte
	depth   int
	inStr   bool
	escaped bool
}

// feed processes one input line, which may finish the open object, start a new
// one, or both — so whatever follows a closing brace is fed back through the
// same states rather than discarded.
func (s *objScanner) feed(line []byte, emit func(rec []byte)) {
	for {
		if s.depth == 0 {
			rest, ok := s.start(line, emit)
			if !ok {
				return
			}
			line = rest
		}
		n := s.scan(line)
		s.buf = append(s.buf, line[:n]...)
		line = line[n:]
		switch {
		case s.depth == 0:
			emit(s.buf)
			s.buf = s.buf[:0]
		case len(s.buf) >= maxRecord:
			s.reset(emit)
			return
		default:
			// The object continues on the next line, and its newline is part
			// of the record.
			s.buf = append(s.buf, '\n')
			return
		}
	}
}

// start positions line at the next object's opening brace. When the line holds
// no object it handles the line itself — dropping array punctuation, emitting
// anything else as a plain-text record — and reports that no object began.
func (s *objScanner) start(line []byte, emit func(rec []byte)) ([]byte, bool) {
	i := bytes.IndexByte(line, '{')
	// Everything before the brace must be punctuation too, or this is a text
	// line that merely happens to contain one.
	if i < 0 || !isArrayPunct(line[:i]) {
		if t := bytes.TrimSpace(line); len(t) > 0 && !isArrayPunct(t) {
			emit(line)
		}
		return nil, false
	}
	return line[i:], true
}

// isArrayPunct reports whether b holds only the brackets, commas and space
// that frame an array — the bytes between records, which are not records.
func isArrayPunct(b []byte) bool {
	for _, c := range b {
		switch c {
		case '[', ']', ',', ' ', '\t', '\r':
		default:
			return false
		}
	}
	return true
}

// scan advances the object state across line and returns how many bytes belong
// to the current object: all of line, or up to and including the brace that
// closes it. Quotes and backslash escapes are honoured, so a brace inside a
// user_agent or a SQL statement cannot move the depth.
func (s *objScanner) scan(line []byte) int {
	for i, c := range line {
		switch {
		case s.escaped:
			s.escaped = false
		case s.inStr:
			switch c {
			case '\\':
				s.escaped = true
			case '"':
				s.inStr = false
			}
		default:
			switch c {
			case '"':
				s.inStr = true
			case '{':
				s.depth++
			case '}':
				if s.depth--; s.depth == 0 {
					return i + 1
				}
			}
		}
	}
	return len(line)
}

// reset emits whatever has accumulated and clears the scanner, for EOF with an
// object still open and for one that outgrew maxRecord. The trailing newline
// goes: a partial object's last line break carries no meaning.
func (s *objScanner) reset(emit func(rec []byte)) {
	if buf := bytes.TrimRight(s.buf, "\n"); len(buf) > 0 {
		emit(buf)
	}
	s.buf, s.depth, s.inStr, s.escaped = s.buf[:0], 0, false, false
}
```

- [ ] **Step 5: Update the existing test helper**

In `internal/ingest/reader_test.go`, change `collectRecords` (line 43) to detect as before:

```go
func collectRecords(t *testing.T, input string) []string {
	t.Helper()
	var got []string
	err := Records(strings.NewReader(input), config.FormatAuto, func(rec []byte) {
		got = append(got, string(rec))
	})
	if err != nil {
		t.Fatalf("Records error: %v", err)
	}
	return got
}
```

Add `"github.com/jimbeveridge/loghorn/internal/config"` to that file's imports. Change nothing else in it: the four existing `Records` tests must keep passing exactly as written, which is what proves plain-text and YAML streams are unaffected.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/ingest/ -v`
Expected: PASS, including the four pre-existing `TestRecords*` tests.

- [ ] **Step 7: Check formatting and the whole suite**

Run: `gofmt -l . && go test ./...`
Expected: `gofmt -l` prints nothing; every package passes.

- [ ] **Step 8: Commit**

```bash
git add internal/ingest/reader.go internal/ingest/jsonarray.go \
        internal/ingest/reader_test.go internal/ingest/jsonarray_test.go \
        internal/headless/headless.go main.go
git commit -m "feat(ingest): frame gcloud's multi-line JSON array into records

gcloud beta logging tail --json opens with a bare '[' and pretty-prints each
entry across many lines, so one-record-per-line framing turned every entry
into dozens of unparseable fragments.

Depth tracking honours strings and escapes, so a brace inside a user_agent or
a SQL statement can't swallow the stream, and a line that isn't JSON is still
emitted as its own plain-text record. A tail stopped with Ctrl-C leaves the
array unclosed and the last object cut off mid-key: that partial record is
emitted rather than dropped, and the adapter flags it Malformed."
```

---

### Task 3: snake_case fields and numeric severity in `internal/adapter`

**Files:**
- Modify: `internal/adapter/adapter.go` (whole file)
- Modify: `internal/adapter/logentry.go` (`LogEntryAdapter`, `fromLogEntryObject`, `logEntryMessage`, `correlationCandidates`, `correlationID`)
- Modify: `internal/adapter/yamlentry.go` (`YAMLAdapter`)
- Create: `internal/adapter/gcloud_test.go`

**Interfaces:**
- Consumes: `config.Input`, `config.FieldNaming`, `config.NamingCamel`, `config.NamingSnake`, `config.SeverityMode`, `config.SeverityString`, `config.SeverityNumeric`, `config.Default` from Task 1.
- Produces:
  - `type Parser struct{ ... }`, `func New(in config.Input) *Parser`, `func (p *Parser) ParseLine(line []byte) entry.Entry`
  - `func ParseLine(line []byte) entry.Entry` — unchanged signature, a wrapper over a default-configured `Parser`
  - `LogEntryAdapter` and `YAMLAdapter` each gain an exported `In config.Input` field; their zero values keep behaving as `auto`

**Scope note:** the spec lists eight aliased keys, but the adapter only ever *looks up* `jsonPayload`, `textPayload` and `httpRequest` (plus the correlation keys). `insertId`, `logName`, `receiveTimestamp` and `traceSampled` are never read — they reach the detail pane through `Entry.JSON` verbatim, whatever they are spelled. Alias only the keys that are read; aliasing the rest would be dead code. `snakeCase` is a general converter, so adding a read of another field later needs no table edit.

- [ ] **Step 1: Write the failing tests**

Create `internal/adapter/gcloud_test.go`:

```go
package adapter

import (
	"testing"

	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/entry"
)

// What gcloud beta logging tail --json actually emits: snake_case LogEntry
// field names and a numeric severity.
const gcloudJSON = `{
  "http_request": {
    "request_method": "GET",
    "status": 503,
    "user_agent": "curl/8.7.1"
  },
  "insert_id": "6aad671100091ffaa983635f",
  "json_payload": {
    "message": "Database: QUERY failed",
    "requestId": "f17c46797e7721ce34df0c5a65fdac5d"
  },
  "log_name": "projects/p/logs/run.googleapis.com%2Fstdout",
  "receive_timestamp": "2026-09-18T16:30:09.936810442Z",
  "severity": 500,
  "span_id": "43e3c93c8a43b1cb",
  "timestamp": "2026-09-18T16:30:09.586452Z"
}`

func TestParseLineGcloudJSON(t *testing.T) {
	e := ParseLine([]byte(gcloudJSON))
	if e.Format != entry.FormatLogEntry || e.Malformed {
		t.Fatalf("Format = %v Malformed = %v, want FormatLogEntry", e.Format, e.Malformed)
	}
	if e.Severity != entry.SevError {
		t.Errorf("Severity = %v, want SevError from the numeric 500", e.Severity)
	}
	if e.HTTPStatus != 503 {
		t.Errorf("HTTPStatus = %d, want 503 from http_request.status", e.HTTPStatus)
	}
	if e.Message != "Database: QUERY failed" {
		t.Errorf("Message = %q, want json_payload.message", e.Message)
	}
	if e.CorrelationID != "f17c46797e7721ce34df0c5a65fdac5d" {
		t.Errorf("CorrelationID = %q, want json_payload.requestId", e.CorrelationID)
	}
	if e.Timestamp.IsZero() {
		t.Error("Timestamp should be parsed")
	}
}

// The gcloud --format=yaml shape, which shares the JSON one's field names.
func TestParseLineGcloudYAML(t *testing.T) {
	rec := []byte("---\n" +
		"http_request:\n  status: 503\n" +
		"json_payload:\n  message: 'Database: QUERY failed'\n" +
		"severity: 500\n" +
		"span_id: ''\n" +
		"timestamp: '2026-09-18T16:45:24.697Z'\n")
	e := ParseLine(rec)
	if e.Format != entry.FormatLogEntry || e.Malformed {
		t.Fatalf("Format = %v Malformed = %v, want FormatLogEntry", e.Format, e.Malformed)
	}
	if e.Severity != entry.SevError {
		t.Errorf("Severity = %v, want SevError from the numeric 500", e.Severity)
	}
	if e.HTTPStatus != 503 {
		t.Errorf("HTTPStatus = %d, want 503", e.HTTPStatus)
	}
	if e.Message != "Database: QUERY failed" {
		t.Errorf("Message = %q, want json_payload.message", e.Message)
	}
	// An empty span_id is no correlation id.
	if e.CorrelationID != "" {
		t.Errorf("CorrelationID = %q, want empty: span_id is ''", e.CorrelationID)
	}
}

// Both spellings must produce the same Entry, so nothing downstream has to
// care which producer a record came from.
func TestBothSpellingsAgree(t *testing.T) {
	camel := []byte(`{"severity":"ERROR","httpRequest":{"status":503},"textPayload":"boom","trace":"t1"}`)
	snake := []byte(`{"severity":500,"http_request":{"status":503},"text_payload":"boom","trace":"t1"}`)
	c, s := ParseLine(camel), ParseLine(snake)
	if c.Severity != s.Severity || c.HTTPStatus != s.HTTPStatus ||
		c.Message != s.Message || c.CorrelationID != s.CorrelationID {
		t.Errorf("spellings disagree:\ncamel: %+v\nsnake: %+v", c, s)
	}
}

func TestFieldNamingRestrictions(t *testing.T) {
	snake := []byte(`{"severity":500,"http_request":{"status":503},"text_payload":"boom"}`)
	camel := []byte(`{"severity":"ERROR","httpRequest":{"status":503},"textPayload":"boom"}`)

	// camel refuses to read gcloud's spelling.
	e := New(config.Input{FieldNaming: config.NamingCamel}).ParseLine(snake)
	if e.HTTPStatus != 0 {
		t.Errorf("HTTPStatus = %d under camel, want 0: http_request must be ignored", e.HTTPStatus)
	}
	// snake refuses to read the canonical spelling.
	e = New(config.Input{FieldNaming: config.NamingSnake}).ParseLine(camel)
	if e.HTTPStatus != 0 {
		t.Errorf("HTTPStatus = %d under snake, want 0: httpRequest must be ignored", e.HTTPStatus)
	}
	// Each reads its own.
	if e := New(config.Input{FieldNaming: config.NamingSnake}).ParseLine(snake); e.HTTPStatus != 503 {
		t.Errorf("HTTPStatus = %d under snake with snake input, want 503", e.HTTPStatus)
	}
	if e := New(config.Input{FieldNaming: config.NamingCamel}).ParseLine(camel); e.HTTPStatus != 503 {
		t.Errorf("HTTPStatus = %d under camel with camel input, want 503", e.HTTPStatus)
	}
}

func TestNumericSeverityLadder(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want entry.Severity
	}{
		{"0", entry.SevDefault},
		{"100", entry.SevDebug},
		{"200", entry.SevInfo},
		{"300", entry.SevNotice},
		{"400", entry.SevWarning},
		{"500", entry.SevError},
		{"600", entry.SevCritical},
		{"700", entry.SevAlert},
		{"800", entry.SevEmergency},
		// Off the ladder: the nearest value at or below.
		{"250", entry.SevInfo},
		{"900", entry.SevEmergency},
		{"50", entry.SevDefault},
		// Neither of these is a LogSeverity code.
		{"-100", entry.SevDefault},
		{"200.5", entry.SevDefault},
	} {
		e := ParseLine([]byte(`{"severity":` + tc.in + `}`))
		if e.Severity != tc.want {
			t.Errorf("severity %s = %v, want %v", tc.in, e.Severity, tc.want)
		}
	}
}

func TestSeverityModeRestrictions(t *testing.T) {
	if e := New(config.Input{Severity: config.SeverityString}).ParseLine([]byte(`{"severity":500}`)); e.Severity != entry.SevDefault {
		t.Errorf("Severity = %v under string mode, want SevDefault for a number", e.Severity)
	}
	if e := New(config.Input{Severity: config.SeverityNumeric}).ParseLine([]byte(`{"severity":"ERROR"}`)); e.Severity != entry.SevDefault {
		t.Errorf("Severity = %v under numeric mode, want SevDefault for a string", e.Severity)
	}
}

// gcloud renames LogEntry's own fields but not the payload's, which is
// whatever the application emitted — so span_id is aliased at the root and
// requestId is not inside the payload.
func TestCorrelationFromGcloudSpanID(t *testing.T) {
	if e := ParseLine([]byte(`{"span_id":"43e3c93c"}`)); e.CorrelationID != "43e3c93c" {
		t.Errorf("CorrelationID = %q, want span_id", e.CorrelationID)
	}
	if e := ParseLine([]byte(`{"json_payload":{"requestId":"r1"}}`)); e.CorrelationID != "r1" {
		t.Errorf("CorrelationID = %q, want json_payload.requestId", e.CorrelationID)
	}
}

// A zero-value Input must behave as auto, so the package-level adapters and
// every existing test construction keep working.
func TestZeroInputBehavesAsAuto(t *testing.T) {
	if New(config.Input{}).ParseLine([]byte(gcloudJSON)).Severity != entry.SevError {
		t.Error("a zero-value Input should behave as auto")
	}
	if (LogEntryAdapter{}).Detect([]byte(gcloudJSON)) != true {
		t.Error("LogEntryAdapter{} should still detect a JSON object")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/adapter/`
Expected: FAIL — `New` is undefined, and the gcloud tests fail on severity, HTTP status and message.

- [ ] **Step 3: Replace `internal/adapter/adapter.go`**

```go
// Package adapter turns raw log lines into normalized entry.Entry values.
package adapter

import (
	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/entry"
)

// Adapter detects and parses one log format. Detect must be cheap.
type Adapter interface {
	Detect(line []byte) bool
	Parse(line []byte) (entry.Entry, error)
}

// Parser normalizes records under one input configuration — which spelling of
// a LogEntry field name it accepts, and which encoding of a severity.
type Parser struct {
	logEntry  LogEntryAdapter
	yamlEntry YAMLAdapter
	rawText   RawTextAdapter
}

// New returns a Parser for in. A zero-value in behaves as if every setting
// were "auto".
func New(in config.Input) *Parser {
	return &Parser{
		logEntry:  LogEntryAdapter{In: in},
		yamlEntry: YAMLAdapter{In: in},
	}
}

// defaultParser serves ParseLine, for the callers and tests that configure
// nothing.
var defaultParser = New(config.Default().Input)

// ParseLine normalizes a single record with the default configuration.
func ParseLine(line []byte) entry.Entry { return defaultParser.ParseLine(line) }

// ParseLine normalizes a single record — usually one line, but one whole
// document when ingest.Records has split a gcloud `--format=yaml` stream on
// "---", or one whole object from a `--json` array. It never returns an error:
// a record that looks like JSON or YAML but fails to parse falls back to raw
// text, flagged Malformed.
func (p *Parser) ParseLine(line []byte) entry.Entry {
	switch {
	case p.logEntry.Detect(line):
		if e, err := p.logEntry.Parse(line); err == nil {
			return e
		}
	case p.yamlEntry.Detect(line):
		if e, err := p.yamlEntry.Parse(line); err == nil {
			return e
		}
	default:
		e, _ := p.rawText.Parse(line)
		return e
	}
	e, _ := p.rawText.Parse(line)
	e.Malformed = true
	return e
}
```

- [ ] **Step 4: Update `internal/adapter/logentry.go`**

Change the adapter type and thread `In` through. Replace lines 11-24 with:

```go
// LogEntryAdapter parses one JSON LogEntry. In selects which spelling of a
// field name and which encoding of a severity it accepts; a zero value
// behaves as "auto".
type LogEntryAdapter struct{ In config.Input }

func (LogEntryAdapter) Detect(line []byte) bool {
	t := bytes.TrimSpace(line)
	return len(t) > 0 && t[0] == '{'
}

func (a LogEntryAdapter) Parse(line []byte) (entry.Entry, error) {
	var obj map[string]any
	if err := json.Unmarshal(line, &obj); err != nil {
		return entry.Entry{}, err
	}
	return fromLogEntryObject(line, obj, a.In), nil
}
```

Replace `fromLogEntryObject` (lines 26-48) with:

```go
// fromLogEntryObject builds an Entry from a decoded LogEntry object. Shared by
// the JSON and YAML adapters, which differ only in how they get from bytes to
// this map — the GCP LogEntry schema and every field below it are the same
// either way.
func fromLogEntryObject(raw []byte, obj map[string]any, in config.Input) entry.Entry {
	e := entry.Entry{
		Raw:    append([]byte(nil), raw...),
		Format: entry.FormatLogEntry,
		JSON:   obj,
	}
	e.Severity = severityOf(obj, in.Severity)
	if hr, ok := fieldMap(obj, "httpRequest", in.FieldNaming); ok {
		if st, ok := hr["status"].(float64); ok {
			e.HTTPStatus = int(st)
		}
	}
	e.Timestamp = logEntryTimestamp(obj)
	e.Message = logEntryMessage(obj, in.FieldNaming)
	e.CorrelationID = correlationID(obj, in.FieldNaming)
	return e
}

// field returns obj's value for a canonical camelCase LogEntry key, honouring
// the configured spelling. gcloud names every LogEntry field in snake_case
// (json_payload, http_request, …) while the canonical schema and most other
// producers use camelCase. Under auto — the default, and any value other than
// an explicit camel or snake — both are accepted: one document can't spell the
// same key both ways, so there is nothing to disambiguate.
func field(obj map[string]any, camel string, naming config.FieldNaming) (any, bool) {
	if naming != config.NamingSnake {
		if v, ok := obj[camel]; ok {
			return v, true
		}
		if naming == config.NamingCamel {
			return nil, false
		}
	}
	v, ok := obj[snakeCase(camel)]
	return v, ok
}

func fieldString(obj map[string]any, camel string, naming config.FieldNaming) (string, bool) {
	v, ok := field(obj, camel, naming)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func fieldMap(obj map[string]any, camel string, naming config.FieldNaming) (map[string]any, bool) {
	v, ok := field(obj, camel, naming)
	if !ok {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}

// snakeCase converts a camelCase LogEntry key to gcloud's spelling:
// jsonPayload becomes json_payload. Only ever called with LogEntry key names,
// all of which are plain camelCase with no acronyms or digits.
func snakeCase(camel string) string {
	var b strings.Builder
	b.Grow(len(camel) + 2)
	for i := 0; i < len(camel); i++ {
		if c := camel[i]; c >= 'A' && c <= 'Z' {
			b.WriteByte('_')
			b.WriteByte(c + 'a' - 'A')
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// severityOf reads the entry's severity. The canonical LogEntry carries a
// string name; gcloud's --json and --format=yaml emit the numeric LogSeverity
// code instead. Under auto either is accepted.
func severityOf(obj map[string]any, mode config.SeverityMode) entry.Severity {
	v, ok := obj["severity"] // spelled the same in both conventions
	if !ok {
		return entry.SevDefault
	}
	if mode != config.SeverityNumeric {
		if s, ok := v.(string); ok {
			return entry.ParseSeverity(s)
		}
		if mode == config.SeverityString {
			return entry.SevDefault
		}
	}
	if n, ok := v.(float64); ok {
		return numericSeverity(n)
	}
	return entry.SevDefault
}

// severityLadder is GCP's LogSeverity enum, highest first so the first code at
// or below a value wins.
var severityLadder = []struct {
	code int
	sev  entry.Severity
}{
	{800, entry.SevEmergency}, {700, entry.SevAlert}, {600, entry.SevCritical},
	{500, entry.SevError}, {400, entry.SevWarning}, {300, entry.SevNotice},
	{200, entry.SevInfo}, {100, entry.SevDebug},
}

// numericSeverity maps a LogSeverity code. A value not exactly on the ladder
// takes the nearest one at or below it, so a future intermediate code degrades
// sensibly instead of becoming DEFAULT. A negative or fractional value is no
// LogSeverity code at all, so it is DEFAULT.
func numericSeverity(n float64) entry.Severity {
	if n < 0 || n != math.Trunc(n) {
		return entry.SevDefault
	}
	for _, l := range severityLadder {
		if int(n) >= l.code {
			return l.sev
		}
	}
	return entry.SevDefault
}
```

Replace `logEntryMessage` (lines 63-82) with:

```go
// logEntryMessage picks the best human-facing message: textPayload, then
// jsonPayload.message, then top-level message, else the compact JSON.
func logEntryMessage(obj map[string]any, naming config.FieldNaming) string {
	if tp, ok := fieldString(obj, "textPayload", naming); ok && tp != "" {
		return tp
	}
	if jp, ok := fieldMap(obj, "jsonPayload", naming); ok {
		if m, ok := jp["message"].(string); ok && m != "" {
			return m
		}
	}
	if m, ok := obj["message"].(string); ok && m != "" {
		return m
	}
	if b, err := json.Marshal(obj); err == nil {
		return string(b)
	}
	return ""
}
```

Replace `correlationCandidates` and `correlationID` with:

```go
// correlationCandidates are checked in order, at the root then inside the
// payload. The root list carries gcloud's spelling (span_id) directly rather
// than going through snakeCase, because two of these keys are neither
// camelCase nor gcloud's to rename. The payload is whatever the application
// emitted — gcloud renames only LogEntry's own fields — so payload keys get no
// snake_case variants.
// Captured in v0; the correlated request view that consumes it is a v1 feature.
var correlationCandidates = []string{
	"trace", "requestId", "logging.googleapis.com/trace",
	"logging.googleapis.com/spanId", // what Cloud Run actually emits; bare "spanId" is the local convention
	"spanId", "span_id",
}

func correlationID(obj map[string]any, naming config.FieldNaming) string {
	for _, k := range correlationCandidates {
		if s, ok := obj[k].(string); ok && s != "" {
			return s
		}
	}
	if jp, ok := fieldMap(obj, "jsonPayload", naming); ok {
		for _, k := range correlationCandidates {
			if s, ok := jp[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
```

Set that file's import block to:

```go
import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/entry"
)
```

- [ ] **Step 5: Update `internal/adapter/yamlentry.go`**

Replace lines 10-27 with:

```go
// YAMLAdapter parses gcloud logging read's `--format=yaml` output: one
// LogEntry per document. ingest.Records already splits the stream into
// per-document records on the "---" separator between them, keeping each
// record's leading separator, so Detect only needs to check for that prefix.
// In carries the same field-naming and severity settings as LogEntryAdapter;
// a zero value behaves as "auto".
type YAMLAdapter struct{ In config.Input }

func (YAMLAdapter) Detect(rec []byte) bool {
	t := bytes.TrimSpace(rec)
	return len(t) >= 2 && t[0] == '-' && t[1] == '-'
}

func (a YAMLAdapter) Parse(rec []byte) (entry.Entry, error) {
	var obj map[string]any
	if err := yaml.Unmarshal(rec, &obj); err != nil {
		return entry.Entry{}, err
	}
	return fromLogEntryObject(rec, normalizeYAML(obj).(map[string]any), a.In), nil
}
```

Add `"github.com/jimbeveridge/loghorn/internal/config"` to that file's imports.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/adapter/ -v`
Expected: PASS, including every pre-existing test in `adapter_test.go` — those all use camelCase and string severities, so they prove `auto` is backward compatible.

- [ ] **Step 7: Check formatting and the whole suite**

Run: `gofmt -l . && go test ./...`
Expected: `gofmt -l` prints nothing; every package passes.

- [ ] **Step 8: Commit**

```bash
git add internal/adapter/adapter.go internal/adapter/logentry.go \
        internal/adapter/yamlentry.go internal/adapter/gcloud_test.go
git commit -m "feat(adapter): accept gcloud's snake_case fields and numeric severity

gcloud spells LogEntry's own fields json_payload, http_request, text_payload
and encodes severity as the numeric LogSeverity code, so every record from it
parsed with severity DEFAULT, no HTTP status, and a compact-JSON dump for a
message — severity colouring, importance and alerting were all dead on real
gcloud output.

Under auto both spellings and both encodings are accepted: one document can't
spell a key both ways, so there is nothing to disambiguate. Only the keys the
adapter actually reads are aliased; the rest reach the detail pane verbatim
through Entry.JSON whatever they're called.

ParseLine keeps its signature as a wrapper over a default-configured Parser,
so the TUI's call sites are untouched."
```

---

### Task 4: keep multi-line records replayable

**Files:**
- Create: `internal/ingest/compact.go`
- Create: `internal/ingest/roundtrip_test.go`

**Interfaces:**
- Consumes: `Records` from Task 2.
- Produces: `func CompactRecord(rec []byte) []byte`

- [ ] **Step 1: Write the failing tests**

Create `internal/ingest/roundtrip_test.go`:

```go
package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/logfile"
)

func TestCompactRecord(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"single line untouched", `{"a":1}`, `{"a":1}`},
		{"plain text untouched", "just a log line", "just a log line"},
		{
			"multi-line json compacted",
			"{\n  \"a\": 1,\n  \"b\": {\n    \"c\": 2\n  }\n}",
			`{"a":1,"b":{"c":2}}`,
		},
		{
			// A YAML document needs no help: it keeps its leading "---", so a
			// replay re-detects YAML framing and re-groups it.
			"yaml untouched",
			"---\nseverity: 500\nmessage: boom",
			"---\nseverity: 500\nmessage: boom",
		},
		{
			"multi-line non-json untouched",
			"Traceback:\n  at handler",
			"Traceback:\n  at handler",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(CompactRecord([]byte(tc.in))); got != tc.want {
				t.Errorf("CompactRecord:\ngot:  %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// The log file holds one record per line, so a multi-line JSON record must be
// compacted before it is written. Without that, -historical replay reads the
// stored file — which starts with '{', not '[' — as line-framed and shreds
// each record back into fragments.
func TestMultiLineRecordSurvivesTheLogFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	w, err := logfile.Open(dir, time.Now, time.Local)
	if err != nil {
		t.Fatalf("logfile.Open: %v", err)
	}

	stream := "[\n  {\n    \"insert_id\": \"a1\",\n    \"severity\": 500\n  },\n" +
		"  {\n    \"insert_id\": \"a2\",\n    \"severity\": 200\n  }\n]\n"

	var ingested []string
	err = Records(strings.NewReader(stream), config.FormatAuto, func(rec []byte) {
		ingested = append(ingested, string(rec))
		if err := w.Write(CompactRecord(rec)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(ingested) != 2 {
		t.Fatalf("ingested %d records, want 2: %q", len(ingested), ingested)
	}

	f, err := os.Open(filepath.Join(dir, "loghorn.log"))
	if err != nil {
		t.Fatalf("open stored log: %v", err)
	}
	defer f.Close()

	var replayed []string
	if err := Records(f, config.FormatAuto, func(rec []byte) {
		replayed = append(replayed, string(rec))
	}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	want := []string{`{"insert_id":"a1","severity":500}`, `{"insert_id":"a2","severity":200}`}
	if strings.Join(replayed, "|") != strings.Join(want, "|") {
		t.Fatalf("replay:\ngot:  %q\nwant: %q", replayed, want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/ingest/ -run 'Compact|SurvivesTheLogFile'`
Expected: FAIL with `undefined: CompactRecord`.

- [ ] **Step 3: Write the implementation**

Create `internal/ingest/compact.go`:

```go
package ingest

import (
	"bytes"
	"encoding/json"
)

// CompactRecord returns rec as a single line, for the log file.
//
// logfile's Writer appends a record and a newline, so the stored file holds
// one record per line. A multi-line JSON record written verbatim would break
// that: the stored file starts with '{' rather than '[', so -historical reads
// it back as line-framed and shreds every record into fragments. YAML needs no
// such help — its records keep their leading "---", so a replay re-detects
// YAML framing and re-groups them.
//
// rec comes back unchanged when it holds no newline, and when it isn't valid
// JSON, so YAML documents and multi-line plain text pass through untouched.
// Only the on-disk copy is compacted: Entry.Raw keeps the original bytes, so
// the detail pane, find and yank still show what arrived on the wire.
func CompactRecord(rec []byte) []byte {
	if bytes.IndexByte(rec, '\n') < 0 {
		return rec
	}
	var out bytes.Buffer
	if err := json.Compact(&out, rec); err != nil {
		return rec
	}
	return out.Bytes()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/ingest/ -run 'Compact|SurvivesTheLogFile' -v`
Expected: PASS.

- [ ] **Step 5: Check formatting and the whole suite**

Run: `gofmt -l . && go test ./...`
Expected: `gofmt -l` prints nothing; every package passes.

- [ ] **Step 6: Commit**

```bash
git add internal/ingest/compact.go internal/ingest/roundtrip_test.go
git commit -m "fix(ingest): compact multi-line records for the log file

The log file holds one record per line. A multi-line JSON record written
verbatim would be shredded on -historical replay, because the stored file
starts with '{' rather than '[' and so reads back as line-framed. YAML
escapes this by keeping its leading '---'.

Only the on-disk copy is compacted; Entry.Raw keeps the original bytes, so
the detail pane, find and yank still show what arrived on the wire."
```

---

### Task 5: wire the config through `main.go` and `internal/headless`

**Files:**
- Modify: `main.go` — `usage` (lines 45-75), flag block (lines 88-100), config load after the theme check (line 117), `--filter` dispatch (lines 172-197), ingest call (line 292), `recordSink` (lines 437-446)
- Modify: `internal/headless/headless.go`
- Modify: `internal/headless/headless_test.go` — three `Run` call sites at lines 23, 64, 99
- Modify: `main_test.go` — add a test for the new flag's plumbing

**Interfaces:**
- Consumes: `config.Load`, `config.Config`, `config.Input` (Task 1); `ingest.Records`, `ingest.CompactRecord` (Tasks 2, 4); `adapter.New` (Task 3).
- Produces: `func Run(r io.Reader, w io.Writer, in config.Input, sink func(rec []byte)) error` — a signature change from `Run(r, w, sink)`.

- [ ] **Step 1: Write the failing test**

Add to `main_test.go`:

```go
// configFor is what main uses to resolve the config: the working directory
// anchors it, the same as .loghorn/ and the source-tree refusal, so all three
// agree on what "this project" means.
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
```

Add `"github.com/jimbeveridge/loghorn/internal/config"` to `main_test.go`'s imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test . -run TestConfigForUsesWorkingDirectory`
Expected: FAIL to build — `main_test.go` does not yet import `config` and the package may not compile until step 3 updates the `headless.Run` call sites.

- [ ] **Step 3: Update `internal/headless/headless.go`**

```go
// Package headless runs the loghorn engine as a stdin->stdout filter (no UI).
package headless

import (
	"io"

	"github.com/jimbeveridge/loghorn/internal/adapter"
	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/engine"
	"github.com/jimbeveridge/loghorn/internal/ingest"
)

// Run reads records from r and writes the original bytes of each important one
// (plus a newline) to w. Output is byte-for-byte faithful to the input, so a
// multi-line record prints across as many lines as it arrived on. If sink is
// non-nil it receives every record first, important or not — the log file
// keeps the whole stream. The slice passed to sink is only valid during the
// call. in decides both how the stream is framed and how a record's fields are
// read.
func Run(r io.Reader, w io.Writer, in config.Input, sink func(rec []byte)) error {
	p := adapter.New(in)
	var writeErr error
	err := ingest.Records(r, in.Format, func(rec []byte) {
		if sink != nil {
			sink(rec)
		}
		if writeErr != nil {
			return
		}
		e := p.ParseLine(rec)
		if !engine.IsImportant(e) {
			return
		}
		if _, err := w.Write(e.Raw); err != nil {
			writeErr = err
			return
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			writeErr = err
		}
	})
	if err != nil {
		return err
	}
	return writeErr
}
```

In `internal/headless/headless_test.go`, pass a default `Input` at all three call sites — `Run(strings.NewReader(input), &out, config.Default().Input, nil)` at line 23, `Run(strings.NewReader(tc.input), io.Discard, config.Default().Input, sink)` at line 64, and `Run(strings.NewReader(input), errWriter{wantErr}, config.Default().Input, sink)` at line 99 — and add the `config` import.

- [ ] **Step 4: Wire `main.go`**

Add `configFlag := flag.Bool("config", false, "print the config file in effect and the settings it resolves to, then exit")` to the flag block, and add `"github.com/jimbeveridge/loghorn/internal/config"` to the imports.

After the `--theme` check (after line 117), add:

```go
	// The config file is resolved from the working directory, the same anchor
	// as .loghorn/ and the source-tree refusal, so all three agree on what
	// "this project" means. A bad file is a usage error like a bad --theme,
	// reported before anything is launched or written.
	cwd, _ := os.Getwd()
	cfg, cfgPath, err := config.Load(cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "loghorn:", err)
		os.Exit(2)
	}
	if *configFlag {
		if cfgPath == "" {
			fmt.Println("no config file found; using defaults")
		} else {
			fmt.Println(cfgPath)
		}
		fmt.Print(cfg)
		return
	}
```

Note `err` is already declared by the `parseTheme` call above, so this uses `=` for it and `:=` for the new names — written as above, `cwd, cfg, cfgPath` are new so `:=` is correct and `err` is reassigned.

In the `--filter` dispatch, pass the input config to both `headless.Run` calls: `headless.Run(f, os.Stdout, cfg.Input, nil)` at line 180 and `headless.Run(os.Stdin, os.Stdout, cfg.Input, sink)` at line 193.

At line 292, replace the `config.FormatAuto` placeholder from Task 2 with the resolved setting, and build a configured parser once outside the loop. Before the `go func()` that reads the sources, add:

```go
	parser := adapter.New(cfg.Input)
```

and inside it change the two lines to:

```go
			err = ingest.Records(src, cfg.Input.Format, func(line []byte) {
				sink(line)
				e := parser.ParseLine(line)
```

In `recordSink` (line 437), compact before writing:

```go
// recordSink returns the function that copies every record to the log file.
// A multi-line JSON record is compacted first: the file holds one record per
// line, and -historical replay depends on that.
func recordSink(w *logfile.Writer, onFail func(error)) func(rec []byte) {
	if w == nil {
		return func([]byte) {}
	}
	return func(rec []byte) {
		if err := w.Write(ingest.CompactRecord(rec)); err != nil {
			onFail(err)
		}
	}
}
```

In `usage`, add a paragraph after the one about `.loghorn/`, and `-config` will appear in `flag.PrintDefaults()` automatically:

```
Settings come from .config/loghorn/config.toml, found by walking up from the
directory loghorn starts in; the nearest one wins outright, and if there is
none, ~/.config/loghorn/config.toml applies. It selects how input is framed
(`format`) and how LogEntry fields and severities are read (`field-naming`,
`severity`) — gcloud's snake_case and numeric severities are accepted by
default. Run with -config to see which file is in effect.
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test . ./internal/headless/ -v`
Expected: PASS, including the pre-existing headless tests.

- [ ] **Step 6: Verify the flag by hand**

```bash
go build -o /tmp/loghorn . && cd /tmp && /tmp/loghorn -config
```

Expected: `no config file found; using defaults` followed by the `[input]` block with three `"auto"` values. Then:

```bash
mkdir -p /tmp/lhtest/.config/loghorn && printf '[input]\nformat = "json"\n' > /tmp/lhtest/.config/loghorn/config.toml
cd /tmp/lhtest && /tmp/loghorn -config
```

Expected: the path `/tmp/lhtest/.config/loghorn/config.toml` and `format = "json"`.

```bash
printf '[input]\nformat = "jsonl"\n' > /tmp/lhtest/.config/loghorn/config.toml
cd /tmp/lhtest && /tmp/loghorn -config; echo "exit=$?"
```

Expected: an error naming the file, the key `input.format`, the value `"jsonl"` and the permitted set, with `exit=2`. Clean up: `rm -rf /tmp/lhtest`.

- [ ] **Step 7: Check formatting and the whole suite**

Run: `gofmt -l . && go test ./...`
Expected: `gofmt -l` prints nothing; every package passes.

- [ ] **Step 8: Commit**

```bash
git add main.go main_test.go internal/headless/headless.go internal/headless/headless_test.go
git commit -m "feat: resolve the config file and wire it through input

The config is resolved from the working directory, the same anchor as
.loghorn/ and the source-tree refusal, so all three agree on what 'this
project' means. A bad file is a usage error reported before anything is
launched or written, like a bad --theme.

-config prints the file in effect and the settings it resolves to, because
walking up from the working directory makes 'which file am I getting?' the
first question when discovery misbehaves."
```

---

### Task 6: end-to-end coverage against gcloud's real shapes

**Files:**
- Create: `testdata/gcloud-tail.json`
- Create: `testdata/gcloud-read.yaml`
- Create: `internal/headless/gcloud_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1-5. Produces nothing new.

- [ ] **Step 1: Create the JSON fixture**

Create `testdata/gcloud-tail.json` — the shape `gcloud beta logging tail --json` emits, trimmed to what matters: a bare `[`, snake_case fields, numeric severities, a brace and a bracket inside a string, an interleaved plain-text line, and a final object truncated the way Ctrl-C leaves it.

```
[
  {
    "http_request": {
      "request_method": "GET",
      "status": 204,
      "user_agent": "Mozilla/5.0 (Macintosh) {not-a-brace} [nor-this]"
    },
    "insert_id": "6aad671100091ffaa983635f",
    "log_name": "projects/anvil-pulse-7451/logs/run.googleapis.com%2Frequests",
    "receive_timestamp": "2026-09-18T16:30:09.936810442Z",
    "resource": {
      "labels": {
        "service_name": "taa-backend"
      },
      "type": "cloud_run_revision"
    },
    "severity": 200,
    "span_id": "43e3c93c8a43b1cb",
    "timestamp": "2026-09-18T16:30:09.586452Z"
  },
WARNING: gcloud notice interleaved on the same stream
  {
    "error_groups": [],
    "json_payload": {
      "message": "Database: QUERY failed",
      "requestId": "f17c46797e7721ce34df0c5a65fdac5d"
    },
    "severity": 500,
    "timestamp": "2026-09-18T16:45:24.697Z"
  },
  {
    "http_request": {
      "status": 503
    },
    "severity": 500,
    "timestamp": "2026-09-18T16:45:25.100Z"
  },
  {
    "json_payload": {
      "message": "cut off by ctrl-c
```

- [ ] **Step 2: Create the YAML fixture**

Create `testdata/gcloud-read.yaml` — the `--format=yaml` shape, including the empty `span_id` the real output carries:

```
---
insert_id: 6aad6aa4000aa039f4a1a513
json_payload:
  message: 'Database: QUERY returned 1 row'
  requestId: f17c46797e7721ce34df0c5a65fdac5d
log_name: projects/anvil-pulse-7451/logs/run.googleapis.com%2Fstdout
receive_timestamp: '2026-09-18T16:45:24.986702005Z'
severity: 200
span_id: ''
timestamp: '2026-09-18T16:45:24.697Z'
---
http_request:
  request_method: GET
  status: 503
json_payload:
  message: upstream unavailable
severity: 500
span_id: 43e3c93c8a43b1cb
timestamp: '2026-09-18T16:45:25.000Z'
```

- [ ] **Step 3: Write the end-to-end test**

Create `internal/headless/gcloud_test.go`:

```go
package headless

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimbeveridge/loghorn/internal/config"
)

// runFixture pipes a committed gcloud sample through the whole pipeline —
// framing, parsing, importance — and returns what the filter printed and every
// record the log-file sink saw.
func runFixture(t *testing.T, name string) (out string, records []string) {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	var buf strings.Builder
	err = Run(f, &buf, config.Default().Input, func(rec []byte) {
		records = append(records, string(rec))
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return buf.String(), records
}

// The JSON array fixture: four objects (the last truncated) plus one
// interleaved plain-text line.
func TestGcloudTailJSONFixture(t *testing.T) {
	out, records := runFixture(t, "gcloud-tail.json")

	if len(records) != 5 {
		t.Fatalf("got %d records, want 5 (4 objects + 1 plain-text line):\n%q", len(records), records)
	}
	if records[1] != "WARNING: gcloud notice interleaved on the same stream" {
		t.Errorf("record 1 = %q, want the plain-text line passed through", records[1])
	}
	if !strings.Contains(records[0], "not-a-brace") {
		t.Errorf("record 0 lost the braced string:\n%s", records[0])
	}

	// A numeric severity of 500 is an error, so those two print; the 200 does
	// not. This is the whole point of the change: importance works on gcloud
	// output.
	if !strings.Contains(out, "Database: QUERY failed") {
		t.Errorf("severity 500 record should be important:\n%s", out)
	}
	if strings.Contains(out, "run.googleapis.com%2Frequests") {
		t.Errorf("severity 200 record should not be important:\n%s", out)
	}
	// http_request.status 503 must count even with no message.
	if !strings.Contains(out, `"status": 503`) {
		t.Errorf("http_request.status 503 should be important:\n%s", out)
	}
}

func TestGcloudReadYAMLFixture(t *testing.T) {
	out, records := runFixture(t, "gcloud-read.yaml")

	if len(records) != 2 {
		t.Fatalf("got %d records, want 2 documents:\n%q", len(records), records)
	}
	for i, r := range records {
		if !strings.HasPrefix(r, "---") {
			t.Errorf("record %d should keep its leading separator: %q", i, r)
		}
	}
	if !strings.Contains(out, "upstream unavailable") {
		t.Errorf("severity 500 document should be important:\n%s", out)
	}
	if strings.Contains(out, "returned 1 row") {
		t.Errorf("severity 200 document should not be important:\n%s", out)
	}
}

// Forcing text framing turns grouping off, which is the escape hatch for a
// producer whose output only looks like one of the framed shapes.
func TestForcedTextFramingDisablesGrouping(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "gcloud-tail.json"))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	var n int
	in := config.Default().Input
	in.Format = config.FormatText
	if err := Run(f, io.Discard, in, func([]byte) { n++ }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n < 30 {
		t.Errorf("forced text framing emitted %d records, want one per line", n)
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/headless/ -run Gcloud -v` then `go test ./internal/headless/ -run Forced -v`
Expected: PASS. If the record counts differ, fix the *fixture* to match the shapes described in steps 1-2 rather than loosening the assertions — the counts are the contract.

- [ ] **Step 5: Smoke-test against the real captures**

These files live outside the repo and may not exist on every machine; skip this step if they're absent.

```bash
go build -o /tmp/loghorn . && /tmp/loghorn --filter < ../auction/x | head -20
/tmp/loghorn --filter < ../auction/y | head -20
```

Expected: whole records, not line fragments, and only error-severity ones. Confirm no fragment like a bare `"latency": "0.003s",` appears on its own.

- [ ] **Step 6: Check formatting and the whole suite**

Run: `gofmt -l . && go test ./...`
Expected: `gofmt -l` prints nothing; every package passes.

- [ ] **Step 7: Commit**

```bash
git add testdata/gcloud-tail.json testdata/gcloud-read.yaml internal/headless/gcloud_test.go
git commit -m "test: cover gcloud's real tail and read shapes end to end

Trimmed captures of gcloud beta logging tail --json and logging read
--format=yaml, carrying the details that broke things: a bare '[', snake_case
fields, numeric severities, a brace inside a string, an interleaved plain-text
line, and a final object cut off the way Ctrl-C leaves it.

The assertions are about importance, not just framing — a numeric severity of
500 has to reach the filter, which is what the change is for."
```

---

## Self-Review

**Spec coverage.** Section 1 (config subsystem) → Task 1, with the `-config` flag and precedence in Task 5. Section 2 (JSON-array framing, detection, the 16MiB cap, truncated tail) → Task 2. Section 3 (field aliases, numeric severity ladder, correlation ids, the `Parser` API shape) → Task 3. Section 4 (log-file round-trip) → Task 4. Section 5 (wiring) → Task 5. Testing → distributed across all six, with the real-shape fixtures in Task 6.

**One deliberate narrowing, recorded in Task 3.** The spec lists eight aliased keys; the plan aliases only the three the adapter reads plus the correlation keys, because `insertId`, `logName`, `receiveTimestamp` and `traceSampled` are never looked up — they reach the detail pane through `Entry.JSON` whatever they are spelled, so aliasing them would be dead code. `snakeCase` is a general converter, so reading another field later needs no table edit.

**Type consistency.** `config.Input` is the value passed to `ingest.Records` (as `Input.Format`), `adapter.New` and `headless.Run`. `Records` takes `config.Format`, not `config.Input`, since framing is all it needs. `CompactRecord` and `Records` both live in `internal/ingest`. `Parser.ParseLine` and the package-level `ParseLine` share one signature.

**Order dependency.** Task 2 changes `Records`'s signature, so it patches `main.go` and `headless.go` with `config.FormatAuto` placeholders to keep the tree compiling; Task 5 replaces those with the resolved setting. A reviewer seeing `config.FormatAuto` hard-coded at the end of Task 2 should expect it.
