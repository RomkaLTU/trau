//go:build !windows

package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// StopName names the mechanism StopGracefully asks with, for the status surfaces
// that report how a loop was told to stop.
const StopName = "SIGTERM"

// Detached returns the attributes that start a child in its own process group,
// so a signal delivered to the parent's group never propagates to it.
func Detached() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// StopGracefully asks pid to shut down rather than ending it: SIGTERM, which the
// loop's own handler treats the same as the Ctrl-C interrupt, so in-flight work
// checkpoints before the process exits.
func StopGracefully(pid int) error {
	target, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return target.Signal(syscall.SIGTERM)
}

// Alive reports whether pid names a running process, probing with signal 0 and
// treating a permission-denied answer as alive (the process exists, we just may
// not own it).
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// KillGroup guarantees pid's end: it SIGKILLs the whole process group so the
// child's own children die with it, falling back to the bare pid when the group
// never existed or the group signal fails for any other reason.
func KillGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

// PortListeners reports the pids listening on port. Nothing in the standard
// library maps a port to a process, so this shells out to lsof, whose non-zero
// exit means "no match" rather than a failure worth reporting (ADR 0023 §7).
func PortListeners(ctx context.Context, port int) ([]int, error) {
	out, err := exec.CommandContext(ctx, "lsof", "-t", "-i", fmt.Sprintf("tcp:%d", port), "-sTCP:LISTEN").Output()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errors.New("lsof is not installed, so the process holding the port cannot be identified")
	}
	pids := make([]int, 0, 1)
	for _, field := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

// PortInspectHint names the command that shows what holds port, so a caller can
// suggest it without branching on the OS itself (ADR 0023 §7).
func PortInspectHint(port int) string {
	return fmt.Sprintf("`lsof -i tcp:%d`", port)
}

// LookBin returns bin unchanged. Every unix spawn seam ends in os/exec, which
// resolves a bare name against $PATH when the child starts, and resolving here
// instead would change the argv[0] the child sees — ADR 0023 keeps unix spawn
// behavior byte-identical.
func LookBin(bin string) (string, error) { return bin, nil }
