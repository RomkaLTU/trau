package hubstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// BlockerRef is one inbound "blocked by" link as a tracker reader reports it:
// the blocker's identifier and whether the tracker already shows it resolved.
type BlockerRef struct {
	ID       string
	Resolved bool
}

// AddRelation records that blocker blocks blocked, idempotently: re-adding an
// edge that already exists is a no-op, so re-wiring an epic's graph on a retry
// files nothing new. Either side may name an identifier not yet in issues — a
// slice created out of order — and the edge is kept dangling rather than
// rejected; eligibility counts a dangling blocker as unresolved.
func (s *Issues) AddRelation(repo, blocker, blocked string) error {
	blocker = strings.TrimSpace(blocker)
	blocked = strings.TrimSpace(blocked)
	if blocker == "" || blocked == "" {
		return errors.New("relation needs both a blocker and a blocked identifier")
	}
	if strings.EqualFold(blocker, blocked) {
		return fmt.Errorf("%s cannot block itself", blocked)
	}
	_, err := s.db.Exec(
		`INSERT INTO issue_relations(repo, blocker, blocked) VALUES(?, ?, ?)
		 ON CONFLICT(repo, blocker, blocked) DO NOTHING`,
		repo, blocker, blocked,
	)
	return err
}

// RemoveRelation drops the blocker→blocked edge, tolerating one that was never
// recorded.
func (s *Issues) RemoveRelation(repo, blocker, blocked string) error {
	_, err := s.db.Exec(
		`DELETE FROM issue_relations WHERE repo = ? AND blocker = ? AND blocked = ?`,
		repo, blocker, blocked,
	)
	return err
}

// Blockers returns the identifiers blocking blocked, ordered.
func (s *Issues) Blockers(repo, blocked string) ([]string, error) {
	return s.relationSide(
		`SELECT blocker FROM issue_relations WHERE repo = ? AND blocked = ? ORDER BY blocker`,
		repo, blocked,
	)
}

// Dependents returns the identifiers blocked by blocker, ordered.
func (s *Issues) Dependents(repo, blocker string) ([]string, error) {
	return s.relationSide(
		`SELECT blocked FROM issue_relations WHERE repo = ? AND blocker = ? ORDER BY blocked`,
		repo, blocker,
	)
}

func (s *Issues) relationSide(query, repo, identifier string) (out []string, err error) {
	rows, err := s.db.Query(query, repo, identifier)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out = []string{}
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, scanErr
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ReflectBlockers reconciles a synced issue's stored blocked-by edges against the
// links a sync pull reports: edges the tracker no longer reports are dropped,
// missing ones are added, and ones already stored are left in place — so a re-sync
// never wipes and re-adds the graph. A blocker the tracker reports as already
// resolved is reflected only when its issue is in the store, where its live status
// settles resolution anyway; kept otherwise it would dangle and block its
// dependent forever, since a blocker outside the Project never syncs in to resolve.
// Only a stored synced row is reflected — an internal issue's graph belongs to
// AddRelation, and inbound sync never touches it (ADR 0007).
func (s *Issues) ReflectBlockers(repo, blocked string, refs []BlockerRef) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	var one int
	switch err = tx.QueryRow(
		`SELECT 1 FROM issues WHERE repo = ? AND identifier = ? AND source <> ?`,
		repo, blocked, SourceInternal,
	).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return tx.Rollback()
	case err != nil:
		return errors.Join(err, tx.Rollback())
	}
	kept := make([]string, 0, len(refs))
	for _, ref := range refs {
		id := strings.TrimSpace(ref.ID)
		if id == "" || strings.EqualFold(id, blocked) {
			continue
		}
		if ref.Resolved {
			var stored int
			switch err := tx.QueryRow(`SELECT 1 FROM issues WHERE repo = ? AND identifier = ?`, repo, id).Scan(&stored); {
			case errors.Is(err, sql.ErrNoRows):
				continue
			case err != nil:
				return errors.Join(err, tx.Rollback())
			}
		}
		kept = append(kept, id)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(kept)), ",")
	args := append([]any{repo, blocked}, toAnys(kept)...)
	del := `DELETE FROM issue_relations WHERE repo = ? AND blocked = ?`
	if len(kept) > 0 {
		del += ` AND blocker NOT IN (` + placeholders + `)`
	}
	if _, err := tx.Exec(del, args...); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	for _, blocker := range kept {
		if _, err := tx.Exec(
			`INSERT INTO issue_relations(repo, blocker, blocked) VALUES(?, ?, ?)
			 ON CONFLICT(repo, blocker, blocked) DO NOTHING`,
			repo, blocker, blocked,
		); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	return tx.Commit()
}

// attachBlockers fills Blockers and Blocked on each issue: its stored blocked-by
// edges, and whether any of them is unresolved. A blocker is unresolved while its
// issue is live and not yet settled — or absent from the store entirely, the
// dangling edge that stays conservative until the blocker syncs in or is created.
// A tombstoned blocker no longer blocks: the tracker removed it, so it will never
// settle.
func (s *Issues) attachBlockers(repo string, issues []Issue) (err error) {
	byID := make(map[string]int, len(issues))
	ids := make([]any, 0, len(issues))
	for i := range issues {
		issues[i].Blockers = []string{}
		byID[issues[i].Identifier] = i
		ids = append(ids, issues[i].Identifier)
	}
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := append([]any{repo}, ids...)
	rows, err := s.db.Query(
		`SELECT r.blocked, r.blocker,
			CASE
				WHEN b.id IS NULL THEN 1
				WHEN b.deleted_at <> '' THEN 0
				WHEN b.status_group IN ('done', 'canceled') THEN 0
				ELSE 1
			END
		 FROM issue_relations r
		 LEFT JOIN issues b ON b.repo = r.repo AND b.identifier = r.blocker
		 WHERE r.repo = ? AND r.blocked IN (`+placeholders+`)
		 ORDER BY r.blocker`,
		args...,
	)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var (
			blocked, blocker string
			unresolved       int
		)
		if scanErr := rows.Scan(&blocked, &blocker, &unresolved); scanErr != nil {
			return scanErr
		}
		if i, ok := byID[blocked]; ok {
			issues[i].Blockers = append(issues[i].Blockers, blocker)
			if unresolved != 0 {
				issues[i].Blocked = true
			}
		}
	}
	return rows.Err()
}

func toAnys(vals []string) []any {
	out := make([]any, len(vals))
	for i, v := range vals {
		out[i] = v
	}
	return out
}
