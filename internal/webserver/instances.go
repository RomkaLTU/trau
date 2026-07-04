package webserver

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// Instance is a live loop as the hub sees it: the registry record plus the
// current ticket and phase derived from the repo's newest run artifacts.
type Instance struct {
	PID        int    `json:"pid"`
	Repo       string `json:"repo"`
	RepoRoot   string `json:"repo_root"`
	RunsDir    string `json:"runs_dir"`
	StartedAt  string `json:"started_at"`
	Ticket     string `json:"ticket,omitempty"`
	Phase      string `json:"phase,omitempty"`
	PhaseSince string `json:"phase_since,omitempty"`
}

// RepoView is a repo the hub knows about, flagged with whether a loop is
// currently running in it. Repos linger here after their loop exits so their
// runs stay browsable.
type RepoView struct {
	registry.Repo
	Live bool `json:"live"`
}

// InstancesResponse is the /api/v1/instances resource: the live loops and every
// repo the hub has ever seen a loop run in.
type InstancesResponse struct {
	Instances []Instance `json:"instances"`
	Repos     []RepoView `json:"repos"`
}

func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	entries := registry.Live(s.home)
	registry.RememberRepos(s.home, entries)

	liveRoots := make(map[string]bool, len(entries))
	instances := make([]Instance, 0, len(entries))
	for _, e := range entries {
		liveRoots[e.RepoRoot] = true
		inst := Instance{
			PID:       e.PID,
			Repo:      filepath.Base(e.RepoRoot),
			RepoRoot:  e.RepoRoot,
			RunsDir:   e.RunsDir,
			StartedAt: e.StartedAt.UTC().Format(time.RFC3339),
		}
		if run, ok := activeRun(e.RunsDir); ok {
			inst.Ticket = run.ticket
			inst.Phase = run.phase
			inst.PhaseSince = run.since.UTC().Format(time.RFC3339)
		}
		instances = append(instances, inst)
	}

	known := registry.Repos(s.home)
	repos := make([]RepoView, 0, len(known))
	for _, repo := range known {
		repos = append(repos, RepoView{Repo: repo, Live: liveRoots[repo.Root]})
	}

	writeJSON(w, http.StatusOK, InstancesResponse{Instances: instances, Repos: repos})
}

type runInfo struct {
	ticket string
	phase  string
	since  time.Time
}

// activeRun derives the ticket and phase a loop is currently working from the
// newest in-flight checkpoint under runsDir. Terminal and unknown-phase
// checkpoints are ignored, so a repo between tickets reports no active run.
func activeRun(runsDir string) (runInfo, bool) {
	store := state.NewStore(runsDir)
	var best runInfo
	for _, id := range store.Tickets() {
		phase := store.Get(id, "PHASE")
		if rank := state.Idx(phase); rank < 1 || rank > 5 {
			continue
		}
		info, err := os.Stat(filepath.Join(runsDir, id, "state"))
		if err != nil {
			continue
		}
		if info.ModTime().After(best.since) {
			best = runInfo{ticket: id, phase: phase, since: info.ModTime()}
		}
	}
	return best, best.ticket != ""
}
