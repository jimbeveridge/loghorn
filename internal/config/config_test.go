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

// The surprising direction, pinned because it reads backwards from the XDG
// convention: for a working directory under $HOME, the upward walk reaches
// ~/.config/loghorn/config.toml before the user-level stop is ever consulted,
// so XDG_CONFIG_HOME does not redirect config for anyone working inside their
// own home tree.
func TestHomeWalkBeatsXDGForACwdUnderHome(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "[input]\nformat = \"yaml\"\n")

	xdg := filepath.Join(t.TempDir(), "xdg")
	p := filepath.Join(xdg, "loghorn", "config.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("[input]\nformat = \"json\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, path, err := Loader{Dir: work, XDG: xdg, Home: home}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(home, RelPath); path != want {
		t.Errorf("path = %q, want %q: the walk reaches ~/.config before the XDG stop", path, want)
	}
	if cfg.Input.Format != FormatYAML {
		t.Errorf("Format = %q, want yaml from the home file", cfg.Input.Format)
	}
}

// [display] unquote-identifiers is off unless a file turns it on: it rewrites
// the SQL the detail pane shows and yank copies, so it is opt-in.
func TestDisplayUnquoteIdentifiers(t *testing.T) {
	if Default().Display.UnquoteIdentifiers {
		t.Error("unquote-identifiers should default to false")
	}
	root := t.TempDir()
	writeConfig(t, root, "[display]\nunquote-identifiers = true\n")
	cfg, _, err := loaderAt(root, filepath.Join(root, "nohome")).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Display.UnquoteIdentifiers {
		t.Error("unquote-identifiers = true should be read from the file")
	}
	// The section is independent of [input], which keeps its defaults.
	if cfg.Input.Format != FormatAuto {
		t.Errorf("Input.Format = %q, want auto", cfg.Input.Format)
	}
	if got := cfg.String(); !strings.Contains(got, "[display]") || !strings.Contains(got, "unquote-identifiers = true") {
		t.Errorf("String() should render the display section:\n%s", got)
	}
}

// keyword-case is an enum, and an unknown value stops the load rather than
// being ignored: sql-formatter answers an unknown keywordCase by deleting every
// keyword from the statement, so a typo must never get past the config file.
func TestDisplayKeywordCase(t *testing.T) {
	if got := Default().Display.KeywordCase; got != KeywordPreserve {
		t.Errorf("KeywordCase default = %q, want preserve", got)
	}
	root := t.TempDir()
	writeConfig(t, root, "[display]\nkeyword-case = \"upper\"\n")
	cfg, _, err := loaderAt(root, filepath.Join(root, "nohome")).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Display.KeywordCase != KeywordUpper {
		t.Errorf("KeywordCase = %q, want upper", cfg.Display.KeywordCase)
	}

	bad := t.TempDir()
	writeConfig(t, bad, "[display]\nkeyword-case = \"shouty\"\n")
	if _, _, err := loaderAt(bad, filepath.Join(bad, "nohome")).Load(); err == nil {
		t.Fatal("an unknown keyword-case should be rejected")
	} else if !strings.Contains(err.Error(), "display.keyword-case") {
		t.Errorf("error should name the key: %v", err)
	}
}

// format-sql is on by default, so the pane lays statements out without a config
// file — the behaviour loghorn had before the key existed. It is the one display
// key whose default is true, which is why Default() must be the base every
// Display value is built from rather than a zero struct.
func TestDisplayFormatSQL(t *testing.T) {
	if !Default().Display.FormatSQL {
		t.Error("format-sql should default to true")
	}
	root := t.TempDir()
	writeConfig(t, root, "[display]\nformat-sql = false\n")
	cfg, _, err := loaderAt(root, filepath.Join(root, "nohome")).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Display.FormatSQL {
		t.Error("format-sql = false should be read from the file")
	}
	// An unrelated display key leaves it at its default.
	other := t.TempDir()
	writeConfig(t, other, "[display]\nunquote-identifiers = true\n")
	cfg, _, err = loaderAt(other, filepath.Join(other, "nohome")).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Display.FormatSQL {
		t.Error("format-sql should stay true when the file sets only another key")
	}
	if got := cfg.String(); !strings.Contains(got, "format-sql") {
		t.Errorf("String() should render format-sql:\n%s", got)
	}
}
