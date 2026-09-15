package logfile

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// HistoricalFiles returns the stored log files in dir, in the order
// -historical reads them: archived days oldest first, then today's
// loghorn.log last if it exists. Names that don't fit either shape —
// loghorn.lock, a stray other.log, a hand-renamed loghorn-junk.log — are
// ignored, since -historical only ever means to read loghorn's own files.
//
// A directory that doesn't exist (no loghorn has ever run there) is not an
// error: it simply has no stored files. Any other os.ReadDir failure, such
// as a permissions problem, is returned unchanged.
func HistoricalFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var archives []string
	haveCurrent := false
	for _, e := range entries {
		name := e.Name()
		if name == currentName {
			haveCurrent = true
			continue
		}
		if e.IsDir() {
			// A directory can't be a stored log no matter what it's named;
			// skip it before parseArchiveDay gets a chance to match its name.
			continue
		}
		if _, ok := parseArchiveDay(name, time.UTC); ok {
			archives = append(archives, name)
		}
	}
	// os.ReadDir already returns entries in name order, which is date order
	// for loghorn-YYYY-MM-DD.log names, so this sort is defensive rather than
	// load-bearing; it's kept in case that ever changes. HistoricalFiles only
	// orders archive dates against each other, never against a clock-derived
	// cutoff, so UTC is enough here — unlike prune, which must parse in the
	// writer's own location (see parseArchiveDay in writer.go).
	sort.Slice(archives, func(i, j int) bool {
		di, _ := parseArchiveDay(archives[i], time.UTC)
		dj, _ := parseArchiveDay(archives[j], time.UTC)
		return di.Before(dj)
	})

	files := make([]string, 0, len(archives)+1)
	for _, name := range archives {
		files = append(files, filepath.Join(dir, name))
	}
	if haveCurrent {
		files = append(files, filepath.Join(dir, currentName))
	}
	return files, nil
}
