// Package tokens persists the loop's normalized per-call token/cost accounting.
//
// After every agent call the runner appends one JSON line to
// runs/<bucket>/tokens.jsonl, phase-labeled; summed across a ticket's phases
// this is its total token spend (consumed by Total / --status). The schema and
// the single total formula are fixed so logged lines stay stable
// and directly comparable across runs:
//
//	{ts, phase, input, output, cache_read, cache_creation, reasoning,
//	 total, cost_usd, turns, is_error}   with total = input+output+cache_read+cache_creation
//
// Normalization: input is stored as the non-cached portion for every provider
// (claude's usage.input_tokens already excludes cache; codex's includes it and
// is adjusted), so the columns and total mean the same thing regardless of
// backend. Zero-total / uncaptured calls are dropped (select on total > 0).
//
// runs/ root is a directory supplied to [New] — main resolves it from the
// RUNS_DIR knob, defaulting to ./runs (relative to the working directory). runs/
// is gitignored. Writes are failure-tolerant: a marshal/mkdir/write error is
// dropped on purpose so token logging can never abort the loop (same contract as
// event.Log).
package tokens

import (
	"bufio"
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const loopBucket = "_loop"

// PlanBucket is the shared token bucket for the planning module. Planning agent
// calls carry no ticket, so [Sink.SetTicket] points them here — runs/_plans/ —
// keeping planning spend out of _loop and letting --status surface it as one
// planning row folded into the session total. It sits alongside the per-session
// plan directories under runs/_plans/, which never hold a tokens.jsonl of their own.
const PlanBucket = "_plans"

// Record is one call's raw, already-normalized counts, handed to [Sink.Append].
// Input is the NON-cached input portion (see the package doc). CostUSD is a
// pointer so a provider that reports no per-call cost (codex on a ChatGPT-plan
// login) records a JSON null; the claude path always supplies a value
// (0 when the field is absent).
type Record struct {
	Input         int
	Output        int
	CacheRead     int
	CacheCreation int
	Reasoning     int
	CostUSD       *float64
	Turns         int
	IsError       bool
	Provider      string
	Model         string
	Context       int
	Skills        []string
}

type line struct {
	TS            string   `json:"ts"`
	Phase         string   `json:"phase"`
	Input         int      `json:"input"`
	Output        int      `json:"output"`
	CacheRead     int      `json:"cache_read"`
	CacheCreation int      `json:"cache_creation"`
	Reasoning     int      `json:"reasoning"`
	Total         int      `json:"total"`
	CostUSD       *float64 `json:"cost_usd"`
	Turns         int      `json:"turns"`
	IsError       bool     `json:"is_error"`
	Provider      string   `json:"provider,omitempty"`
	Model         string   `json:"model,omitempty"`
	Context       int      `json:"context,omitempty"`
	Skills        []string `json:"skills,omitempty"`
}

// Sink appends normalized token lines under a runs/ root, bucketed by the current
// ticket. The runner calls [Sink.Append] after each agent call; the main loop
// calls [Sink.SetTicket] when it enters/leaves a ticket so in-ticket calls land
// in runs/<ID>/ and everything else falls back to runs/_loop/. It is safe for
// concurrent use.
type Sink struct {
	root   string
	mu     sync.Mutex
	bucket string
	now    func() time.Time
	// session holds only what THIS process appended, per bucket — the persisted
	// tokens.jsonl accumulates across runs, so a resumed ticket's file also
	// carries earlier sessions' spend. Session views (SessionTotal, Flag) read
	// this ledger so a 4-minute rerun never reports a prior run's dollars.
	session map[string]*sessionSpend
}

type sessionSpend struct {
	tokens  int
	cost    float64
	metered bool
	phases  map[string]*phaseSpend
	order   []string
}

type phaseSpend struct {
	output, turns int
	cost          float64
}

// New returns a Sink rooting per-ticket artifacts at root (the runs/ directory).
func New(root string) *Sink {
	return &Sink{root: root, now: time.Now, session: map[string]*sessionSpend{}}
}

// WithClock overrides the timestamp source; intended for deterministic tests.
func (s *Sink) WithClock(now func() time.Time) *Sink {
	s.now = now
	return s
}

// Root returns the runs/ directory this Sink writes under (used by --status to
// enumerate runs/*/state alongside the token totals).
func (s *Sink) Root() string { return s.root }

// SetTicket points subsequent Appends at runs/<id>/tokens.jsonl. An empty id
// resets to the _loop bucket (the loop sets the current ticket on entry and
// clears it on exit).
func (s *Sink) SetTicket(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bucket = id
}

// Append writes one normalized line for a phase. Calls whose total is zero are
// dropped (uncaptured/empty calls). Any I/O or marshal error is swallowed so
// logging never aborts the loop.
func (s *Sink) Append(phase string, rec Record) {
	total := rec.Input + rec.Output + rec.CacheRead + rec.CacheCreation
	if total <= 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	bucket := s.bucket
	if bucket == "" {
		bucket = loopBucket
	}
	s.recordSession(bucket, phase, rec, total)
	ln := line{
		TS:            s.now().Format("2006-01-02T15:04:05"),
		Phase:         phase,
		Input:         rec.Input,
		Output:        rec.Output,
		CacheRead:     rec.CacheRead,
		CacheCreation: rec.CacheCreation,
		Reasoning:     rec.Reasoning,
		Total:         total,
		CostUSD:       rec.CostUSD,
		Turns:         rec.Turns,
		IsError:       rec.IsError,
		Provider:      rec.Provider,
		Model:         rec.Model,
		Context:       rec.Context,
		Skills:        rec.Skills,
	}
	data, err := json.Marshal(ln)
	if err != nil {
		return
	}
	dir := filepath.Join(s.root, bucket)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "tokens.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(data, '\n'))
}

// recordSession folds one appended call into the in-memory session ledger.
// Caller holds s.mu.
func (s *Sink) recordSession(bucket, phase string, rec Record, total int) {
	if s.session == nil {
		s.session = map[string]*sessionSpend{}
	}
	sp := s.session[bucket]
	if sp == nil {
		sp = &sessionSpend{metered: true, phases: map[string]*phaseSpend{}}
		s.session[bucket] = sp
	}
	sp.tokens += total
	if rec.CostUSD != nil {
		sp.cost += *rec.CostUSD
	} else {
		sp.metered = false
	}
	ps := sp.phases[phase]
	if ps == nil {
		ps = &phaseSpend{}
		sp.phases[phase] = ps
		sp.order = append(sp.order, phase)
	}
	ps.output += rec.Output
	ps.turns += rec.Turns
	if rec.CostUSD != nil {
		ps.cost += *rec.CostUSD
	}
}

// SessionTotal sums the token + cost spend THIS process recorded for id,
// following Total's metered contract. Unlike Total it excludes spend loaded
// from earlier runs' tokens.jsonl, so the end-of-session summary reflects what
// the session actually spent.
func (s *Sink) SessionTotal(id string) (tokens int, cost float64, metered bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp := s.session[id]
	if sp == nil {
		return 0, 0, true
	}
	return sp.tokens, math.Round(sp.cost*100) / 100, sp.metered
}

// Total sums a ticket's logged token + cost spend across all phases from
// runs/<id>/tokens.jsonl. cost is summed raw then rounded once to cents
// ((c*100|round)/100). metered is false when any logged line carried no per-call
// cost (a codex/kimi subscription call that records tokens but no dollars): the
// summed cost is then a lower bound, not a measured total, so callers surface
// that rather than printing a misleading $0. A missing, empty, or unreadable file
// yields (0, 0, true) — never an error — so callers can print it unconditionally.
// Malformed lines are skipped (the writer only ever emits valid JSON).
func (s *Sink) Total(id string) (tokens int, cost float64, metered bool) {
	metered = true
	f, err := os.Open(filepath.Join(s.root, id, "tokens.jsonl"))
	if err != nil {
		return 0, 0, true
	}
	defer func() { _ = f.Close() }()

	var sum float64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var ln line
		if err := json.Unmarshal(b, &ln); err != nil {
			continue
		}
		tokens += ln.Total
		if ln.CostUSD != nil {
			sum += *ln.CostUSD
		} else {
			metered = false
		}
	}
	return tokens, math.Round(sum*100) / 100, metered
}

// PhaseTotal is one phase's summed token + cost spend across all of its logged
// calls in a ticket's tokens.jsonl. Metered carries the same lower-bound contract
// as Total: false when any of the phase's lines recorded no per-call cost, so Cost
// is then a floor rather than a measured total.
type PhaseTotal struct {
	Phase         string
	Input         int
	Output        int
	CacheRead     int
	CacheCreation int
	Reasoning     int
	Total         int
	Cost          float64
	Turns         int
	Calls         int
	Metered       bool
}

// PhaseTotals breaks a ticket's spend down by phase, one row per distinct phase
// label in runs/<id>/tokens.jsonl, in the order each phase first appears in the
// log. Costs sum raw then round once to cents per phase, matching Total. A
// missing, empty, or unreadable file yields nil — never an error — so callers can
// render an empty table unconditionally. Malformed lines are skipped.
func (s *Sink) PhaseTotals(id string) []PhaseTotal {
	f, err := os.Open(filepath.Join(s.root, id, "tokens.jsonl"))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var out []PhaseTotal
	idx := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var ln line
		if err := json.Unmarshal(b, &ln); err != nil {
			continue
		}
		i, ok := idx[ln.Phase]
		if !ok {
			i = len(out)
			idx[ln.Phase] = i
			out = append(out, PhaseTotal{Phase: ln.Phase, Metered: true})
		}
		p := &out[i]
		p.Input += ln.Input
		p.Output += ln.Output
		p.CacheRead += ln.CacheRead
		p.CacheCreation += ln.CacheCreation
		p.Reasoning += ln.Reasoning
		p.Total += ln.Total
		p.Turns += ln.Turns
		p.Calls++
		if ln.CostUSD != nil {
			p.Cost += *ln.CostUSD
		} else {
			p.Metered = false
		}
	}
	for i := range out {
		out[i].Cost = math.Round(out[i].Cost*100) / 100
	}
	return out
}

// DayTotal sums token + cost spend across ALL buckets for calls whose timestamp
// falls on the given local date (YYYY-MM-DD) — the per-day window the budget caps
// enforce. It globs runs/<bucket>/tokens.jsonl (including the _loop bucket, since
// picker spend still counts toward the day), scans each, and keeps only lines whose
// ts begins with date. metered follows the same lower-bound contract as Total:
// false when any in-window line carried no per-call cost. A runs/ root with no logs
// yields (0, 0, true) — never an error — so callers can print it unconditionally.
func (s *Sink) DayTotal(date string) (tokens int, cost float64, metered bool) {
	metered = true
	matches, _ := filepath.Glob(filepath.Join(s.root, "*", "tokens.jsonl"))

	var sum float64
	for _, path := range matches {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			b := bytes.TrimSpace(sc.Bytes())
			if len(b) == 0 {
				continue
			}
			var ln line
			if err := json.Unmarshal(b, &ln); err != nil {
				continue
			}
			if !strings.HasPrefix(ln.TS, date) {
				continue
			}
			tokens += ln.Total
			if ln.CostUSD != nil {
				sum += *ln.CostUSD
			} else {
				metered = false
			}
		}
		_ = f.Close()
	}
	return tokens, math.Round(sum*100) / 100, metered
}

// DayPhaseCost is one (local date, phase) cell of spend, summed across every
// bucket under a runs root. Cost is left unrounded so a caller folding cells
// across repos rounds once at the end; Metered is false when any contributing
// line carried no per-call cost, so Cost is then a lower bound.
type DayPhaseCost struct {
	Date    string
	Phase   string
	Tokens  int
	Cost    float64
	Metered bool
}

// Rollup scans every runs/<bucket>/tokens.jsonl under the root and returns one
// cell per (local date, phase) whose date falls within [from, to] inclusive
// (YYYY-MM-DD bounds, compared lexically). It is the machine-wide costs page's
// per-repo reader: summing cells over phase gives a day's spend, over date gives
// a phase's spend, and over both a repo's window total. A runs root with no
// in-window logs yields nil — never an error. Malformed lines are skipped.
func (s *Sink) Rollup(from, to string) []DayPhaseCost {
	matches, _ := filepath.Glob(filepath.Join(s.root, "*", "tokens.jsonl"))

	type key struct{ date, phase string }
	cells := map[key]*DayPhaseCost{}
	var order []key
	for _, path := range matches {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			b := bytes.TrimSpace(sc.Bytes())
			if len(b) == 0 {
				continue
			}
			var ln line
			if err := json.Unmarshal(b, &ln); err != nil {
				continue
			}
			if len(ln.TS) < 10 {
				continue
			}
			date := ln.TS[:10]
			if date < from || date > to {
				continue
			}
			k := key{date, ln.Phase}
			c := cells[k]
			if c == nil {
				c = &DayPhaseCost{Date: date, Phase: ln.Phase, Metered: true}
				cells[k] = c
				order = append(order, k)
			}
			c.Tokens += ln.Total
			if ln.CostUSD != nil {
				c.Cost += *ln.CostUSD
			} else {
				c.Metered = false
			}
		}
		_ = f.Close()
	}
	if len(order) == 0 {
		return nil
	}
	out := make([]DayPhaseCost, 0, len(order))
	for _, k := range order {
		out = append(out, *cells[k])
	}
	return out
}

// DetailCost is the analytics reader's finest grain: one (date, provider, model,
// phase) cell of spend under a runs root, keeping the model and its resolved
// provider so callers can regroup and filter along any dimension. Cost is left
// unrounded so a caller folding cells across repos rounds once at the end;
// Metered is false when any contributing line carried no per-call cost.
type DetailCost struct {
	Date     string
	Phase    string
	Provider string
	Model    string
	Tokens   int
	Cost     float64
	Metered  bool
}

// RollupDetail scans every runs/<bucket>/tokens.jsonl under the root and returns
// one cell per (local date, provider, model, phase) within [from, to] inclusive.
// Each line's provider is the one recorded inline, falling back to [ProviderForModel]
// for historical lines logged before the provider was persisted. It is the
// analytics endpoint's per-repo reader; summing cells across any subset of
// dimensions yields that grouping's spend. A root with no in-window logs yields
// nil — never an error. Malformed lines are skipped.
func (s *Sink) RollupDetail(from, to string) []DetailCost {
	matches, _ := filepath.Glob(filepath.Join(s.root, "*", "tokens.jsonl"))

	type key struct{ date, provider, model, phase string }
	cells := map[key]*DetailCost{}
	var order []key
	for _, path := range matches {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			b := bytes.TrimSpace(sc.Bytes())
			if len(b) == 0 {
				continue
			}
			var ln line
			if err := json.Unmarshal(b, &ln); err != nil {
				continue
			}
			if len(ln.TS) < 10 {
				continue
			}
			date := ln.TS[:10]
			if date < from || date > to {
				continue
			}
			provider := ln.Provider
			if provider == "" {
				provider = ProviderForModel(ln.Model)
			}
			k := key{date, provider, ln.Model, ln.Phase}
			c := cells[k]
			if c == nil {
				c = &DetailCost{Date: date, Provider: provider, Model: ln.Model, Phase: ln.Phase, Metered: true}
				cells[k] = c
				order = append(order, k)
			}
			c.Tokens += ln.Total
			if ln.CostUSD != nil {
				c.Cost += *ln.CostUSD
			} else {
				c.Metered = false
			}
		}
		_ = f.Close()
	}
	if len(order) == 0 {
		return nil
	}
	out := make([]DetailCost, 0, len(order))
	for _, k := range order {
		out = append(out, *cells[k])
	}
	return out
}

// Pair returns Total(id) rendered as the "<tokens> <cost>" string that --status
// consumes (e.g. "0 0", "15234 1.2").
func (s *Sink) Pair(id string) string {
	t, c, _ := s.Total(id)
	return FormatPair(t, c)
}

// FormatPair renders a token/cost pair as "<tokens> <cost>": the cost is printed
// with jq's number formatting — the shortest decimal with no trailing zeros, so
// 0 → "0", 1.20 → "1.2", 1.25 → "1.25".
func FormatPair(tokens int, cost float64) string {
	return strconv.Itoa(tokens) + " " + strconv.FormatFloat(cost, 'f', -1, 64)
}

type modelRate struct{ input, output float64 }

var rates = []struct {
	match string
	modelRate
}{
	{"opus-4-8", modelRate{5, 25}},
	{"opus-4-7", modelRate{5, 25}},
	{"opus-4-6", modelRate{5, 25}},
	{"opus-4-5", modelRate{5, 25}},
	{"opus", modelRate{5, 25}},
	{"sonnet-5", modelRate{3, 15}},
	{"sonnet-4-6", modelRate{3, 15}},
	{"sonnet", modelRate{3, 15}},
	{"haiku-4-5", modelRate{1, 5}},
	{"haiku", modelRate{1, 5}},
	{"fable-5", modelRate{10, 50}},
	{"fable", modelRate{10, 50}},
	{"mythos-5", modelRate{10, 50}},
}

// EstimateCost returns the notional USD cost of one call from its token counts
// and the model that ran it. Cache reads bill at 0.1× input, 5-minute cache
// writes at 1.25× input. Returns 0 for an unknown/empty model so an unpriced call
// contributes nothing rather than a wrong number.
func EstimateCost(model string, input, output, cacheRead, cacheCreation int) float64 {
	r, ok := rateFor(model)
	if !ok {
		return 0
	}
	const m = 1_000_000.0
	return float64(input)*r.input/m +
		float64(output)*r.output/m +
		float64(cacheRead)*(r.input*0.1)/m +
		float64(cacheCreation)*(r.input*1.25)/m
}

func rateFor(model string) (modelRate, bool) {
	for _, r := range rates {
		if strings.Contains(model, r.match) {
			return r.modelRate, true
		}
	}
	return modelRate{}, false
}

// ProviderForModel maps a recorded model id back to the provider that served it,
// mirroring the built-in provider set (claude / codex / kimi). It is the read-side
// fallback for historical token lines logged before the provider was recorded
// inline; an unrecognized or empty model yields "" so callers bucket it as unknown.
func ProviderForModel(model string) string {
	m := strings.ToLower(model)
	switch {
	case m == "":
		return ""
	case strings.Contains(m, "claude"), strings.Contains(m, "opus"),
		strings.Contains(m, "sonnet"), strings.Contains(m, "haiku"),
		strings.Contains(m, "fable"), strings.Contains(m, "mythos"):
		return "claude"
	case strings.Contains(m, "gpt"), strings.Contains(m, "codex"):
		return "codex"
	case strings.Contains(m, "kimi"), strings.Contains(m, "k2"),
		strings.Contains(m, "moonshot"):
		return "kimi"
	default:
		return ""
	}
}
