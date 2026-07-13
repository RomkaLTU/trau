// Package registry holds the shared vocabulary of instance presence: the session
// states a loop reports, the Entry the hub records for each live loop, the Repo a
// loop runs in, and the pid-liveness probe the hub reaps presence with. Presence
// itself moved to a hub heartbeat API (ADR 0008 §7); this package no longer keeps
// per-PID files on disk. Home still resolves the trau home the hub roots its
// database under.
package registry

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Session states an instance reports through its heartbeat. The entry records
// only that a session reached a state, never why: the why (failure class, reason)
// stays on the ticket's checkpoint.
const (
	StateIdle     = "idle"
	StateGrazing  = "grazing"
	StateWorking  = "working"
	StateParked   = "parked"
	StateStopping = "stopping"
)

// Home returns the trau home directory that roots the hub's database: $TRAU_HOME
// when set, else ~/.trau. An empty string means neither is resolvable.
func Home() string {
	if h := os.Getenv("TRAU_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".trau")
}

// Entry is one loop's presence: enough to identify the process, find its run
// artifacts, and probe whether it is still alive. The hub holds it keyed by PID
// (ADR 0008 §7) and echoes the reported session state verbatim.
type Entry struct {
	PID          int       `json:"pid"`
	RepoRoot     string    `json:"repo_root"`
	RunsDir      string    `json:"runs_dir"`
	StartedAt    time.Time `json:"started_at"`
	Heartbeat    time.Time `json:"heartbeat"`
	SessionState string    `json:"session_state,omitempty"`
	Ticket       string    `json:"ticket,omitempty"`
	Phase        string    `json:"phase,omitempty"`
	Activity     string    `json:"activity,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	StateSince   time.Time `json:"state_since,omitzero"`
}

// Repo is a repository the hub has seen a loop run in. It outlives the loop so a
// repo's runs stay browsable after the loop exits.
type Repo struct {
	Name    string `json:"name"`
	Root    string `json:"root"`
	RunsDir string `json:"runs_dir"`
}

// Alive reports whether pid names a running process. The hub reaps presence with
// it: a loop whose process is gone ages out, while a suspended-but-alive process
// keeps its entry — liveness is pid-only, never heartbeat staleness (ADR 0005,
// ADR 0008 §7).
func Alive(pid int) bool { return alive(pid) }

// alive reports whether pid names a running process, treating a permission-denied
// probe as alive (the process exists, we just may not own it).
func alive(pid int) bool {
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
