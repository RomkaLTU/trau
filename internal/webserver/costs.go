package webserver

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tokens"
)

const (
	defaultWindowDays = 30
	maxWindowDays     = 365
	dateLayout        = "2006-01-02"
)

// CostsResponse is the /api/v1/costs resource: the machine-wide money view over a
// rolling window. It folds every repo's per-run tokens.jsonl into daily,
// per-repo, and per-phase rollups, carries the configured daily budget caps as
// context, and surfaces every flagged cost anomaly across repos.
type CostsResponse struct {
	WindowDays int           `json:"window_days"`
	From       string        `json:"from"`
	To         string        `json:"to"`
	Totals     CostSpend     `json:"totals"`
	Budget     CostBudget    `json:"budget"`
	Daily      []DailyCost   `json:"daily"`
	Repos      []RepoCost    `json:"repos"`
	Phases     []PhaseSpend  `json:"phases"`
	Anomalies  []CostAnomaly `json:"anomalies"`
}

// CostSpend is an accumulated (tokens, cost) figure. Metered is false when any
// contributing call recorded no per-call cost, so CostUSD is then a lower bound.
type CostSpend struct {
	Tokens  int     `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
	Metered bool    `json:"metered"`
}

// CostBudget is the machine-wide daily ceiling: the sum of every repo's
// configured daily cap, rendered as context against the daily spend chart.
type CostBudget struct {
	DailyUSD    float64 `json:"daily_usd,omitempty"`
	DailyTokens int     `json:"daily_tokens,omitempty"`
}

// DailyCost is one calendar day's spend across all repos. The window is
// zero-filled so the chart has a continuous date axis.
type DailyCost struct {
	Date    string  `json:"date"`
	Tokens  int     `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
	Metered bool    `json:"metered"`
}

// RepoCost is one repo's window spend plus its configured daily budget caps, so
// the breakdown reads actual against ceiling.
type RepoCost struct {
	Repo              string  `json:"repo"`
	Tokens            int     `json:"tokens"`
	CostUSD           float64 `json:"cost_usd"`
	Metered           bool    `json:"metered"`
	DailyBudgetUSD    float64 `json:"daily_budget_usd,omitempty"`
	DailyBudgetTokens int     `json:"daily_budget_tokens,omitempty"`
}

// PhaseSpend is one phase's spend summed across every repo in the window.
type PhaseSpend struct {
	Phase   string  `json:"phase"`
	Tokens  int     `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
	Metered bool    `json:"metered"`
}

// CostAnomaly is a flagged anomaly located to the run it belongs to, so the page
// can link straight to the affected run.
type CostAnomaly struct {
	Repo   string `json:"repo"`
	Ticket string `json:"ticket"`
	AnomalyView
}

func (s *Server) handleCosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	days := parseWindow(r.URL.Query().Get("days"))
	now := time.Now()
	to := now.Format(dateLayout)
	from := now.AddDate(0, 0, -(days - 1)).Format(dateLayout)
	writeJSON(w, http.StatusOK, s.costs(days, from, to))
}

// spend accumulates one (tokens, cost) figure, tracking whether every
// contributing call was metered.
type spend struct {
	tokens  int
	cost    float64
	metered bool
}

func (b *spend) add(c tokens.DayPhaseCost) {
	b.tokens += c.Tokens
	b.cost += c.Cost
	b.metered = b.metered && c.Metered
}

// costs folds every repo's rollup over [from, to] into the machine-wide daily,
// per-repo, and per-phase views, with budget caps and flagged anomalies attached.
func (s *Server) costs(days int, from, to string) CostsResponse {
	repos := s.repoViews()

	daily := map[string]*spend{}
	phases := map[string]*spend{}
	total := spend{metered: true}
	repoCosts := make([]RepoCost, 0, len(repos))
	anomalies := make([]CostAnomaly, 0)
	var budget CostBudget

	for _, rv := range repos {
		cells := tokens.New(rv.RunsDir).Rollup(from, to)
		rc := RepoCost{Repo: rv.Name, Metered: true}
		for _, c := range cells {
			bucket(daily, c.Date).add(c)
			bucket(phases, c.Phase).add(c)
			rc.Tokens += c.Tokens
			rc.CostUSD += c.Cost
			rc.Metered = rc.Metered && c.Metered
			total.add(c)
		}
		rc.CostUSD = round2(rc.CostUSD)
		rc.DailyBudgetUSD, rc.DailyBudgetTokens = s.repoDailyBudget(rv.Repo)
		budget.DailyUSD += rc.DailyBudgetUSD
		budget.DailyTokens += rc.DailyBudgetTokens

		if len(cells) > 0 || rc.DailyBudgetUSD > 0 || rc.DailyBudgetTokens > 0 {
			repoCosts = append(repoCosts, rc)
		}
		anomalies = append(anomalies, repoAnomalies(rv)...)
	}

	total.cost = round2(total.cost)
	sortRepoCosts(repoCosts)
	sortAnomalies(anomalies)

	return CostsResponse{
		WindowDays: days,
		From:       from,
		To:         to,
		Totals:     CostSpend{Tokens: total.tokens, CostUSD: total.cost, Metered: total.metered},
		Budget:     budget,
		Daily:      dailySeries(from, days, daily),
		Repos:      repoCosts,
		Phases:     phaseSeries(phases),
		Anomalies:  anomalies,
	}
}

func bucket(m map[string]*spend, key string) *spend {
	b := m[key]
	if b == nil {
		b = &spend{metered: true}
		m[key] = b
	}
	return b
}

// dailySeries renders the daily map as a zero-filled, date-ordered slice covering
// every day in the window so the chart axis is continuous.
func dailySeries(from string, days int, daily map[string]*spend) []DailyCost {
	start, err := time.Parse(dateLayout, from)
	if err != nil {
		return nil
	}
	out := make([]DailyCost, 0, days)
	for i := 0; i < days; i++ {
		date := start.AddDate(0, 0, i).Format(dateLayout)
		if b := daily[date]; b != nil {
			out = append(out, DailyCost{Date: date, Tokens: b.tokens, CostUSD: round2(b.cost), Metered: b.metered})
		} else {
			out = append(out, DailyCost{Date: date, Metered: true})
		}
	}
	return out
}

// phaseSeries renders the phase map as a slice ordered by spend, most expensive
// first, so the costliest phase leads the breakdown.
func phaseSeries(phases map[string]*spend) []PhaseSpend {
	out := make([]PhaseSpend, 0, len(phases))
	for phase, b := range phases {
		out = append(out, PhaseSpend{Phase: phase, Tokens: b.tokens, CostUSD: round2(b.cost), Metered: b.metered})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CostUSD != out[j].CostUSD {
			return out[i].CostUSD > out[j].CostUSD
		}
		if out[i].Tokens != out[j].Tokens {
			return out[i].Tokens > out[j].Tokens
		}
		return out[i].Phase < out[j].Phase
	})
	return out
}

func sortRepoCosts(repos []RepoCost) {
	sort.Slice(repos, func(i, j int) bool {
		if repos[i].CostUSD != repos[j].CostUSD {
			return repos[i].CostUSD > repos[j].CostUSD
		}
		if repos[i].Tokens != repos[j].Tokens {
			return repos[i].Tokens > repos[j].Tokens
		}
		return repos[i].Repo < repos[j].Repo
	})
}

func sortAnomalies(anomalies []CostAnomaly) {
	sort.Slice(anomalies, func(i, j int) bool {
		if anomalies[i].CostUSD != anomalies[j].CostUSD {
			return anomalies[i].CostUSD > anomalies[j].CostUSD
		}
		if anomalies[i].Repo != anomalies[j].Repo {
			return anomalies[i].Repo < anomalies[j].Repo
		}
		return anomalies[i].Ticket < anomalies[j].Ticket
	})
}

// repoAnomalies gathers every flagged anomaly across a repo's runs, located to
// the ticket that produced it. Enumerating via the checkpoint store confines
// reads to real run directories.
func repoAnomalies(rv RepoView) []CostAnomaly {
	store := state.NewStore(rv.RunsDir)
	sink := tokens.New(rv.RunsDir)
	var out []CostAnomaly
	for _, id := range store.Tickets() {
		for _, a := range sink.Anomalies(id) {
			out = append(out, CostAnomaly{
				Repo:   rv.Name,
				Ticket: id,
				AnomalyView: AnomalyView{
					Phase:   a.Phase,
					Output:  a.Output,
					Turns:   a.Turns,
					CostUSD: a.Cost,
					Reasons: a.Reasons,
				},
			})
		}
	}
	return out
}

// repoDailyBudget reads a repo's configured daily spend caps from its layered
// config (project and user layers). A parse error or an unset cap reads as zero.
func (s *Server) repoDailyBudget(repo registry.Repo) (usd float64, tokenCap int) {
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		return 0, 0
	}
	return cfg.MaxDailyUSD, cfg.MaxDailyTokens
}

func parseWindow(raw string) int {
	if raw == "" {
		return defaultWindowDays
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return defaultWindowDays
	}
	if n > maxWindowDays {
		return maxWindowDays
	}
	return n
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
