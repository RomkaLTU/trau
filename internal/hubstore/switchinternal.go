package hubstore

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/RomkaLTU/trau/internal/queue"
)

// CountSynced counts the issues the repo holds on an external tracker's behalf —
// exactly the rows a switch to the internal tracker drops. A repo that never
// mirrored anything counts zero and needs no migration (ADR 0042), only the key
// write.
func (s *Issues) CountSynced(repo string) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM issues WHERE repo = ? AND source <> ?`, repo, SourceInternal,
	).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// InternalSeq is the next number the repo's internal tracker will mint, one for a
// repo that has never minted. Every smaller number is spoken for — by an issue the
// hub holds, or by an id a dropped mirror retired when RaiseInternalSeq lifted the
// sequence clear of it.
func (s *Issues) InternalSeq(repo string) (int64, error) {
	var next int64
	err := s.db.QueryRow(`SELECT next FROM issue_seq WHERE repo = ?`, repo).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	return next, nil
}

// BusyExternalItem names the first live queue entry the repo still holds for a
// ticket the external tracker owns, or nil when nothing blocks. A legacy entry
// stamped with no source counts as external: it predates the internal tracker,
// so it cannot be one of its issues.
func (s *Issues) BusyExternalItem(root string) (*IssueBusyError, error) {
	var busy IssueBusyError
	err := s.db.QueryRow(
		`SELECT id, status FROM queue_items
		 WHERE root = ? AND source <> ? AND status IN (?, ?, ?)
		 ORDER BY position LIMIT 1`,
		root, SourceInternal, queue.StatusPending, queue.StatusPaused, queue.StatusRunning,
	).Scan(&busy.Identifier, &busy.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &busy, nil
}

// RaiseInternalSeq lifts the repo's internal identifier sequence past every id
// already spoken for under prefix, and returns the sequence in force afterwards.
// The floor spans the repo's issues and every id its queue has ever carried: the
// mirror a switch to the internal tracker drops takes its identifiers with it,
// and run history keyed on them stays behind, so the sequence has to clear them
// before they stop being visible. The sequence only ever moves forward.
func (s *Issues) RaiseInternalSeq(repo, prefix string) (int64, error) {
	prefix = strings.ToUpper(strings.TrimSpace(prefix))
	if prefix == "" {
		return 0, errors.New("issue prefix is empty")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	floor, err := prefixFloor(tx, repo, prefix)
	if err != nil {
		return 0, errors.Join(err, tx.Rollback())
	}
	next, err := nextInternalSeq(tx, repo)
	if err != nil {
		return 0, errors.Join(err, tx.Rollback())
	}
	if floor+1 > next {
		next = floor + 1
		if _, err := tx.Exec(`UPDATE issue_seq SET next = ? WHERE repo = ?`, next, repo); err != nil {
			return 0, errors.Join(err, tx.Rollback())
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
}

// prefixFloor is the highest number already minted under prefix across the repo's
// issues and its queue history, zero when the prefix has never been used.
func prefixFloor(tx *sql.Tx, repo, prefix string) (int64, error) {
	var floor int64
	for _, q := range []string{
		`SELECT identifier FROM issues WHERE repo = ? AND identifier LIKE ?`,
		`SELECT id FROM queue_items WHERE root = ? AND id LIKE ?`,
	} {
		if err := scanFloor(tx, q, repo, prefix, &floor); err != nil {
			return 0, err
		}
	}
	return floor, nil
}

// scanFloor raises floor to the largest prefix-N suffix query returns. The LIKE
// only narrows the scan; the suffix is parsed in Go, so a prefix that merely
// shares a leading run of characters cannot inflate the floor.
func scanFloor(tx *sql.Tx, query, key, prefix string, floor *int64) (err error) {
	rows, err := tx.Query(query, key, prefix+"-%")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return scanErr
		}
		if n, ok := prefixSuffix(id, prefix); ok && n > *floor {
			*floor = n
		}
	}
	return rows.Err()
}

// prefixSuffix reads the numeric suffix of a prefix-N identifier, reporting false
// for anything else.
func prefixSuffix(id, prefix string) (int64, bool) {
	rest, ok := strings.CutPrefix(strings.ToUpper(strings.TrimSpace(id)), prefix+"-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
