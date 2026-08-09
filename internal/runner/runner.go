// Package runner launches the log producer as a child process so clog owns its
// lifecycle.
//
// The alternative — being a pipeline peer, as in `npm run dev | clog` — cannot
// work properly for an interactive producer. Only stdout is piped there; both
// processes still hold /dev/tty open and both read it, so the kernel splits
// keystrokes between them at random. The producer ends up executing whatever
// clog's keys and mouse escape sequences happen to mean to it, and since node
// ignores SIGPIPE it outlives clog with no way to be signalled.
package runner

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Runner is a launched producer and the pipes clog talks to it through.
type Runner struct {
	cmd    *exec.Cmd
	ptmx   *os.File      // master side of the child's stdin pty
	output *os.File      // read end of the merged stdout+stderr pipe
	exited chan struct{} // closed once the child has been reaped
	code   int
	name   string
}

// Start launches argv with stdout and stderr merged into one pipe and stdin on a
// pty, in its own process group.
//
// The pty matters: a plain pipe would serve the lifecycle just as well, but
// vite, next and nodemon all gate their keypress handling on
// process.stdin.isTTY, so Send would silently do nothing against exactly the
// tools it exists for.
//
// The new process group is what makes shutdown reliable — signalling -pgid
// reaches everything the producer spawned, not just the producer. Session is
// deliberately left alone: the child stays out of the terminal's foreground
// group, so typed input reaches only clog.
func Start(argv []string) (*Runner, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("no command given")
	}

	ptmx, pts, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("allocating a pty for %s: %w", argv[0], err)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		ptmx.Close()
		pts.Close()
		return nil, fmt.Errorf("creating the output pipe: %w", err)
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = pts
	// One pipe for both streams, so a producer's stderr can't bypass clog and
	// paint over the TUI — the same thing `2>&1 |` does.
	cmd.Stdout = pw
	cmd.Stderr = pw
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		ptmx.Close()
		pts.Close()
		pr.Close()
		pw.Close()
		return nil, fmt.Errorf("starting %s: %w", argv[0], err)
	}
	// The child holds its own copies now; clog must drop these ends or the
	// output pipe never reports EOF.
	pts.Close()
	pw.Close()

	r := &Runner{cmd: cmd, ptmx: ptmx, output: pr, exited: make(chan struct{}), name: argv[0]}
	go func() {
		err := cmd.Wait()
		if ee, ok := err.(*exec.ExitError); ok {
			r.code = ee.ExitCode()
		} else if err != nil {
			r.code = -1
		}
		close(r.exited)
	}()
	return r, nil
}

// Output is the child's merged stdout and stderr.
func (r *Runner) Output() io.Reader { return r.output }

// Name is the command as invoked, for display.
func (r *Runner) Name() string { return r.name }

// Exited is closed once the child has been reaped.
func (r *Runner) Exited() <-chan struct{} { return r.exited }

// ExitCode is only meaningful after Exited is closed.
func (r *Runner) ExitCode() int { return r.code }

// Send writes keystrokes to the child's stdin.
func (r *Runner) Send(p []byte) error {
	_, err := r.ptmx.Write(p)
	return err
}

// Terminate signals the child's whole process group and waits for it to go,
// escalating to SIGKILL if it outlasts grace. Signalling the group rather than
// the process is the point: `npm run dev` is a shell wrapper around the real
// dev server, and SIGTERM to npm alone orphans the server underneath it.
//
// It returns once the child is reaped or the escalation has been sent.
func (r *Runner) Terminate(grace time.Duration) {
	select {
	case <-r.exited:
		return // already gone
	default:
	}

	pgid := r.cmd.Process.Pid // == the pgid, because of Setpgid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)

	select {
	case <-r.exited:
	case <-time.After(grace):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		select {
		case <-r.exited:
		case <-time.After(time.Second):
		}
	}
}

// Close releases the pty and pipe. It does not signal the child, so a detached
// quit ('Q') leaves it running.
func (r *Runner) Close() {
	r.ptmx.Close()
	r.output.Close()
}
