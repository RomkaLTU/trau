//go:build !windows

package proc

import (
	"errors"
	"os"
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
