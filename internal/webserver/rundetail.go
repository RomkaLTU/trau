package webserver

import (
	"encoding/json"
	"net/http"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// RunDetail is the /api/v1/repos/{repo}/runs/{ticket} resource: one ticket's
// full drill-down. It carries the checkpoint state keys (embedded RunView), the
// per-phase token/cost breakdown, and the phase artifacts — the handoff brief, the
// acceptance rubric, the verify verdict, and the build notes — served from the
// authoritative artifact store (ADR 0008). Every artifact is optional: an
// early-phase run that has not been handed off, graded, or opened a PR yet simply
// omits them, and Artifacts records which ones are present.
type RunDetail struct {
	RunView
	Costs      []PhaseCost    `json:"costs"`
	Durations  []StepDuration `json:"durations,omitempty"`
	Anomalies  []AnomalyView  `json:"anomalies,omitempty"`
	Handoff    string         `json:"handoff,omitempty"`
	Rubric     *RubricView    `json:"rubric,omitempty"`
	Verdict    *VerdictView   `json:"verdict,omitempty"`
	BuildNotes string         `json:"build_notes,omitempty"`
	Artifacts  ArtifactSet    `json:"artifacts"`
	// NoSkills is true when this run's build loaded no skills in a repo that has
	// skills installed — the durable build_no_skills warning, surfaced so the
	// page can flag a silently skill-less build.
	NoSkills bool `json:"no_skills,omitempty"`
	// NoBrowser is true when this run's verify left a UI slice undriven — the
	// durable verify_no_browser warning, surfaced so the page can flag browser QA
	// that never happened on a front-end diff.
	NoBrowser bool `json:"no_browser,omitempty"`
	// Removed is true when the run's ticket is a synced issue the tracker no longer
	// holds — deleted, archived, or moved out of the Project and tombstoned by a
	// reconciliation sweep. The run detail still renders from its durable artifacts;
	// the flag lets the page mark that the underlying ticket is gone.
	Removed bool `json:"removed,omitempty"`
}

// AnomalyView is one flagged cost anomaly for a run: the phase that cleared a soft
// threshold, its output/turns/cost, and the human reasons it was flagged.
type AnomalyView struct {
	Phase   string   `json:"phase"`
	Output  int      `json:"output"`
	Turns   int      `json:"turns"`
	CostUSD float64  `json:"cost_usd"`
	Reasons []string `json:"reasons"`
}

// PhaseCost is one phase's summed token + cost spend. Metered is false when any of
// the phase's calls recorded no per-call cost, so CostUSD is then a lower bound
// rather than a measured total.
type PhaseCost struct {
	Phase         string  `json:"phase"`
	Input         int     `json:"input"`
	Output        int     `json:"output"`
	CacheRead     int     `json:"cache_read"`
	CacheCreation int     `json:"cache_creation"`
	Reasoning     int     `json:"reasoning"`
	Total         int     `json:"total"`
	CostUSD       float64 `json:"cost_usd"`
	Metered       bool    `json:"metered"`
	Calls         int     `json:"calls"`
	Turns         int     `json:"turns"`
}

// RubricView is the structured acceptance rubric the handoff phase distilled,
// read from the artifact store.
type RubricView struct {
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
	NonGoals           []string `json:"non_goals,omitempty"`
	RequiredTests      []string `json:"required_tests,omitempty"`
	UIPaths            []string `json:"ui_paths,omitempty"`
	FailConditions     []string `json:"fail_conditions,omitempty"`
}

// VerdictView is the cold verifier's graded outcome, read from the artifact
// store: whether the slice passed, a one-line summary, the concrete failures, the
// gated check results, and the self-reported browser-QA outcome (driven, skipped,
// or not-applicable) with its notes.
type VerdictView struct {
	Pass         bool        `json:"pass"`
	Summary      string      `json:"summary,omitempty"`
	Failures     []string    `json:"failures,omitempty"`
	Checks       []CheckView `json:"checks,omitempty"`
	Browser      string      `json:"browser,omitempty"`
	BrowserNotes string      `json:"browser_notes,omitempty"`
}

// CheckView is one verify-check outcome carried inside the verdict.
type CheckView struct {
	Name     string `json:"name"`
	Severity string `json:"severity,omitempty"`
	Pass     bool   `json:"pass"`
	Detail   string `json:"detail,omitempty"`
}

// ArtifactSet flags which per-run artifacts the store holds so the page can render
// a present-but-empty section differently from a not-yet-produced one.
type ArtifactSet struct {
	Handoff    bool `json:"handoff"`
	Rubric     bool `json:"rubric"`
	Verdict    bool `json:"verdict"`
	BuildNotes bool `json:"build_notes"`
	Tokens     bool `json:"tokens"`
}

func (s *Server) handleRunDetail(w http.ResponseWriter, r *http.Request) {
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
	ticket := r.PathValue("ticket")
	s.importCheckpoints(repo)
	s.importArtifacts(repo)
	view, ok := s.runViewFor(repo.Root, ticket)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown run"})
		return
	}
	writeJSON(w, http.StatusOK, s.runDetail(repo, ticket, view))
}

// runDetail assembles a ticket's run detail from its resolved checkpoint and its
// durable artifacts, and marks it removed when the ticket is a synced issue the
// tracker no longer holds — the store's tombstone. The lookup degrades to
// not-removed on any store error so a store hiccup never fails an
// otherwise-renderable run.
func (s *Server) runDetail(repo registry.Repo, ticket string, view RunView) RunDetail {
	d := runDetail(s.stores.Tokens(), s.stores.Artifacts(), s.stores.Events(), repo.Root, ticket, view, s.noSkillsWarning(repo, ticket), s.noBrowserWarning(repo, ticket))
	if iss, ok, err := s.stores.Issues().Get(repo.Root, ticket); err == nil && ok && iss.DeletedAt != "" {
		d.Removed = true
	}
	return d
}

// noSkillsWarning reports whether the authoritative event table carries a
// build_no_skills warning for ticket — a build that used none of the repo's
// installed skills (ADR 0008). A store error degrades to no warning rather than
// failing an otherwise-renderable run.
func (s *Server) noSkillsWarning(repo registry.Repo, ticket string) bool {
	has, err := s.stores.Events().HasKind(repo.Root, event.KindBuildNoSkills, ticket)
	if err != nil {
		logger.Verbosef("no-skills flag %s/%s: %v", repo.Name, ticket, err)
	}
	return has
}

// noBrowserWarning reports whether the authoritative event table carries a
// verify_no_browser warning for ticket — a UI slice whose verify never drove the
// browser (ADR 0008). A store error degrades to no warning rather than failing an
// otherwise-renderable run.
func (s *Server) noBrowserWarning(repo registry.Repo, ticket string) bool {
	has, err := s.stores.Events().HasKind(repo.Root, event.KindVerifyNoBrowser, ticket)
	if err != nil {
		logger.Verbosef("no-browser flag %s/%s: %v", repo.Name, ticket, err)
	}
	return has
}

// runViewFor resolves a ticket's checkpoint row for the detail page from the
// authoritative checkpoints table (ADR 0008) — the same table the board reads.
// Legacy state files are folded in by the caller's importCheckpoints before this
// runs, so a hub-only or imported ticket resolves here; a run the hub has never
// seen reports false and 404s.
func (s *Server) runViewFor(root, ticket string) (RunView, bool) {
	if ticket == "" {
		return RunView{}, false
	}
	row, found, err := s.stores.Checkpoints().One(root, ticket)
	if err != nil {
		logger.Verbosef("run detail %s/%s: %v", root, ticket, err)
	}
	if !found {
		return RunView{}, false
	}
	return runViewFromCheckpoint(hubstore.TicketCheckpoint{Ticket: ticket, CheckpointRow: row}), true
}

func runDetail(toks *hubstore.Tokens, arts *hubstore.Artifacts, evs *hubstore.Events, root, ticket string, view RunView, noSkills, noBrowser bool) RunDetail {
	costs := phaseCosts(toks, root, ticket)
	all, err := arts.All(root, ticket)
	if err != nil {
		logger.Verbosef("run artifacts %s/%s: %v", root, ticket, err)
		all = map[string]string{}
	}
	handoff, hasHandoff := all[hubstore.ArtifactHandoff]
	notes, hasNotes := all[hubstore.ArtifactBuildNotes]
	rubricContent, hasRubric := all[hubstore.ArtifactRubric]
	verdictContent, hasVerdict := all[hubstore.ArtifactVerdict]
	return RunDetail{
		RunView:    view,
		Costs:      costs,
		Durations:  runStepDurations(evs, root, ticket),
		Anomalies:  anomalyViews(toks, root, ticket),
		Handoff:    handoff,
		Rubric:     parseRubric(rubricContent),
		Verdict:    parseVerdict(verdictContent),
		BuildNotes: notes,
		Artifacts: ArtifactSet{
			Handoff:    hasHandoff,
			Rubric:     hasRubric,
			Verdict:    hasVerdict,
			BuildNotes: hasNotes,
			Tokens:     len(costs) > 0,
		},
		NoSkills:  noSkills,
		NoBrowser: noBrowser,
	}
}

// phaseCosts reads the run's per-phase token/cost breakdown from the authoritative
// store, in the order each phase first appears in the ticket's calls (ADR 0008). A
// store error reads as no costs rather than failing the run.
func phaseCosts(toks *hubstore.Tokens, root, ticket string) []PhaseCost {
	totals, err := toks.PhaseTotals(root, ticket)
	if err != nil {
		logger.Verbosef("phase costs %s/%s: %v", root, ticket, err)
		return nil
	}
	out := make([]PhaseCost, 0, len(totals))
	for _, t := range totals {
		out = append(out, PhaseCost{
			Phase:         t.Phase,
			Input:         t.Input,
			Output:        t.Output,
			CacheRead:     t.CacheRead,
			CacheCreation: t.CacheCreation,
			Reasoning:     t.Reasoning,
			Total:         t.Total,
			CostUSD:       t.Cost,
			Metered:       t.Metered,
			Calls:         t.Calls,
			Turns:         t.Turns,
		})
	}
	return out
}

// anomalyViews reads the run's flagged cost anomalies from the authoritative store
// (ADR 0008). A run that never tripped a threshold, or a store error, yields nil.
func anomalyViews(toks *hubstore.Tokens, root, ticket string) []AnomalyView {
	flagged, err := toks.Anomalies(root, ticket)
	if err != nil || len(flagged) == 0 {
		return nil
	}
	out := make([]AnomalyView, 0, len(flagged))
	for _, a := range flagged {
		out = append(out, AnomalyView{
			Phase:   a.Phase,
			Output:  a.Output,
			Turns:   a.Turns,
			CostUSD: a.Cost,
			Reasons: a.Reasons,
		})
	}
	return out
}

// parseRubric parses a stored rubric artifact. Empty or malformed content reads
// as nil — degrade to "no rubric yet" rather than erroring the resource.
func parseRubric(content string) *RubricView {
	if content == "" {
		return nil
	}
	var rv RubricView
	if err := json.Unmarshal([]byte(content), &rv); err != nil {
		return nil
	}
	return &rv
}

// parseVerdict parses a stored verdict artifact. Empty or malformed content reads
// as nil — a run that has not been graded yet, not an error.
func parseVerdict(content string) *VerdictView {
	if content == "" {
		return nil
	}
	var vv VerdictView
	if err := json.Unmarshal([]byte(content), &vv); err != nil {
		return nil
	}
	return &vv
}
