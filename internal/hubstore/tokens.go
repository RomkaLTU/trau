package hubstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
)

// TokenCall is one normalized provider call a child sends to the hub for the
// authoritative token ledger (ADR 0008). Each carries its own ticket, so one
// append batch may span buckets. CostUSD is nil for a call a provider reported no
// per-call cost for. Effort, DurationMS, and ConfigHash record what the call ran
// under, so spend can be grouped into configuration cohorts; an empty ConfigHash
// is the unknown cohort historical rows fall into.
type TokenCall struct {
	Ticket        string
	TS            string
	Phase         string
	Input         int
	Output        int
	CacheRead     int
	CacheCreation int
	Reasoning     int
	Total         int
	CostUSD       *float64
	Turns         int
	IsError       bool
	Provider      string
	Model         string
	Effort        string
	Context       int
	DurationMS    int
	ConfigHash    string
	Skills        string
}

// Spend is an accumulated (tokens, cost) figure. Metered is false when any call
// folded into it recorded no per-call cost, so Cost is then a lower bound. Cost is
// rounded once to cents, matching the file-era readers.
type Spend struct {
	Tokens  int
	Cost    float64
	Metered bool
}

// PhaseTotal is one phase's summed spend across a ticket's calls. Metered carries
// the same lower-bound contract as Spend.
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

// Anomaly is one flagged cost anomaly for a run, as the hub stores and serves it:
// the phase that cleared a soft threshold, its output/turns/cost, and the human
// reasons it was flagged.
type Anomaly struct {
	TS      string
	Phase   string
	Output  int
	Turns   int
	Cost    float64
	Reasons []string
}

// TicketAnomaly tags an Anomaly with the ticket that produced it, for the costs
// page's whole-repo read.
type TicketAnomaly struct {
	Ticket string
	Anomaly
}

// CostCell is one aggregated slice of token spend from token_calls, grouped by
// repo, local date, provider, model, and phase. Provider and Model are as the call
// logged them — the read-side provider fallback for older, provider-less lines is
// the caller's to apply. Cost sums the per-call cost (left unrounded so a caller
// folding cells across repos rounds once) and is a lower bound when Metered is
// false: some contributing call recorded no per-call cost.
type CostCell struct {
	Repo     string
	Date     string
	Phase    string
	Provider string
	Model    string
	Tokens   int
	Cost     float64
	Metered  bool
}

// Tokens is the hub's authoritative token-call and anomaly store (ADR 0008).
// Children POST every provider call and flagged anomaly here over HTTP, so the
// tables — not per-run log files — are the source of truth. They are real migrated
// tables, never dropped and rebuilt.
type Tokens struct {
	db        *sql.DB
	retention int
}

// NewTokens returns a Tokens store over db, pruned to the most recent retention
// token calls per repo. The caller owns db's lifecycle.
func NewTokens(db *sql.DB, retention int) *Tokens { return &Tokens{db: db, retention: retention} }

// Prune keeps the most recent retention token calls per repo and drops older
// rows, ranked by the monotonic id (ADR 0008); flagged anomalies are pruned to
// the same per-repo window so the sibling table cannot grow unbounded. A
// non-positive retention disables pruning.
func (t *Tokens) Prune() error {
	if err := pruneKeepingRecent(t.db, "token_calls", t.retention); err != nil {
		return err
	}
	return pruneKeepingRecent(t.db, "token_anomalies", t.retention)
}

// Append inserts calls for repo in arrival order in one transaction. Each row's id
// is assigned in insertion order, which is what orders a ticket's phase breakdown.
func (t *Tokens) Append(repo string, calls []TokenCall) error {
	if len(calls) == 0 {
		return nil
	}
	tx, err := t.db.Begin()
	if err != nil {
		return err
	}
	for _, c := range calls {
		if _, err := tx.Exec(
			`INSERT INTO token_calls(
			     repo, ticket, ts, phase, input, output, cache_read, cache_creation,
			     reasoning, total, cost_usd, turns, is_error, provider, model, effort,
			     context, duration_ms, config_hash, skills)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			repo, c.Ticket, c.TS, c.Phase, c.Input, c.Output, c.CacheRead, c.CacheCreation,
			c.Reasoning, c.Total, c.CostUSD, c.Turns, boolToInt(c.IsError), c.Provider, c.Model, c.Effort,
			c.Context, c.DurationMS, c.ConfigHash, c.Skills,
		); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	return tx.Commit()
}

// Total sums a ticket's token + cost spend across all phases. A ticket with no
// calls yields (0, 0, true) — never an error — matching the file reader's contract
// for a missing log.
func (t *Tokens) Total(repo, ticket string) (Spend, error) {
	return t.spend(
		`SELECT SUM(total), SUM(cost_usd), SUM(cost_usd IS NULL)
		 FROM token_calls WHERE repo = ? AND ticket = ?`, repo, ticket,
	)
}

// DayTotal sums repo's token + cost spend for calls whose local date (the ts's
// YYYY-MM-DD prefix) equals date — the per-day window the budget day cap enforces,
// across every bucket including _loop and _plans.
func (t *Tokens) DayTotal(repo, date string) (Spend, error) {
	return t.spend(
		`SELECT SUM(total), SUM(cost_usd), SUM(cost_usd IS NULL)
		 FROM token_calls WHERE repo = ? AND substr(ts, 1, 10) = ?`, repo, date,
	)
}

func (t *Tokens) spend(query string, args ...any) (Spend, error) {
	var (
		toks      sql.NullInt64
		cost      sql.NullFloat64
		unmetered sql.NullInt64
	)
	if err := t.db.QueryRow(query, args...).Scan(&toks, &cost, &unmetered); err != nil {
		return Spend{Metered: true}, err
	}
	return Spend{
		Tokens:  int(toks.Int64),
		Cost:    roundCents(cost.Float64),
		Metered: unmetered.Int64 == 0,
	}, nil
}

// PhaseTotals breaks a ticket's spend down by phase, one row per distinct phase in
// the order each phase first appears in the ticket's calls. Costs sum raw then
// round once to cents per phase, matching Total.
func (t *Tokens) PhaseTotals(repo, ticket string) (rows []PhaseTotal, err error) {
	q, err := t.db.Query(
		`SELECT phase, input, output, cache_read, cache_creation, reasoning, total, cost_usd, turns
		 FROM token_calls WHERE repo = ? AND ticket = ? ORDER BY id`, repo, ticket,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()

	rows = []PhaseTotal{}
	idx := map[string]int{}
	for q.Next() {
		var (
			r    PhaseTotal
			cost sql.NullFloat64
		)
		if err := q.Scan(
			&r.Phase, &r.Input, &r.Output, &r.CacheRead, &r.CacheCreation, &r.Reasoning, &r.Total, &cost, &r.Turns,
		); err != nil {
			return nil, err
		}
		i, ok := idx[r.Phase]
		if !ok {
			i = len(rows)
			idx[r.Phase] = i
			rows = append(rows, PhaseTotal{Phase: r.Phase, Metered: true})
		}
		p := &rows[i]
		p.Input += r.Input
		p.Output += r.Output
		p.CacheRead += r.CacheRead
		p.CacheCreation += r.CacheCreation
		p.Reasoning += r.Reasoning
		p.Total += r.Total
		p.Turns += r.Turns
		p.Calls++
		if cost.Valid {
			p.Cost += cost.Float64
		} else {
			p.Metered = false
		}
	}
	for i := range rows {
		rows[i].Cost = roundCents(rows[i].Cost)
	}
	return rows, q.Err()
}

// CostCells aggregates the token calls whose local date falls within [from, to]
// inclusive into one cell per (repo, date, provider, model, phase), summing tokens
// and cost in SQL. The date is the ts's YYYY-MM-DD prefix compared lexically, so
// the window is filtered in the query rather than by scanning every call. A cell is
// metered only when every call folded into it recorded a per-call cost.
func (t *Tokens) CostCells(from, to string) (cells []CostCell, err error) {
	q, err := t.db.Query(
		`SELECT repo, substr(ts, 1, 10) AS day, phase, provider, model,
		        SUM(total), SUM(cost_usd), SUM(cost_usd IS NULL)
		 FROM token_calls
		 WHERE length(ts) >= 10 AND substr(ts, 1, 10) >= ? AND substr(ts, 1, 10) <= ?
		 GROUP BY repo, day, phase, provider, model
		 ORDER BY repo, day, phase, provider, model`,
		from, to,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	cells = []CostCell{}
	for q.Next() {
		var (
			c         CostCell
			cost      sql.NullFloat64
			unmetered int
		)
		if err := q.Scan(&c.Repo, &c.Date, &c.Phase, &c.Provider, &c.Model, &c.Tokens, &cost, &unmetered); err != nil {
			return nil, err
		}
		c.Cost = cost.Float64
		c.Metered = unmetered == 0
		cells = append(cells, c)
	}
	return cells, q.Err()
}

// RecordAnomalies replaces a ticket's flagged anomalies with the given set, so a
// re-run (resume) reflects current totals rather than appending duplicates. An
// empty set is a no-op that leaves any prior anomalies untouched, matching the file
// writer: a run that trips nothing never clears an earlier run's flags.
func (t *Tokens) RecordAnomalies(repo, ticket string, anomalies []Anomaly) error {
	if len(anomalies) == 0 {
		return nil
	}
	tx, err := t.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM token_anomalies WHERE repo = ? AND ticket = ?`, repo, ticket); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	for _, a := range anomalies {
		if _, err := tx.Exec(
			`INSERT INTO token_anomalies(repo, ticket, ts, phase, output, turns, cost_usd, reasons)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			repo, ticket, a.TS, a.Phase, a.Output, a.Turns, a.Cost, marshalReasons(a.Reasons),
		); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	return tx.Commit()
}

// Anomalies returns a ticket's flagged anomalies in the order they were recorded. A
// run that never tripped a threshold yields an empty slice.
func (t *Tokens) Anomalies(repo, ticket string) ([]Anomaly, error) {
	rows, err := t.scanAnomalies(
		`SELECT ticket, ts, phase, output, turns, cost_usd, reasons
		 FROM token_anomalies WHERE repo = ? AND ticket = ? ORDER BY id`, repo, ticket,
	)
	if err != nil {
		return nil, err
	}
	out := make([]Anomaly, len(rows))
	for i, ta := range rows {
		out[i] = ta.Anomaly
	}
	return out, nil
}

// RepoAnomalies returns every flagged anomaly across a repo's runs, each located to
// the ticket that produced it — the costs page's whole-repo read.
func (t *Tokens) RepoAnomalies(repo string) ([]TicketAnomaly, error) {
	return t.scanAnomalies(
		`SELECT ticket, ts, phase, output, turns, cost_usd, reasons
		 FROM token_anomalies WHERE repo = ? ORDER BY ticket, id`, repo,
	)
}

func (t *Tokens) scanAnomalies(query string, args ...any) (out []TicketAnomaly, err error) {
	q, err := t.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	out = []TicketAnomaly{}
	for q.Next() {
		var (
			ta      TicketAnomaly
			reasons string
		)
		if err := q.Scan(&ta.Ticket, &ta.TS, &ta.Phase, &ta.Output, &ta.Turns, &ta.Cost, &reasons); err != nil {
			return nil, err
		}
		ta.Reasons = unmarshalReasons(reasons)
		out = append(out, ta)
	}
	return out, q.Err()
}

func marshalReasons(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	b, err := json.Marshal(reasons)
	if err != nil {
		return ""
	}
	return string(b)
}

func unmarshalReasons(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if json.Unmarshal([]byte(s), &out) != nil {
		return nil
	}
	return out
}

func roundCents(v float64) float64 { return math.Round(v*100) / 100 }
