package webserver

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/RomkaLTU/trau/internal/folderrepo"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// Instance is a live loop as the hub sees it: the registry record plus the
// session state the loop reports through its heartbeat. The hub echoes that
// state verbatim and never derives activity from run artifacts.
type Instance struct {
	PID          int    `json:"pid"`
	Repo         string `json:"repo"`
	RepoRoot     string `json:"repo_root"`
	RunsDir      string `json:"runs_dir"`
	StartedAt    string `json:"started_at"`
	SessionState string `json:"session_state"`
	Ticket       string `json:"ticket,omitempty"`
	Phase        string `json:"phase,omitempty"`
	Activity     string `json:"activity,omitempty"`
	Detail       string `json:"detail,omitempty"`
	StateSince   string `json:"state_since,omitempty"`
}

// RepoView is a repo the hub knows about, flagged with whether a loop is
// currently running in it and whether the hub may start one there. Repos linger
// here after their loop exits so their runs stay browsable; an unallowed repo is
// observe-only. Registered marks a repo whose startability comes from a web
// registration rather than the SERVE_WORKSPACE seed, so the UI offers unregister
// only where it applies. Seeded marks the config-owned grant no removal can take
// back, which a registration can overlap. Kind tells a Folder repo from an
// ordinary one and ChildRepos counts the Child repos behind it. Freshness carries
// the issue-store sync state and is attached only on the repos API, where the
// background sync surfaces it.
type RepoView struct {
	registry.Repo
	Live       bool           `json:"live"`
	Allowed    bool           `json:"allowed"`
	Registered bool           `json:"registered"`
	Seeded     bool           `json:"seeded"`
	Kind       string         `json:"kind"`
	ChildRepos int            `json:"child_repos,omitempty"`
	Freshness  *RepoFreshness `json:"freshness,omitempty"`
}

const (
	repoKindRepo   = "repo"
	repoKindFolder = "folder"
)

// withKind fills in the folder facts, derived on every read rather than stored:
// an ordinary repo costs the one stat the scan short-circuits on, and only a real
// Folder repo pays the directory read. The instances resource polls every few
// seconds, and that scan is the price of a count that is always right and a
// registration store that never has to be told a child was cloned or removed. A
// directory that cannot be read reads as an ordinary repo rather than failing the
// request.
func (v RepoView) withKind() RepoView {
	v.Kind = repoKindRepo
	if census := folderrepo.Scan(v.Root); census.IsFolder {
		v.Kind, v.ChildRepos = repoKindFolder, len(census.Children)
	}
	return v
}

// RepoFreshness is a repo's issue-store freshness: its derived health state, when
// it last synced from the tracker, whether a background sync is running right now,
// the error from the last failed attempt and what it takes to clear (both empty
// once a sync succeeds), the counts the last good sync wrote, and how many issues
// the store now holds. On the repos API it always carries a State so the Instances
// page renders a designed state; the backlog attaches only the sync fields, leaving
// State and IssueCount unset.
type RepoFreshness struct {
	State         RepoHealthState `json:"state,omitempty"`
	LastSyncedAt  string          `json:"last_synced_at,omitempty"`
	Syncing       bool            `json:"syncing"`
	LastError     string          `json:"last_error,omitempty"`
	LastErrorKind string          `json:"last_error_kind,omitempty"`
	LastIssues    int             `json:"last_issues,omitempty"`
	LastComments  int             `json:"last_comments,omitempty"`
	IssueCount    int             `json:"issue_count,omitempty"`
}

// InstancesResponse is the /api/v1/instances resource: the live loops and every
// repo the hub has ever seen a loop run in. TakeoverSupported reports whether
// this hub's platform can open a terminal takeover (ADR 0018), so the web can
// hide the action instead of offering a guaranteed 501.
type InstancesResponse struct {
	Instances         []Instance `json:"instances"`
	Repos             []RepoView `json:"repos"`
	TakeoverSupported bool       `json:"takeover_supported"`
}

// instanceHeartbeatBody is a loop's reported presence on a register or heartbeat.
// It mirrors hubclient.InstanceHeartbeat: the hub keys presence by the {pid} path
// segment and stamps its own last-seen, so the body carries only what the loop
// reports.
type instanceHeartbeatBody struct {
	RepoRoot     string    `json:"repo_root"`
	RunsDir      string    `json:"runs_dir"`
	StartedAt    time.Time `json:"started_at"`
	SessionState string    `json:"session_state"`
	Ticket       string    `json:"ticket,omitempty"`
	Phase        string    `json:"phase,omitempty"`
	Activity     string    `json:"activity,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	StateSince   time.Time `json:"state_since,omitzero"`
}

func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	s.listInstances(w, r)
}

// handleInstance is a loop's presence seam (ADR 0008 §7): the loop PUTs its
// heartbeat — on start, on every session-state change, and on a timer — keyed by
// its PID, and DELETEs it on clean exit. The hub echoes the reported state and
// reaps a dead PID via signal 0, so a crashed loop that never DELETEs still ages
// out. Presence is best-effort on the loop side; the hub answers plainly. A PID
// the store has never seen is a loop starting and a DELETE is one settling, which
// is where team sync folds the teammates' records in and publishes this machine's.
func (s *Server) handleInstance(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil || pid <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid pid"})
		return
	}
	instances := s.stores.Instances()

	switch r.Method {
	case http.MethodPut:
		var req instanceHeartbeatBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		entry := registry.Entry{
			PID:          pid,
			RepoRoot:     req.RepoRoot,
			RunsDir:      req.RunsDir,
			StartedAt:    req.StartedAt,
			Heartbeat:    time.Now(),
			SessionState: req.SessionState,
			Ticket:       req.Ticket,
			Phase:        req.Phase,
			Activity:     req.Activity,
			Detail:       req.Detail,
			StateSince:   req.StateSince,
		}
		_, known, err := instances.Get(pid)
		if err != nil {
			logger.Verbosef("instances get %d: %v", pid, err)
		}
		if err := instances.Upsert(entry); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if !known {
			s.team.kick(entry.RepoRoot)
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "pid": pid})
	case http.MethodDelete:
		entry, _, err := instances.Get(pid)
		if err != nil {
			logger.Verbosef("instances get %d: %v", pid, err)
		}
		if err := instances.Remove(pid); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		s.team.kick(entry.RepoRoot)
		writeJSON(w, http.StatusOK, map[string]any{"status": "removed", "pid": pid})
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// liveInstances is the hub's read of the live loops on this machine, from the
// authoritative presence store: the entries whose PID still passes signal 0, with
// the dead ones reaped. A store error reads as no live loops, keeping every
// consumer's fail-safe (nothing live) the same as the file-era empty glob.
func (s *Server) liveInstances() []registry.Entry {
	entries, err := s.stores.Instances().Live()
	if err != nil {
		logger.Verbosef("instances live: %v", err)
		return nil
	}
	return entries
}

// repoIsLive reports whether any loop is registered in root right now, idle
// dashboards included, matching what the repos list flags as live. The presence
// sweep remembers every live loop's repo, so removing one would not stick. An
// entry's root and the stored row's can spell the same directory differently, and
// the refusal has to fire on either spelling.
func (s *Server) repoIsLive(root string) bool {
	canonical := registry.CanonicalRoot(root)
	return slices.ContainsFunc(s.liveInstances(), func(e registry.Entry) bool {
		return registry.CanonicalRoot(e.RepoRoot) == canonical
	})
}

// folderCollisionError refuses work in a repo whose enclosing Folder repo, or one
// of whose Child repos, already has a loop live in it. It names both the repo and
// the ticket its loop is on, so the board states the reason before anything spawns.
type folderCollisionError struct{ entry registry.Entry }

func (e folderCollisionError) Error() string {
	return folderrepo.CollisionReason(e.entry.Ticket, e.entry.RepoRoot)
}

// folderCollision names the live loop a run in root would share a working tree
// with: one in a Child repo when root is a Folder repo, or one in the Folder repo
// that holds root. Every live entry blocks, a takeover included — a terminal
// session holds that working tree just as firmly as a loop does.
func (s *Server) folderCollision(root string) (folderCollisionError, bool) {
	for _, e := range s.liveInstances() {
		if folderrepo.Collides(root, e.RepoRoot) {
			return folderCollisionError{entry: e}, true
		}
	}
	return folderCollisionError{}, false
}

// hasBusyInstance reports whether a live instance in root is past idle — a run in
// flight, held WIP, or a takeover terminal owning the working tree (ADR 0018). An
// idle instance is an open dashboard rather than a run, and a legacy entry with no
// state counts as busy. An empty root asks across every repo.
func (s *Server) hasBusyInstance(root string) bool {
	for _, e := range s.liveInstances() {
		if root != "" && e.RepoRoot != root {
			continue
		}
		if e.SessionState != registry.StateIdle {
			return true
		}
	}
	return false
}

func (s *Server) listInstances(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, InstancesResponse{
		Instances:         s.instanceViews(),
		Repos:             s.repoViews(),
		TakeoverSupported: s.takeoverSupported(),
	})
}

// instanceViews maps the live presence entries onto the JSON instance rows the
// Instances page and the MCP tool share.
func (s *Server) instanceViews() []Instance {
	entries := s.liveInstances()
	instances := make([]Instance, 0, len(entries))
	for _, e := range entries {
		inst := Instance{
			PID:       e.PID,
			Repo:      filepath.Base(e.RepoRoot),
			RepoRoot:  e.RepoRoot,
			RunsDir:   e.RunsDir,
			StartedAt: e.StartedAt.UTC().Format(time.RFC3339),
		}
		if e.SessionState == "" {
			inst.SessionState = "unknown"
		} else {
			inst.SessionState = e.SessionState
			inst.Ticket = e.Ticket
			inst.Phase = e.Phase
			inst.Activity = e.Activity
			inst.Detail = e.Detail
			if !e.StateSince.IsZero() {
				inst.StateSince = e.StateSince.UTC().Format(time.RFC3339)
			}
		}
		instances = append(instances, inst)
	}
	return instances
}
