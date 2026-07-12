package hubstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/RomkaLTU/trau/internal/state"
)

// CheckpointRow is a ticket's checkpoint: the fields the run board reads directly
// plus the full key set as JSON so nothing the loop recorded is lost.
type CheckpointRow struct {
	Phase         string
	Title         string
	Branch        string
	PR            string
	PRURL         string
	FailureReason string
	UpdatedAt     string
	Data          string
}

// TicketCheckpoint pairs a checkpoint with the ticket it belongs to, for the run
// board's and resume scan's whole-repo reads.
type TicketCheckpoint struct {
	Ticket string
	CheckpointRow
}

// Checkpoints is the hub's authoritative per-ticket checkpoint store (ADR 0008).
// The loop child writes every phase transition here over HTTP, so the table — not
// a run file — is the source of truth. It is a real migrated table, never dropped
// and rebuilt.
type Checkpoints struct {
	db       *sql.DB
	mu       sync.Mutex
	imported map[string]bool
}

// NewCheckpoints returns a Checkpoints store over db. The caller owns db's lifecycle.
func NewCheckpoints(db *sql.DB) *Checkpoints {
	return &Checkpoints{db: db, imported: map[string]bool{}}
}

// Upsert writes a ticket's checkpoint from its full key set, deriving the
// projected columns the board reads. It is keyed by (repo, ticket), so a rewrite
// replaces the row in place.
func (c *Checkpoints) Upsert(root, ticket string, data map[string]string) error {
	row, err := checkpointRowFromData(data)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(
		`INSERT INTO checkpoints(
		     repo, ticket, phase, title, branch, pr, pr_url, failure_reason, updated_at, data)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(repo, ticket) DO UPDATE SET
		     phase = excluded.phase, title = excluded.title, branch = excluded.branch,
		     pr = excluded.pr, pr_url = excluded.pr_url, failure_reason = excluded.failure_reason,
		     updated_at = excluded.updated_at, data = excluded.data`,
		root, ticket, row.Phase, row.Title, row.Branch, row.PR, row.PRURL, row.FailureReason, row.UpdatedAt, row.Data,
	)
	return err
}

// One returns a ticket's checkpoint and whether it exists.
func (c *Checkpoints) One(root, ticket string) (CheckpointRow, bool, error) {
	var r CheckpointRow
	err := c.db.QueryRow(
		`SELECT phase, title, branch, pr, pr_url, failure_reason, updated_at, data
		 FROM checkpoints WHERE repo = ? AND ticket = ?`, root, ticket,
	).Scan(&r.Phase, &r.Title, &r.Branch, &r.PR, &r.PRURL, &r.FailureReason, &r.UpdatedAt, &r.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return CheckpointRow{}, false, nil
	}
	if err != nil {
		return CheckpointRow{}, false, err
	}
	return r, true, nil
}

// Phase returns a ticket's checkpoint phase, or "" when it has no checkpoint.
func (c *Checkpoints) Phase(root, ticket string) string {
	row, ok, err := c.One(root, ticket)
	if err != nil || !ok {
		return ""
	}
	return row.Phase
}

// All returns every checkpoint for repo, each tagged with its ticket and ordered
// by ticket.
func (c *Checkpoints) All(root string) (rows []TicketCheckpoint, err error) {
	q, err := c.db.Query(
		`SELECT ticket, phase, title, branch, pr, pr_url, failure_reason, updated_at, data
		 FROM checkpoints WHERE repo = ? ORDER BY ticket`, root,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	rows = []TicketCheckpoint{}
	for q.Next() {
		var r TicketCheckpoint
		if err := q.Scan(
			&r.Ticket, &r.Phase, &r.Title, &r.Branch, &r.PR, &r.PRURL,
			&r.FailureReason, &r.UpdatedAt, &r.Data,
		); err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, q.Err()
}

// Remove drops a ticket's checkpoint. A missing row is not an error.
func (c *Checkpoints) Remove(root, ticket string) error {
	_, err := c.db.Exec(`DELETE FROM checkpoints WHERE repo = ? AND ticket = ?`, root, ticket)
	return err
}

// ImportLegacy folds any file-era state files under runsDir into the checkpoints
// table on the hub's first touch of a repo, removing each file only after its row
// commits (the COD-770 idiom). A ticket the hub already holds is left untouched
// and its stale file removed: the table row is authoritative and at least as
// fresh as the file (the loop only ever wrote files before the cutover), so a
// re-import never clobbers a checkpoint the hub has since progressed. It is
// idempotent, and a repo already imported this serve lifetime is skipped without
// touching disk.
func (c *Checkpoints) ImportLegacy(root, runsDir string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.imported[root] || runsDir == "" {
		c.imported[root] = true
		return nil
	}
	fs := state.NewStore(runsDir)
	for _, ticket := range fs.Tickets() {
		data, _, _, ok := fs.Load(ticket)
		if !ok {
			continue
		}
		_, exists, err := c.One(root, ticket)
		if err != nil {
			return fmt.Errorf("import legacy checkpoint %s/%s: %w", root, ticket, err)
		}
		if !exists {
			if err := c.Upsert(root, ticket, data); err != nil {
				return fmt.Errorf("import legacy checkpoint %s/%s: %w", root, ticket, err)
			}
		}
		if err := fs.RemoveState(ticket); err != nil {
			return fmt.Errorf("remove legacy checkpoint %s/%s: %w", root, ticket, err)
		}
	}
	c.imported[root] = true
	return nil
}

func checkpointRowFromData(data map[string]string) (CheckpointRow, error) {
	blob, err := json.Marshal(data)
	if err != nil {
		return CheckpointRow{}, err
	}
	return CheckpointRow{
		Phase:         data["PHASE"],
		Title:         data["TITLE"],
		Branch:        data["BRANCH"],
		PR:            data["PR"],
		PRURL:         data["PR_URL"],
		FailureReason: data["FAILURE_REASON"],
		UpdatedAt:     data["UPDATED"],
		Data:          string(blob),
	}, nil
}
