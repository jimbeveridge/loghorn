package logfile

import (
	"os"
	"path/filepath"
	"slices"
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
	if !slices.Equal(got, want) {
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
	if !slices.Equal(got, want) {
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

// A directory named like an archive is not a stored log, however it's named.
func TestHistoricalFilesSkipsDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "loghorn-2026-09-13.log"), 0o755); err != nil {
		t.Fatal(err)
	}
	putFile(t, filepath.Join(dir, "loghorn-2026-09-14.log"), "x\n", time.Time{})

	got, err := HistoricalFiles(dir)
	if err != nil {
		t.Fatalf("HistoricalFiles: %v", err)
	}
	want := []string{filepath.Join(dir, "loghorn-2026-09-14.log")}
	if !slices.Equal(got, want) {
		t.Fatalf("HistoricalFiles = %v, want %v", got, want)
	}
}

// No loghorn has ever run there; that's not an error.
func TestHistoricalFilesMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	got, err := HistoricalFiles(dir)
	if err != nil {
		t.Fatalf("HistoricalFiles: %v", err)
	}
	if got != nil {
		t.Fatalf("HistoricalFiles = %v, want nil", got)
	}
}
