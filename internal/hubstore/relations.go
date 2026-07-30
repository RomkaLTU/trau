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

// ErrRelationTargetMissing marks a relation write naming an identifier no issue in
// the repo holds. A sync-reflected edge is allowed to dangle — the slice it names
// may sync in later — but a hand-authored one is refused so a typo never blocks an
// issue forever.
var ErrRelationTargetMissing = errors.New("relation target is not an issue in this repo")

// ErrRelationTargetSynced marks a hand-authored edge that would land on a synced
// ticket's graph. An edge belongs to the issue it blocks — the side ReflectBlockers
// reconciles against the tracker — so a local one there is dropped by the next sync
// pull (ADR 0007).
var ErrRelationTargetSynced = errors.New("relation target is a synced ticket; its blockers are owned by the tracker")

// RelationEdits names the blocking edges around one issue: the issues that block
// it, and the issues it blocks.
type RelationEdits struct {
	BlockedBy []string
	Blocks    []string
}

// AddRelation records that blocker blocks blocked, idempotently: re-adding an
// edge that already exists is a no-op, so re-wiring an epic's graph on a retry
// files nothing new. Either side may name an identifier not yet in issues — a
// slice created out of order — and the edge is kept dangling rather than
// rejected; eligibility counts a dangling blocker as unresolved.
func (s *Issues) AddRelation(repo, blocker, blocked string) error {
	blocker, blocked, err := relationPair(blocker, blocked)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO issue_relations(repo, blocker, blocked) VALUES(?, ?, ?)
		 ON CONFLICT(repo, blocker, blocked) DO NOTHING`,
		repo, blocker, blocked,
	)
	return err
}

// AddRelations wires the edges e names around identifier in one transaction,
// refusing the whole write when an endpoint names no stored issue, or when an edge
// would block a synced ticket.
func (s *Issues) AddRelations(repo, identifier string, e RelationEdits) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := addRelations(tx, repo, identifier, e); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

// RemoveRelations drops the edges e names around identifier, tolerating ones that
// were never recorded.
func (s *Issues) RemoveRelations(repo, identifier string, e RelationEdits) error {
	for _, blocker := range e.BlockedBy {
		if err := s.RemoveRelation(repo, strings.TrimSpace(blocker), identifier); err != nil {
			return err
		}
	}
	for _, blocked := range e.Blocks {
		if err := s.RemoveRelation(repo, identifier, strings.TrimSpace(blocked)); err != nil {
			return err
		}
	}
	return nil
}

// addRelations inserts e's edges inside tx, so a create wires its graph in the
// same transaction that files the issue.
func addRelations(tx *sql.Tx, repo, identifier string, e RelationEdits) error {
	for _, blocker := range e.BlockedBy {
		if err := insertRelation(tx, repo, blocker, identifier); err != nil {
			return err
		}
	}
	for _, blocked := range e.Blocks {
		if err := insertRelation(tx, repo, identifier, blocked); err != nil {
			return err
		}
	}
	return nil
}

// insertRelation files one hand-authored edge, refusing an endpoint the repo does
// not hold and a blocked side the tracker owns.
func insertRelation(tx *sql.Tx, repo, blocker, blocked string) error {
	blocker, blocked, err := relationPair(blocker, blocked)
	if err != nil {
		return err
	}
	if _, err := relationEndpoint(tx, repo, blocker); err != nil {
		return err
	}
	source, err := relationEndpoint(tx, repo, blocked)
	if err != nil {
		return err
	}
	if source != SourceInternal {
		return fmt.Errorf("%s: %w", blocked, ErrRelationTargetSynced)
	}
	_, err = tx.Exec(
		`INSERT INTO issue_relations(repo, blocker, blocked) VALUES(?, ?, ?)
		 ON CONFLICT(repo, blocker, blocked) DO NOTHING`,
		repo, blocker, blocked,
	)
	return err
}

// relationEndpoint returns the source of the issue identifier names, refusing one
// no issue in the repo holds.
func relationEndpoint(tx *sql.Tx, repo, identifier string) (string, error) {
	var source string
	switch err := tx.QueryRow(
		`SELECT source FROM issues WHERE repo = ? AND identifier = ?`, repo, identifier,
	).Scan(&source); {
	case errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("%s: %w", identifier, ErrRelationTargetMissing)
	case err != nil:
		return "", err
	}
	return source, nil
}

// relationPair trims one edge's endpoints, requiring both and refusing an issue
// that blocks itself.
func relationPair(blocker, blocked string) (string, string, error) {
	blocker, blocked = strings.TrimSpace(blocker), strings.TrimSpace(blocked)
	if blocker == "" || blocked == "" {
		return "", "", errors.New("relation needs both a blocker and a blocked identifier")
	}
	if strings.EqualFold(blocker, blocked) {
		return "", "", fmt.Errorf("%s cannot block itself", blocked)
	}
	return blocker, blocked, nil
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

// UnresolvedBlockers returns, per identifier, the blockers still holding it back,
// applying the same rule attachBlockers does: a dangling edge or a live, unsettled
// blocker is unresolved, while a tombstoned one no longer blocks. Identifiers with
// nothing unresolved are absent from the map.
func (s *Issues) UnresolvedBlockers(repo string, identifiers []string) (out map[string][]string, err error) {
	out = map[string][]string{}
	if len(identifiers) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(identifiers)), ",")
	args := append([]any{repo}, toAnys(identifiers)...)
	rows, err := s.db.Query(
		`SELECT r.blocked, r.blocker
		 FROM issue_relations r
		 LEFT JOIN issues b ON b.repo = r.repo AND b.identifier = r.blocker
		 WHERE r.repo = ? AND r.blocked IN (`+placeholders+`)
		   AND (b.id IS NULL OR (b.deleted_at = '' AND b.status_group NOT IN ('done', 'canceled')))
		 ORDER BY r.blocker`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var blocked, blocker string
		if scanErr := rows.Scan(&blocked, &blocker); scanErr != nil {
			return nil, scanErr
		}
		out[blocked] = append(out[blocked], blocker)
	}
	return out, rows.Err()
}

func toAnys(vals []string) []any {
	out := make([]any, len(vals))
	for i, v := range vals {
		out[i] = v
	}
	return out
}
