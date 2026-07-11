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

const (
	sourceEvents     = "events"
	sourceTokens     = "tokens"
	sourceCheckpoint = "checkpoint"
)

// derivedTables lists the derived tables in drop order. ingest_sources holds the
// per-file byte offsets and checkpoint size/mtime the ingester tails by, so it is
// wiped alongside the projection and every source re-reads from the start.
var derivedTables = []string{"events", "token_calls", "checkpoints", "ingest_sources"}

const derivedSchema = `
CREATE TABLE events (
    repo   TEXT NOT NULL,
    seq    INTEGER NOT NULL,
    ts     TEXT NOT NULL DEFAULT '',
    kind   TEXT NOT NULL DEFAULT '',
    phase  TEXT NOT NULL DEFAULT '',
    msg    TEXT NOT NULL DEFAULT '',
    fields TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo, seq)
) STRICT;

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

CREATE TABLE checkpoints (
    repo           TEXT NOT NULL,
    ticket         TEXT NOT NULL,
    phase          TEXT NOT NULL DEFAULT '',
    title          TEXT NOT NULL DEFAULT '',
    branch         TEXT NOT NULL DEFAULT '',
    pr             TEXT NOT NULL DEFAULT '',
    pr_url         TEXT NOT NULL DEFAULT '',
    failure_reason TEXT NOT NULL DEFAULT '',
    updated_at     TEXT NOT NULL DEFAULT '',
    data           TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY (repo, ticket)
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

// Derived is the hub's rebuildable projection of the run artifacts loops append
// to files: events, per-call token spend, and checkpoints. Files stay the durable
// source of truth (ADR 0007 §3); the hub tails them by byte offset into these
// tables and, on any version mismatch or corruption, drops and rebuilds them
// without ever touching the authoritative tables or the files.
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

// EventRow is one event line tagged with the byte offset at its line end, the
// cursor the events feed resumes from.
type EventRow struct {
	Seq    int64
	TS     string
	Kind   string
	Phase  string
	Msg    string
	Fields string
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

// CheckpointRow is a ticket's checkpoint projection: the fields the run board
// reads directly plus the full key set as JSON so nothing on disk is lost.
type CheckpointRow struct {
	Phase         string
	Title         string
	Branch        string
	PR            string
	PRURL         string
	FailureReason string
	UpdatedAt     string
	Data          string
}

// TicketCheckpoint pairs a checkpoint projection with the ticket it belongs to,
// for the run board's whole-repo read.
type TicketCheckpoint struct {
	Ticket string
	CheckpointRow
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

// EventCursor returns the byte offset ingestion last read repo's events.jsonl to,
// or 0 when it has none.
func (d *Derived) EventCursor(repo string) (int64, error) {
	return d.offset(repo, sourceEvents, "")
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

// CheckpointCursor returns the size and modification time ingestion last saw for a
// ticket's state file, both 0 when it has none — the change signal that lets an
// unchanged checkpoint be skipped.
func (d *Derived) CheckpointCursor(repo, ticket string) (size, mtime int64, err error) {
	err = d.db.QueryRow(
		`SELECT size, mtime FROM ingest_sources WHERE repo = ? AND kind = ? AND ticket = ?`,
		repo, sourceCheckpoint, ticket,
	).Scan(&size, &mtime)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	return size, mtime, err
}

// IngestEvents appends repo's new event rows and advances its events cursor to
// offset in one transaction. When resync is set — the file was rewritten shorter
// than the cursor — repo's existing events are dropped first and rows carries the
// file re-read from the start.
func (d *Derived) IngestEvents(repo string, resync bool, rows []EventRow, offset int64) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	if resync {
		if _, err := tx.Exec(`DELETE FROM events WHERE repo = ?`, repo); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	for _, r := range rows {
		if _, err := tx.Exec(
			`INSERT INTO events(repo, seq, ts, kind, phase, msg, fields)
			 VALUES(?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(repo, seq) DO UPDATE SET
			     ts = excluded.ts, kind = excluded.kind, phase = excluded.phase,
			     msg = excluded.msg, fields = excluded.fields`,
			repo, r.Seq, r.TS, r.Kind, r.Phase, r.Msg, r.Fields,
		); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	if err := setOffset(tx, repo, sourceEvents, "", offset); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
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

// UpsertCheckpoint writes a ticket's checkpoint row and records the state file's
// size and mtime so an unchanged file is skipped on the next pass.
func (d *Derived) UpsertCheckpoint(repo, ticket string, cp CheckpointRow, size, mtime int64) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO checkpoints(
		     repo, ticket, phase, title, branch, pr, pr_url, failure_reason, updated_at, data)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(repo, ticket) DO UPDATE SET
		     phase = excluded.phase, title = excluded.title, branch = excluded.branch,
		     pr = excluded.pr, pr_url = excluded.pr_url, failure_reason = excluded.failure_reason,
		     updated_at = excluded.updated_at, data = excluded.data`,
		repo, ticket, cp.Phase, cp.Title, cp.Branch, cp.PR, cp.PRURL, cp.FailureReason, cp.UpdatedAt, cp.Data,
	); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if _, err := tx.Exec(
		`INSERT INTO ingest_sources(repo, kind, ticket, size, mtime)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(repo, kind, ticket) DO UPDATE SET size = excluded.size, mtime = excluded.mtime`,
		repo, sourceCheckpoint, ticket, size, mtime,
	); err != nil {
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

// Events returns repo's ingested event rows in feed order.
func (d *Derived) Events(repo string) (rows []EventRow, err error) {
	q, err := d.db.Query(
		`SELECT seq, ts, kind, phase, msg, fields FROM events WHERE repo = ? ORDER BY seq`, repo,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	rows = []EventRow{}
	for q.Next() {
		var r EventRow
		if err := q.Scan(&r.Seq, &r.TS, &r.Kind, &r.Phase, &r.Msg, &r.Fields); err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, q.Err()
}

// RecentEvents returns up to limit of repo's events, newest first. A positive
// before pages to older events by bounding the result to seq below it; 0 returns
// the latest page. Ordering is by seq — the line-end byte offset — so reversing
// the result yields the feed's chronological order. The (repo, seq) primary key
// serves the ordering and the limit from an index, never a full scan.
func (d *Derived) RecentEvents(repo string, limit int, before int64) (rows []EventRow, err error) {
	query := `SELECT seq, ts, kind, phase, msg, fields FROM events WHERE repo = ?`
	args := []any{repo}
	if before > 0 {
		query += ` AND seq < ?`
		args = append(args, before)
	}
	query += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit)

	q, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	rows = []EventRow{}
	for q.Next() {
		var r EventRow
		if err := q.Scan(&r.Seq, &r.TS, &r.Kind, &r.Phase, &r.Msg, &r.Fields); err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, q.Err()
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

// Checkpoint returns a ticket's checkpoint row and whether it has been ingested.
func (d *Derived) Checkpoint(repo, ticket string) (CheckpointRow, bool, error) {
	var cp CheckpointRow
	err := d.db.QueryRow(
		`SELECT phase, title, branch, pr, pr_url, failure_reason, updated_at, data
		 FROM checkpoints WHERE repo = ? AND ticket = ?`, repo, ticket,
	).Scan(&cp.Phase, &cp.Title, &cp.Branch, &cp.PR, &cp.PRURL, &cp.FailureReason, &cp.UpdatedAt, &cp.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return CheckpointRow{}, false, nil
	}
	if err != nil {
		return CheckpointRow{}, false, err
	}
	return cp, true, nil
}

// Checkpoints returns every ingested checkpoint for repo, each tagged with its
// ticket and ordered by ticket. It backs the run board's whole-repo read from the
// projection, replacing a per-field re-read of each state file on every poll.
func (d *Derived) Checkpoints(repo string) (rows []TicketCheckpoint, err error) {
	q, err := d.db.Query(
		`SELECT ticket, phase, title, branch, pr, pr_url, failure_reason, updated_at, data
		 FROM checkpoints WHERE repo = ? ORDER BY ticket`, repo,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	rows = []TicketCheckpoint{}
	for q.Next() {
		var r TicketCheckpoint
		if err := q.Scan(
			&r.Ticket, &r.Phase, &r.Title, &r.Branch, &r.PR, &r.PRURL,
			&r.FailureReason, &r.UpdatedAt, &r.Data,
		); err != nil {
			return nil, err
		}
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
