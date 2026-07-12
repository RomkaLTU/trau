package webserver

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/tokens"
)

// TimeseriesResponse is the /api/v1/costs/timeseries resource: machine-wide spend
// over a window, split into one series per value of the group-by dimension and
// narrowed by any repo/provider/model/phase filters. Each series carries a
// zero-filled daily point sequence so the chart axis is continuous, plus its
// window total. Facets list every dimension value present in the window (before
// filtering) so the UI can populate its filter controls. Comparing two windows is
// a client concern: request the endpoint twice with different ranges and diff the
// per-series totals.
type TimeseriesResponse struct {
	From    string            `json:"from"`
	To      string            `json:"to"`
	Days    int               `json:"days"`
	GroupBy string            `json:"group_by"`
	Totals  CostSpend         `json:"totals"`
	Series  []TimeseriesGroup `json:"series"`
	Facets  CostFacets        `json:"facets"`
}

// TimeseriesGroup is one series: the spend for a single group-by value across the
// window, with its zero-filled daily points and window total.
type TimeseriesGroup struct {
	Key     string            `json:"key"`
	Tokens  int               `json:"tokens"`
	CostUSD float64           `json:"cost_usd"`
	Metered bool              `json:"metered"`
	Points  []TimeseriesPoint `json:"points"`
}

// TimeseriesPoint is one calendar day of a series' spend.
type TimeseriesPoint struct {
	Date    string  `json:"date"`
	Tokens  int     `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
}

// CostFacets is the set of dimension values present in the window, so the UI can
// offer them as filters regardless of the current selection.
type CostFacets struct {
	Repos     []string `json:"repos"`
	Providers []string `json:"providers"`
	Models    []string `json:"models"`
	Phases    []string `json:"phases"`
}

func (s *Server) handleTimeseries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	q := r.URL.Query()
	from, to, days := parseRange(q)
	groupBy := parseGroupBy(q.Get("group_by"))
	filter := costFilter{
		repos:     parseSet(q, "repo"),
		providers: parseSet(q, "provider"),
		models:    parseSet(q, "model"),
		phases:    parseSet(q, "phase"),
	}
	writeJSON(w, http.StatusOK, s.timeseries(from, to, days, groupBy, filter))
}

// costFilter narrows the cells folded into the series: a cell must match every
// non-empty dimension (AND across dimensions, OR within one).
type costFilter struct {
	repos, providers, models, phases map[string]bool
}

func (f costFilter) allow(repo string, c tokens.DetailCost) bool {
	return inSet(f.repos, repo) && inSet(f.providers, orUnknown(c.Provider)) &&
		inSet(f.models, orUnknown(c.Model)) && inSet(f.phases, orUnknown(c.Phase))
}

func inSet(set map[string]bool, v string) bool {
	return len(set) == 0 || set[v]
}

// timeseries folds every repo's detail rollup over [from, to] into one series per
// group-by value, filtered by the given dimensions. Facets are gathered from the
// unfiltered window so the filter controls stay stable as selections change.
func (s *Server) timeseries(from, to string, days int, groupBy string, filter costFilter) TimeseriesResponse {
	type series struct {
		total  spend
		byDate map[string]*spend
	}
	groups := map[string]*series{}
	total := spend{metered: true}
	facetRepos, facetProviders, facetModels, facetPhases := set{}, set{}, set{}, set{}

	cells := s.costCellsByRepo(from, to)
	for _, rv := range s.repoViews() {
		for _, cell := range cells[rv.Root] {
			c := detailCost(cell)
			facetRepos.add(rv.Name)
			facetProviders.add(orUnknown(c.Provider))
			facetModels.add(orUnknown(c.Model))
			facetPhases.add(orUnknown(c.Phase))

			if !filter.allow(rv.Name, c) {
				continue
			}
			key := groupKey(groupBy, rv.Name, c)
			g := groups[key]
			if g == nil {
				g = &series{byDate: map[string]*spend{}}
				groups[key] = g
			}
			g.total.addRaw(c.Tokens, c.Cost, c.Metered)
			bucket(g.byDate, c.Date).addRaw(c.Tokens, c.Cost, c.Metered)
			total.addRaw(c.Tokens, c.Cost, c.Metered)
		}
	}

	out := make([]TimeseriesGroup, 0, len(groups))
	for key, g := range groups {
		out = append(out, TimeseriesGroup{
			Key:     key,
			Tokens:  g.total.tokens,
			CostUSD: round2(g.total.cost),
			Metered: g.total.metered,
			Points:  points(from, days, g.byDate),
		})
	}
	sortGroups(out)

	return TimeseriesResponse{
		From:    from,
		To:      to,
		Days:    days,
		GroupBy: groupBy,
		Totals:  CostSpend{Tokens: total.tokens, CostUSD: round2(total.cost), Metered: total.metered},
		Series:  out,
		Facets: CostFacets{
			Repos:     facetRepos.sorted(),
			Providers: facetProviders.sorted(),
			Models:    facetModels.sorted(),
			Phases:    facetPhases.sorted(),
		},
	}
}

// detailCost resolves a store cost cell into the analytics grain, applying the
// read-side provider fallback for older lines logged before the provider was
// recorded inline.
func detailCost(c hubstore.CostCell) tokens.DetailCost {
	provider := c.Provider
	if provider == "" {
		provider = tokens.ProviderForModel(c.Model)
	}
	return tokens.DetailCost{
		Date:     c.Date,
		Phase:    c.Phase,
		Provider: provider,
		Model:    c.Model,
		Tokens:   c.Tokens,
		Cost:     c.Cost,
		Metered:  c.Metered,
	}
}

func groupKey(groupBy, repo string, c tokens.DetailCost) string {
	switch groupBy {
	case "repo":
		return repo
	case "model":
		return orUnknown(c.Model)
	case "phase":
		return orUnknown(c.Phase)
	default:
		return orUnknown(c.Provider)
	}
}

// points renders a series' daily map as a zero-filled, date-ordered slice covering
// every day in the window, mirroring the daily chart's continuous axis.
func points(from string, days int, byDate map[string]*spend) []TimeseriesPoint {
	start, err := time.Parse(dateLayout, from)
	if err != nil {
		return nil
	}
	out := make([]TimeseriesPoint, 0, days)
	for i := 0; i < days; i++ {
		date := start.AddDate(0, 0, i).Format(dateLayout)
		if b := byDate[date]; b != nil {
			out = append(out, TimeseriesPoint{Date: date, Tokens: b.tokens, CostUSD: round2(b.cost)})
		} else {
			out = append(out, TimeseriesPoint{Date: date})
		}
	}
	return out
}

func sortGroups(g []TimeseriesGroup) {
	sort.Slice(g, func(i, j int) bool {
		if g[i].CostUSD != g[j].CostUSD {
			return g[i].CostUSD > g[j].CostUSD
		}
		if g[i].Tokens != g[j].Tokens {
			return g[i].Tokens > g[j].Tokens
		}
		return g[i].Key < g[j].Key
	})
}

func orUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

type set map[string]bool

func (s set) add(v string) { s[v] = true }

func (s set) sorted() []string {
	out := make([]string, 0, len(s))
	for v := range s {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// parseRange resolves the window. Explicit from/to dates win when both parse and
// order correctly (the two-window comparison path), clamped to maxWindowDays;
// otherwise it is the trailing days-long window ending today.
func parseRange(q url.Values) (from, to string, days int) {
	if f, t := q.Get("from"), q.Get("to"); f != "" && t != "" {
		fd, ferr := time.Parse(dateLayout, f)
		td, terr := time.Parse(dateLayout, t)
		if ferr == nil && terr == nil && !fd.After(td) {
			span := int(td.Sub(fd).Hours()/24) + 1
			if span > maxWindowDays {
				fd = td.AddDate(0, 0, -(maxWindowDays - 1))
				span = maxWindowDays
			}
			return fd.Format(dateLayout), td.Format(dateLayout), span
		}
	}
	days = parseWindow(q.Get("days"))
	now := time.Now()
	return now.AddDate(0, 0, -(days - 1)).Format(dateLayout), now.Format(dateLayout), days
}

func parseGroupBy(raw string) string {
	switch raw {
	case "repo", "provider", "model", "phase":
		return raw
	default:
		return "provider"
	}
}

// parseSet reads a filter dimension from the query, accepting both repeated keys
// and comma-separated values. An absent or empty dimension yields nil (no filter).
func parseSet(q url.Values, key string) map[string]bool {
	vals := q[key]
	if len(vals) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, v := range vals {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out[part] = true
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
