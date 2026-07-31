package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// PortListeners reports the pids listening on port. Windows maps a port to a
// process no more portably than unix does, so this reads netstat's connection
// table (ADR 0023 §7), unfiltered: `-p tcp` lists IPv4 only, so a hub bound to
// ::1 would read as nothing holding the port at all. A listening row is
// recognised by its shape rather than by the LISTENING label, which is
// translated on a localized Windows: a local address ending in :port and an
// unspecified foreign address, which is also what leaves the UDP rows (`*:*`) an
// unfiltered table carries out. One process listening on both families has a row
// per family, so each pid is reported once. A non-zero exit means "no match", as
// it does for lsof.
func PortListeners(ctx context.Context, port int) ([]int, error) {
	out, err := exec.CommandContext(ctx, "netstat", "-ano").Output()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errors.New("netstat is not installed, so the process holding the port cannot be identified")
	}
	local := ":" + strconv.Itoa(port)
	pids := make([]int, 0, 1)
	seen := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.HasSuffix(fields[1], local) {
			continue
		}
		if fields[2] != "0.0.0.0:0" && fields[2] != "[::]:0" {
			continue
		}
		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil || pid == 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		pids = append(pids, pid)
	}
	return pids, nil
}

// PortInspectHint names the command that shows what holds port, so a caller can
// suggest it without branching on the OS itself (ADR 0023 §7).
func PortInspectHint(port int) string {
	return fmt.Sprintf("`netstat -ano | findstr :%d`", port)
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
