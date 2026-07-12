package hubstore

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Lesson is one distilled repair-experiment record in a repo's durable ledger: the
// takeaway a future run should apply plus the context it came from — the ticket and
// phase, the failure type, the evidence, how the repair ended, and when it was
// recorded. Records accrete append-only so a failed or repaired run teaches later
// runs (COD-529). The JSON tags are the file-era ledger shape, used to fold a legacy
// runs/memory/lessons.jsonl in on first touch.
type Lesson struct {
	Ticket       string   `json:"ticket,omitempty"`
	Phase        string   `json:"phase,omitempty"`
	FailureType  string   `json:"failure_type,omitempty"`
	AttemptedFix string   `json:"attempted_fix,omitempty"`
	Evidence     []string `json:"evidence,omitempty"`
	Result       string   `json:"result,omitempty"`
	Lesson       string   `json:"lesson"`
	Tags         []string `json:"tags,omitempty"`
	RecordedAt   string   `json:"recorded_at,omitempty"`
}

// Lessons is the hub's authoritative per-repo lessons ledger (ADR 0008). Verify
// posts each distilled lesson over HTTP; a later build/verify/repair recalls the
// relevant ones for prompt injection. It is a real migrated table, never dropped and
// rebuilt. The legacy file folds in on the hub's first touch of a repo.
type Lessons struct {
	db       *sql.DB
	mu       sync.Mutex
	imported map[string]bool
}

// NewLessons returns a Lessons store over db. The caller owns db's lifecycle.
func NewLessons(db *sql.DB) *Lessons {
	return &Lessons{db: db, imported: map[string]bool{}}
}

// execer is the shared write surface of *sql.DB and *sql.Tx, so a single insert
// backs both the one-off Append and the batched legacy import.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// Append records one distilled lesson for repo, after the ledger's existing records.
func (l *Lessons) Append(repo string, lesson Lesson) error {
	return insertLesson(l.db, repo, lesson)
}

func insertLesson(ex execer, repo string, l Lesson) error {
	_, err := ex.Exec(
		`INSERT INTO lessons(repo, ticket, phase, failure_type, attempted_fix, evidence, result, lesson, tags, recorded_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		repo, l.Ticket, l.Phase, l.FailureType, l.AttemptedFix, encodeList(l.Evidence),
		l.Result, l.Lesson, encodeList(l.Tags), l.RecordedAt,
	)
	return err
}

// All returns every lesson the hub holds for repo, most recent first. A repo the
// loop has taught nothing yet yields an empty slice.
func (l *Lessons) All(repo string) (out []Lesson, err error) {
	q, err := l.db.Query(
		`SELECT ticket, phase, failure_type, attempted_fix, evidence, result, lesson, tags, recorded_at
		 FROM lessons WHERE repo = ? ORDER BY id DESC`, repo,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	out = []Lesson{}
	for q.Next() {
		var ls Lesson
		var evidence, tags string
		if scanErr := q.Scan(
			&ls.Ticket, &ls.Phase, &ls.FailureType, &ls.AttemptedFix, &evidence,
			&ls.Result, &ls.Lesson, &tags, &ls.RecordedAt,
		); scanErr != nil {
			return nil, scanErr
		}
		ls.Evidence = decodeList(evidence)
		ls.Tags = decodeList(tags)
		out = append(out, ls)
	}
	return out, q.Err()
}

// ImportLegacy folds a repo's file-era ledger (runs/memory/lessons.jsonl under
// runsDir) into the table on the hub's first touch of the repo, removing the file
// only after its rows commit. It is idempotent: a repo already imported this serve
// lifetime is skipped without touching disk, and across serves the removed file
// leaves nothing to re-fold. Any lessons the child has already recorded through the
// hub sit alongside the folded-in file — the file holds only pre-migration records,
// so the two never overlap.
func (l *Lessons) ImportLegacy(root, runsDir string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.imported[root] || runsDir == "" {
		l.imported[root] = true
		return nil
	}
	path := filepath.Join(runsDir, "memory", "lessons.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			l.imported[root] = true
			return nil
		}
		return fmt.Errorf("import legacy lessons %s: %w", root, err)
	}
	if err := l.appendAll(root, parseLedger(data)); err != nil {
		return fmt.Errorf("import legacy lessons %s: %w", root, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove legacy lessons %s: %w", path, err)
	}
	l.imported[root] = true
	return nil
}

func (l *Lessons) appendAll(repo string, records []Lesson) error {
	if len(records) == 0 {
		return nil
	}
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, r := range records {
		if err := insertLesson(tx, repo, r); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// parseLedger reads the file-era JSONL ledger into records in append order,
// skipping any blank or malformed line and any record with no distilled lesson —
// matching the file-era reader, so a single corrupt line never poisons the import.
func parseLedger(data []byte) []Lesson {
	var out []Lesson
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var l Lesson
		if err := json.Unmarshal(line, &l); err != nil {
			continue
		}
		if strings.TrimSpace(l.Lesson) == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}

// encodeList renders a string slice as a JSON array for a TEXT column, empty for
// none so a lesson with no evidence or tags stores the column's default.
func encodeList(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	b, err := json.Marshal(xs)
	if err != nil {
		return ""
	}
	return string(b)
}

// decodeList parses a JSON-array TEXT column back to a slice, nil for an empty or
// malformed value so a corrupt list never drops the whole lesson.
func decodeList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var xs []string
	if err := json.Unmarshal([]byte(s), &xs); err != nil {
		return nil
	}
	return xs
}
