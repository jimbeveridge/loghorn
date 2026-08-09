package runner

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func waitExit(t *testing.T, r *Runner, d time.Duration) {
	t.Helper()
	select {
	case <-r.Exited():
	case <-time.After(d):
		t.Fatalf("child did not exit within %s", d)
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// Both streams have to reach loghorn through the one pipe, so a producer's stderr
// can't bypass it and paint over the TUI.
func TestMergesStdoutAndStderr(t *testing.T) {
	r, err := Start([]string{"sh", "-c", "echo to-stdout; echo to-stderr >&2"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var got []string
	sc := bufio.NewScanner(r.Output())
	for sc.Scan() {
		got = append(got, sc.Text())
	}
	joined := strings.Join(got, "|")
	if !strings.Contains(joined, "to-stdout") || !strings.Contains(joined, "to-stderr") {
		t.Fatalf("both streams should arrive, got %q", joined)
	}
}

// The child's stdin must look like a terminal, or vite/next/nodemon never enable
// the keypress handling that Send exists to drive.
func TestChildStdinIsATTY(t *testing.T) {
	r, err := Start([]string{"sh", "-c", "test -t 0 && echo tty || echo not-tty"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	out, _ := bufio.NewReader(r.Output()).ReadString('\n')
	if strings.TrimSpace(out) != "tty" {
		t.Fatalf("child stdin should be a tty, got %q", out)
	}
}

// Send reaches the child.
func TestSendReachesChild(t *testing.T) {
	r, err := Start([]string{"sh", "-c", "read line; echo \"got:$line\""})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if err := r.Send([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	out, _ := bufio.NewReader(r.Output()).ReadString('\n')
	if !strings.Contains(out, "got:hello") {
		t.Fatalf("child should have received the keystrokes, got %q", out)
	}
}

// The whole process group goes, not just the process loghorn spawned: `npm run dev`
// is a wrapper, and signalling only the wrapper orphans the dev server beneath.
func TestTerminateKillsTheWholeGroup(t *testing.T) {
	// A shell that spawns a grandchild and reports both pids.
	r, err := Start([]string{"sh", "-c", `sleep 60 & echo "$$ $!"; wait`})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	line, _ := bufio.NewReader(r.Output()).ReadString('\n')
	parts := strings.Fields(strings.TrimSpace(line))
	if len(parts) != 2 {
		t.Fatalf("expected two pids, got %q", line)
	}
	child, _ := strconv.Atoi(parts[0])
	grandchild, _ := strconv.Atoi(parts[1])
	if !alive(grandchild) {
		t.Fatalf("precondition: grandchild %d should be running", grandchild)
	}

	r.Terminate(2 * time.Second)
	waitExit(t, r, 2*time.Second)

	// The grandchild is reparented on death, so give the kernel a moment.
	for i := 0; i < 50 && alive(grandchild); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(grandchild) {
		t.Fatalf("grandchild %d survived termination of the group (child %d)", grandchild, child)
	}
}

// A producer that ignores SIGTERM — which is exactly the failure being fixed —
// gets killed once the grace period is up.
func TestTerminateEscalatesToKill(t *testing.T) {
	r, err := Start([]string{"sh", "-c", `trap '' TERM; echo ready; while :; do sleep 0.1; done`})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	out, _ := bufio.NewReader(r.Output()).ReadString('\n')
	if strings.TrimSpace(out) != "ready" {
		t.Fatalf("child never started, got %q", out)
	}

	start := time.Now()
	r.Terminate(300 * time.Millisecond)
	waitExit(t, r, 3*time.Second)
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("escalated before the grace period elapsed (%s)", elapsed)
	}
}

// Terminate on an already-dead child is a no-op, not a signal to a recycled pid.
func TestTerminateAfterExitIsSafe(t *testing.T) {
	r, err := Start([]string{"sh", "-c", "exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	waitExit(t, r, 2*time.Second)
	if got := r.ExitCode(); got != 3 {
		t.Fatalf("exit code should be 3, got %d", got)
	}
	r.Terminate(time.Second) // must not hang or panic
}

// Close releases loghorn's ends without signalling the child — that is what makes a
// detached quit ('Q') leave it running.
func TestCloseDoesNotSignalChild(t *testing.T) {
	r, err := Start([]string{"sh", "-c", "echo ready; sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := bufio.NewReader(r.Output()).ReadString('\n')
	if strings.TrimSpace(out) != "ready" {
		t.Fatalf("child never started, got %q", out)
	}
	pid := r.cmd.Process.Pid

	r.Close()
	time.Sleep(200 * time.Millisecond)
	if !alive(pid) {
		t.Fatalf("Close must not kill the child — 'Q' relies on it surviving")
	}
	r.Terminate(2 * time.Second) // clean up
}

// The output pipe reports EOF once the child is gone, so ingest can finish.
func TestOutputEndsAtChildExit(t *testing.T) {
	r, err := Start([]string{"sh", "-c", "echo one; echo two"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	done := make(chan int, 1)
	go func() {
		n := 0
		sc := bufio.NewScanner(r.Output())
		for sc.Scan() {
			n++
		}
		done <- n
	}()
	select {
	case n := <-done:
		if n != 2 {
			t.Fatalf("expected 2 lines before EOF, got %d", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("output never reached EOF — a write end is still open in loghorn")
	}
}

func TestStartRejectsEmptyCommand(t *testing.T) {
	if _, err := Start(nil); err == nil {
		t.Fatalf("expected an error for an empty command")
	}
}

func TestStartReportsMissingBinary(t *testing.T) {
	if _, err := Start([]string{"loghorn-no-such-binary-xyz"}); err == nil {
		t.Fatalf("expected an error for a missing binary")
	} else if !strings.Contains(err.Error(), "loghorn-no-such-binary-xyz") {
		t.Fatalf("error should name the command, got %v", err)
	}
}

func TestMain(m *testing.M) { os.Exit(m.Run()) }
