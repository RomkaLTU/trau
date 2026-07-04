package webserver

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

// SpawnSpec describes a headless loop child the hub launches: the working
// directory to run it in and the argument vector and environment it starts with.
// The binary is always the hub's own executable, so a hub-started loop is the
// same trau a human would run.
type SpawnSpec struct {
	Dir  string
	Args []string
	Env  []string
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
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}

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
