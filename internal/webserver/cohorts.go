package webserver

import (
	"maps"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
)

// unknownCohort names the cohort calls with no recorded config_hash fold into —
// everything the ledger logged before it fingerprinted the routing config.
const unknownCohort = "unknown"

const maxConfigChangeEvents = 1000

// ConfigCohortsResponse is the /repos/{repo}/metrics/config-cohorts resource: the
// repo's token ledger grouped into the routing configurations it ran under, newest
// cohort first. Since and Until echo the requested window; Phase echoes the filter
// applied to each cohort's phase breakdown — cohort totals and pipeline rates stay
// whole under it, since a repair rate measured over one phase means nothing.
type ConfigCohortsResponse struct {
	Repo    string         `json:"repo"`
	Since   string         `json:"since,omitempty"`
	Until   string         `json:"until,omitempty"`
	Phase   string         `json:"phase,omitempty"`
	Cohorts []ConfigCohort `json:"cohorts"`
}

// ConfigCohort is every call one routing configuration produced: what it ran under,
// what it cost, and how the pipeline behaved while it was in force. CostUSD is a
// lower bound when Metered is false. Routing is the resolved key/value fingerprint
// behind the hash, absent for a cohort whose boundary event the feed no longer
// holds.
type ConfigCohort struct {
	Hash            string            `json:"hash"`
	FirstSeen       string            `json:"first_seen"`
	LastSeen        string            `json:"last_seen"`
	Tickets         int               `json:"tickets"`
	Calls           int               `json:"calls"`
	CostUSD         float64           `json:"cost_usd"`
	Metered         bool              `json:"metered"`
	CostPerTicket   float64           `json:"cost_per_ticket"`
	VerifyRetryRate float64           `json:"verify_retry_rate"`
	RepairRate      float64           `json:"repair_rate"`
	Routing         map[string]string `json:"routing,omitempty"`
	Phases          []CohortPhase     `json:"phases"`
}

// CohortPhase is one canonical phase's cost and speed inside a cohort, with the
// route the fingerprint sent it to. Retry labels fold into the phase they retry, so
// verify carries its own retries. Averages are per call.
type CohortPhase struct {
	Phase         string  `json:"phase"`
	Provider      string  `json:"provider,omitempty"`
	Model         string  `json:"model,omitempty"`
	Effort        string  `json:"effort,omitempty"`
	Calls         int     `json:"calls"`
	CostUSD       float64 `json:"cost_usd"`
	AvgCostUSD    float64 `json:"avg_cost_usd"`
	AvgDurationMS int64   `json:"avg_duration_ms"`
	AvgTurns      float64 `json:"avg_turns"`
	AvgContext    int     `json:"avg_context"`
	Metered       bool    `json:"metered"`
}

// handleConfigCohorts serves repo's ledger grouped by configuration cohort (GET) —
// the read that answers whether a routing change moved cost or speed. ?since= and
// ?until= bound the window by local date; ?phase= narrows the per-phase breakdown
// to one canonical phase.
func (s *Server) handleConfigCohorts(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	query := r.URL.Query()
	since, sinceOK := parseCohortDate(query.Get("since"))
	until, untilOK := parseCohortDate(query.Get("until"))
	if !sinceOK || !untilOK {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "since and until must be YYYY-MM-DD dates"})
		return
	}

	filter := hubstore.CohortFilter{Since: since, Until: until}
	totals, err := s.stores.Tokens().ConfigCohortTotals(repo.Root, filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	cells, err := s.stores.Tokens().ConfigCohortPhases(repo.Root, filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	phase := strings.TrimSpace(query.Get("phase"))

	writeJSON(w, http.StatusOK, ConfigCohortsResponse{
		Repo:    repo.Name,
		Since:   since,
		Until:   until,
		Phase:   phase,
		Cohorts: configCohorts(totals, cells, s.cohortRouting(repo.Root), phase),
	})
}

// parseCohortDate accepts an empty (unbounded) or YYYY-MM-DD window bound. A bound
// that does not parse is rejected rather than ignored: silently dropping the window
// would answer a different question than the one asked.
func parseCohortDate(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	if _, err := time.Parse(dateLayout, raw); err != nil {
		return "", false
	}
	return raw, true
}

func configCohorts(
	totals []hubstore.CohortTotal,
	cells []hubstore.CohortPhaseCell,
	routing map[string]map[string]string,
	phase string,
) []ConfigCohort {
	byHash := map[string][]hubstore.CohortPhaseCell{}
	for _, c := range cells {
		byHash[c.Hash] = append(byHash[c.Hash], c)
	}
	out := make([]ConfigCohort, 0, len(totals))
	for _, t := range totals {
		keys := routing[t.Hash]
		out = append(out, ConfigCohort{
			Hash:            cohortName(t.Hash),
			FirstSeen:       t.FirstTS,
			LastSeen:        t.LastTS,
			Tickets:         t.Tickets,
			Calls:           t.Calls,
			CostUSD:         round2(t.Cost),
			Metered:         t.Metered,
			CostPerTicket:   round4(perTicket(t.Cost, t.Tickets)),
			VerifyRetryRate: rate(t.RetryCalls, t.VerifyCalls),
			RepairRate:      rate(t.RepairCalls, t.VerifyCalls),
			Routing:         keys,
			Phases:          cohortPhases(byHash[t.Hash], keys, phase),
		})
	}
	return out
}

type phaseSums struct {
	calls    int
	cost     float64
	duration int64
	turns    int
	context  int
	metered  bool
}

// cohortPhases folds a cohort's raw phase labels into canonical phases, emitted in
// pipeline order, so verify carries its own retries.
func cohortPhases(cells []hubstore.CohortPhaseCell, keys map[string]string, phase string) []CohortPhase {
	sums := make(map[string]*phaseSums, len(cells))
	for _, c := range cells {
		key := agent.RouteKey(c.Phase)
		s := sums[key]
		if s == nil {
			s = &phaseSums{metered: true}
			sums[key] = s
		}
		s.calls += c.Calls
		s.cost += c.Cost
		s.duration += c.DurationMS
		s.turns += c.Turns
		s.context += c.Context
		s.metered = s.metered && c.Metered
	}

	out := make([]CohortPhase, 0, len(sums))
	for _, name := range agent.Phases {
		s := sums[name]
		if s == nil || (phase != "" && phase != name) {
			continue
		}
		provider, model, effort := routeParts(keys["PHASE_"+strings.ToUpper(name)])
		calls := float64(s.calls)
		out = append(out, CohortPhase{
			Phase:         name,
			Provider:      provider,
			Model:         model,
			Effort:        effort,
			Calls:         s.calls,
			CostUSD:       round2(s.cost),
			AvgCostUSD:    round4(s.cost / calls),
			AvgDurationMS: int64(math.Round(float64(s.duration) / calls)),
			AvgTurns:      round2(float64(s.turns) / calls),
			AvgContext:    int(math.Round(float64(s.context) / calls)),
			Metered:       s.metered,
		})
	}
	return out
}

// cohortRouting resolves each config_hash to the routing values behind it by
// replaying the repo's config_change events in order: every event carries only the
// keys that moved. The repo's current fingerprint is overlaid last, so the live
// cohort resolves in full even once retention has pruned its boundary event. A store
// error degrades to unresolved cohorts rather than failing the resource.
func (s *Server) cohortRouting(root string) map[string]map[string]string {
	evs, err := s.stores.Events().Query(root, hubstore.EventFilter{
		Kind:  event.KindConfigChange,
		Limit: maxConfigChangeEvents,
	})
	if err != nil {
		logger.Verbosef("config cohort routing %s: %v", root, err)
	}
	out := make(map[string]map[string]string, len(evs)+1)
	keys := map[string]string{}
	for _, ev := range evs {
		fields := unmarshalFields(ev.Fields)
		hash, _ := fields["hash"].(string)
		applyRoutingChanges(keys, fields["changes"])
		out[hash] = maps.Clone(keys)
	}
	last, err := s.stores.Routing().Last(root)
	if err != nil {
		logger.Verbosef("config cohort routing %s: %v", root, err)
		return out
	}
	if last.Hash != "" && len(last.Keys) > 0 {
		out[last.Hash] = last.Keys
	}
	return out
}

func applyRoutingChanges(keys map[string]string, raw any) {
	changes, _ := raw.([]any)
	for _, c := range changes {
		change, _ := c.(map[string]any)
		key, _ := change["key"].(string)
		if key == "" {
			continue
		}
		to, _ := change["to"].(string)
		keys[key] = to
	}
}

// routeParts splits a fingerprint's per-phase route — provider:model:effort, as
// config.ResolveRouting renders it — into its three fields.
func routeParts(spec string) (provider, model, effort string) {
	provider, rest, _ := strings.Cut(spec, ":")
	model, effort, _ = strings.Cut(rest, ":")
	return provider, model, effort
}

func cohortName(hash string) string {
	if hash == "" {
		return unknownCohort
	}
	return hash
}

func perTicket(cost float64, tickets int) float64 {
	if tickets == 0 {
		return 0
	}
	return cost / float64(tickets)
}

func rate(n, of int) float64 {
	if of == 0 {
		return 0
	}
	return round3(float64(n) / float64(of))
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
