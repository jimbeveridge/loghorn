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
