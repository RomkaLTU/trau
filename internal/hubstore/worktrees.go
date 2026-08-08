package hubstore

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// The standings a worktree row can hold. Active means a tree is on disk; settled
// means the ticket finished and the tree was removed; orphaned means the row
// outlived its directory and nothing is left to remove.
const (
	WorktreeActive   = "active"
	WorktreeSettled  = "settled"
	WorktreeOrphaned = "orphaned"
)

// ErrWorktreeNotFound is returned when a worktree id is not present in the repo.
var ErrWorktreeNotFound = errors.New("worktree not found")

// ErrWorktreeRequired is returned when a row is filed without a ticket or a path.
// Either one missing would leave a record nothing can act on: the ticket is the
// key, the path is what gets removed.
var ErrWorktreeRequired = errors.New("worktree ticket and path are required")

// Worktree is the hub's record of one per-ticket git worktree: where it is, which
// branch it holds, and whether it is still standing. The path is computed from
// configuration by the loop and the hub alike, so this row exists to drive the
// lifecycle — removal on settle, orphan reconcile on boot — not to locate the tree.
type Worktree struct {
	ID        int64
	Repo      string
	Ticket    string
	Path      string
	Branch    string
	CreatedAt string
	State     string
}

// WorktreeInput is the content a child reports when it creates or adopts a tree.
type WorktreeInput struct {
	Ticket string
	Path   string
	Branch string
	// State defaults to active; a settle path reports the terminal standing
	// directly so the CLI and the hub agree without a second round trip.
	State string
}

// Worktrees is the hub's store of per-ticket worktrees, keyed by (repo, ticket) so
// a resume that adopts an existing tree updates the row it already has instead of
// filing a second one. The caller owns db's lifecycle.
type Worktrees struct {
	db  *sql.DB
	now func() time.Time
}

// NewWorktrees returns a Worktrees store over db.
func NewWorktrees(db *sql.DB) *Worktrees {
	return &Worktrees{db: db, now: time.Now}
}

const worktreeSelect = `SELECT id, repo, ticket, path, branch, created_at, state FROM worktrees`

// Record files the tree a run created or adopted, or updates the row a previous
// run left for the same ticket. The created stamp survives an update: a resumed
// tree is the same tree, and when it first appeared is the interesting fact.
func (w *Worktrees) Record(repo string, in WorktreeInput) (Worktree, error) {
	in, err := in.normalized()
	if err != nil {
		return Worktree{}, err
	}
	_, err = w.db.Exec(
		`INSERT INTO worktrees(repo, ticket, path, branch, created_at, state)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(repo, ticket) DO UPDATE SET
		     path = excluded.path, branch = excluded.branch, state = excluded.state`,
		repo, in.Ticket, in.Path, in.Branch, w.stamp(), in.State,
	)
	if err != nil {
		return Worktree{}, err
	}
	return w.byTicket(repo, in.Ticket)
}

// List returns the repo's worktree rows, newest first.
func (w *Worktrees) List(repo string) ([]Worktree, error) {
	return w.scan(worktreeSelect+` WHERE repo = ? ORDER BY created_at DESC, id DESC`, repo)
}

// Active returns every active row across every repo — what a boot reconcile
// compares against git's own worktree list and the disk.
func (w *Worktrees) Active() ([]Worktree, error) {
	return w.scan(worktreeSelect+` WHERE state = ? ORDER BY repo, ticket`, WorktreeActive)
}

// AllForRepo returns every row for a repo regardless of state, so a reconcile can
// also prune the tree a settled row unexpectedly still has on disk.
func (w *Worktrees) AllForRepo(repo string) ([]Worktree, error) {
	return w.scan(worktreeSelect+` WHERE repo = ? ORDER BY id`, repo)
}

// Get returns one row by id and whether it exists in the repo.
func (w *Worktrees) Get(repo string, id int64) (Worktree, bool, error) {
	return found(w.one(worktreeSelect+` WHERE repo = ? AND id = ?`, repo, id))
}

// ByTicket returns the repo's row for a ticket and whether one exists.
func (w *Worktrees) ByTicket(repo, ticket string) (Worktree, bool, error) {
	return found(w.byTicket(repo, ticket))
}

// SetState moves a row's standing — settled once its tree is removed, orphaned
// once its directory is found gone. It returns ErrWorktreeNotFound when the id is
// not in the repo.
func (w *Worktrees) SetState(repo string, id int64, state string) (Worktree, error) {
	res, err := w.db.Exec(`UPDATE worktrees SET state = ? WHERE repo = ? AND id = ?`, state, repo, id)
	if err != nil {
		return Worktree{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Worktree{}, err
	}
	if n == 0 {
		return Worktree{}, ErrWorktreeNotFound
	}
	return w.one(worktreeSelect+` WHERE repo = ? AND id = ?`, repo, id)
}

// SettleTicket marks a ticket's row settled and reports whether there was one.
// It is the ticket-keyed settle every removal path lands on, since a settle knows
// the ticket it finished, never a row id.
func (w *Worktrees) SettleTicket(repo, ticket string) (bool, error) {
	res, err := w.db.Exec(
		`UPDATE worktrees SET state = ? WHERE repo = ? AND ticket = ?`,
		WorktreeSettled, repo, ticket,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Delete removes a row outright and reports whether one was deleted.
func (w *Worktrees) Delete(repo string, id int64) (bool, error) {
	res, err := w.db.Exec(`DELETE FROM worktrees WHERE repo = ? AND id = ?`, repo, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (w *Worktrees) byTicket(repo, ticket string) (Worktree, error) {
	return w.one(worktreeSelect+` WHERE repo = ? AND ticket = ?`, repo, ticket)
}

func (w *Worktrees) one(query string, args ...any) (Worktree, error) {
	var t Worktree
	err := w.db.QueryRow(query, args...).Scan(
		&t.ID, &t.Repo, &t.Ticket, &t.Path, &t.Branch, &t.CreatedAt, &t.State,
	)
	return t, err
}

func (w *Worktrees) scan(query string, args ...any) (out []Worktree, err error) {
	rows, err := w.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out = []Worktree{}
	for rows.Next() {
		var t Worktree
		if err := rows.Scan(&t.ID, &t.Repo, &t.Ticket, &t.Path, &t.Branch, &t.CreatedAt, &t.State); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (w *Worktrees) stamp() string { return w.now().UTC().Format(time.RFC3339) }

func (in WorktreeInput) normalized() (WorktreeInput, error) {
	in.Ticket = strings.TrimSpace(in.Ticket)
	in.Path = strings.TrimSpace(in.Path)
	in.Branch = strings.TrimSpace(in.Branch)
	in.State = strings.TrimSpace(in.State)
	if in.Ticket == "" || in.Path == "" {
		return in, ErrWorktreeRequired
	}
	switch in.State {
	case "":
		in.State = WorktreeActive
	case WorktreeActive, WorktreeSettled, WorktreeOrphaned:
	default:
		return in, errors.New("worktree state must be active, settled, or orphaned")
	}
	return in, nil
}

// found turns the no-rows case into the (zero, false, nil) every reader wants,
// keeping "absent" apart from "the read failed".
func found(t Worktree, err error) (Worktree, bool, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return Worktree{}, false, nil
	}
	if err != nil {
		return Worktree{}, false, err
	}
	return t, true, nil
}
