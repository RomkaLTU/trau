package hubstore

import (
	"database/sql"
	"errors"
)

// TeamRecord is one teammate's fetched snapshot: the writer id it was published
// under, the git identity to attribute it to, when they published it, when this
// machine last read it, and the raw payload as it arrived.
type TeamRecord struct {
	WriterID    string
	AuthorName  string
	AuthorEmail string
	UpdatedAt   string
	FetchedAt   string
	Payload     string
}

// TeamSyncState is a repo's team-sync bookkeeping: the writer id this machine
// publishes under, when its last pass finished, and the error that pass ended
// with — empty after a clean one.
type TeamSyncState struct {
	WriterID   string
	LastSyncAt string
	LastError  string
}

// TeamSync is the hub's read side for the records teammates publish over the
// repo's git remote, alongside the bookkeeping the settings surface and doctor
// report from. It never holds locally recorded lessons: those stay authoritative
// in the lessons table and are unioned with these at read time.
type TeamSync struct{ db *sql.DB }

// NewTeamSync returns a TeamSync store over db. The caller owns db's lifecycle.
func NewTeamSync(db *sql.DB) *TeamSync { return &TeamSync{db: db} }

// Replace swaps repo's teammate records for the ones a fetch just read. The remote
// refs are the whole truth about what teammates share, so a writer who withdrew
// their ref disappears here without any tombstone.
func (t *TeamSync) Replace(repo string, records []TeamRecord) error {
	tx, err := t.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM team_records WHERE repo = ?`, repo); err != nil {
		return err
	}
	for _, r := range records {
		if _, err := tx.Exec(
			`INSERT INTO team_records(repo, writer_id, author_name, author_email, updated_at, fetched_at, payload)
			 VALUES(?, ?, ?, ?, ?, ?, ?)`,
			repo, r.WriterID, r.AuthorName, r.AuthorEmail, r.UpdatedAt, r.FetchedAt, r.Payload,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Records returns the teammate snapshots the hub holds for repo, in writer-id
// order so a read is stable across passes. A repo nobody shares with yields an
// empty slice.
func (t *TeamSync) Records(repo string) (out []TeamRecord, err error) {
	q, err := t.db.Query(
		`SELECT writer_id, author_name, author_email, updated_at, fetched_at, payload
		 FROM team_records WHERE repo = ? ORDER BY writer_id`, repo,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	out = []TeamRecord{}
	for q.Next() {
		var r TeamRecord
		if scanErr := q.Scan(
			&r.WriterID, &r.AuthorName, &r.AuthorEmail, &r.UpdatedAt, &r.FetchedAt, &r.Payload,
		); scanErr != nil {
			return nil, scanErr
		}
		out = append(out, r)
	}
	return out, q.Err()
}

// SaveState records the outcome of repo's latest team-sync pass, replacing the
// previous one.
func (t *TeamSync) SaveState(repo string, st TeamSyncState) error {
	_, err := t.db.Exec(
		`INSERT INTO team_sync_state(repo, writer_id, last_sync_at, last_error) VALUES(?, ?, ?, ?)
		 ON CONFLICT(repo) DO UPDATE SET writer_id = excluded.writer_id,
		   last_sync_at = excluded.last_sync_at, last_error = excluded.last_error`,
		repo, st.WriterID, st.LastSyncAt, st.LastError,
	)
	return err
}

// State returns repo's team-sync bookkeeping. A repo that has never synced yields
// a zero state, not an error.
func (t *TeamSync) State(repo string) (TeamSyncState, error) {
	var st TeamSyncState
	err := t.db.QueryRow(
		`SELECT writer_id, last_sync_at, last_error FROM team_sync_state WHERE repo = ?`, repo,
	).Scan(&st.WriterID, &st.LastSyncAt, &st.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamSyncState{}, nil
	}
	if err != nil {
		return TeamSyncState{}, err
	}
	return st, nil
}
