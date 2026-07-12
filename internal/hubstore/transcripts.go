package hubstore

import (
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// NewTranscriptChunk is one ordered slice of an agent session's PTY output as the
// child posts it: the transcript-session stem, its seq within the session, the
// terminal dimensions the session painted at, and the raw bytes.
type NewTranscriptChunk struct {
	Stem string
	Seq  int64
	Cols int
	Rows int
	Data []byte
}

// TranscriptChunk is one persisted chunk as a reader pages it back: its seq (the
// per-session cursor) and raw bytes.
type TranscriptChunk struct {
	Seq  int64
	Data []byte
}

// TranscriptSession summarizes one transcript session for the replay picker: its
// stem, the dimensions to size the terminal, the total byte size, and when it was
// last appended to.
type TranscriptSession struct {
	Stem     string
	Cols     int
	Rows     int
	Size     int64
	Modified time.Time
}

// Transcripts is the hub's chunked transcript store over the separate
// transcripts.db (ADR 0008 §4). The child POSTs batched chunks; the hub appends
// them here and replays finished runs from here. Bulk isolation keeps this volume
// out of the authoritative store. retention bounds how many sessions per repo Prune
// keeps. The caller owns the database's lifecycle.
type Transcripts struct {
	db        *sql.DB
	retention int
}

// NewTranscripts returns a Transcripts store over db (the transcripts database),
// pruned to the most recent retention sessions per repo. A nil db yields a store
// whose operations are inert, so a hub built without a transcripts database (tests)
// still constructs.
func NewTranscripts(db *sql.DB, retention int) *Transcripts {
	return &Transcripts{db: db, retention: retention}
}

// Append persists repo's chunks in arrival order in one transaction, stamping each
// with the hub's insert time. A chunk the store already holds — a retried batch
// after a hub blip — is ignored, so appends are idempotent.
func (t *Transcripts) Append(repo string, chunks []NewTranscriptChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	tx, err := t.db.Begin()
	if err != nil {
		return err
	}
	now := time.Now().UnixNano()
	for _, c := range chunks {
		if _, err := tx.Exec(
			`INSERT INTO transcript_chunks(repo, stem, seq, ts, cols, rows, data)
			 VALUES(?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(repo, stem, seq) DO NOTHING`,
			repo, c.Stem, c.Seq, now, c.Cols, c.Rows, c.Data,
		); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	return tx.Commit()
}

// Sessions lists repo's transcript sessions, most recently appended first — the
// replay picker's index. Size is the session's total bytes and Modified its last
// append.
func (t *Transcripts) Sessions(repo string) (out []TranscriptSession, err error) {
	q, err := t.db.Query(
		`SELECT stem, MAX(cols), MAX(rows), SUM(LENGTH(data)), MAX(ts)
		 FROM transcript_chunks WHERE repo = ?
		 GROUP BY stem ORDER BY MAX(ts) DESC`,
		repo,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	out = []TranscriptSession{}
	for q.Next() {
		var s TranscriptSession
		var modified int64
		if err := q.Scan(&s.Stem, &s.Cols, &s.Rows, &s.Size, &modified); err != nil {
			return nil, err
		}
		s.Modified = time.Unix(0, modified)
		out = append(out, s)
	}
	return out, q.Err()
}

// Dims returns the terminal dimensions a session painted at, read from its first
// chunk, and whether the session exists.
func (t *Transcripts) Dims(repo, stem string) (cols, rows int, ok bool, err error) {
	err = t.db.QueryRow(
		`SELECT cols, rows FROM transcript_chunks WHERE repo = ? AND stem = ? ORDER BY seq LIMIT 1`,
		repo, stem,
	).Scan(&cols, &rows)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	return cols, rows, true, nil
}

// Chunks returns a session's chunks with seq greater than after, in order — the
// replay body and the reconnect backfill from a client's last-seen seq.
func (t *Transcripts) Chunks(repo, stem string, after int64) (out []TranscriptChunk, err error) {
	q, err := t.db.Query(
		`SELECT seq, data FROM transcript_chunks
		 WHERE repo = ? AND stem = ? AND seq > ? ORDER BY seq`,
		repo, stem, after,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	out = []TranscriptChunk{}
	for q.Next() {
		var c TranscriptChunk
		if err := q.Scan(&c.Seq, &c.Data); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, q.Err()
}

// NewestStem returns the repo's most recent transcript session at or after
// sinceNanos — the follow-mode target, which advances as new phases start. Recency
// is the session-start time encoded in the stem, so a bound run page never
// time-travels into a previous run's transcript. ok is false when none qualifies.
func (t *Transcripts) NewestStem(repo string, sinceNanos int64) (stem string, cols, rows int, ok bool, err error) {
	q, err := t.db.Query(
		`SELECT stem, MAX(cols), MAX(rows) FROM transcript_chunks WHERE repo = ? GROUP BY stem`,
		repo,
	)
	if err != nil {
		return "", 0, 0, false, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	var bestStart int64 = -1
	for q.Next() {
		var s string
		var c, r int
		if err := q.Scan(&s, &c, &r); err != nil {
			return "", 0, 0, false, err
		}
		start := stemStartNanos(s)
		if sinceNanos > 0 && start < sinceNanos {
			continue
		}
		if start > bestStart {
			bestStart, stem, cols, rows, ok = start, s, c, r, true
		}
	}
	if err := q.Err(); err != nil {
		return "", 0, 0, false, err
	}
	return stem, cols, rows, ok, nil
}

// Prune keeps the most recent retention sessions per repo and drops the rest,
// then reclaims the freed pages with an incremental vacuum (ADR 0008 §4). Recency
// is the session-start time in the stem, so an in-flight session — the newest — is
// never pruned. A non-positive retention disables pruning.
func (t *Transcripts) Prune() error {
	if t.db == nil || t.retention <= 0 {
		return nil
	}
	stale, err := t.staleStems(t.retention)
	if err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}
	tx, err := t.db.Begin()
	if err != nil {
		return err
	}
	for _, s := range stale {
		if _, err := tx.Exec(`DELETE FROM transcript_chunks WHERE repo = ? AND stem = ?`, s.repo, s.stem); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = t.db.Exec(`PRAGMA incremental_vacuum`)
	return err
}

type repoStem struct{ repo, stem string }

// staleStems collects, per repo, every session ranked beyond the retention window
// by session-start recency.
func (t *Transcripts) staleStems(retention int) (stale []repoStem, err error) {
	q, err := t.db.Query(`SELECT DISTINCT repo, stem FROM transcript_chunks`)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	byRepo := map[string][]string{}
	for q.Next() {
		var repo, stem string
		if err := q.Scan(&repo, &stem); err != nil {
			return nil, err
		}
		byRepo[repo] = append(byRepo[repo], stem)
	}
	if err := q.Err(); err != nil {
		return nil, err
	}
	for repo, stems := range byRepo {
		if len(stems) <= retention {
			continue
		}
		sort.Slice(stems, func(i, j int) bool { return stemStartNanos(stems[i]) > stemStartNanos(stems[j]) })
		for _, stem := range stems[retention:] {
			stale = append(stale, repoStem{repo: repo, stem: stem})
		}
	}
	return stale, nil
}

// stemStartNanos reads the session-start time encoded in a stem, which the agent
// names <unix-nano>-<label>. A stem with no leading nanosecond timestamp sorts
// oldest.
func stemStartNanos(stem string) int64 {
	digits := stem
	if i := strings.IndexByte(stem, '-'); i >= 0 {
		digits = stem[:i]
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
