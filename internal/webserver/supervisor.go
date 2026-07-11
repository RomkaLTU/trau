package webserver

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// spawnStderrTailBytes bounds the stderr kept from a spawned child. A
// dead-on-arrival startup error is a line or two, so a few KB is ample; the
// cap keeps a chatty child from pinning an unbounded buffer for the hub's life.
const spawnStderrTailBytes = 8 << 10

// SpawnSpec describes a headless loop child the hub launches: the working
// directory to run it in and the argument vector and environment it starts with.
// The binary is always the hub's own executable, so a hub-started loop is the
// same trau a human would run. OnExit, when set, is invoked once from the reaper
// after the child exits, carrying its exit status and a bounded tail of its
// stderr — the seam through which a dead-on-arrival child becomes observable
// without the supervisor knowing anything about repos or events.
type SpawnSpec struct {
	Dir    string
	Args   []string
	Env    []string
	OnExit func(SpawnOutcome)
}

// SpawnOutcome is how a spawned child ended: its pid, its exit code (0 clean,
// -1 when it could not be determined), and a bounded tail of the stderr it
// printed before exiting — the actual error text of a child that died before it
// could register or write a checkpoint.
type SpawnOutcome struct {
	PID      int
	ExitCode int
	Stderr   string
}

// Supervisor is the hub's process-control seam. It isolates OS process
// management — spawning children, running one to completion, and signalling
// processes — so the control layer never reaches for os/exec or syscall
// directly and tests can drive it with a fake that records the calls instead of
// touching real processes.
type Supervisor interface {
	Spawn(SpawnSpec) (pid int, err error)
	Capture(context.Context, SpawnSpec) (stdout []byte, err error)
	Signal(pid int, sig syscall.Signal) error
}

// osSupervisor is the production Supervisor, backed by the real OS.
type osSupervisor struct{}

func newOSSupervisor() osSupervisor { return osSupervisor{} }

// Spawn starts the hub's own binary as a detached child in its own process
// group, so a signal delivered to the hub's group never propagates to a loop it
// started — a hub-started loop outlives the hub and is stopped only on purpose.
// The child is reaped in the background to keep it from lingering as a zombie.
func (osSupervisor) Spawn(spec SpawnSpec) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(exe, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr *tailWriter
	if spec.OnExit != nil {
		stderr = newTailWriter(spawnStderrTailBytes)
		cmd.Stderr = stderr
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	go func() {
		err := cmd.Wait()
		if spec.OnExit != nil {
			spec.OnExit(SpawnOutcome{PID: pid, ExitCode: spawnExitCode(err), Stderr: stderr.String()})
		}
	}()
	return pid, nil
}

// spawnExitCode reads the process exit code from a cmd.Wait error: 0 on a clean
// exit, the real code from an ExitError, and -1 when the failure is something
// other than a non-zero exit (the child was never observed to run).
func spawnExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// tailWriter is an io.Writer that retains only the last max bytes written,
// discarding the rest. It captures a spawned child's stderr tail without letting
// a verbose child grow the buffer without bound. cmd.Wait sequences the final
// read after the copy completes, so no lock is needed.
type tailWriter struct {
	buf []byte
	max int
}

func newTailWriter(max int) *tailWriter { return &tailWriter{max: max} }

func (t *tailWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailWriter) String() string { return string(t.buf) }

// Capture runs the hub's own binary to completion in spec.Dir and returns its
// stdout, which is byte-stable for scripted modes like --dry-run. It is the
// synchronous counterpart to Spawn: used for read-only previews that must hand a
// result back to the caller rather than detach and be watched.
func (osSupervisor) Capture(ctx context.Context, spec SpawnSpec) ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, exe, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	return cmd.Output()
}

func (osSupervisor) Signal(pid int, sig syscall.Signal) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(sig)
}
