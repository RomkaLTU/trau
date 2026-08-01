package hubstore

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// ErrAppURLNotFound is returned when an app URL id is not present in the repo.
var ErrAppURLNotFound = errors.New("app url not found")

// ErrAppURLRequired is returned when an entry is filed without a URL. The URL is
// the whole content of an entry — a blank one would leave browser verify with
// nothing to drive.
var ErrAppURLRequired = errors.New("url is required")

// ErrAppURLWorkspaceTaken is returned when a write would put a second entry on a
// (repo, workspace) pair. Callers check ByWorkspace first, but that check and the
// write are not atomic, so this is what the loser of a race gets.
var ErrAppURLWorkspaceTaken = errors.New("app url workspace already taken")

// AppURL is one stored browser-verify target: the URL, a human label, and the
// workspace whose app it serves. The entry with an empty Workspace is the repo's
// default target (today's APP_URL); an entry naming a workspace covers the slices
// whose changed files live under it (today's APP_URLS).
type AppURL struct {
	ID        int64
	Label     string
	URL       string
	Workspace string
	CreatedAt string
	UpdatedAt string
}

// AppURLInput is the editable content of an app URL entry.
type AppURLInput struct {
	Label     string
	URL       string
	Workspace string
}

// AppURLs is the hub's per-repo store of browser-verify targets. Uniqueness on
// (repo, workspace) keeps every workspace to one entry and, since the default
// entry is the one with an empty workspace, keeps the default single too. The
// caller owns db's lifecycle.
type AppURLs struct {
	db  *sql.DB
	now func() time.Time
}

// NewAppURLs returns an AppURLs store over db.
func NewAppURLs(db *sql.DB) *AppURLs {
	return &AppURLs{db: db, now: time.Now}
}

const appURLSelect = `SELECT id, label, url, workspace, created_at, updated_at FROM app_urls`

// Create files a new app URL entry for the repo, stamping its created and updated
// times, and returns it with its allocated id.
func (a *AppURLs) Create(repo string, in AppURLInput) (AppURL, error) {
	in, err := in.normalized()
	if err != nil {
		return AppURL{}, err
	}
	now := a.stamp()
	res, err := a.db.Exec(
		`INSERT INTO app_urls(repo, label, url, workspace, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		repo, in.Label, in.URL, in.Workspace, now, now,
	)
	if err != nil {
		return AppURL{}, appURLWriteErr(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return AppURL{}, err
	}
	return a.get(repo, id)
}

// List returns the repo's app URL entries, the default entry first and the
// workspace entries by workspace behind it.
func (a *AppURLs) List(repo string) ([]AppURL, error) {
	return a.scan(appURLSelect+` WHERE repo = ? ORDER BY workspace`, repo)
}

// Get returns one app URL entry by id and whether it exists in the repo.
func (a *AppURLs) Get(repo string, id int64) (AppURL, bool, error) {
	u, err := a.get(repo, id)
	if errors.Is(err, sql.ErrNoRows) {
		return AppURL{}, false, nil
	}
	if err != nil {
		return AppURL{}, false, err
	}
	return u, true, nil
}

// ByWorkspace returns the repo's entry for the given workspace and whether one
// exists, so a create or a re-scope can reject a duplicate — including a second
// default, which is the empty-workspace case — before it hits the constraint.
func (a *AppURLs) ByWorkspace(repo, workspace string) (AppURL, bool, error) {
	u, err := a.one(appURLSelect+` WHERE repo = ? AND workspace = ?`, repo, workspace)
	if errors.Is(err, sql.ErrNoRows) {
		return AppURL{}, false, nil
	}
	if err != nil {
		return AppURL{}, false, err
	}
	return u, true, nil
}

// Update overwrites an app URL entry's content, refreshing its updated time. It
// returns ErrAppURLNotFound when the id is not in the repo.
func (a *AppURLs) Update(repo string, id int64, in AppURLInput) (AppURL, error) {
	in, err := in.normalized()
	if err != nil {
		return AppURL{}, err
	}
	res, err := a.db.Exec(
		`UPDATE app_urls SET label = ?, url = ?, workspace = ?, updated_at = ?
		 WHERE repo = ? AND id = ?`,
		in.Label, in.URL, in.Workspace, a.stamp(), repo, id,
	)
	if err != nil {
		return AppURL{}, appURLWriteErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return AppURL{}, err
	}
	if n == 0 {
		return AppURL{}, ErrAppURLNotFound
	}
	return a.get(repo, id)
}

// Delete removes an app URL entry and reports whether a row was deleted.
func (a *AppURLs) Delete(repo string, id int64) (bool, error) {
	res, err := a.db.Exec(`DELETE FROM app_urls WHERE repo = ? AND id = ?`, repo, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (a *AppURLs) get(repo string, id int64) (AppURL, error) {
	return a.one(appURLSelect+` WHERE repo = ? AND id = ?`, repo, id)
}

func (a *AppURLs) one(query string, args ...any) (AppURL, error) {
	var u AppURL
	err := a.db.QueryRow(query, args...).Scan(
		&u.ID, &u.Label, &u.URL, &u.Workspace, &u.CreatedAt, &u.UpdatedAt,
	)
	return u, err
}

func (a *AppURLs) scan(query string, args ...any) (out []AppURL, err error) {
	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out = []AppURL{}
	for rows.Next() {
		var u AppURL
		if err := rows.Scan(&u.ID, &u.Label, &u.URL, &u.Workspace, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (a *AppURLs) stamp() string { return a.now().UTC().Format(time.RFC3339) }

func (in AppURLInput) normalized() (AppURLInput, error) {
	in.URL = strings.TrimSpace(in.URL)
	if in.URL == "" {
		return in, ErrAppURLRequired
	}
	return in, nil
}

// appURLWriteErr turns the (repo, workspace) UNIQUE violation into
// ErrAppURLWorkspaceTaken so a racing writer reads the same as one the
// ByWorkspace pre-check rejected, and drops the driver's text with it.
func appURLWriteErr(err error) error {
	var serr *sqlite.Error
	if errors.As(err, &serr) && serr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		return ErrAppURLWorkspaceTaken
	}
	return err
}
