package logfile

import (
	"os"
	"path/filepath"
	"testing"
)

func writeGoMod(t *testing.T, dir, module string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "module " + module + "\n\ngo 1.25.0\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Running from anywhere below loghorn's go.mod is refused, and the message names
// the root, not the subdirectory you happened to be in.
func TestSourceTreeFindsLoghornFromNestedDir(t *testing.T) {
	root := t.TempDir()
	writeGoMod(t, root, "github.com/jimbeveridge/loghorn")
	nested := filepath.Join(root, "internal", "tui")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	got, ok := SourceTree(nested)
	if !ok || got != root {
		t.Fatalf("SourceTree(%s) = %q, %v; want %q, true", nested, got, ok, root)
	}
}

// go.mod allows the module path to be quoted.
func TestSourceTreeAcceptsQuotedModulePath(t *testing.T) {
	root := t.TempDir()
	writeGoMod(t, root, `"github.com/jimbeveridge/loghorn"`)
	if _, ok := SourceTree(root); !ok {
		t.Fatalf("quoted module path should still be recognised")
	}
}

// A project's own go.mod is where loghorn is meant to run.
func TestSourceTreeIgnoresOtherModule(t *testing.T) {
	root := t.TempDir()
	writeGoMod(t, root, "example.com/app")
	if got, ok := SourceTree(root); ok {
		t.Fatalf("another module must not be refused, got %q", got)
	}
}

// The nearest go.mod decides: a different module nested inside loghorn's tree
// is not loghorn.
func TestSourceTreeNearestGoModWins(t *testing.T) {
	root := t.TempDir()
	writeGoMod(t, root, "github.com/jimbeveridge/loghorn")
	inner := filepath.Join(root, "examples", "app")
	writeGoMod(t, inner, "example.com/app")
	if _, ok := SourceTree(inner); ok {
		t.Fatalf("the nearest go.mod belongs to another module; must not refuse")
	}
}

func TestSourceTreeWithoutGoMod(t *testing.T) {
	if got, ok := SourceTree(t.TempDir()); ok {
		t.Fatalf("no go.mod anywhere above a temp dir, got %q", got)
	}
}

// The real thing: these tests run inside loghorn's own tree.
func TestSourceTreeRecognisesThisRepo(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := SourceTree(wd); !ok {
		t.Fatalf("the package directory %s is inside loghorn's tree", wd)
	}
}

// A relative dir must not stop at "." after one step up: filepath.Dir(".")
// is "." again, so without resolving to an absolute path first the walk
// would give up immediately instead of reaching the repo root.
func TestSourceTreeAcceptsRelativeDir(t *testing.T) {
	if _, ok := SourceTree("."); !ok {
		t.Fatalf("SourceTree(\".\") from the package directory should find the repo")
	}
}

// A go.mod that exists but can't be read must stop the walk there rather
// than skip it and keep looking upward: the nearest go.mod decides.
func TestSourceTreeUnreadableGoModStopsTheWalk(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read files regardless of mode")
	}
	root := t.TempDir()
	writeGoMod(t, root, "github.com/jimbeveridge/loghorn")
	inner := filepath.Join(root, "examples", "app")
	writeGoMod(t, inner, "example.com/app")
	goMod := filepath.Join(inner, "go.mod")
	if err := os.Chmod(goMod, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(goMod, 0o644) })

	if got, ok := SourceTree(inner); ok {
		t.Fatalf("an unreadable go.mod must stop the walk, not fall through to the loghorn root above it, got %q", got)
	}
}

// A module line may follow leading "//" comment lines and a blank line.
func TestSourceTreeModulePathAfterComments(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "// generated file\n// do not edit\n\nmodule github.com/jimbeveridge/loghorn\n\ngo 1.25.0\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := SourceTree(root); !ok {
		t.Fatalf("a module line after leading comments and a blank line should be recognised")
	}
}

// A go.mod with no module line at all is not loghorn.
func TestSourceTreeGoModWithoutModuleLine(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("go 1.25.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := SourceTree(root); ok {
		t.Fatalf("a go.mod without a module line must not be refused, got %q", got)
	}
}

// A trailing comment on the module line itself must not become part of the
// module path.
func TestSourceTreeModulePathWithTrailingComment(t *testing.T) {
	root := t.TempDir()
	writeGoMod(t, root, "github.com/jimbeveridge/loghorn // comment")
	if _, ok := SourceTree(root); !ok {
		t.Fatalf("a trailing comment on the module line should still be recognised")
	}
}
