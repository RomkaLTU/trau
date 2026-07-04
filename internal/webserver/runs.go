package webserver

import (
	"net/http"
	"sort"

	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// ReposResponse is the /api/v1/repos resource: every repo the hub has seen a
// loop run in, each flagged live when a loop is currently running in it.
type ReposResponse struct {
	Repos []RepoView `json:"repos"`
}

// RunView is one ticket's run as read from its durable checkpoint. It carries the
// checkpoint phase and rank the board orders on, the branch and PR reference for
// the row, and the failure class/reason that flags a paused, faulted, or
// quarantined run.
type RunView struct {
	Ticket        string `json:"ticket"`
	Title         string `json:"title,omitempty"`
	Phase         string `json:"phase"`
	PhaseRank     int    `json:"phase_rank"`
	Terminal      bool   `json:"terminal"`
	Branch        string `json:"branch,omitempty"`
	PR            string `json:"pr,omitempty"`
	PRURL         string `json:"pr_url,omitempty"`
	FailureClass  string `json:"failure_class,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

// RunsResponse is the /api/v1/repos/{repo}/runs resource: the repo's tickets in
// checkpoint-board order.
type RunsResponse struct {
	Repo string    `json:"repo"`
	Runs []RunView `json:"runs"`
}

func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, ReposResponse{Repos: s.repoViews()})
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	writeJSON(w, http.StatusOK, RunsResponse{Repo: repo.Name, Runs: collectRuns(repo.RunsDir)})
}

// repoViews lists every repo the hub knows, flagging the ones a loop is running
// in right now and the ones the hub is allowed to start a loop in. It folds the
// live loops' repos into the persistent set first, so a repo appears the moment
// its loop starts and lingers after it exits, then merges in allowlisted repos
// that have never run so they are startable before their first loop.
func (s *Server) repoViews() []RepoView {
	entries := registry.Live(s.home)
	registry.RememberRepos(s.home, entries)
	live := make(map[string]bool, len(entries))
	for _, e := range entries {
		live[e.RepoRoot] = true
	}
	seen := make(map[string]bool)
	known := registry.Repos(s.home)
	views := make([]RepoView, 0, len(known)+len(s.workspace))
	for _, repo := range known {
		seen[repo.Root] = true
		views = append(views, RepoView{Repo: repo, Live: live[repo.Root], Allowed: s.allows(repo.Root)})
	}
	for _, root := range s.workspace {
		if seen[root] {
			continue
		}
		views = append(views, RepoView{Repo: workspaceRepo(root), Allowed: true})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views
}

// findRepo resolves a {repo} path segment to a known repo by name, remembering
// any live loops' repos first so a just-started loop's repo is resolvable.
func (s *Server) findRepo(name string) (registry.Repo, bool) {
	if name == "" {
		return registry.Repo{}, false
	}
	registry.RememberRepos(s.home, registry.Live(s.home))
	for _, repo := range registry.Repos(s.home) {
		if repo.Name == name {
			return repo, true
		}
	}
	return registry.Repo{}, false
}

// collectRuns reads every checkpoint under runsDir into a board-ordered run list.
// It is file-first: the runs read the same whether the loop is live, exited, or
// never controlled by this hub.
func collectRuns(runsDir string) []RunView {
	store := state.NewStore(runsDir)
	ids := store.Tickets()
	runs := make([]RunView, 0, len(ids))
	for _, id := range ids {
		runs = append(runs, runView(store, id))
	}
	sortRuns(runs)
	return runs
}

func runView(store *state.Store, id string) RunView {
	phase := store.Get(id, "PHASE")
	reason := store.Get(id, "FAILURE_REASON")
	class := state.FailureClass(phase, store.Get(id, "FAILURE_CLASS"), reason)
	if phase == state.Merged {
		reason = ""
	}
	return RunView{
		Ticket:        id,
		Title:         store.Get(id, "TITLE"),
		Phase:         phase,
		PhaseRank:     state.Idx(phase),
		Terminal:      state.Terminal(phase),
		Branch:        store.Get(id, "BRANCH"),
		PR:            store.Get(id, "PR"),
		PRURL:         store.Get(id, "PR_URL"),
		FailureClass:  class,
		FailureReason: reason,
		UpdatedAt:     store.Get(id, "UPDATED"),
	}
}

// sortRuns orders the board by phase rank, then by ticket so the column contents
// stay stable across reads.
func sortRuns(runs []RunView) {
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].PhaseRank != runs[j].PhaseRank {
			return runs[i].PhaseRank < runs[j].PhaseRank
		}
		return runs[i].Ticket < runs[j].Ticket
	})
}
