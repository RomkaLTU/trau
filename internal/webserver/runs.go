package webserver

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
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
	Ticket        string   `json:"ticket"`
	Title         string   `json:"title,omitempty"`
	Phase         string   `json:"phase"`
	PhaseRank     int      `json:"phase_rank"`
	Terminal      bool     `json:"terminal"`
	Branch        string   `json:"branch,omitempty"`
	PR            string   `json:"pr,omitempty"`
	PRURL         string   `json:"pr_url,omitempty"`
	FailureClass  string   `json:"failure_class,omitempty"`
	FailureReason string   `json:"failure_reason,omitempty"`
	CostUSD       *float64 `json:"cost_usd,omitempty"`
	UpdatedAt     string   `json:"updated_at,omitempty"`
}

// RunsResponse is the /api/v1/repos/{repo}/runs resource: the repo's tickets in
// checkpoint-board order.
type RunsResponse struct {
	Repo string    `json:"repo"`
	Runs []RunView `json:"runs"`
}

func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, ReposResponse{Repos: s.withFreshness(s.repoViews())})
	case http.MethodPost:
		s.registerRepo(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// withFreshness attaches each repo's issue-store freshness — its derived health
// state, last synced, currently syncing, last error, the last good counts, and
// the current issue count. Every repo carries a state, so unlike the backlog's
// freshness this is present for a repo that has never synced (never-synced or
// unconfigured) rather than absent. It is applied only here so the shared
// repoViews path stays a pure read for the other endpoints.
func (s *Server) withFreshness(views []RepoView) []RepoView {
	for i := range views {
		views[i].Freshness = s.repoFreshness(views[i].Repo)
	}
	return views
}

// repoFreshness reads a repo's issue-store state and folds in the live syncing
// flag, the derived health state, and the current issue count. It always returns
// a freshness so the repos API surfaces a state for every repo; only a store read
// error drops it.
func (s *Server) repoFreshness(repo registry.Repo) *RepoFreshness {
	st, err := s.stores.Issues().SyncState(repo.Root)
	if err != nil {
		return nil
	}
	count, _ := s.stores.Issues().Count(repo.Root)
	syncing := s.syncer.syncing(repo.Root)
	return &RepoFreshness{
		State:        deriveHealthState(s.repoConfigured(repo), syncing, st),
		LastSyncedAt: st.LastSyncedAt,
		Syncing:      syncing,
		LastError:    st.LastError,
		LastIssues:   st.LastIssues,
		LastComments: st.LastComments,
		IssueCount:   count,
	}
}

// freshnessFrom builds a repo's freshness from an already-read sync state, folding
// in whether a background sync is running right now. It returns nil for a repo
// that has never synced and is not syncing, so the field stays absent where there
// is no tracker.
func (s *Server) freshnessFrom(root string, st hubstore.SyncState) *RepoFreshness {
	syncing := s.syncer.syncing(root)
	if st.LastSyncedAt == "" && st.LastError == "" && !syncing {
		return nil
	}
	return &RepoFreshness{
		LastSyncedAt: st.LastSyncedAt,
		Syncing:      syncing,
		LastError:    st.LastError,
		LastIssues:   st.LastIssues,
		LastComments: st.LastComments,
	}
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
	s.importCheckpoints(repo)
	writeJSON(w, http.StatusOK, RunsResponse{Repo: repo.Name, Runs: s.collectRuns(repo.Root)})
}

// repoViews lists every repo the hub knows, flagging the ones a loop is running
// in right now and the ones the hub is allowed to start a loop in. It unions the
// live loops' repos with the persisted known set, so a repo appears the moment
// its loop starts and lingers after it exits, then merges in allowlisted repos
// that have never run so they are startable before their first loop. It is a
// pure read: the known set is persisted off the request path by the sweep.
func (s *Server) repoViews() []RepoView {
	entries := s.liveInstances()
	live := make(map[string]bool, len(entries))
	for _, e := range entries {
		live[e.RepoRoot] = true
	}
	roots := s.effectiveRoots()
	allowed := make(map[string]bool, len(roots))
	for _, root := range roots {
		allowed[root] = true
	}
	registered := make(map[string]bool)
	if roots, err := s.stores.Registrations().Registered(); err == nil {
		for _, root := range roots {
			registered[root] = true
		}
	}
	seen := make(map[string]bool)
	known := s.knownRepos(entries)
	views := make([]RepoView, 0, len(known)+len(roots))
	for _, repo := range known {
		seen[repo.Root] = true
		views = append(views, RepoView{Repo: repo, Live: live[repo.Root], Allowed: allowed[repo.Root], Registered: registered[repo.Root]})
	}
	for _, root := range roots {
		if seen[root] {
			continue
		}
		views = append(views, RepoView{Repo: workspaceRepo(root), Allowed: true, Registered: registered[root]})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views
}

// knownRepos is the repos the hub has seen a loop run in: the persisted known set
// unioned with the currently live loops, sorted by name. Reading it never writes;
// the sweep persists live loops so they linger after exit. entries is the live
// snapshot the caller already read, folded in so a just-started loop resolves
// before the next sweep.
func (s *Server) knownRepos(entries []registry.Entry) []registry.Repo {
	byRoot := make(map[string]registry.Repo)
	if persisted, err := s.stores.Registrations().Known(); err == nil {
		for _, repo := range persisted {
			byRoot[repo.Root] = repo
		}
	}
	for _, repo := range reposFromEntries(entries) {
		if _, ok := byRoot[repo.Root]; !ok {
			byRoot[repo.Root] = repo
		}
	}
	repos := make([]registry.Repo, 0, len(byRoot))
	for _, repo := range byRoot {
		repos = append(repos, repo)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	return repos
}

// findRepo resolves a {repo} path segment to a repo by name against the same union
// the repos list shows: the repos a loop has run in (the known set and any live
// loops) first, then the startable roots (the workspace seed and web registrations)
// synthesized as workspace views, so a freshly registered repo resolves before its
// first loop runs. Known and live entries win over a synthesized view on a name
// collision.
func (s *Server) findRepo(name string) (registry.Repo, bool) {
	if name == "" {
		return registry.Repo{}, false
	}
	for _, repo := range s.knownRepos(s.liveInstances()) {
		if repo.Name == name {
			return repo, true
		}
	}
	if root, ok := matchRoot(s.effectiveRoots(), name); ok {
		return workspaceRepo(root), true
	}
	return registry.Repo{}, false
}

// collectRuns reads every checkpoint the authoritative table holds for root into
// a board-ordered run list. The loop writes checkpoints straight to that table
// over HTTP (ADR 0008), so a poll never re-reads a checkpoint per field.
func (s *Server) collectRuns(root string) []RunView {
	rows, err := s.stores.Checkpoints().All(root)
	if err != nil {
		logger.Verbosef("checkpoints %s: %v", root, err)
		rows = nil
	}
	runs := make([]RunView, 0, len(rows))
	for _, tc := range rows {
		runs = append(runs, runViewFromCheckpoint(tc))
	}
	sortRuns(runs)
	return runs
}

func runViewFromCheckpoint(tc hubstore.TicketCheckpoint) RunView {
	phase := tc.Phase
	reason := tc.FailureReason
	class := state.FailureClass(phase, checkpointField(tc.Data, "FAILURE_CLASS"), reason)
	if phase == state.Merged {
		reason = ""
	}
	return RunView{
		Ticket:        tc.Ticket,
		Title:         tc.Title,
		Phase:         phase,
		PhaseRank:     state.Idx(phase),
		Terminal:      state.Terminal(phase),
		Branch:        tc.Branch,
		PR:            tc.PR,
		PRURL:         tc.PRURL,
		FailureClass:  class,
		FailureReason: reason,
		CostUSD:       tc.CostUSD,
		UpdatedAt:     tc.UpdatedAt,
	}
}

// checkpointField pulls one raw state key out of a checkpoint's JSON data blob,
// the board's source for fields it does not project into a column (the stored
// failure class). A missing key or unparseable blob reads as empty.
func checkpointField(data, key string) string {
	if data == "" {
		return ""
	}
	var m map[string]string
	if json.Unmarshal([]byte(data), &m) != nil {
		return ""
	}
	return m[key]
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
