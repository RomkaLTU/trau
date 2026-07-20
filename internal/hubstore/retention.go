package hubstore

import (
	"database/sql"
	"errors"
)

// Retention bounds how much per-repo run data each authoritative store keeps
// before its periodic prune drops the oldest (ADR 0008). A non-positive field
// disables pruning for that store. Checkpoints — the per-ticket run summaries —
// are never pruned. AttachmentCacheBytes is the odd one out: it bounds bytes on
// disk rather than rows per repo, because attachments tie to issue lifecycle
// rather than to a run.
type Retention struct {
	Transcripts          int
	Events               int
	TokenCalls           int
	Grill                int
	AttachmentCacheBytes int64
}

// pruneKeepingRecent deletes the rows of table beyond the retention newest per
// repo, ranked by the monotonic id. A non-positive retention is a no-op. table is
// a trusted in-package constant — SQLite cannot parameterize an identifier — never
// user input. Deleted rows leave free pages for reuse rather than a vacuum:
// trau.db is the hot authoritative store, not a bulk file like transcripts.db.
func pruneKeepingRecent(db *sql.DB, table string, retention int) error {
	if retention <= 0 {
		return nil
	}
	repos, err := distinctRepos(db, table)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		var cutoff int64
		err := db.QueryRow(
			`SELECT id FROM `+table+` WHERE repo = ? ORDER BY id DESC LIMIT 1 OFFSET ?`,
			repo, retention,
		).Scan(&cutoff)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := db.Exec(`DELETE FROM `+table+` WHERE repo = ? AND id <= ?`, repo, cutoff); err != nil {
			return err
		}
	}
	return nil
}

func distinctRepos(db *sql.DB, table string) (repos []string, err error) {
	q, err := db.Query(`SELECT DISTINCT repo FROM ` + table)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	for q.Next() {
		var repo string
		if err := q.Scan(&repo); err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, q.Err()
}
