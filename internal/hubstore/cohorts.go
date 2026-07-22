package hubstore

import (
	"database/sql"
	"errors"
)

// CohortFilter bounds a config-cohort read to a local-date window, each bound
// inclusive and matched against the call timestamp's YYYY-MM-DD prefix. An empty
// bound is unbounded.
type CohortFilter struct {
	Since string
	Until string
}

// CohortTotal is one configuration cohort's ledger-wide aggregate: every call
// stamped with the same config_hash. Cost is left unrounded and is a lower bound
// when Metered is false. VerifyCalls counts first-attempt verifies, RetryCalls the
// verify-retry passes a failure forced, and RepairCalls the repair passes — the
// counters the derived pipeline rates divide.
type CohortTotal struct {
	Hash        string
	Calls       int
	Tickets     int
	FirstTS     string
	LastTS      string
	Cost        float64
	Metered     bool
	VerifyCalls int
	RetryCalls  int
	RepairCalls int
}

// CohortPhaseCell is one (cohort, phase label) aggregate. Phase is the raw label
// the call logged — "verify-retry2", "repair1" — so a reader keeps the attempt-level
// detail or collapses it to a canonical phase; the sums stay raw so averages are
// derived after any such folding.
type CohortPhaseCell struct {
	Hash       string
	Phase      string
	Calls      int
	Cost       float64
	Metered    bool
	DurationMS int64
	Turns      int
	Context    int
}

// ConfigCohortTotals folds repo's ledger into one row per config_hash, newest
// cohort first by last call. Calls carrying no config_hash — everything logged
// before the ledger fingerprinted the routing config — fold into the single
// empty-hash cohort rather than being dropped.
func (t *Tokens) ConfigCohortTotals(repo string, f CohortFilter) (cohorts []CohortTotal, err error) {
	where, args := cohortWhere(repo, f)
	q, err := t.db.Query(
		`SELECT config_hash, COUNT(*), COUNT(DISTINCT ticket), MIN(ts), MAX(ts),
		        SUM(cost_usd), SUM(cost_usd IS NULL),
		        SUM(phase = 'verify'), SUM(phase LIKE 'verify-retry%'), SUM(phase LIKE 'repair%')
		 FROM token_calls`+where+`
		 GROUP BY config_hash
		 ORDER BY MAX(ts) DESC, config_hash`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	cohorts = []CohortTotal{}
	for q.Next() {
		var (
			c         CohortTotal
			cost      sql.NullFloat64
			unmetered int
		)
		if err := q.Scan(
			&c.Hash, &c.Calls, &c.Tickets, &c.FirstTS, &c.LastTS, &cost, &unmetered,
			&c.VerifyCalls, &c.RetryCalls, &c.RepairCalls,
		); err != nil {
			return nil, err
		}
		c.Cost = cost.Float64
		c.Metered = unmetered == 0
		cohorts = append(cohorts, c)
	}
	return cohorts, q.Err()
}

// ConfigCohortPhases aggregates repo's ledger into one cell per (config_hash, phase
// label).
func (t *Tokens) ConfigCohortPhases(repo string, f CohortFilter) (cells []CohortPhaseCell, err error) {
	where, args := cohortWhere(repo, f)
	q, err := t.db.Query(
		`SELECT config_hash, phase, COUNT(*), SUM(cost_usd), SUM(cost_usd IS NULL),
		        SUM(duration_ms), SUM(turns), SUM(context)
		 FROM token_calls`+where+`
		 GROUP BY config_hash, phase
		 ORDER BY config_hash, phase`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	cells = []CohortPhaseCell{}
	for q.Next() {
		var (
			c         CohortPhaseCell
			cost      sql.NullFloat64
			unmetered int
		)
		if err := q.Scan(
			&c.Hash, &c.Phase, &c.Calls, &cost, &unmetered, &c.DurationMS, &c.Turns, &c.Context,
		); err != nil {
			return nil, err
		}
		c.Cost = cost.Float64
		c.Metered = unmetered == 0
		cells = append(cells, c)
	}
	return cells, q.Err()
}

func cohortWhere(repo string, f CohortFilter) (string, []any) {
	where := ` WHERE repo = ?`
	args := []any{repo}
	if f.Since == "" && f.Until == "" {
		return where, args
	}
	where += ` AND length(ts) >= 10`
	if f.Since != "" {
		where += ` AND substr(ts, 1, 10) >= ?`
		args = append(args, f.Since)
	}
	if f.Until != "" {
		where += ` AND substr(ts, 1, 10) <= ?`
		args = append(args, f.Until)
	}
	return where, args
}
