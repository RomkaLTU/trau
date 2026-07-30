package proc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code GetExitCodeProcess reports for a process that has
// not exited yet.
const stillActive = 259

// StopName names the mechanism StopGracefully asks with, for the status surfaces
// that report how a loop was told to stop.
const StopName = "CTRL_BREAK"

// Detached returns the attributes that start a child in its own console process
// group, the Windows analogue of Setpgid: a Ctrl-C or Ctrl-Break delivered to
// the parent's group never reaches it.
func Detached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// StopGracefully asks pid to shut down rather than ending it. Windows has no
// SIGTERM, so the request is a console Ctrl-Break to the group Detached made pid
// the leader of: the Go runtime turns CTRL_BREAK_EVENT into os.Interrupt, so the
// loop's existing handler checkpoints exactly as it does on a Unix SIGTERM.
// Delivery needs a console shared with pid, which a hub started as a detached
// service does not have; the caller escalates to KillGroup on the error, the way
// it escalates when a signalled loop refuses to exit (ADR 0023 §7).
func StopGracefully(pid int) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid))
}

// Alive reports whether pid names a running process. os.FindProcess never fails
// on Windows and there is no signal-0 probe, so liveness is a real handle open
// plus an exit-code read; a denied open means the process exists but is not ours,
// which is alive — the Unix EPERM case.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// KillGroup guarantees pid's end together with everything it spawned. Windows
// has no process-group kill, so the tree walk is taskkill's: /T ends the
// descendants and /F skips the graceful request StopGracefully already made.
// TerminateProcess is the fallback, so a machine that cannot run taskkill still
// ends the process itself, though its children then outlive it.
func KillGroup(pid int) error {
	if err := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run(); err == nil {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// LookBin resolves bin the way a shell would — %PATH% with PATHEXT — and
// returns it absolute. The ConPTY spawn behind the terminalStarter seam is a
// raw CreateProcess that never consults %PATH%: a bare name with a working
// directory set is probed against that directory alone, so the name must
// arrive already resolved. A match in the process's own cwd (exec.ErrDot) is
// accepted and, like every other match, made absolute so no later join can
// reinterpret it against a different directory.
func LookBin(bin string) (string, error) {
	path, err := exec.LookPath(bin)
	if err != nil && !errors.Is(err, exec.ErrDot) {
		return "", err
	}
	return filepath.Abs(path)
}
