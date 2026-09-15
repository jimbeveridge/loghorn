package logfile

import (
	"os"
	"path/filepath"
	"strings"
)

// loghornModule is the module path that marks loghorn's own source tree.
const loghornModule = "github.com/jimbeveridge/loghorn"

// SourceTree reports whether dir lies inside loghorn's own source tree, and if
// so the directory holding its go.mod. It walks up to the nearest go.mod; that
// file alone decides, so a project nested in loghorn's tree with its own module
// is not loghorn. dir should be absolute.
//
// loghorn refuses to run from its own tree: `go run .` only works from there, and
// it builds the binary into a temporary directory Go deletes on exit, which would
// take the log files next to it along too. Checking go.mod rather than the git
// remote needs no git, no origin remote, and still fires in a fork.
func SourceTree(dir string) (root string, ok bool) {
	for {
		if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			if modulePath(data) == loghornModule {
				return dir, true
			}
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
