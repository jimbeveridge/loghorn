package logfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const lockName = "loghorn.lock"

// LockedError reports that another loghorn already holds the log directory.
// PID is the holder's, read from the lock file for the message only; it is 0
// when the holder hasn't written it yet.
type LockedError struct {
	Dir string
	PID int
}

func (e *LockedError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("another loghorn (pid %d) is writing logs in %s", e.PID, e.Dir)
	}
	return "another loghorn is writing logs in " + e.Dir
}

// Holder names the process holding the lock, for a short message.
func (e *LockedError) Holder() string {
	if e.PID > 0 {
		return fmt.Sprintf("pid %d", e.PID)
	}
	return "another loghorn"
}

// acquire takes an exclusive flock on dir's lock file and writes this process's
// PID into it. The returned file holds the lock until it is closed.
//
// Without the lock, two loghorns would both roll over at midnight and one would
// end up writing to an unlinked file, silently losing a day. flock, not the PID,
// decides who holds it: the kernel drops the lock when the process exits or
// crashes, so it is never stale, whereas a PID can be reused by an unrelated
// process. The lock file is never deleted, since that would race the next locker.
func acquire(dir string) (*os.File, error) {
	// 0o600: the lock file only ever holds a PID, but owner-only keeps every
	// file this package creates to the same, easily-audited permission.
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		defer f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &LockedError{Dir: dir, PID: readPID(f)}
		}
		return nil, fmt.Errorf("locking %s: %w", f.Name(), err)
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// readPID reads the holder's PID from a just-opened lock file, or 0.
func readPID(f *os.File) int {
	b, err := io.ReadAll(f)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
