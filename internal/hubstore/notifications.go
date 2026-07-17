package hubstore

import (
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// Notification kinds. grill_question is the one coalescing kind — at most one
// unread row per grilling session — while the run kinds each record a distinct
// pause, fault, or quarantine fact.
const (
	NotificationGrillQuestion  = "grill_question"
	NotificationRunPaused      = "run_paused"
	NotificationRunFaulted     = "run_faulted"
	NotificationRunQuarantined = "run_quarantined"
)

// notificationReadRetention bounds how many read notifications the store keeps;
// older read rows are pruned on insert. Unread rows are never pruned.
const notificationReadRetention = 200

// Notification is one durable needs-attention record: a grilling session awaiting
// the user or a run that paused, faulted, or was quarantined. Ref is the grilling
// session id or the run's ticket; IssueID names the tracker issue when there is
// one. ReadAt is empty while the notification is unread.
type Notification struct {
	ID        int64  `json:"id"`
	Repo      string `json:"repo"`
	Kind      string `json:"kind"`
	Ref       string `json:"ref"`
	IssueID   string `json:"issue_id,omitempty"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	ReadAt    string `json:"read_at,omitempty"`
}

// Notifications is the hub's authoritative notification store. Producers write
// through the two Notify methods, the web reads and clears through List /
// MarkRead / MarkAllRead, and grilling sessions self-clear through
// ResolveGrillQuestion when they leave the awaiting set. The caller owns db's
// lifecycle.
type Notifications struct {
	db *sql.DB
}

// NewNotifications returns a Notifications store over db.
func NewNotifications(db *sql.DB) *Notifications { return &Notifications{db: db} }

const notificationSelect = `SELECT id, repo, kind, ref, COALESCE(issue_id, ''), title, body,
		created_at, updated_at, COALESCE(read_at, '')
	 FROM notifications`

// NotifyGrillQuestion records that a grilling session is awaiting the user. It
// coalesces on the session's unread (grill_question, ref) row: an existing unread
// notification is bumped — its updated_at refreshed and its body replaced when a
// new one is supplied — rather than stacking a second row, so a session shows at
// most one unread entry. A session whose last notification was already read
// inserts a fresh unread one.
func (n *Notifications) NotifyGrillQuestion(repo string, sessionID int64, issueID, title, body string) (Notification, error) {
	ref := strconv.FormatInt(sessionID, 10)
	now := formatNotificationTime(time.Now())
	tx, err := n.db.Begin()
	if err != nil {
		return Notification{}, err
	}
	var id int64
	err = tx.QueryRow(
		`SELECT id FROM notifications WHERE kind = ? AND ref = ? AND read_at IS NULL ORDER BY id DESC LIMIT 1`,
		NotificationGrillQuestion, ref,
	).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id, err = insertNotification(tx, repo, NotificationGrillQuestion, ref, issueID, title, body, now)
		if err != nil {
			return Notification{}, errors.Join(err, tx.Rollback())
		}
	case err != nil:
		return Notification{}, errors.Join(err, tx.Rollback())
	default:
		// Refresh recency and replace the body only when a fresh one is supplied, so a
		// later transition carrying no text (a park after the question) keeps the
		// stored question rather than blanking it.
		if _, err := tx.Exec(
			`UPDATE notifications SET body = CASE WHEN ? <> '' THEN ? ELSE body END, updated_at = ? WHERE id = ?`,
			body, body, now, id,
		); err != nil {
			return Notification{}, errors.Join(err, tx.Rollback())
		}
	}
	return n.commitAndLoad(tx, id)
}

// NotifyRunAttention records a run that needs attention — a pause, fault, or
// quarantine. Each is a distinct fact, so it always inserts.
func (n *Notifications) NotifyRunAttention(repo, kind, runID, issueID, title, body string) (Notification, error) {
	now := formatNotificationTime(time.Now())
	tx, err := n.db.Begin()
	if err != nil {
		return Notification{}, err
	}
	id, err := insertNotification(tx, repo, kind, runID, issueID, title, body, now)
	if err != nil {
		return Notification{}, errors.Join(err, tx.Rollback())
	}
	return n.commitAndLoad(tx, id)
}

// ResolveGrillQuestion marks the session's unread grill_question notification read,
// clearing a stale "needs you" entry when the session leaves the awaiting set. It
// is a no-op when the session has no unread notification.
func (n *Notifications) ResolveGrillQuestion(sessionID int64) error {
	_, err := n.db.Exec(
		`UPDATE notifications SET read_at = ? WHERE kind = ? AND ref = ? AND read_at IS NULL`,
		formatNotificationTime(time.Now()), NotificationGrillQuestion, strconv.FormatInt(sessionID, 10),
	)
	return err
}

// List returns the most recent notifications, newest first by updated_at, capped at
// limit. A non-positive limit defaults to 100.
func (n *Notifications) List(limit int) ([]Notification, error) {
	if limit <= 0 {
		limit = 100
	}
	return n.scan(notificationSelect+` ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
}

// UnreadCount returns how many notifications are unread.
func (n *Notifications) UnreadCount() (int, error) {
	var count int
	err := n.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE read_at IS NULL`).Scan(&count)
	return count, err
}

// MarkRead marks one notification read. Marking an already-read or unknown row is a
// no-op.
func (n *Notifications) MarkRead(id int64) error {
	_, err := n.db.Exec(
		`UPDATE notifications SET read_at = ? WHERE id = ? AND read_at IS NULL`,
		formatNotificationTime(time.Now()), id,
	)
	return err
}

// MarkAllRead marks every unread notification read.
func (n *Notifications) MarkAllRead() error {
	_, err := n.db.Exec(
		`UPDATE notifications SET read_at = ? WHERE read_at IS NULL`,
		formatNotificationTime(time.Now()),
	)
	return err
}

// commitAndLoad prunes read rows past the retention window, commits, and returns the
// stored notification. Pruning inside the insert transaction keeps the read history
// bounded without a separate sweep.
func (n *Notifications) commitAndLoad(tx *sql.Tx, id int64) (Notification, error) {
	if _, err := tx.Exec(
		`DELETE FROM notifications WHERE read_at IS NOT NULL AND id NOT IN (
			SELECT id FROM notifications WHERE read_at IS NOT NULL ORDER BY id DESC LIMIT ?
		)`,
		notificationReadRetention,
	); err != nil {
		return Notification{}, errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return Notification{}, err
	}
	return n.byID(id)
}

func (n *Notifications) byID(id int64) (Notification, error) {
	rows, err := n.scan(notificationSelect+` WHERE id = ?`, id)
	if err != nil {
		return Notification{}, err
	}
	if len(rows) == 0 {
		return Notification{}, sql.ErrNoRows
	}
	return rows[0], nil
}

func insertNotification(tx *sql.Tx, repo, kind, ref, issueID, title, body, now string) (int64, error) {
	res, err := tx.Exec(
		`INSERT INTO notifications(repo, kind, ref, issue_id, title, body, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		repo, kind, ref, issueID, title, body, now, now,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (n *Notifications) scan(query string, args ...any) (out []Notification, err error) {
	q, err := n.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	out = []Notification{}
	for q.Next() {
		var nt Notification
		if err := q.Scan(
			&nt.ID, &nt.Repo, &nt.Kind, &nt.Ref, &nt.IssueID, &nt.Title, &nt.Body,
			&nt.CreatedAt, &nt.UpdatedAt, &nt.ReadAt,
		); err != nil {
			return nil, err
		}
		out = append(out, nt)
	}
	return out, q.Err()
}

func formatNotificationTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
