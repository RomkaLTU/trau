// Package registry is the machine-local instance registry: every trau loop
// best-effort records itself under the user's trau home on start and clears the
// record on clean exit, so `trau serve` can list the loops running on this
// machine. Registration must never block or fail a loop — an unwritable registry
// is silently ignored — so every write here is best-effort by design.
package registry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const heartbeatInterval = 30 * time.Second

// Session states an instance reports through its heartbeated entry. The entry
// records only that a session reached a state, never why: the why (failure
// class, reason) stays on the ticket's checkpoint.
const (
	StateIdle     = "idle"
	StateGrazing  = "grazing"
	StateWorking  = "working"
	StateParked   = "parked"
	StateStopping = "stopping"
)

// Home returns the trau home directory that roots the registry: $TRAU_HOME when
// set, else ~/.trau. An empty string means neither is resolvable, in which case
// registration is skipped entirely.
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

// Entry is one loop's record: enough to identify the process, find its run
// artifacts, and probe whether it is still alive.
type Entry struct {
	PID          int       `json:"pid"`
	RepoRoot     string    `json:"repo_root"`
	RunsDir      string    `json:"runs_dir"`
	StartedAt    time.Time `json:"started_at"`
	Heartbeat    time.Time `json:"heartbeat"`
	SessionState string    `json:"session_state,omitempty"`
	Ticket       string    `json:"ticket,omitempty"`
	Phase        string    `json:"phase,omitempty"`
	StateSince   time.Time `json:"state_since,omitzero"`
}

// Repo is a repository the hub has seen a loop run in. It outlives the loop so a
// repo's runs stay browsable after the loop exits.
type Repo struct {
	Name    string `json:"name"`
	Root    string `json:"root"`
	RunsDir string `json:"runs_dir"`
}

// Handle is a live registration. Deregister is safe to call on a nil or
// never-registered Handle, so callers can register unconditionally and defer the
// cleanup without checking whether the write actually landed.
type Handle struct {
	path  string
	mu    sync.Mutex
	entry Entry
	stop  chan struct{}
	once  sync.Once
}

func instancesDir(home string) string { return filepath.Join(home, "instances") }

// Register records the calling process as a live loop under home and heartbeats
// the entry until Deregister. It is best-effort: any failure yields a no-op
// Handle and the loop carries on. A relative runsDir is resolved against
// repoRoot so the hub, running elsewhere, can find it.
func Register(home, repoRoot, runsDir string) *Handle {
	h := &Handle{}
	if home == "" {
		return h
	}
	if runsDir != "" && !filepath.IsAbs(runsDir) && repoRoot != "" {
		runsDir = filepath.Join(repoRoot, runsDir)
	}
	dir := instancesDir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return h
	}
	now := time.Now()
	h.entry = Entry{
		PID:          os.Getpid(),
		RepoRoot:     repoRoot,
		RunsDir:      runsDir,
		StartedAt:    now,
		Heartbeat:    now,
		SessionState: StateIdle,
		StateSince:   now,
	}
	h.path = filepath.Join(dir, entryName(h.entry.PID))
	if writeJSON(h.path, h.entry) != nil {
		h.path = ""
		return h
	}
	h.stop = make(chan struct{})
	go h.beat()
	return h
}

func (h *Handle) beat() {
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
			h.mu.Lock()
			h.entry.Heartbeat = time.Now()
			_ = writeJSON(h.path, h.entry)
			h.mu.Unlock()
		}
	}
}

// SetState records what the session is doing and rewrites the entry file at
// once, without waiting for the next heartbeat. StateSince advances whenever the
// reported activity changes — the state, ticket, or phase — so it reads as "in
// this phase since", the timestamp the hub shows instead of a file mtime.
// Best-effort and safe on a nil or never-registered Handle.
func (h *Handle) SetState(state, ticket, phase string) {
	if h == nil || h.path == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.entry.SessionState != state || h.entry.Ticket != ticket || h.entry.Phase != phase {
		h.entry.StateSince = time.Now()
	}
	h.entry.SessionState = state
	h.entry.Ticket = ticket
	h.entry.Phase = phase
	_ = writeJSON(h.path, h.entry)
}

// Deregister stops the heartbeat and removes the entry. Best-effort and
// idempotent.
func (h *Handle) Deregister() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		if h.stop != nil {
			close(h.stop)
		}
		if h.path != "" {
			_ = os.Remove(h.path)
		}
	})
}

// Live returns the entries whose process is still alive, reaping the files of any
// that are not. It is the hub's read side of the registry.
func Live(home string) []Entry {
	if home == "" {
		return nil
	}
	dir := instancesDir(home)
	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil
	}
	live := make([]Entry, 0, len(names))
	for _, name := range names {
		var e Entry
		if !readJSON(name, &e) {
			continue
		}
		if alive(e.PID) {
			live = append(live, e)
			continue
		}
		_ = os.Remove(name)
	}
	sort.Slice(live, func(i, j int) bool {
		return live[i].StartedAt.Before(live[j].StartedAt)
	})
	return live
}

func entryName(pid int) string {
	return strconv.Itoa(pid) + ".json"
}

// Alive reports whether pid names a running process. The hub uses it to tell
// whether a child it recorded against a queued item is still running.
func Alive(pid int) bool { return alive(pid) }

// alive reports whether pid names a running process, treating a
// permission-denied probe as alive (the process exists, we just may not own it).
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

func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

func readJSON(path string, v *Entry) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, v) == nil
}
