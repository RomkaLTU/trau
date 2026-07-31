package hubstore

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/queue"
)

// IssueBusyError reports a detach refused because a subtree member still has
// live queue work. That work is tracked under the identifier the conversion
// would retire, so nothing moves until it settles.
type IssueBusyError struct {
	Identifier string
	Status     string
}

func (e *IssueBusyError) Error() string { return e.Identifier + " has a " + e.Status + " queue entry" }

// DetachedIssue is one converted member: the tracker identifier it answered to
// and the internal one it now carries.
type DetachedIssue struct {
	From string
	To   string
}

// DetachResult is what a detach converted: the anchor's own re-keying, and every
// member whose tracker identifier was retired — the set an external write-back
// still has to notify.
type DetachResult struct {
	From      string
	To        string
	Converted []DetachedIssue
}

// DetachToInternal converts a synced issue and everything nested under it into
// internal issues in one transaction. Each member is re-keyed to a fresh
// identifier from the repo's sequence, restamped source internal, and stripped of
// the tracker's external id, url and sync stamp, while its comments, attachments,
// notifications, blocking edges, grilling sessions and queue history follow it to
// the new identifier. Every retired tracker identifier is tombstoned, so the next
// inbound sync cannot re-import the ticket beside the internal issue it became.
//
// A member already internal keeps its identifier and only follows its parent, and
// the anchor's own parent is left alone — the subtree moves, not the epic above
// it. A member the queue still holds refuses the whole conversion with an
// IssueBusyError, and an identifier the repo does not hold reports found=false.
//
// Run data — events, phase logs, token calls, checkpoints, artifacts, transcripts
// — keys on the ticket it ran for and is deliberately untouched, as Purge leaves
// it.
func (s *Issues) DetachToInternal(repo, prefix, identifier string) (DetachResult, bool, error) {
	prefix = strings.ToUpper(strings.TrimSpace(prefix))
	if prefix == "" {
		return DetachResult{}, false, errors.New("issue prefix is empty")
	}

	// Queue.persist rewrites a root's rows wholesale from a snapshot taken before
	// this transaction, so a concurrent drain would resurrect the retired ids.
	queueMu.Lock()
	defer queueMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return DetachResult{}, false, err
	}
	members, err := detachSubtree(tx, repo, identifier)
	if err != nil {
		return DetachResult{}, false, errors.Join(err, tx.Rollback())
	}
	if len(members) == 0 {
		return DetachResult{}, false, tx.Rollback()
	}

	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.identifier
	}
	busy, status, err := busyMember(tx, repo, ids)
	if err != nil {
		return DetachResult{}, false, errors.Join(err, tx.Rollback())
	}
	if busy != "" {
		return DetachResult{}, false, errors.Join(&IssueBusyError{Identifier: busy, Status: status}, tx.Rollback())
	}

	next, err := nextInternalSeq(tx, repo)
	if err != nil {
		return DetachResult{}, false, errors.Join(err, tx.Rollback())
	}
	renamed := make(map[string]string, len(members))
	for i, m := range members {
		if m.source == SourceInternal {
			continue
		}
		id, idErr := freeIdentifier(tx, repo, prefix, &next)
		if idErr != nil {
			return DetachResult{}, false, errors.Join(idErr, tx.Rollback())
		}
		next++
		members[i].to = id
		renamed[m.identifier] = id
	}
	if _, err := tx.Exec(`UPDATE issue_seq SET next = ? WHERE repo = ?`, next, repo); err != nil {
		return DetachResult{}, false, errors.Join(err, tx.Rollback())
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	result := DetachResult{From: identifier, To: renamed[identifier]}
	for _, m := range members {
		parent := m.parent
		if moved, ok := renamed[parent]; ok {
			parent = moved
		}
		if m.to == "" {
			if _, err := tx.Exec(
				`UPDATE issues SET parent = ?, updated_at = ? WHERE repo = ? AND identifier = ?`,
				parent, now, repo, m.identifier,
			); err != nil {
				return DetachResult{}, false, errors.Join(err, tx.Rollback())
			}
			continue
		}
		group, display := normalizeState(m.statusGroup)
		if _, err := tx.Exec(
			`UPDATE issues SET identifier = ?, source = ?, status = ?, status_group = ?, parent = ?,
				external_id = '', url = '', synced_at = '', deleted_at = '', updated_at = ?
			 WHERE repo = ? AND identifier = ?`,
			m.to, SourceInternal, display, group, parent, now, repo, m.identifier,
		); err != nil {
			return DetachResult{}, false, errors.Join(err, tx.Rollback())
		}
		if err := rekeyIssueRefs(tx, repo, m.identifier, m.to); err != nil {
			return DetachResult{}, false, errors.Join(err, tx.Rollback())
		}
		if _, err := tx.Exec(
			`INSERT INTO issue_tombstones(repo, identifier, deleted_at) VALUES(?, ?, ?)
			 ON CONFLICT(repo, identifier) DO NOTHING`,
			repo, m.identifier, now,
		); err != nil {
			return DetachResult{}, false, errors.Join(err, tx.Rollback())
		}
		result.Converted = append(result.Converted, DetachedIssue{From: m.identifier, To: m.to})
	}
	if err := tx.Commit(); err != nil {
		return DetachResult{}, false, err
	}
	return result, true, nil
}

// detachMember is one issue a detach carries over, with to holding the internal
// identifier allocated for it — empty for a member that is already internal.
type detachMember struct {
	identifier  string
	source      string
	parent      string
	statusGroup string
	to          string
}

// detachSubtree resolves the issue and every descendant beneath it, across every
// source and however deep the nesting goes. It returns nothing when the repo does
// not hold the identifier.
func detachSubtree(tx *sql.Tx, repo, identifier string) (members []detachMember, err error) {
	rows, err := tx.Query(
		`WITH RECURSIVE subtree(identifier, source, parent, status_group) AS (
			SELECT identifier, source, parent, status_group FROM issues WHERE repo = ? AND identifier = ?
			UNION
			SELECT i.identifier, i.source, i.parent, i.status_group
			  FROM issues i JOIN subtree s ON i.parent = s.identifier
			 WHERE i.repo = ?
		 )
		 SELECT identifier, source, parent, status_group FROM subtree ORDER BY identifier`,
		repo, identifier, repo,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var m detachMember
		if scanErr := rows.Scan(&m.identifier, &m.source, &m.parent, &m.statusGroup); scanErr != nil {
			return nil, scanErr
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// busyMember names the first subtree member the queue still holds — draining it
// now, or waiting to — and the status it holds it under.
func busyMember(tx *sql.Tx, root string, ids []string) (identifier, status string, err error) {
	args := append([]any{root}, toAnys(ids)...)
	args = append(args, queue.StatusPending, queue.StatusPaused, queue.StatusRunning)
	err = tx.QueryRow(
		`SELECT id, status FROM queue_items
		 WHERE root = ? AND id IN (`+placeholders(len(ids))+`) AND status IN (?, ?, ?) LIMIT 1`,
		args...,
	).Scan(&identifier, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return identifier, status, err
}

// rekeyIssueRefs points every hub-local row that names an issue by identifier at
// the one it was renamed to, so a converted ticket keeps its attachments,
// notifications, blocking edges, grilling sessions and queue history.
func rekeyIssueRefs(tx *sql.Tx, repo, from, to string) error {
	updates := []string{
		`UPDATE issue_relations SET blocker = ? WHERE repo = ? AND blocker = ?`,
		`UPDATE issue_relations SET blocked = ? WHERE repo = ? AND blocked = ?`,
		`UPDATE grill_relations SET blocker = ? WHERE repo = ? AND blocker = ?`,
		`UPDATE grill_relations SET blocked = ? WHERE repo = ? AND blocked = ?`,
		`UPDATE grill_sessions SET issue_id = ? WHERE repo = ? AND issue_id = ?`,
		`UPDATE attachments SET issue_identifier = ? WHERE repo = ? AND issue_identifier = ?`,
		`UPDATE notifications SET issue_id = ? WHERE repo = ? AND issue_id = ?`,
		`UPDATE queue_items SET id = ? WHERE root = ? AND id = ?`,
		`UPDATE queue_sub_issues SET item_id = ? WHERE root = ? AND item_id = ?`,
		`UPDATE queue_sub_issues SET id = ? WHERE root = ? AND id = ?`,
	}
	for _, q := range updates {
		if _, err := tx.Exec(q, to, repo, from); err != nil {
			return err
		}
	}
	return nil
}

// nextInternalSeq reads the repo's identifier sequence, starting it at one when
// the repo has never allocated an internal issue.
func nextInternalSeq(tx *sql.Tx, repo string) (int64, error) {
	if _, err := tx.Exec(`INSERT INTO issue_seq(repo, next) VALUES(?, 1) ON CONFLICT(repo) DO NOTHING`, repo); err != nil {
		return 0, err
	}
	var next int64
	err := tx.QueryRow(`SELECT next FROM issue_seq WHERE repo = ?`, repo).Scan(&next)
	return next, err
}
