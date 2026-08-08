package webserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/RomkaLTU/trau/internal/proc"
)

// spawnStderrTailBytes bounds the stderr kept from a spawned child. A startup
// error is a line or two, so a few KB is ample; the cap keeps a chatty child
// from pinning an unbounded buffer for the hub's life.
const spawnStderrTailBytes = 8 << 10

// SpawnSpec describes a headless loop child the hub launches: the working
// directory to run it in and the argument vector and environment it starts with.
// The binary is always the hub's own executable, so a hub-started loop is the
// same trau a human would run.
type SpawnSpec struct {
	Dir  string
	Args []string
	Env  []string
}

// AppSpec describes the app a worktree serves: the shell command to run, the tree
// to run it in, the environment it starts with, and the file its output is
// captured to. Unlike SpawnSpec the binary is not trau's own — it is whatever
// APP_START_CMD names, handed to the host shell.
type AppSpec struct {
	Dir     string
	Command string
	Env     []string
	LogPath string
}

// Supervisor is the hub's process-control seam. It isolates OS process
// management — spawning children, running one to completion, and stopping or
// killing processes — so the control layer never reaches for os/exec or the
// platform's process primitives directly and tests can drive it with a fake that
// records the calls instead of touching real processes.
type Supervisor interface {
	Spawn(SpawnSpec) (pid int, err error)
	SpawnApp(AppSpec) (pid int, err error)
	Capture(context.Context, SpawnSpec) (stdout []byte, err error)
	Stop(pid int) error
	Kill(pid int) error
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
	cmd.SysProcAttr = proc.Detached()
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}

// SpawnApp starts a worktree's app under the host shell, detached in its own
// process group exactly as Spawn is, so stopping it later kills the whole tree of
// processes a dev server spawns rather than only the shell. Its output goes
// straight to a file the child owns for its lifetime — not through a pipe the hub
// holds — because a detached app outlives the hub, and a hub exit must not leave
// it writing into a closed pipe. The file is truncated at every start, so what it
// holds is this run of the app and nothing older.
func (osSupervisor) SpawnApp(spec AppSpec) (int, error) {
	if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0o755); err != nil {
		return 0, err
	}
	log, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	defer func() { _ = log.Close() }()
	cmd := hostShell(context.Background(), spec.Command)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = proc.Detached()
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
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
	out, err := cmd.Output()
	if err != nil {
		return nil, captureError(err)
	}
	return out, nil
}

// captureError decorates a capture failure with the child's first stderr line,
// so a refused or crashed child reports why instead of a bare "exit status 1".
func captureError(err error) error {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return err
	}
	for _, line := range strings.Split(string(ee.Stderr), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return fmt.Errorf("%w: %s", err, line)
		}
	}
	return err
}

// Stop asks pid to shut down gracefully, so a loop checkpoints what it has and
// exits on its own instead of being ended where it stands.
func (osSupervisor) Stop(pid int) error { return proc.StopGracefully(pid) }

// Kill guarantees pid's end: it kills the whole process group so a loop's own
// children die with it.
func (osSupervisor) Kill(pid int) error { return proc.KillGroup(pid) }
