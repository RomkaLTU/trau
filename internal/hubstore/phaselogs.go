package hubstore

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// legacyTranscriptDir is the runs-dir subdirectory holding the live-view PTY
// transcripts (*.pty.log), a different log family than the inspector's per-phase
// logs. ImportLegacy skips it. It mirrors agent.ResultsSubdir, duplicated to keep
// this store free of a dependency on the agent package.
const legacyTranscriptDir = "_agent-results"

// PhaseLog is one phase's stored agent output, as the inspector reads it back.
// Updated orders the set most-recently-written first.
type PhaseLog struct {
	Phase   string
	Content string
	Updated time.Time
}

// PhaseLogs is the hub's authoritative per-run phase-log store (ADR 0008). The
// child posts each phase's final output as the phase produces it; the TUI log
// inspector reads them back over HTTP instead of listing and reading the run
// directory. It is a real migrated table, never dropped and rebuilt.
type PhaseLogs struct {
	db       *sql.DB
	mu       sync.Mutex
	imported map[string]bool
}

// NewPhaseLogs returns a PhaseLogs store over db. The caller owns db's lifecycle.
func NewPhaseLogs(db *sql.DB) *PhaseLogs {
	return &PhaseLogs{db: db, imported: map[string]bool{}}
}

// Upsert writes a ticket's log for a phase, replacing the row in place and
// stamping it now so the inspector orders it ahead of earlier phases.
func (l *PhaseLogs) Upsert(root, ticket, phase, content string) error {
	return l.upsertAt(root, ticket, phase, content, time.Now().UnixNano())
}

func (l *PhaseLogs) upsertAt(root, ticket, phase, content string, updatedAt int64) error {
	_, err := l.db.Exec(
		`INSERT INTO phase_logs(repo, ticket, phase, content, updated_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(repo, ticket, phase) DO UPDATE SET content = excluded.content, updated_at = excluded.updated_at`,
		root, ticket, phase, content, updatedAt,
	)
	return err
}

// List returns a ticket's phase logs, most-recently-written first with the phase
// name breaking ties — the order the file-era inspector derived from file mtimes.
func (l *PhaseLogs) List(root, ticket string) (out []PhaseLog, err error) {
	q, err := l.db.Query(
		`SELECT phase, content, updated_at FROM phase_logs
		 WHERE repo = ? AND ticket = ? ORDER BY updated_at DESC, phase DESC`,
		root, ticket,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	for q.Next() {
		var pl PhaseLog
		var updated int64
		if err := q.Scan(&pl.Phase, &pl.Content, &updated); err != nil {
			return nil, err
		}
		pl.Updated = time.Unix(0, updated)
		out = append(out, pl)
	}
	return out, q.Err()
}

// Remove drops every phase log for a ticket — the reset/clear/fresh-build sweep.
// A ticket with none is not an error.
func (l *PhaseLogs) Remove(root, ticket string) error {
	_, err := l.db.Exec(`DELETE FROM phase_logs WHERE repo = ? AND ticket = ?`, root, ticket)
	return err
}

// ImportLegacy folds any file-era per-phase logs (runs/<ticket>/<phase>.log) into
// the table on the hub's first touch of a repo, removing each file only after its
// row commits. A phase the hub already holds is left untouched and its stale file
// removed. The _agent-results transcript dir and .pty.log files are skipped: they
// are the live-view transcript family, not inspector phase logs. It is idempotent,
// and a repo already imported this serve lifetime is skipped without touching disk.
func (l *PhaseLogs) ImportLegacy(root, runsDir string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.imported[root] || runsDir == "" {
		l.imported[root] = true
		return nil
	}
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			l.imported[root] = true
			return nil
		}
		return fmt.Errorf("import legacy phase logs %s: %w", root, err)
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == legacyTranscriptDir {
			continue
		}
		if err := l.importTicket(root, e.Name(), runsDir); err != nil {
			return err
		}
	}
	l.imported[root] = true
	return nil
}

func (l *PhaseLogs) importTicket(root, ticket, runsDir string) error {
	dir := filepath.Join(runsDir, ticket)
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || !strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".pty.log") {
			continue
		}
		if err := l.importFile(root, ticket, dir, f); err != nil {
			return err
		}
	}
	return nil
}

func (l *PhaseLogs) importFile(root, ticket, dir string, f os.DirEntry) error {
	phase := strings.TrimSuffix(f.Name(), ".log")
	path := filepath.Join(dir, f.Name())
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if len(bytes.TrimSpace(data)) > 0 {
		exists, err := l.has(root, ticket, phase)
		if err != nil {
			return fmt.Errorf("import legacy phase log %s/%s/%s: %w", root, ticket, phase, err)
		}
		if !exists {
			updatedAt := time.Now().UnixNano()
			if info, err := f.Info(); err == nil {
				updatedAt = info.ModTime().UnixNano()
			}
			if err := l.upsertAt(root, ticket, phase, string(data), updatedAt); err != nil {
				return fmt.Errorf("import legacy phase log %s/%s/%s: %w", root, ticket, phase, err)
			}
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove legacy phase log %s: %w", path, err)
	}
	return nil
}

func (l *PhaseLogs) has(root, ticket, phase string) (bool, error) {
	var n int
	err := l.db.QueryRow(
		`SELECT 1 FROM phase_logs WHERE repo = ? AND ticket = ? AND phase = ?`, root, ticket, phase,
	).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
