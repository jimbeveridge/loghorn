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
		if _, ok := archiveDate(name); ok {
			archives = append(archives, name)
		}
	}
	sort.Slice(archives, func(i, j int) bool {
		di, _ := archiveDate(archives[i])
		dj, _ := archiveDate(archives[j])
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

// archiveDate parses the date out of an archive's name with no location.
// Unlike (*Writer).archiveDay, HistoricalFiles never compares the result
// against a clock-derived cutoff — it only orders archive dates against each
// other — so a fixed epoch is enough to sort them correctly.
func archiveDate(name string) (time.Time, bool) {
	s, ok := archiveName(name)
	if !ok {
		return time.Time{}, false
	}
	day, err := time.Parse(dateLayout, s)
	if err != nil {
		return time.Time{}, false
	}
	return day, true
}
