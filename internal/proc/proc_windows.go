package proc

import (
	"os"
	"syscall"
)

// Detached returns the attributes that start a child in its own console process
// group, the Windows analogue of Setpgid: a Ctrl-C or Ctrl-Break delivered to
// the parent's group never reaches it.
func Detached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// KillGroup terminates pid. Windows has no process-group signal, so descendants
// the child spawned outlive it; closing that gap needs a job object and belongs
// to the native port's process work, not to this compile seam.
func KillGroup(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
