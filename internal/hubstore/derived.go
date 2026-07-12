package hubstore

import (
	"database/sql"
	"errors"
	"strconv"
)

// derivedVersion is the schema version of the rebuildable run-history projection.
// It is tracked independently of the authoritative schema_version and the derived
// tables are never migrated: bumping this drops and rebuilds them from the files
// on the next open (ADR 0007 §3).
const derivedVersion = 1

const derivedVersionKey = "derived_version"

const sourceTokens = "tokens"

// derivedTables lists the derived tables in drop order. ingest_sources holds the
// per-file byte offsets the ingester tails by, so it is wiped alongside the
// projection and every source re-reads from the start. Checkpoints and events are
// no longer derived — they are authoritative migrated tables (ADR 0008,
// hubstore.Checkpoints and hubstore.Events).
var derivedTables = []string{"token_calls", "ingest_sources"}

const derivedSchema = `
CREATE TABLE token_calls (
    repo           TEXT NOT NULL,
    ticket         TEXT NOT NULL,
    seq            INTEGER NOT NULL,
    ts             TEXT NOT NULL DEFAULT '',
    phase          TEXT NOT NULL DEFAULT '',
    input          INTEGER NOT NULL DEFAULT 0,
    output         INTEGER NOT NULL DEFAULT 0,
    cache_read     INTEGER NOT NULL DEFAULT 0,
    cache_creation INTEGER NOT NULL DEFAULT 0,
    reasoning      INTEGER NOT NULL DEFAULT 0,
    total          INTEGER NOT NULL DEFAULT 0,
    cost_usd       REAL,
    turns          INTEGER NOT NULL DEFAULT 0,
    is_error       INTEGER NOT NULL DEFAULT 0,
    provider       TEXT NOT NULL DEFAULT '',
    model          TEXT NOT NULL DEFAULT '',
    context        INTEGER NOT NULL DEFAULT 0,
    skills         TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo, ticket, seq)
) STRICT;

CREATE TABLE ingest_sources (
    repo        TEXT NOT NULL,
    kind        TEXT NOT NULL,
    ticket      TEXT NOT NULL DEFAULT '',
    byte_offset INTEGER NOT NULL DEFAULT 0,
    size        INTEGER NOT NULL DEFAULT 0,
    mtime       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (repo, kind, ticket)
) STRICT;
`

// Derived is the hub's rebuildable projection of the per-call token spend loops
// append to files. Token files stay the durable source of truth (ADR 0007 §3);
// the hub tails them by byte offset into these tables and, on any version mismatch
// or corruption, drops and rebuilds them without ever touching the authoritative
// tables or the files.
type Derived struct {
	db *sql.DB
}

// NewDerived returns a Derived store over db. The caller owns db's lifecycle.
func NewDerived(db *sql.DB) *Derived { return &Derived{db: db} }

// EnsureSchema brings the derived projection to derivedVersion. It is a no-op when
// the stored version matches and the tables are intact; on a version bump, a
// missing or corrupt derived table, or a fresh database it drops the derived
// tables and recreates them empty, resetting every ingest cursor so the next pass
// rebuilds from the files. Authoritative tables and the files are never touched.
func (d *Derived) EnsureSchema() error {
	v, err := d.storedVersion()
	if err != nil {
		return err
	}
	if v == derivedVersion && d.tablesHealthy() {
		return nil
	}
	return d.rebuild()
}

func (d *Derived) storedVersion() (int, error) {
	var val string
	err := d.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, derivedVersionKey).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(val)
}

// tablesHealthy reports whether every derived table is present and queryable — a
// dropped or corrupt table fails the probe and forces a rebuild.
func (d *Derived) tablesHealthy() bool {
	for _, t := range derivedTables {
		if _, err := d.db.Exec(`SELECT 1 FROM ` + t + ` LIMIT 1`); err != nil {
			return false
		}
	}
	return true
}

func (d *Derived) rebuild() error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	for _, t := range derivedTables {
		if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + t); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	if _, err := tx.Exec(derivedSchema); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if _, err := tx.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		derivedVersionKey, strconv.Itoa(derivedVersion),
	); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

// TokenRow is one normalized token/cost call tagged with its line-end byte offset.
// CostUSD is nil for a call a provider reported no per-call cost for.
type TokenRow struct {
	Seq           int64
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
	Context       int
	Skills        string
}

// CostCell is one aggregated slice of token spend from token_calls, grouped by
// repo, local date, provider, model, and phase. Provider and Model are as the call
// logged them — the read-side provider fallback for older, provider-less lines is
// the caller's to apply. Cost sums the per-call cost and is a lower bound when
// Metered is false: some contributing call recorded no per-call cost.
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

// TokenCursor returns the byte offset ingestion last read a ticket's tokens.jsonl
// to, or 0 when it has none.
func (d *Derived) TokenCursor(repo, ticket string) (int64, error) {
	return d.offset(repo, sourceTokens, ticket)
}

func (d *Derived) offset(repo, kind, ticket string) (int64, error) {
	var off int64
	err := d.db.QueryRow(
		`SELECT byte_offset FROM ingest_sources WHERE repo = ? AND kind = ? AND ticket = ?`,
		repo, kind, ticket,
	).Scan(&off)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return off, err
}

// IngestTokens appends a ticket's new token calls and advances its cursor to
// offset in one transaction, dropping the ticket's existing calls first on resync.
func (d *Derived) IngestTokens(repo, ticket string, resync bool, rows []TokenRow, offset int64) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	if resync {
		if _, err := tx.Exec(`DELETE FROM token_calls WHERE repo = ? AND ticket = ?`, repo, ticket); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	for _, r := range rows {
		if _, err := tx.Exec(
			`INSERT INTO token_calls(
			     repo, ticket, seq, ts, phase, input, output, cache_read, cache_creation,
			     reasoning, total, cost_usd, turns, is_error, provider, model, context, skills)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(repo, ticket, seq) DO UPDATE SET
			     ts = excluded.ts, phase = excluded.phase, input = excluded.input,
			     output = excluded.output, cache_read = excluded.cache_read,
			     cache_creation = excluded.cache_creation, reasoning = excluded.reasoning,
			     total = excluded.total, cost_usd = excluded.cost_usd, turns = excluded.turns,
			     is_error = excluded.is_error, provider = excluded.provider,
			     model = excluded.model, context = excluded.context, skills = excluded.skills`,
			repo, ticket, r.Seq, r.TS, r.Phase, r.Input, r.Output, r.CacheRead, r.CacheCreation,
			r.Reasoning, r.Total, r.CostUSD, r.Turns, boolToInt(r.IsError), r.Provider, r.Model, r.Context, r.Skills,
		); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	if err := setOffset(tx, repo, sourceTokens, ticket, offset); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

func setOffset(tx *sql.Tx, repo, kind, ticket string, offset int64) error {
	_, err := tx.Exec(
		`INSERT INTO ingest_sources(repo, kind, ticket, byte_offset)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(repo, kind, ticket) DO UPDATE SET byte_offset = excluded.byte_offset`,
		repo, kind, ticket, offset,
	)
	return err
}

// TokenCalls returns a ticket's ingested token calls in append order.
func (d *Derived) TokenCalls(repo, ticket string) (rows []TokenRow, err error) {
	q, err := d.db.Query(
		`SELECT seq, ts, phase, input, output, cache_read, cache_creation, reasoning,
		        total, cost_usd, turns, is_error, provider, model, context, skills
		 FROM token_calls WHERE repo = ? AND ticket = ? ORDER BY seq`, repo, ticket,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	rows = []TokenRow{}
	for q.Next() {
		var (
			r       TokenRow
			isError int
		)
		if err := q.Scan(
			&r.Seq, &r.TS, &r.Phase, &r.Input, &r.Output, &r.CacheRead, &r.CacheCreation, &r.Reasoning,
			&r.Total, &r.CostUSD, &r.Turns, &isError, &r.Provider, &r.Model, &r.Context, &r.Skills,
		); err != nil {
			return nil, err
		}
		r.IsError = isError != 0
		rows = append(rows, r)
	}
	return rows, q.Err()
}

// CostCells aggregates the token calls whose local date falls within [from, to]
// inclusive into one cell per (repo, date, provider, model, phase), summing tokens
// and cost in SQL. The date is the ts's YYYY-MM-DD prefix compared lexically, so
// the window is filtered in the query rather than by scanning every call. A cell is
// metered only when every call folded into it recorded a per-call cost.
func (d *Derived) CostCells(from, to string) (cells []CostCell, err error) {
	q, err := d.db.Query(
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
