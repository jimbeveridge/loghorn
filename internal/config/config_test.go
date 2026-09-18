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
