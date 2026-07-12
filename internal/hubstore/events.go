package hubstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
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
	db        *sql.DB
	retention int
}

// NewEvents returns an Events store over db, pruned to the most recent retention
// events per repo. The caller owns db's lifecycle.
func NewEvents(db *sql.DB, retention int) *Events { return &Events{db: db, retention: retention} }

// Prune keeps the most recent retention events per repo and drops older rows,
// ranked by the monotonic id (ADR 0008). Checkpoints — the run summaries — are
// untouched. A non-positive retention disables pruning.
func (e *Events) Prune() error {
	return pruneKeepingRecent(e.db, "events", e.retention)
}

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

// EventFilter narrows a forensics event read. Kind is an exact match on
// the kind column; Ticket matches events carrying the id in their fields or msg;
// Since bounds events at or after a wall-clock time; Grep is a case-insensitive
// substring over the whole event; After pages forward past an id for a follow tail;
// Limit caps the rows returned.
type EventFilter struct {
	Kind   string
	Ticket string
	Grep   string
	Since  time.Time
	After  int64
	Limit  int
}

const (
	defaultEventQueryLimit = 200
	maxEventQueryScan      = 10000
)

// Query returns repo's events matching f in chronological order. Without After it
// returns the most recent matching window (newest bounded, then ordered oldest
// first); with After it returns matching events with a larger id, the forward page
// a follow tail polls. Kind filters in SQL; ticket, since, and grep filter in Go so
// the fields JSON and timestamps are matched the way the old event-log grep did.
func (e *Events) Query(repo string, f EventFilter) ([]EventRow, error) {
	outLimit := f.Limit
	if outLimit <= 0 {
		outLimit = defaultEventQueryLimit
	}
	scan := outLimit
	if f.Ticket != "" || f.Grep != "" || !f.Since.IsZero() {
		scan = maxEventQueryScan
	}

	query := `SELECT id, ts, kind, phase, msg, fields FROM events WHERE repo = ?`
	args := []any{repo}
	if f.After > 0 {
		query += ` AND id > ?`
		args = append(args, f.After)
	}
	if f.Kind != "" {
		query += ` AND kind = ?`
		args = append(args, f.Kind)
	}
	order := "DESC"
	if f.After > 0 {
		order = "ASC"
	}
	query += ` ORDER BY id ` + order + ` LIMIT ?`
	args = append(args, scan)

	rows, err := e.scan(query, args...)
	if err != nil {
		return nil, err
	}
	grep := strings.ToLower(f.Grep)
	out := make([]EventRow, 0, min(len(rows), outLimit))
	for _, r := range rows {
		if !matchEvent(r, f, grep) {
			continue
		}
		out = append(out, r)
		if len(out) >= outLimit {
			break
		}
	}
	if f.After == 0 {
		reverseEventRows(out)
	}
	return out, nil
}

// matchEvent applies the Go-side filters (ticket, since, grep) to a row already
// narrowed by the SQL kind and cursor bounds. grep is pre-lowered.
func matchEvent(r EventRow, f EventFilter, grep string) bool {
	if f.Ticket != "" && !eventMatchesTicket(r, f.Ticket) {
		return false
	}
	if !f.Since.IsZero() {
		if ts, ok := parseEventTime(r.TS); ok && ts.Before(f.Since) {
			return false
		}
	}
	if grep != "" {
		hay := strings.ToLower(r.Kind + " " + r.Phase + " " + r.Msg + " " + r.Fields)
		if !strings.Contains(hay, grep) {
			return false
		}
	}
	return true
}

// eventMatchesTicket reports whether a row belongs to ticket. A structured ticket
// or id in the fields JSON is authoritative; lacking one, the ticket must appear as
// a whole token in the msg or fields — so a query for COD-1 does not bleed into
// COD-10 the way a bare substring match would.
func eventMatchesTicket(r EventRow, ticket string) bool {
	if r.Fields != "" {
		var m map[string]any
		if json.Unmarshal([]byte(r.Fields), &m) == nil {
			structured := false
			for _, key := range []string{"ticket", "id"} {
				if s, ok := m[key].(string); ok {
					if s == ticket {
						return true
					}
					structured = true
				}
			}
			if structured {
				return false
			}
		}
	}
	return containsTicketToken(r.Msg, ticket) || containsTicketToken(r.Fields, ticket)
}

// containsTicketToken reports whether ticket appears in s bounded by non-alphanumeric
// characters, so it matches a whole id and not a longer one it is a prefix of.
func containsTicketToken(s, ticket string) bool {
	from := 0
	for {
		i := strings.Index(s[from:], ticket)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(ticket)
		if (start == 0 || !isAlphanumeric(s[start-1])) && (end == len(s) || !isAlphanumeric(s[end])) {
			return true
		}
		from = start + 1
	}
}

func isAlphanumeric(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

// parseEventTime parses an event timestamp, accepting RFC3339 (what the loop emits)
// and the zoneless form some fixtures use. ok is false when neither parses.
func parseEventTime(ts string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func reverseEventRows(rows []EventRow) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
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
