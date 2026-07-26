package webserver

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/teamsync"
)

// sharedRunCap bounds the run history one machine publishes to its most recent
// settled runs. A push rewrites the whole payload, so this cap — not how long the
// machine has been running — is what keeps a snapshot commit small.
const sharedRunCap = 50

// SharedRun is a teammate's run exactly as it travelled, plus the attribution to
// show it under: the ref segment it was published on, the git name behind that,
// and when this machine last read it.
type SharedRun struct {
	teamsync.Run
	Writer    string `json:"writer"`
	Author    string `json:"author,omitempty"`
	FetchedAt string `json:"fetched_at,omitempty"`
}

// sharedRuns snapshots this machine's settled runs for publication, most recently
// ended first and capped at sharedRunCap. A run still in flight never travels: only
// a terminal checkpoint, or one the loop classified as failed, has an outcome worth
// sharing.
func (s *Server) sharedRuns(root string) []teamsync.Run {
	rows, err := s.stores.Checkpoints().All(root)
	if err != nil {
		logger.Verbosef("shared runs %s: %v", root, err)
		return nil
	}
	settled := make([]RunView, 0, len(rows))
	for _, tc := range rows {
		view := runViewFromCheckpoint(tc)
		if view.Terminal || view.FailureClass != "" {
			settled = append(settled, view)
		}
	}
	sort.Slice(settled, func(i, j int) bool { return settled[i].UpdatedAt > settled[j].UpdatedAt })
	if len(settled) > sharedRunCap {
		settled = settled[:sharedRunCap]
	}

	name := filepath.Base(root)
	if repo, ok := s.findRepoByRoot(root); ok {
		name = repo.Name
	}
	out := make([]teamsync.Run, 0, len(settled))
	for _, view := range settled {
		out = append(out, s.sharedRun(root, name, view))
	}
	return out
}

func (s *Server) sharedRun(root, name string, view RunView) teamsync.Run {
	startedAt, activities := runActivitySpans(s.stores.Events(), root, view.Ticket)
	provider, model, err := s.stores.Tokens().Route(root, view.Ticket)
	if err != nil {
		logger.Verbosef("shared run route %s/%s: %v", root, view.Ticket, err)
	}
	spend, err := s.stores.Tokens().Total(root, view.Ticket)
	if err != nil {
		logger.Verbosef("shared run spend %s/%s: %v", root, view.Ticket, err)
	}
	return teamsync.Run{
		Ticket:        view.Ticket,
		Title:         view.Title,
		Repo:          name,
		Branch:        view.Branch,
		PRURL:         view.PRURL,
		Phase:         view.Phase,
		FailureClass:  view.FailureClass,
		FailureReason: view.FailureReason,
		StartedAt:     startedAt,
		EndedAt:       view.UpdatedAt,
		Provider:      provider,
		Model:         model,
		Tokens:        spend.Tokens,
		CostUSD:       view.CostUSD,
		Activities:    activities,
	}
}

// TeamRunsResponse is the /api/v1/repos/{repo}/team-runs resource: the settled runs
// teammates published over the repo's git remote, most recently ended first. It is
// deliberately a resource of its own rather than extra rows on /runs — every reader
// of that one (the picker's checkpoints, the attention counts, the board) means
// this machine's work, and a teammate's history must never reach them.
type TeamRunsResponse struct {
	Repo string    `json:"repo"`
	Runs []RunView `json:"runs"`
}

func (s *Server) handleTeamRuns(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, TeamRunsResponse{Repo: repo.Name, Runs: s.teammateRunViews(repo.Root)})
}

// teammateRunViews reads the runs teammates published for the repo as ledger rows,
// each attributed to the git name behind its writer id, most recently ended first.
// A repo that has not opted in reads none: the records an earlier opt-in fetched
// stay stored, but the toggle — not the store — decides whether they are visible,
// so turning sharing off puts the ledger back the way it was. A payload that no
// longer decodes is skipped: a teammate's stale snapshot never breaks the read.
func (s *Server) teammateRunViews(root string) []RunView {
	if cfg, ok := s.repoLayeredConfig(root); !ok || !cfg.TeamSync {
		return []RunView{}
	}
	records, err := s.stores.TeamSync().Records(root)
	if err != nil {
		logger.Verbosef("team sync: read records %s: %v", root, err)
		return nil
	}
	out := []RunView{}
	for _, rec := range records {
		var p teamsync.Payload
		if err := json.Unmarshal([]byte(rec.Payload), &p); err != nil {
			continue
		}
		author := strings.TrimSpace(rec.AuthorName)
		if author == "" {
			author = rec.WriterID
		}
		for _, r := range p.Runs {
			out = append(out, teammateRunView(rec, author, r))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

func teammateRunView(rec hubstore.TeamRecord, author string, r teamsync.Run) RunView {
	return RunView{
		Ticket:        r.Ticket,
		Title:         r.Title,
		Phase:         r.Phase,
		PhaseRank:     state.Idx(r.Phase),
		Terminal:      state.Terminal(r.Phase),
		Branch:        r.Branch,
		PRURL:         r.PRURL,
		FailureClass:  r.FailureClass,
		FailureReason: r.FailureReason,
		CostUSD:       r.CostUSD,
		UpdatedAt:     r.EndedAt,
		Shared: &SharedRun{
			Run:       r,
			Writer:    rec.WriterID,
			Author:    author,
			FetchedAt: rec.FetchedAt,
		},
	}
}

// kickOnSettle publishes this machine's records when a run settles, reading the
// same terminal state_change the ledger takes a run's outcome from. Every other
// event kind passes through untouched.
func (ts *teamSyncer) kickOnSettle(root string, row hubstore.EventRow) {
	if row.Kind != stateChangeKind {
		return
	}
	if st, _ := unmarshalFields(row.Fields)["state"].(string); terminalStates[st] {
		ts.kick(root)
	}
}
