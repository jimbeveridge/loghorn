package logfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// loghornModule is the module path that marks loghorn's own source tree.
const loghornModule = "github.com/jimbeveridge/loghorn"

// SourceTree reports whether dir lies inside loghorn's own source tree, and if
// so the directory holding its go.mod. It walks up to the nearest go.mod; that
// file alone decides, so a project nested in loghorn's tree with its own module
// is not loghorn. dir need not be absolute: it is resolved against the working
// directory first, since filepath.Dir(".") == "." would otherwise stop the walk
// after a single step for a relative dir.
//
// loghorn refuses to run from its own tree: loghorn is meant to be run from the
// project it watches, and loghorn's own source tree is never that project.
// Checking go.mod rather than the git remote needs no git, no origin remote,
// and still fires in a fork.
func SourceTree(dir string) (root string, ok bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		switch {
		case err == nil:
			if modulePath(data) == loghornModule {
				return dir, true
			}
			return "", false
		case !errors.Is(err, fs.ErrNotExist):
			// The nearest go.mod decides; one that exists but can't be read
			// (permissions, say) must not be skipped in favour of a more
			// distant one that might say otherwise.
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// modulePath returns the path on go.mod's module line, unquoted.
func modulePath(gomod []byte) string {
	for _, line := range strings.Split(string(gomod), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`)
		}
	}
	return ""
}
