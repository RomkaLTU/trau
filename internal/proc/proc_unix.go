//go:build !windows

package proc

import "syscall"

// Detached returns the attributes that start a child in its own process group,
// so a signal delivered to the parent's group never propagates to it.
func Detached() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// KillGroup guarantees pid's end: it SIGKILLs the whole process group so the
// child's own children die with it, falling back to the bare pid when the group
// never existed or the group signal fails for any other reason.
func KillGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}
