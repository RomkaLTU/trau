package hubstore

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// EventRow is one persisted event carrying the monotonic id that orders the feed
// and doubles as the SSE resume cursor (ADR 0008 — a real id replaces the file's
// byte offset). Fields is the event's fields bag as a JSON object string.
type EventRow struct {
	ID     int64
	TS     string
	Kind   string
	Phase  string
	Msg    string
	Fields string
}

// RepoEventRow tags an EventRow with its repo, for the machine-wide feed that
// interleaves every repo's events on the one global id order.
type RepoEventRow struct {
	Repo string
	EventRow
}

// NewEvent is one event a child sends to the hub, before its id is assigned.
type NewEvent struct {
	TS     string
	Kind   string
	Phase  string
	Msg    string
	Fields string
}

// Events is the hub's authoritative event store (ADR 0008). Children POST every
// event here over HTTP, so the table — not a per-repo log file — is the feed's
// source of truth. It is a real migrated table, never dropped and rebuilt.
type Events struct {
	db *sql.DB
}

// NewEvents returns an Events store over db. The caller owns db's lifecycle.
func NewEvents(db *sql.DB) *Events { return &Events{db: db} }

// Append inserts evs for repo in arrival order in one transaction and returns each
// row with its assigned id, so the caller can fan the batch out to live streams.
// The ids are monotonic in insertion order, which is what preserves per-run
// ordering under batching.
func (e *Events) Append(repo string, evs []NewEvent) ([]EventRow, error) {
	if len(evs) == 0 {
		return nil, nil
	}
	tx, err := e.db.Begin()
	if err != nil {
		return nil, err
	}
	rows := make([]EventRow, 0, len(evs))
	for _, ev := range evs {
		res, err := tx.Exec(
			`INSERT INTO events(repo, ts, kind, phase, msg, fields) VALUES(?, ?, ?, ?, ?, ?)`,
			repo, ev.TS, ev.Kind, ev.Phase, ev.Msg, ev.Fields,
		)
		if err != nil {
			return nil, errors.Join(err, tx.Rollback())
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, errors.Join(err, tx.Rollback())
		}
		rows = append(rows, EventRow{
			ID: id, TS: ev.TS, Kind: ev.Kind, Phase: ev.Phase, Msg: ev.Msg, Fields: ev.Fields,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return rows, nil
}

// Recent returns up to limit of repo's events, newest first; a positive before
// pages to older events by bounding to ids below it. The (repo, id) index serves
// the order and the limit without a scan.
func (e *Events) Recent(repo string, limit int, before int64) ([]EventRow, error) {
	query := `SELECT id, ts, kind, phase, msg, fields FROM events WHERE repo = ?`
	args := []any{repo}
	if before > 0 {
		query += ` AND id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	return e.scan(query, args...)
}

// Since returns repo's events with id greater than after, in feed order — the
// per-repo SSE reconnect backfill from a Last-Event-ID.
func (e *Events) Since(repo string, after int64) ([]EventRow, error) {
	return e.scan(
		`SELECT id, ts, kind, phase, msg, fields FROM events WHERE repo = ? AND id > ? ORDER BY id`,
		repo, after,
	)
}

// RecentAll returns up to limit events across every repo, newest first, paging by
// before — the machine-wide feed's fresh-connect backfill.
func (e *Events) RecentAll(limit int, before int64) ([]RepoEventRow, error) {
	query := `SELECT repo, id, ts, kind, phase, msg, fields FROM events`
	var args []any
	if before > 0 {
		query += ` WHERE id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	return e.scanRepo(query, args...)
}

// SinceAll returns events across every repo with id greater than after, in feed
// order — the machine-wide SSE reconnect backfill.
func (e *Events) SinceAll(after int64) ([]RepoEventRow, error) {
	return e.scanRepo(
		`SELECT repo, id, ts, kind, phase, msg, fields FROM events WHERE id > ? ORDER BY id`,
		after,
	)
}

// HasKind reports whether repo has an event of kind whose fields carry the given
// ticket — the run-detail durable-flag query (e.g. build_no_skills). Events of a
// given kind are rare, so it scans the kind's rows and matches the ticket field in
// Go rather than depending on a JSON SQL function.
func (e *Events) HasKind(repo, kind, ticket string) (found bool, err error) {
	q, err := e.db.Query(`SELECT fields FROM events WHERE repo = ? AND kind = ?`, repo, kind)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	for q.Next() {
		var fields string
		if err := q.Scan(&fields); err != nil {
			return false, err
		}
		if eventFieldsTicket(fields) == ticket {
			return true, nil
		}
	}
	return false, q.Err()
}

func (e *Events) scan(query string, args ...any) (rows []EventRow, err error) {
	q, err := e.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	rows = []EventRow{}
	for q.Next() {
		var r EventRow
		if err := q.Scan(&r.ID, &r.TS, &r.Kind, &r.Phase, &r.Msg, &r.Fields); err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, q.Err()
}

func (e *Events) scanRepo(query string, args ...any) (rows []RepoEventRow, err error) {
	q, err := e.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	rows = []RepoEventRow{}
	for q.Next() {
		var r RepoEventRow
		if err := q.Scan(&r.Repo, &r.ID, &r.TS, &r.Kind, &r.Phase, &r.Msg, &r.Fields); err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, q.Err()
}

func eventFieldsTicket(fields string) string {
	if fields == "" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(fields), &m) != nil {
		return ""
	}
	if s, ok := m["ticket"].(string); ok {
		return s
	}
	return ""
}
