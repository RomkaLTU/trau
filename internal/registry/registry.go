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

var reposMu sync.Mutex

var workspaceMu sync.Mutex

func instancesDir(home string) string { return filepath.Join(home, "instances") }

func reposFile(home string) string { return filepath.Join(home, "repos.json") }

func workspaceFile(home string) string { return filepath.Join(home, "workspace.json") }

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
// once, without waiting for the next heartbeat. StateSince advances only when
// the state actually changes. Best-effort and safe on a nil or never-registered
// Handle.
func (h *Handle) SetState(state, ticket, phase string) {
	if h == nil || h.path == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.entry.SessionState != state {
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

// RememberRepos folds the repos of the given entries into the persistent
// known-repos set. The hub owns this file, so writes are serialized here rather
// than across loops. Best-effort.
func RememberRepos(home string, entries []Entry) {
	if home == "" || len(entries) == 0 {
		return
	}
	reposMu.Lock()
	defer reposMu.Unlock()

	known := loadRepos(home)
	changed := false
	for _, e := range entries {
		if e.RepoRoot == "" {
			continue
		}
		if _, ok := known[e.RepoRoot]; ok {
			continue
		}
		known[e.RepoRoot] = Repo{
			Name:    filepath.Base(e.RepoRoot),
			Root:    e.RepoRoot,
			RunsDir: e.RunsDir,
		}
		changed = true
	}
	if changed {
		_ = writeJSON(reposFile(home), known)
	}
}

// Repos returns the known repos, sorted by name, that the hub has seen a loop run
// in — including repos whose loop has since exited.
func Repos(home string) []Repo {
	if home == "" {
		return nil
	}
	reposMu.Lock()
	known := loadRepos(home)
	reposMu.Unlock()

	repos := make([]Repo, 0, len(known))
	for _, r := range known {
		repos = append(repos, r)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	return repos
}

func loadRepos(home string) map[string]Repo {
	known := map[string]Repo{}
	_ = readJSONMap(reposFile(home), &known)
	return known
}

// registered is the on-disk shape of workspace.json: the hub-owned set of repo
// roots registered from the web as startable, kept separate from the config
// allowlist so a registration survives a serve restart without a config edit.
type registered struct {
	Repos []string `json:"repos"`
}

// RegisteredRepos returns the repo roots registered as startable from the web,
// in the order they were added. The hub merges these with the static
// SERVE_WORKSPACE seed to form the effective allowlist, read fresh per request so
// a registration takes effect without restarting serve.
func RegisteredRepos(home string) []string {
	if home == "" {
		return nil
	}
	workspaceMu.Lock()
	defer workspaceMu.Unlock()
	return loadWorkspace(home).Repos
}

// RegisterRepo persists root as a startable repo under the trau home, returning
// without error when it is already registered. Unlike loop registration this is a
// deliberate, user-initiated write, so a failure to persist is reported rather
// than swallowed.
func RegisterRepo(home, root string) error {
	if home == "" {
		return errors.New("no trau home to register into")
	}
	workspaceMu.Lock()
	defer workspaceMu.Unlock()
	ws := loadWorkspace(home)
	for _, r := range ws.Repos {
		if r == root {
			return nil
		}
	}
	ws.Repos = append(ws.Repos, root)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	return writeJSON(workspaceFile(home), ws)
}

// UnregisterRepo removes root from the startable set under the trau home,
// reporting whether it was present. It only revokes startability: the repo's run
// artifacts and known-repos history are left untouched, so it lingers exactly as
// any repo does once its loop exits.
func UnregisterRepo(home, root string) (bool, error) {
	if home == "" {
		return false, errors.New("no trau home to unregister from")
	}
	workspaceMu.Lock()
	defer workspaceMu.Unlock()
	ws := loadWorkspace(home)
	kept := make([]string, 0, len(ws.Repos))
	found := false
	for _, r := range ws.Repos {
		if r == root {
			found = true
			continue
		}
		kept = append(kept, r)
	}
	if !found {
		return false, nil
	}
	ws.Repos = kept
	return true, writeJSON(workspaceFile(home), ws)
}

func loadWorkspace(home string) registered {
	var ws registered
	data, err := os.ReadFile(workspaceFile(home))
	if err != nil {
		return registered{}
	}
	_ = json.Unmarshal(data, &ws)
	return ws
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

func readJSONMap(path string, v *map[string]Repo) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, v) == nil
}
