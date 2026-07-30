package hubstore

import (
	"database/sql"
	"errors"
	"time"
)

// TicketRepo returns the member root the ticket was last started in for the
// project, or "" when the project has never started it.
func (p *Projects) TicketRepo(id, ticket string) (string, error) {
	var root string
	err := p.db.QueryRow(
		`SELECT root FROM project_ticket_repos WHERE project = ? AND ticket = ?`, id, ticket,
	).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return root, nil
}

// LastTicketRepo returns the member root the project started work in most
// recently, or "" when it has never started anything.
func (p *Projects) LastTicketRepo(id string) (string, error) {
	var root string
	err := p.db.QueryRow(
		`SELECT root FROM project_ticket_repos WHERE project = ? ORDER BY started_at DESC LIMIT 1`, id,
	).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return root, nil
}

// RecordTicketRepo remembers root as the member the ticket runs in, replacing an
// earlier answer so the latest choice is the one a re-start pre-selects.
func (p *Projects) RecordTicketRepo(id, ticket, root string) error {
	_, err := p.db.Exec(
		`INSERT INTO project_ticket_repos(project, ticket, root, started_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(project, ticket) DO UPDATE SET root = excluded.root, started_at = excluded.started_at`,
		id, ticket, root, time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func dropTicketRepos(tx *sql.Tx, id string) error {
	_, err := tx.Exec(`DELETE FROM project_ticket_repos WHERE project = ?`, id)
	return err
}
