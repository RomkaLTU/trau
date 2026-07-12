package webserver

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

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
	StateSince   string `json:"state_since,omitempty"`
}

// RepoView is a repo the hub knows about, flagged with whether a loop is
// currently running in it and whether the hub may start one there. Repos linger
// here after their loop exits so their runs stay browsable; an unallowed repo is
// observe-only. Registered marks a repo whose startability comes from a web
// registration rather than the SERVE_WORKSPACE seed, so the UI offers unregister
// only where it applies. Freshness carries the issue-store sync state and is
// attached only on the repos API, where the background sync surfaces it.
type RepoView struct {
	registry.Repo
	Live       bool           `json:"live"`
	Allowed    bool           `json:"allowed"`
	Registered bool           `json:"registered"`
	Freshness  *RepoFreshness `json:"freshness,omitempty"`
}

// RepoFreshness is a repo's issue-store freshness: when it last synced from the
// tracker, whether a background sync is running right now, the error from the
// last failed attempt (empty once a sync succeeds), and the counts the last good
// sync wrote. It is absent for a repo that has never synced and is not syncing.
type RepoFreshness struct {
	LastSyncedAt string `json:"last_synced_at,omitempty"`
	Syncing      bool   `json:"syncing"`
	LastError    string `json:"last_error,omitempty"`
	LastIssues   int    `json:"last_issues,omitempty"`
	LastComments int    `json:"last_comments,omitempty"`
}

// InstancesResponse is the /api/v1/instances resource: the live loops and every
// repo the hub has ever seen a loop run in.
type InstancesResponse struct {
	Instances []Instance `json:"instances"`
	Repos     []RepoView `json:"repos"`
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
	StateSince   time.Time `json:"state_since,omitzero"`
}

func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listInstances(w, r)
	case http.MethodPost:
		s.startInstance(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleInstance is a loop's presence seam (ADR 0008 §7): the loop PUTs its
// heartbeat — on start, on every session-state change, and on a timer — keyed by
// its PID, and DELETEs it on clean exit. The hub echoes the reported state and
// reaps a dead PID via signal 0, so a crashed loop that never DELETEs still ages
// out. Presence is best-effort on the loop side; the hub answers plainly.
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
			StateSince:   req.StateSince,
		}
		if err := instances.Upsert(entry); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "pid": pid})
	case http.MethodDelete:
		if err := instances.Remove(pid); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
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

func (s *Server) listInstances(w http.ResponseWriter, _ *http.Request) {
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
			if !e.StateSince.IsZero() {
				inst.StateSince = e.StateSince.UTC().Format(time.RFC3339)
			}
		}
		instances = append(instances, inst)
	}

	writeJSON(w, http.StatusOK, InstancesResponse{Instances: instances, Repos: s.repoViews()})
}
