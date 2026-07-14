package hubstore

import (
	"database/sql"
	"errors"
	"time"
)

// Grill session states (grilling-prd.md). A session is settled once it reaches
// applied or abandoned; every other state is active and counts against the
// one-active-session-per-issue rule.
const (
	GrillRunning   = "running"
	GrillWaiting   = "waiting"
	GrillParked    = "parked"
	GrillStalled   = "stalled"
	GrillFinished  = "finished"
	GrillApplied   = "applied"
	GrillAbandoned = "abandoned"
)

// Grill message roles and kinds (grilling-prd.md).
const (
	GrillRoleAgent  = "agent"
	GrillRoleUser   = "user"
	GrillRoleSystem = "system"

	GrillKindQuestion = "question"
	GrillKindAnswer   = "answer"
	GrillKindInfo     = "info"
	GrillKindOutcome  = "outcome"
)

var (
	// ErrGrillActiveSession is returned when a session is created for an issue that
	// already has an unsettled one — the one-active-session-per-issue rule.
	ErrGrillActiveSession = errors.New("issue already has an active grill session")
	// ErrGrillNotFound is returned when a session id resolves to no row.
	ErrGrillNotFound = errors.New("grill session not found")
	// ErrGrillTransition is returned when a state change is not legal from the
	// session's current state.
	ErrGrillTransition = errors.New("illegal grill session state transition")
)

// grillTransitions is the legal state machine: the states each state may move to.
// applied and abandoned are terminal.
var grillTransitions = map[string]map[string]bool{
	GrillRunning:   {GrillWaiting: true, GrillParked: true, GrillStalled: true, GrillFinished: true, GrillAbandoned: true},
	GrillWaiting:   {GrillRunning: true, GrillParked: true, GrillStalled: true, GrillFinished: true, GrillAbandoned: true},
	GrillParked:    {GrillRunning: true, GrillStalled: true, GrillFinished: true, GrillAbandoned: true},
	GrillStalled:   {GrillRunning: true, GrillParked: true, GrillFinished: true, GrillAbandoned: true},
	GrillFinished:  {GrillApplied: true, GrillAbandoned: true},
	GrillApplied:   {},
	GrillAbandoned: {},
}

// GrillSession is one grilling session as stored. IssueID is empty for authoring
// sessions that anchor to the repo alone.
type GrillSession struct {
	ID           int64
	Repo         string
	IssueID      string
	State        string
	SessionChain string
	Model        string
	ParkedReason string
	CreatedAt    string
	UpdatedAt    string
}

// NewGrillSession is the input to Create. State always starts at running.
type NewGrillSession struct {
	Repo    string
	IssueID string
	Model   string
}

// GrillMessage is one message in a session's conversation. Payload is the message's
// JSON body as stored (a question's text/options, an answer's text, an outcome's
// disposition and proposal).
type GrillMessage struct {
	ID        int64
	SessionID int64
	Role      string
	Kind      string
	Payload   string
	CreatedAt string
}

// NewGrillMessage is the input to AppendMessage.
type NewGrillMessage struct {
	Role    string
	Kind    string
	Payload string
}

// Grill is the hub's authoritative store of web grilling sessions and their
// messages (ADR 0008). Children reach it only over MCP/HTTP; the tables — not run
// files — are the source of truth. retention bounds how many settled sessions per
// repo Prune keeps.
type Grill struct {
	db        *sql.DB
	retention int
}

// NewGrill returns a Grill store over db, pruned to the most recent retention
// sessions per repo. The caller owns db's lifecycle.
func NewGrill(db *sql.DB, retention int) *Grill { return &Grill{db: db, retention: retention} }

// Create opens a session in the running state, enforcing one active session per
// issue: a create for an issue that already has an unsettled session returns
// ErrGrillActiveSession. Authoring sessions (empty IssueID) anchor to the repo
// alone and are never blocked. The guard is a single atomic insert so concurrent
// creates cannot both win.
func (g *Grill) Create(ns NewGrillSession) (GrillSession, error) {
	now := formatGrillTime(time.Now())
	res, err := g.db.Exec(
		`INSERT INTO grill_sessions(repo, issue_id, state, session_chain, model, parked_reason, created_at, updated_at)
		 SELECT ?, ?, 'running', '', ?, '', ?, ?
		 WHERE ? = '' OR NOT EXISTS (
		     SELECT 1 FROM grill_sessions
		     WHERE repo = ? AND issue_id = ? AND state NOT IN ('applied', 'abandoned'))`,
		ns.Repo, ns.IssueID, ns.Model, now, now, ns.IssueID, ns.Repo, ns.IssueID,
	)
	if err != nil {
		return GrillSession{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return GrillSession{}, err
	}
	if affected == 0 {
		return GrillSession{}, ErrGrillActiveSession
	}
	id, err := res.LastInsertId()
	if err != nil {
		return GrillSession{}, err
	}
	return GrillSession{
		ID:        id,
		Repo:      ns.Repo,
		IssueID:   ns.IssueID,
		State:     GrillRunning,
		Model:     ns.Model,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// Session returns a session by id and whether it exists.
func (g *Grill) Session(id int64) (GrillSession, bool, error) {
	sessions, err := g.scanSessions(
		`SELECT id, repo, issue_id, state, session_chain, model, parked_reason, created_at, updated_at
		 FROM grill_sessions WHERE id = ?`, id,
	)
	if err != nil {
		return GrillSession{}, false, err
	}
	if len(sessions) == 0 {
		return GrillSession{}, false, nil
	}
	return sessions[0], true, nil
}

// List returns repo's sessions, newest first. A non-empty state narrows to that
// state.
func (g *Grill) List(repo, state string) ([]GrillSession, error) {
	query := `SELECT id, repo, issue_id, state, session_chain, model, parked_reason, created_at, updated_at
	          FROM grill_sessions WHERE repo = ?`
	args := []any{repo}
	if state != "" {
		query += ` AND state = ?`
		args = append(args, state)
	}
	query += ` ORDER BY id DESC`
	return g.scanSessions(query, args...)
}

// Messages returns a session's messages with id greater than after, in order —
// the detail read (after 0) and the SSE reconnect backfill from a client's
// last-seen id.
func (g *Grill) Messages(sessionID, after int64) (out []GrillMessage, err error) {
	q, err := g.db.Query(
		`SELECT id, session_id, role, kind, payload, created_at
		 FROM grill_messages WHERE session_id = ? AND id > ? ORDER BY id`,
		sessionID, after,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	out = []GrillMessage{}
	for q.Next() {
		var m GrillMessage
		if err := q.Scan(&m.ID, &m.SessionID, &m.Role, &m.Kind, &m.Payload, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, q.Err()
}

// AppendMessage appends one message to a session and bumps the session's
// updated_at, returning the stored message with its assigned id. It reports
// whether the session exists.
func (g *Grill) AppendMessage(sessionID int64, nm NewGrillMessage) (GrillMessage, bool, error) {
	payload := nm.Payload
	if payload == "" {
		payload = "{}"
	}
	now := formatGrillTime(time.Now())
	tx, err := g.db.Begin()
	if err != nil {
		return GrillMessage{}, false, err
	}
	res, err := tx.Exec(
		`INSERT INTO grill_messages(session_id, role, kind, payload, created_at)
		 SELECT ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM grill_sessions WHERE id = ?)`,
		sessionID, nm.Role, nm.Kind, payload, now, sessionID,
	)
	if err != nil {
		return GrillMessage{}, false, errors.Join(err, tx.Rollback())
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return GrillMessage{}, false, errors.Join(err, tx.Rollback())
	}
	if affected == 0 {
		return GrillMessage{}, false, tx.Rollback()
	}
	id, err := res.LastInsertId()
	if err != nil {
		return GrillMessage{}, false, errors.Join(err, tx.Rollback())
	}
	if _, err := tx.Exec(`UPDATE grill_sessions SET updated_at = ? WHERE id = ?`, now, sessionID); err != nil {
		return GrillMessage{}, false, errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return GrillMessage{}, false, err
	}
	return GrillMessage{ID: id, SessionID: sessionID, Role: nm.Role, Kind: nm.Kind, Payload: payload, CreatedAt: now}, true, nil
}

// MarkBlockRelation records that a split apply has written the blocker→blocked
// relation to the tracker, so a retry skips it rather than filing a duplicate
// link. It is idempotent on the (repo, blocker, blocked) key.
func (g *Grill) MarkBlockRelation(repo, blocker, blocked string) error {
	_, err := g.db.Exec(
		`INSERT INTO grill_relations(repo, blocker, blocked) VALUES(?, ?, ?)
		 ON CONFLICT(repo, blocker, blocked) DO NOTHING`,
		repo, blocker, blocked,
	)
	return err
}

// BlockRelations returns the blocking relations already written for a repo, keyed
// by [blocker, blocked] identifier pair, so an apply re-attempts only the ones
// that never landed.
func (g *Grill) BlockRelations(repo string) (out map[[2]string]bool, err error) {
	q, err := g.db.Query(`SELECT blocker, blocked FROM grill_relations WHERE repo = ?`, repo)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	out = map[[2]string]bool{}
	for q.Next() {
		var blocker, blocked string
		if err := q.Scan(&blocker, &blocked); err != nil {
			return nil, err
		}
		out[[2]string{blocker, blocked}] = true
	}
	return out, q.Err()
}

// Transition moves a session to state, enforcing the legal state machine, and sets
// its parked_reason (the cause a stalled/parked session carries; empty clears it).
// It returns ErrGrillNotFound for an unknown id and ErrGrillTransition for an
// illegal move.
func (g *Grill) Transition(id int64, state, parkedReason string) (GrillSession, error) {
	sess, found, err := g.Session(id)
	if err != nil {
		return GrillSession{}, err
	}
	if !found {
		return GrillSession{}, ErrGrillNotFound
	}
	if !grillTransitions[sess.State][state] {
		return GrillSession{}, ErrGrillTransition
	}
	now := formatGrillTime(time.Now())
	if _, err := g.db.Exec(
		`UPDATE grill_sessions SET state = ?, parked_reason = ?, updated_at = ? WHERE id = ?`,
		state, parkedReason, now, id,
	); err != nil {
		return GrillSession{}, err
	}
	sess.State = state
	sess.ParkedReason = parkedReason
	sess.UpdatedAt = now
	return sess, nil
}

// UpdateChain records the latest Claude session id for a session and bumps its
// updated_at — the per-turn chain update. It reports whether the session exists.
func (g *Grill) UpdateChain(id int64, sessionChain string) (GrillSession, bool, error) {
	sess, found, err := g.Session(id)
	if err != nil || !found {
		return GrillSession{}, found, err
	}
	now := formatGrillTime(time.Now())
	if _, err := g.db.Exec(
		`UPDATE grill_sessions SET session_chain = ?, updated_at = ? WHERE id = ?`,
		sessionChain, now, id,
	); err != nil {
		return GrillSession{}, false, err
	}
	sess.SessionChain = sessionChain
	sess.UpdatedAt = now
	return sess, true, nil
}

// SetIssue anchors a session to issueID and bumps its updated_at — the create-apply
// flow calls it once the parent issue is filed so a retry reuses that issue instead
// of filing it a second time. It reports whether the session exists.
func (g *Grill) SetIssue(id int64, issueID string) (GrillSession, bool, error) {
	sess, found, err := g.Session(id)
	if err != nil || !found {
		return GrillSession{}, found, err
	}
	now := formatGrillTime(time.Now())
	if _, err := g.db.Exec(
		`UPDATE grill_sessions SET issue_id = ?, updated_at = ? WHERE id = ?`,
		issueID, now, id,
	); err != nil {
		return GrillSession{}, false, err
	}
	sess.IssueID = issueID
	sess.UpdatedAt = now
	return sess, true, nil
}

// Prune keeps the most recent retention sessions per repo and drops the settled
// ones beyond that window, ranked by id (grilling-prd.md — the transcript retention
// pattern). Active sessions past the window are kept so a long-parked session is
// never pruned out from under its owner; their messages cascade on delete. A
// non-positive retention disables pruning.
func (g *Grill) Prune() error {
	if g.retention <= 0 {
		return nil
	}
	repos, err := distinctRepos(g.db, "grill_sessions")
	if err != nil {
		return err
	}
	for _, repo := range repos {
		var cutoff int64
		err := g.db.QueryRow(
			`SELECT id FROM grill_sessions WHERE repo = ? ORDER BY id DESC LIMIT 1 OFFSET ?`,
			repo, g.retention,
		).Scan(&cutoff)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := g.db.Exec(
			`DELETE FROM grill_sessions WHERE repo = ? AND id <= ? AND state IN ('applied', 'abandoned')`,
			repo, cutoff,
		); err != nil {
			return err
		}
	}
	return nil
}

// SweepIdle marks every active session last touched before cutoff as abandoned,
// returning the sessions it swept so the caller can announce the state change. It
// is the 30-day idle sweep (grilling-prd.md): a parked session nobody returns to
// eventually settles.
func (g *Grill) SweepIdle(cutoff time.Time) ([]GrillSession, error) {
	stale, err := g.scanSessions(
		`SELECT id, repo, issue_id, state, session_chain, model, parked_reason, created_at, updated_at
		 FROM grill_sessions WHERE state NOT IN ('applied', 'abandoned') AND updated_at < ? ORDER BY id`,
		formatGrillTime(cutoff),
	)
	if err != nil {
		return nil, err
	}
	if len(stale) == 0 {
		return nil, nil
	}
	now := formatGrillTime(time.Now())
	for i := range stale {
		if _, err := g.db.Exec(
			`UPDATE grill_sessions SET state = 'abandoned', parked_reason = '', updated_at = ? WHERE id = ?`,
			now, stale[i].ID,
		); err != nil {
			return nil, err
		}
		stale[i].State = GrillAbandoned
		stale[i].ParkedReason = ""
		stale[i].UpdatedAt = now
	}
	return stale, nil
}

func (g *Grill) scanSessions(query string, args ...any) (out []GrillSession, err error) {
	q, err := g.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	out = []GrillSession{}
	for q.Next() {
		var s GrillSession
		if err := q.Scan(
			&s.ID, &s.Repo, &s.IssueID, &s.State, &s.SessionChain,
			&s.Model, &s.ParkedReason, &s.CreatedAt, &s.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, q.Err()
}

func formatGrillTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
