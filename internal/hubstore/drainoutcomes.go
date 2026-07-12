package hubstore

import (
	"database/sql"
	"errors"

	"github.com/RomkaLTU/trau/internal/queue"
)

// DrainOutcomes is the hub's record of how each queued child exited, keyed by
// repo and ticket (ADR 0008). A queued child posts its outcome over HTTP as it
// exits; the drainer reads it to settle the item and clears it before respawning.
// A row's presence is the signal that distinguishes a child that reported (even a
// clean finish, which reports an empty class) from one killed before it could —
// the boundary between settling done and pausing on an unknown outcome.
type DrainOutcomes struct {
	db *sql.DB
}

// NewDrainOutcomes returns a DrainOutcomes store over db. The caller owns db's
// lifecycle.
func NewDrainOutcomes(db *sql.DB) *DrainOutcomes {
	return &DrainOutcomes{db: db}
}

// Upsert records a ticket's exit outcome, replacing any prior row in place.
func (d *DrainOutcomes) Upsert(root, ticket, class, reason string) error {
	_, err := d.db.Exec(
		`INSERT INTO drain_outcomes(repo, ticket, class, reason)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(repo, ticket) DO UPDATE SET class = excluded.class, reason = excluded.reason`,
		root, ticket, class, reason,
	)
	return err
}

// One returns a ticket's recorded outcome and whether the hub holds one. found is
// true even for a clean finish (an empty class), so the drainer can tell a
// reported clean finish from a child that never reported at all.
func (d *DrainOutcomes) One(root, ticket string) (rep queue.DrainReport, found bool, err error) {
	err = d.db.QueryRow(
		`SELECT class, reason FROM drain_outcomes WHERE repo = ? AND ticket = ?`,
		root, ticket,
	).Scan(&rep.Class, &rep.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return queue.DrainReport{}, false, nil
	}
	if err != nil {
		return queue.DrainReport{}, false, err
	}
	return rep, true, nil
}

// Remove drops a ticket's recorded outcome — cleared before a respawn and after a
// settle. A ticket with none is not an error.
func (d *DrainOutcomes) Remove(root, ticket string) error {
	_, err := d.db.Exec(`DELETE FROM drain_outcomes WHERE repo = ? AND ticket = ?`, root, ticket)
	return err
}
