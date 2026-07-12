package hubstore

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Artifact kinds — the durable per-run phase artifacts, keyed alongside their
// repo and ticket. The values double as the {kind} path segment the child writes
// and the wire label the run detail reads back.
const (
	ArtifactHandoff    = "handoff"
	ArtifactRubric     = "rubric"
	ArtifactVerdict    = "verdict"
	ArtifactBuildNotes = "buildnotes"
)

// legacyArtifact pairs an artifact kind with the file-era name it was mirrored to
// under runs/<ticket>/, for the first-touch import.
type legacyArtifact struct {
	kind string
	file string
}

var legacyArtifacts = []legacyArtifact{
	{ArtifactHandoff, "handoff.md"},
	{ArtifactRubric, "rubric.json"},
	{ArtifactVerdict, "verdict.json"},
	{ArtifactBuildNotes, "buildnotes.md"},
}

// ValidArtifactKind reports whether kind is one the store accepts, so the API can
// reject an unknown kind rather than growing arbitrary rows.
func ValidArtifactKind(kind string) bool {
	switch kind {
	case ArtifactHandoff, ArtifactRubric, ArtifactVerdict, ArtifactBuildNotes:
		return true
	default:
		return false
	}
}

// Artifacts is the hub's authoritative per-run artifact store (ADR 0008). A phase
// posts the brief, rubric, verdict, or notes it produced over HTTP; a resumed run
// restores it from here. It is a real migrated table, never dropped and rebuilt.
type Artifacts struct {
	db       *sql.DB
	mu       sync.Mutex
	imported map[string]bool
}

// NewArtifacts returns an Artifacts store over db. The caller owns db's lifecycle.
func NewArtifacts(db *sql.DB) *Artifacts {
	return &Artifacts{db: db, imported: map[string]bool{}}
}

// Upsert writes a ticket's artifact of the given kind, replacing the row in place.
func (a *Artifacts) Upsert(root, ticket, kind, content string) error {
	_, err := a.db.Exec(
		`INSERT INTO artifacts(repo, ticket, kind, content)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(repo, ticket, kind) DO UPDATE SET content = excluded.content`,
		root, ticket, kind, content,
	)
	return err
}

// One returns a ticket's artifact of the given kind and whether it exists.
func (a *Artifacts) One(root, ticket, kind string) (content string, found bool, err error) {
	err = a.db.QueryRow(
		`SELECT content FROM artifacts WHERE repo = ? AND ticket = ? AND kind = ?`,
		root, ticket, kind,
	).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return content, true, nil
}

// All returns every artifact the hub holds for a ticket, keyed by kind. A ticket
// with none yields an empty map.
func (a *Artifacts) All(root, ticket string) (out map[string]string, err error) {
	q, err := a.db.Query(
		`SELECT kind, content FROM artifacts WHERE repo = ? AND ticket = ?`, root, ticket,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	out = map[string]string{}
	for q.Next() {
		var kind, content string
		if err := q.Scan(&kind, &content); err != nil {
			return nil, err
		}
		out[kind] = content
	}
	return out, q.Err()
}

// Remove drops every artifact for a ticket — the reset/clear/fresh-build sweep. A
// ticket with no artifacts is not an error.
func (a *Artifacts) Remove(root, ticket string) error {
	_, err := a.db.Exec(`DELETE FROM artifacts WHERE repo = ? AND ticket = ?`, root, ticket)
	return err
}

// ImportLegacy folds any file-era artifact files under runsDir into the table on
// the hub's first touch of a repo, removing each file only after its row commits.
// A kind the hub already holds is left untouched and its stale file removed: the
// row is authoritative and at least as fresh as the file. It is idempotent, and a
// repo already imported this serve lifetime is skipped without touching disk.
func (a *Artifacts) ImportLegacy(root, runsDir string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.imported[root] || runsDir == "" {
		a.imported[root] = true
		return nil
	}
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			a.imported[root] = true
			return nil
		}
		return fmt.Errorf("import legacy artifacts %s: %w", root, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ticket := e.Name()
		for _, la := range legacyArtifacts {
			if err := a.importLegacyFile(root, ticket, la, runsDir); err != nil {
				return err
			}
		}
	}
	a.imported[root] = true
	return nil
}

func (a *Artifacts) importLegacyFile(root, ticket string, la legacyArtifact, runsDir string) error {
	path := filepath.Join(runsDir, ticket, la.file)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if len(bytes.TrimSpace(data)) > 0 {
		_, exists, err := a.One(root, ticket, la.kind)
		if err != nil {
			return fmt.Errorf("import legacy artifact %s/%s/%s: %w", root, ticket, la.kind, err)
		}
		if !exists {
			if err := a.Upsert(root, ticket, la.kind, string(data)); err != nil {
				return fmt.Errorf("import legacy artifact %s/%s/%s: %w", root, ticket, la.kind, err)
			}
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove legacy artifact %s: %w", path, err)
	}
	return nil
}
