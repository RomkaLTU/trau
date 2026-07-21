package hubstore

import (
	"database/sql"
	"errors"
	"time"
)

// ErrQAAccountNotFound is returned when a QA account id is not present in the repo.
var ErrQAAccountNotFound = errors.New("qa account not found")

// QAAccount is one stored login the browser verifier can use to reach an
// auth-walled app: a human label, the username and secret, and a description of
// the cases or flows the account covers. Secret is the full value — the store is
// the source of truth; the settings API masks it on read while the loop reads it
// whole.
type QAAccount struct {
	ID          int64
	Label       string
	Username    string
	Secret      string
	Description string
	CreatedAt   string
	UpdatedAt   string
}

// QAAccountInput is the editable content of a QA account.
type QAAccountInput struct {
	Label       string
	Username    string
	Secret      string
	Description string
}

// QAAccounts is the hub's per-repo store of QA credentials and the free-text QA
// notes that accompany them. The caller owns db's lifecycle.
type QAAccounts struct {
	db  *sql.DB
	now func() time.Time
}

// NewQAAccounts returns a QAAccounts store over db.
func NewQAAccounts(db *sql.DB) *QAAccounts {
	return &QAAccounts{db: db, now: time.Now}
}

const qaAccountSelect = `SELECT id, label, username, secret, description, created_at, updated_at FROM qa_accounts`

// Create files a new QA account for the repo, stamping its created and updated
// times, and returns it with its allocated id.
func (q *QAAccounts) Create(repo string, in QAAccountInput) (QAAccount, error) {
	now := q.stamp()
	res, err := q.db.Exec(
		`INSERT INTO qa_accounts(repo, label, username, secret, description, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		repo, in.Label, in.Username, in.Secret, in.Description, now, now,
	)
	if err != nil {
		return QAAccount{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return QAAccount{}, err
	}
	return q.get(repo, id)
}

// List returns the repo's QA accounts, ordered by label, with full secret values.
func (q *QAAccounts) List(repo string) ([]QAAccount, error) {
	return q.scan(qaAccountSelect+` WHERE repo = ? ORDER BY label`, repo)
}

// Get returns one QA account by id and whether it exists in the repo.
func (q *QAAccounts) Get(repo string, id int64) (QAAccount, bool, error) {
	a, err := q.get(repo, id)
	if errors.Is(err, sql.ErrNoRows) {
		return QAAccount{}, false, nil
	}
	if err != nil {
		return QAAccount{}, false, err
	}
	return a, true, nil
}

// ByLabel returns the repo's account with the given label and whether one exists,
// so a create or relabel can reject a duplicate before it hits the constraint.
func (q *QAAccounts) ByLabel(repo, label string) (QAAccount, bool, error) {
	a, err := q.one(qaAccountSelect+` WHERE repo = ? AND label = ?`, repo, label)
	if errors.Is(err, sql.ErrNoRows) {
		return QAAccount{}, false, nil
	}
	if err != nil {
		return QAAccount{}, false, err
	}
	return a, true, nil
}

// Update overwrites a QA account's content, refreshing its updated time. It
// returns ErrQAAccountNotFound when the id is not in the repo.
func (q *QAAccounts) Update(repo string, id int64, in QAAccountInput) (QAAccount, error) {
	res, err := q.db.Exec(
		`UPDATE qa_accounts SET label = ?, username = ?, secret = ?, description = ?, updated_at = ?
		 WHERE repo = ? AND id = ?`,
		in.Label, in.Username, in.Secret, in.Description, q.stamp(), repo, id,
	)
	if err != nil {
		return QAAccount{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return QAAccount{}, err
	}
	if n == 0 {
		return QAAccount{}, ErrQAAccountNotFound
	}
	return q.get(repo, id)
}

// Delete removes a QA account and reports whether a row was deleted.
func (q *QAAccounts) Delete(repo string, id int64) (bool, error) {
	res, err := q.db.Exec(`DELETE FROM qa_accounts WHERE repo = ? AND id = ?`, repo, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Notes returns the repo's free-text QA notes, empty when none are stored.
func (q *QAAccounts) Notes(repo string) (string, error) {
	var notes string
	err := q.db.QueryRow(`SELECT notes FROM qa_notes WHERE repo = ?`, repo).Scan(&notes)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return notes, nil
}

// SetNotes replaces the repo's free-text QA notes.
func (q *QAAccounts) SetNotes(repo, notes string) error {
	_, err := q.db.Exec(
		`INSERT INTO qa_notes(repo, notes, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(repo) DO UPDATE SET notes = excluded.notes, updated_at = excluded.updated_at`,
		repo, notes, q.stamp(),
	)
	return err
}

func (q *QAAccounts) get(repo string, id int64) (QAAccount, error) {
	return q.one(qaAccountSelect+` WHERE repo = ? AND id = ?`, repo, id)
}

func (q *QAAccounts) one(query string, args ...any) (QAAccount, error) {
	var a QAAccount
	err := q.db.QueryRow(query, args...).Scan(
		&a.ID, &a.Label, &a.Username, &a.Secret, &a.Description, &a.CreatedAt, &a.UpdatedAt,
	)
	return a, err
}

func (q *QAAccounts) scan(query string, args ...any) (out []QAAccount, err error) {
	rows, err := q.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out = []QAAccount{}
	for rows.Next() {
		var a QAAccount
		if err := rows.Scan(
			&a.ID, &a.Label, &a.Username, &a.Secret, &a.Description, &a.CreatedAt, &a.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (q *QAAccounts) stamp() string { return q.now().UTC().Format(time.RFC3339) }
