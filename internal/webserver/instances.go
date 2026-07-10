package webserver

import (
	"net/http"
	"path/filepath"
	"time"

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
// only where it applies.
type RepoView struct {
	registry.Repo
	Live       bool `json:"live"`
	Allowed    bool `json:"allowed"`
	Registered bool `json:"registered"`
}

// InstancesResponse is the /api/v1/instances resource: the live loops and every
// repo the hub has ever seen a loop run in.
type InstancesResponse struct {
	Instances []Instance `json:"instances"`
	Repos     []RepoView `json:"repos"`
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

func (s *Server) listInstances(w http.ResponseWriter, _ *http.Request) {
	entries := registry.Live(s.home)
	registry.RememberRepos(s.home, entries)

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
