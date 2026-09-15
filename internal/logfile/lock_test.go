package logfile

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAcquireWritesPID(t *testing.T) {
	dir := t.TempDir()
	f, err := acquire(dir)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer f.Close()

	b, err := os.ReadFile(filepath.Join(dir, lockName))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(b)), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("lock file holds %q, want pid %q", got, want)
	}
}

// A longer PID left by an earlier owner must not survive as trailing digits.
func TestAcquireReplacesPreviousPID(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, lockName), []byte("99999999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := acquire(dir)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer f.Close()

	b, err := os.ReadFile(filepath.Join(dir, lockName))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(b)), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("lock file holds %q, want pid %q", got, want)
	}
}

func TestAcquireReportsHolder(t *testing.T) {
	dir := t.TempDir()
	f, err := acquire(dir)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer f.Close()

	_, err = acquire(dir)
	var locked *LockedError
	if !errors.As(err, &locked) {
		t.Fatalf("second acquire: want *LockedError, got %v", err)
	}
	if locked.PID != os.Getpid() || locked.Dir != dir {
		t.Fatalf("got %+v, want PID %d in %s", locked, os.Getpid(), dir)
	}
}

// The kernel drops the lock with the descriptor; there is never a stale lock.
func TestAcquireAfterReleaseSucceeds(t *testing.T) {
	dir := t.TempDir()
	f, err := acquire(dir)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	f.Close()

	g, err := acquire(dir)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	g.Close()
}

// The holder may not have written its PID yet; the message drops it rather
// than failing.
func TestAcquireUnreadablePIDIsZero(t *testing.T) {
	dir := t.TempDir()
	f, err := acquire(dir)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer f.Close()
	if err := os.WriteFile(filepath.Join(dir, lockName), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = acquire(dir)
	var locked *LockedError
	if !errors.As(err, &locked) || locked.PID != 0 {
		t.Fatalf("want *LockedError with PID 0, got %v", err)
	}
}

// A directory that doesn't exist fails the open itself; that must never be
// mistaken for another loghorn holding the lock.
func TestAcquireOnMissingDirIsNotLockedError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	_, err := acquire(dir)
	if err == nil {
		t.Fatalf("acquire on a missing directory should fail")
	}
	var locked *LockedError
	if errors.As(err, &locked) {
		t.Fatalf("an open failure must not read as *LockedError, got %v", err)
	}
}

func TestLockedErrorText(t *testing.T) {
	for _, tc := range []struct {
		err         LockedError
		msg, holder string
	}{
		{LockedError{Dir: "/d", PID: 48213}, "another loghorn (pid 48213) is writing logs in /d", "pid 48213"},
		{LockedError{Dir: "/d"}, "another loghorn is writing logs in /d", "another loghorn"},
	} {
		if got := tc.err.Error(); got != tc.msg {
			t.Errorf("Error() = %q, want %q", got, tc.msg)
		}
		if got := tc.err.Holder(); got != tc.holder {
			t.Errorf("Holder() = %q, want %q", got, tc.holder)
		}
	}
}
