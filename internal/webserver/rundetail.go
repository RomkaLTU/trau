package webserver

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tokens"
)

// RunDetail is the /api/v1/repos/{repo}/runs/{ticket} resource: one ticket's
// full drill-down. It carries the checkpoint state keys (embedded RunView), the
// per-phase token/cost breakdown, and the QA artifacts — the handoff brief, the
// acceptance rubric, and the verify verdict. Every artifact is optional: an
// early-phase run that has not been handed off, graded, or opened a PR yet simply
// omits them, and Artifacts records which ones are present.
type RunDetail struct {
	RunView
	Costs     []PhaseCost   `json:"costs"`
	Anomalies []AnomalyView `json:"anomalies,omitempty"`
	Handoff   string        `json:"handoff,omitempty"`
	Rubric    *RubricView   `json:"rubric,omitempty"`
	Verdict   *VerdictView  `json:"verdict,omitempty"`
	Artifacts ArtifactSet   `json:"artifacts"`
	// NoSkills is true when this run's build loaded no skills in a repo that has
	// skills installed — the durable build_no_skills warning, surfaced so the
	// page can flag a silently skill-less build.
	NoSkills bool `json:"no_skills,omitempty"`
	// Removed is true when the run's ticket is a synced issue the tracker no longer
	// holds — deleted, archived, or moved out of the Project and tombstoned by a
	// reconciliation sweep. The run detail still renders from its durable artifacts;
	// the flag lets the page mark that the underlying ticket is gone.
	Removed bool `json:"removed,omitempty"`
}

// AnomalyView is one flagged cost anomaly for a run, read from
// runs/<ID>/anomalies.jsonl: the phase that cleared a soft threshold, its
// output/turns/cost, and the human reasons it was flagged.
type AnomalyView struct {
	Phase   string   `json:"phase"`
	Output  int      `json:"output"`
	Turns   int      `json:"turns"`
	CostUSD float64  `json:"cost_usd"`
	Reasons []string `json:"reasons"`
}

// PhaseCost is one phase's summed token + cost spend, read from the run's
// tokens.jsonl. Metered is false when any of the phase's calls recorded no
// per-call cost, so CostUSD is then a lower bound rather than a measured total.
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
// read from runs/<ID>/rubric.json.
type RubricView struct {
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
	NonGoals           []string `json:"non_goals,omitempty"`
	RequiredTests      []string `json:"required_tests,omitempty"`
	UIPaths            []string `json:"ui_paths,omitempty"`
	FailConditions     []string `json:"fail_conditions,omitempty"`
}

// VerdictView is the cold verifier's graded outcome, read from
// runs/<ID>/verdict.json: whether the slice passed, a one-line summary, the
// concrete failures, and any gated check results.
type VerdictView struct {
	Pass     bool        `json:"pass"`
	Summary  string      `json:"summary,omitempty"`
	Failures []string    `json:"failures,omitempty"`
	Checks   []CheckView `json:"checks,omitempty"`
}

// CheckView is one verify-check outcome carried inside the verdict.
type CheckView struct {
	Name     string `json:"name"`
	Severity string `json:"severity,omitempty"`
	Pass     bool   `json:"pass"`
	Detail   string `json:"detail,omitempty"`
}

// ArtifactSet flags which per-run artifacts exist on disk so the page can render
// a present-but-empty section differently from a not-yet-produced one.
type ArtifactSet struct {
	Handoff bool `json:"handoff"`
	Rubric  bool `json:"rubric"`
	Verdict bool `json:"verdict"`
	Tokens  bool `json:"tokens"`
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
	if !runExists(repo.RunsDir, ticket) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown run"})
		return
	}
	writeJSON(w, http.StatusOK, s.runDetail(repo, ticket))
}

// runDetail assembles a ticket's run detail from its durable artifacts and marks
// it removed when the ticket is a synced issue the tracker no longer holds — the
// store's tombstone. The lookup degrades to not-removed on any store error so a
// store hiccup never fails an otherwise-renderable run.
func (s *Server) runDetail(repo registry.Repo, ticket string) RunDetail {
	d := runDetail(repo.RunsDir, ticket)
	if iss, ok, err := s.stores.Issues().Get(repo.Root, ticket); err == nil && ok && iss.DeletedAt != "" {
		d.Removed = true
	}
	return d
}

// runExists reports whether ticket has a durable checkpoint under runsDir.
// Resolving against the known tickets both confirms the run and confines reads to
// a real run directory, so a traversal-shaped {ticket} never escapes runsDir.
func runExists(runsDir, ticket string) bool {
	if ticket == "" {
		return false
	}
	for _, id := range state.NewStore(runsDir).Tickets() {
		if id == ticket {
			return true
		}
	}
	return false
}

func runDetail(runsDir, ticket string) RunDetail {
	store := state.NewStore(runsDir)
	costs := phaseCosts(runsDir, ticket)
	handoff, hasHandoff := readArtifact(runsDir, ticket, "handoff.md")
	rubric := readRubric(runsDir, ticket)
	verdict := readVerdict(runsDir, ticket)
	return RunDetail{
		RunView:   runView(store, ticket),
		Costs:     costs,
		Anomalies: anomalyViews(runsDir, ticket),
		Handoff:   handoff,
		Rubric:    rubric,
		Verdict:   verdict,
		Artifacts: ArtifactSet{
			Handoff: hasHandoff,
			Rubric:  rubric != nil,
			Verdict: verdict != nil,
			Tokens:  len(costs) > 0,
		},
		NoSkills: hasNoSkillsWarning(runsDir, ticket),
	}
}

// hasNoSkillsWarning reports whether the repo event log carries a build_no_skills
// warning for ticket — a build that used none of the repo's installed skills.
func hasNoSkillsWarning(runsDir, ticket string) bool {
	events, _ := readFeed(eventsPath(runsDir))
	for _, ev := range events {
		if ev.Kind == event.KindBuildNoSkills && strField(ev.Fields, "ticket") == ticket {
			return true
		}
	}
	return false
}

func strField(fields map[string]any, key string) string {
	if s, ok := fields[key].(string); ok {
		return s
	}
	return ""
}

// phaseCosts reads the run's per-phase token/cost breakdown, in the order each
// phase first appears in tokens.jsonl.
func phaseCosts(runsDir, ticket string) []PhaseCost {
	totals := tokens.New(runsDir).PhaseTotals(ticket)
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

// anomalyViews reads the run's flagged cost anomalies from anomalies.jsonl. A
// run that never tripped a threshold yields nil.
func anomalyViews(runsDir, ticket string) []AnomalyView {
	flagged := tokens.New(runsDir).Anomalies(ticket)
	if len(flagged) == 0 {
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

// readArtifact returns the text of runs/<ticket>/<name> and whether a non-empty
// file was present.
func readArtifact(runsDir, ticket, name string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(runsDir, ticket, name))
	if err != nil || len(data) == 0 {
		return "", false
	}
	return string(data), true
}

// readRubric parses runs/<ticket>/rubric.json. A missing or malformed rubric
// reads as nil — degrade to "no rubric yet" rather than erroring the resource.
func readRubric(runsDir, ticket string) *RubricView {
	data, err := os.ReadFile(filepath.Join(runsDir, ticket, "rubric.json"))
	if err != nil {
		return nil
	}
	var rv RubricView
	if err := json.Unmarshal(data, &rv); err != nil {
		return nil
	}
	return &rv
}

// readVerdict parses runs/<ticket>/verdict.json. A missing or malformed verdict
// reads as nil — a run that has not been graded yet, not an error.
func readVerdict(runsDir, ticket string) *VerdictView {
	data, err := os.ReadFile(filepath.Join(runsDir, ticket, "verdict.json"))
	if err != nil {
		return nil
	}
	var vv VerdictView
	if err := json.Unmarshal(data, &vv); err != nil {
		return nil
	}
	return &vv
}
