package hubstore

import (
	"database/sql"
	"errors"
	"time"
)

// ErrSteerNoteNotFound is returned when a steer note id is not present in the repo.
var ErrSteerNoteNotFound = errors.New("steer note not found")

// ErrSteerNoteExpired is returned when a note that already left the queue unread
// is acked — the run it was typed at settled before an agent consumed it.
var ErrSteerNoteExpired = errors.New("steer note expired")

// SteerPending, SteerDelivered, and SteerExpired are the states a steer note
// moves through: queued and waiting for an agent, consumed by one, or swept when
// the ticket's run settled with the note still waiting.
const (
	SteerPending   = "pending"
	SteerDelivered = "delivered"
	SteerExpired   = "expired"
)

// SteerNote is one operator message queued against a running ticket. Body is
// free text and may span lines. DeliveredPhase carries the canonical phase label
// of the agent that consumed the note, empty until one does.
type SteerNote struct {
	ID             int64
	Ticket         string
	Body           string
	Status         string
	DeliveredPhase string
	CreatedAt      string
	DeliveredAt    string
}

// SteerNotes is the hub's authoritative per-ticket steer-note queue (ADR 0008).
// Every surface — the web UI, the CLI, and the pipeline child — queues, reads,
// and settles notes through the hub, so no other process writes the table. The
// caller owns db's lifecycle.
type SteerNotes struct {
	db  *sql.DB
	now func() time.Time
}

// NewSteerNotes returns a SteerNotes store over db.
func NewSteerNotes(db *sql.DB) *SteerNotes {
	return &SteerNotes{db: db, now: time.Now}
}

const steerNoteSelect = `SELECT id, ticket, body, status, delivered_phase, created_at, delivered_at FROM steer_notes`

// Queue appends a pending note to the ticket's queue and returns it with its
// allocated id, which is also its delivery order.
func (s *SteerNotes) Queue(repo, ticket, body string) (SteerNote, error) {
	res, err := s.db.Exec(
		`INSERT INTO steer_notes(repo, ticket, body, status, created_at) VALUES(?, ?, ?, ?, ?)`,
		repo, ticket, body, SteerPending, s.stamp(),
	)
	if err != nil {
		return SteerNote{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return SteerNote{}, err
	}
	return s.get(repo, id)
}

// List returns every note the ticket has ever carried, oldest first — the UI
// timeline's read.
func (s *SteerNotes) List(repo, ticket string) ([]SteerNote, error) {
	return s.scan(steerNoteSelect+` WHERE repo = ? AND ticket = ? ORDER BY id`, repo, ticket)
}

// Pending returns the ticket's undelivered notes, oldest first — the child's poll.
func (s *SteerNotes) Pending(repo, ticket string) ([]SteerNote, error) {
	return s.scan(steerNoteSelect+` WHERE repo = ? AND ticket = ? AND status = ? ORDER BY id`, repo, ticket, SteerPending)
}

// Ack marks a note delivered by the agent running phase. It is idempotent: an
// already delivered note is returned untouched, so a retried ack never rewrites
// the phase that first consumed it. A note swept by Expire returns
// ErrSteerNoteExpired, and an id absent from the repo ErrSteerNoteNotFound.
func (s *SteerNotes) Ack(repo string, id int64, phase string) (SteerNote, error) {
	note, err := s.get(repo, id)
	if errors.Is(err, sql.ErrNoRows) {
		return SteerNote{}, ErrSteerNoteNotFound
	}
	if err != nil {
		return SteerNote{}, err
	}
	switch note.Status {
	case SteerDelivered:
		return note, nil
	case SteerExpired:
		return note, ErrSteerNoteExpired
	}
	_, err = s.db.Exec(
		`UPDATE steer_notes SET status = ?, delivered_phase = ?, delivered_at = ? WHERE repo = ? AND id = ?`,
		SteerDelivered, phase, s.stamp(), repo, id,
	)
	if err != nil {
		return SteerNote{}, err
	}
	return s.get(repo, id)
}

// Expire sweeps the ticket's remaining pending notes, leaving delivered ones
// alone, and returns the notes it swept so the caller can report each. It is
// idempotent: a ticket with nothing pending yields an empty slice.
func (s *SteerNotes) Expire(repo, ticket string) ([]SteerNote, error) {
	pending, err := s.Pending(repo, ticket)
	if err != nil || len(pending) == 0 {
		return pending, err
	}
	if _, err := s.db.Exec(
		`UPDATE steer_notes SET status = ? WHERE repo = ? AND ticket = ? AND status = ?`,
		SteerExpired, repo, ticket, SteerPending,
	); err != nil {
		return nil, err
	}
	for i := range pending {
		pending[i].Status = SteerExpired
	}
	return pending, nil
}

func (s *SteerNotes) get(repo string, id int64) (SteerNote, error) {
	var n SteerNote
	err := s.db.QueryRow(steerNoteSelect+` WHERE repo = ? AND id = ?`, repo, id).Scan(
		&n.ID, &n.Ticket, &n.Body, &n.Status, &n.DeliveredPhase, &n.CreatedAt, &n.DeliveredAt,
	)
	return n, err
}

func (s *SteerNotes) scan(query string, args ...any) (out []SteerNote, err error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out = []SteerNote{}
	for rows.Next() {
		var n SteerNote
		if err := rows.Scan(
			&n.ID, &n.Ticket, &n.Body, &n.Status, &n.DeliveredPhase, &n.CreatedAt, &n.DeliveredAt,
		); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *SteerNotes) stamp() string { return s.now().UTC().Format(time.RFC3339) }
