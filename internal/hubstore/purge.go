package hubstore

import (
	"database/sql"
	"errors"
	"time"

	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
)

// IssueRunningError reports a purge refused because a family member has a running
// queue entry. The child owns the ticket's working state, so nothing is deleted
// until it stops.
type IssueRunningError struct {
	Identifier string
}

func (e *IssueRunningError) Error() string { return e.Identifier + " has a running queue entry" }

// PurgeResult is what a purge removed: the identifiers whose rows are gone, and
// the attachment digests whose cached bytes the caller still has to collect once
// the transaction has committed — see Attachments.CollectOrphans.
type PurgeResult struct {
	Deleted       []string
	OrphanedBlobs []string
}

// Purge hard-deletes an issue and everything local hanging off it, in one
// transaction: the issues row (comments cascade through their foreign key, and
// the issues_fts triggers drop the search entry), its grilling sessions and their
// messages, its attachment rows, notifications, blocked-by and grill relations,
// and its queue entries. When the issue heads an epic the whole family goes, the
// issue plus its children, and the result names every identifier removed. Each
// deleted member that is not internal leaves an issue_tombstones row behind, so a
// tracker that still returns the ticket never re-imports it; internal issues need
// none.
//
// Run data — events, phase logs, token calls, checkpoints, artifacts, transcripts
// — is deliberately untouched: what ran stays browsable after the ticket it ran
// for is gone. An unknown identifier reports found=false and changes nothing, as
// does a family member mid-run, which reports an IssueRunningError.
func (s *Issues) Purge(repo registry.Repo, identifier string) (PurgeResult, bool, error) {
	// Queue.persist rewrites a root's rows wholesale from a snapshot taken before
	// this transaction, so a concurrent drain would re-insert the entries deleted
	// here.
	queueMu.Lock()
	defer queueMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return PurgeResult{}, false, err
	}
	members, err := purgeFamily(tx, repo.Root, identifier)
	if err != nil {
		return PurgeResult{}, false, errors.Join(err, tx.Rollback())
	}
	if len(members) == 0 {
		return PurgeResult{}, false, tx.Rollback()
	}

	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.identifier
	}
	running, err := runningMember(tx, repo.Root, ids)
	if err != nil {
		return PurgeResult{}, false, errors.Join(err, tx.Rollback())
	}
	if running != "" {
		return PurgeResult{}, false, errors.Join(&IssueRunningError{Identifier: running}, tx.Rollback())
	}

	in := placeholders(len(ids))
	scoped := append([]any{repo.Root}, toAnys(ids)...)
	twoSided := append(append([]any{repo.Root}, toAnys(ids)...), toAnys(ids)...)

	blobs, err := purgedBlobs(tx, in, scoped)
	if err != nil {
		return PurgeResult{}, false, errors.Join(err, tx.Rollback())
	}

	deletes := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM issues WHERE repo = ? AND identifier IN (` + in + `)`, scoped},
		{`DELETE FROM grill_sessions WHERE repo = ? AND issue_id IN (` + in + `)`, scoped},
		{`DELETE FROM attachments WHERE repo = ? AND issue_identifier IN (` + in + `)`, scoped},
		{`DELETE FROM notifications WHERE repo = ? AND issue_id IN (` + in + `)`, scoped},
		{`DELETE FROM queue_items WHERE root = ? AND id IN (` + in + `)`, scoped},
		{`DELETE FROM queue_sub_issues WHERE root = ? AND (item_id IN (` + in + `) OR id IN (` + in + `))`, twoSided},
		{`DELETE FROM issue_relations WHERE repo = ? AND (blocker IN (` + in + `) OR blocked IN (` + in + `))`, twoSided},
		{`DELETE FROM grill_relations WHERE repo = ? AND (blocker IN (` + in + `) OR blocked IN (` + in + `))`, twoSided},
	}
	for _, d := range deletes {
		if _, err := tx.Exec(d.query, d.args...); err != nil {
			return PurgeResult{}, false, errors.Join(err, tx.Rollback())
		}
	}

	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	for _, m := range members {
		if m.source == SourceInternal {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO issue_tombstones(repo, identifier, deleted_at) VALUES(?, ?, ?)
			 ON CONFLICT(repo, identifier) DO NOTHING`,
			repo.Root, m.identifier, stamp,
		); err != nil {
			return PurgeResult{}, false, errors.Join(err, tx.Rollback())
		}
	}
	if err := tx.Commit(); err != nil {
		return PurgeResult{}, false, err
	}
	return PurgeResult{Deleted: ids, OrphanedBlobs: blobs}, true, nil
}

// purgeMember is one issue a purge takes down, with the source deciding whether
// it earns a tombstone.
type purgeMember struct {
	identifier string
	source     string
}

// purgeFamily resolves the issue and, when it heads an epic, its children across
// every source. It returns nothing when the repo does not hold the identifier.
func purgeFamily(tx *sql.Tx, root, identifier string) (members []purgeMember, err error) {
	var (
		source      string
		hasChildren int
	)
	err = tx.QueryRow(
		`SELECT source, has_children FROM issues WHERE repo = ? AND identifier = ?`,
		root, identifier,
	).Scan(&source, &hasChildren)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	members = []purgeMember{{identifier: identifier, source: source}}
	if hasChildren == 0 {
		return members, nil
	}
	rows, err := tx.Query(
		`SELECT identifier, source FROM issues WHERE repo = ? AND parent = ? ORDER BY identifier`,
		root, identifier,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var m purgeMember
		if scanErr := rows.Scan(&m.identifier, &m.source); scanErr != nil {
			return nil, scanErr
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// runningMember names the first family member the queue is currently draining,
// empty when none is.
func runningMember(tx *sql.Tx, root string, ids []string) (string, error) {
	args := append([]any{root, queue.StatusRunning}, toAnys(ids)...)
	var id string
	err := tx.QueryRow(
		`SELECT id FROM queue_items WHERE root = ? AND status = ? AND id IN (`+placeholders(len(ids))+`) LIMIT 1`,
		args...,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// purgedBlobs reads the attachment digests the family holds, before its rows go,
// so the caller can drop the cached bytes nothing references any more.
func purgedBlobs(tx *sql.Tx, in string, scoped []any) (shas []string, err error) {
	rows, err := tx.Query(
		`SELECT DISTINCT sha256 FROM attachments
		 WHERE sha256 <> '' AND repo = ? AND issue_identifier IN (`+in+`)`,
		scoped...,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var sha string
		if scanErr := rows.Scan(&sha); scanErr != nil {
			return nil, scanErr
		}
		shas = append(shas, sha)
	}
	return shas, rows.Err()
}

// tombstonedIdentifiers reads a repo's hard-deleted identifiers, the set inbound
// sync refuses to re-import.
func tombstonedIdentifiers(tx *sql.Tx, repo string) (set map[string]struct{}, err error) {
	rows, err := tx.Query(`SELECT identifier FROM issue_tombstones WHERE repo = ?`, repo)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	set = map[string]struct{}{}
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, scanErr
		}
		set[id] = struct{}{}
	}
	return set, rows.Err()
}
