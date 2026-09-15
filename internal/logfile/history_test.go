package logfile

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHistoricalFilesOrder(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"loghorn-2026-09-14.log",
		"loghorn-2026-09-12.log",
		"loghorn-2026-09-13.log",
		"loghorn.log",
		"loghorn-junk.log",
		"other.log",
		"loghorn.lock",
	} {
		putFile(t, filepath.Join(dir, name), "x\n", time.Time{})
	}

	got, err := HistoricalFiles(dir)
	if err != nil {
		t.Fatalf("HistoricalFiles: %v", err)
	}
	want := []string{
		filepath.Join(dir, "loghorn-2026-09-12.log"),
		filepath.Join(dir, "loghorn-2026-09-13.log"),
		filepath.Join(dir, "loghorn-2026-09-14.log"),
		filepath.Join(dir, "loghorn.log"),
	}
	if !equalStringSlices(got, want) {
		t.Fatalf("HistoricalFiles = %v, want %v", got, want)
	}
}

func TestHistoricalFilesWithoutCurrent(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"loghorn-2026-09-12.log", "loghorn-2026-09-13.log"} {
		putFile(t, filepath.Join(dir, name), "x\n", time.Time{})
	}

	got, err := HistoricalFiles(dir)
	if err != nil {
		t.Fatalf("HistoricalFiles: %v", err)
	}
	want := []string{
		filepath.Join(dir, "loghorn-2026-09-12.log"),
		filepath.Join(dir, "loghorn-2026-09-13.log"),
	}
	if !equalStringSlices(got, want) {
		t.Fatalf("HistoricalFiles = %v, want %v", got, want)
	}
}

func TestHistoricalFilesEmptyDir(t *testing.T) {
	dir := t.TempDir()
	got, err := HistoricalFiles(dir)
	if err != nil {
		t.Fatalf("HistoricalFiles: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("HistoricalFiles = %v, want empty", got)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
