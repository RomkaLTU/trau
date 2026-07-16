package hubstore

import (
	"database/sql"
	"errors"
	"time"
)

// atlasRetention is how many document versions the store keeps per (repo, view)
// before an insert prunes the oldest (ADR 0013).
const atlasRetention = 10

// atlasTimeLayout is the wall-clock format a document's generated-at is stamped
// in — the same zoneless layout the checkpoint store uses for a run's updated-at,
// so the staleness count compares the two directly.
const atlasTimeLayout = "2006-01-02 15:04:05"

// AtlasDocument is one stored generation of a View. A good generation carries the
// validated document JSON and an empty Error; a failed one carries the error text
// and an empty Document.
type AtlasDocument struct {
	Version   int
	Commit    string
	Document  string
	CostUSD   float64
	Error     string
	CreatedAt string
}

// AtlasMeta is a View's catalog metadata: the version, commit, generated-at, and
// cost of its latest good document, plus the error of the latest attempt when
// that attempt failed. HasDocument is false when the View has no good document.
type AtlasMeta struct {
	HasDocument bool
	Version     int
	Commit      string
	CostUSD     float64
	GeneratedAt string
	Error       string
}

// AtlasDocuments is the hub's authoritative store of agent-generated Atlas View
// documents (ADR 0013). Each generation appends a version; the latest valid one
// surfaces and a failed one is kept as history without displacing it. It is a
// real migrated table, never dropped and rebuilt.
type AtlasDocuments struct {
	db  *sql.DB
	now func() time.Time
}

// NewAtlasDocuments returns an AtlasDocuments store over db. The caller owns db's
// lifecycle.
func NewAtlasDocuments(db *sql.DB) *AtlasDocuments {
	return &AtlasDocuments{db: db, now: time.Now}
}

// Insert appends a generation for (repo, view) under the next version, stamping
// its generated-at, and prunes the (repo, view) history to the retention window.
// A failed generation passes its error text and an empty document; a good one the
// reverse. It returns the assigned version.
func (a *AtlasDocuments) Insert(repo, viewID, commit, document string, costUSD float64, genErr string) (int, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	var version int
	err = tx.QueryRow(
		`SELECT COALESCE(MAX(version), 0) + 1 FROM atlas_documents WHERE repo = ? AND view_id = ?`,
		repo, viewID,
	).Scan(&version)
	if err != nil {
		return 0, errors.Join(err, tx.Rollback())
	}
	if _, err := tx.Exec(
		`INSERT INTO atlas_documents(repo, view_id, version, commit_sha, document, cost_usd, error, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		repo, viewID, version, commit, document, costUSD, genErr, a.now().Format(atlasTimeLayout),
	); err != nil {
		return 0, errors.Join(err, tx.Rollback())
	}
	if err := pruneAtlas(tx, repo, viewID); err != nil {
		return 0, errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return version, nil
}

// Latest returns the newest good document for (repo, view) — the one that
// surfaces — and whether one exists.
func (a *AtlasDocuments) Latest(repo, viewID string) (AtlasDocument, bool, error) {
	return a.one(
		`SELECT version, commit_sha, document, cost_usd, error, created_at
		 FROM atlas_documents WHERE repo = ? AND view_id = ? AND error = ''
		 ORDER BY version DESC LIMIT 1`, repo, viewID,
	)
}

// Version returns a specific version's document for (repo, view), and whether it
// exists — the history read behind ?version=.
func (a *AtlasDocuments) Version(repo, viewID string, version int) (AtlasDocument, bool, error) {
	return a.one(
		`SELECT version, commit_sha, document, cost_usd, error, created_at
		 FROM atlas_documents WHERE repo = ? AND view_id = ? AND version = ?`, repo, viewID, version,
	)
}

// Meta returns (repo, view)'s catalog metadata: the latest good document's
// version, commit, generated-at, and cost, plus the latest attempt's error when
// that attempt failed.
func (a *AtlasDocuments) Meta(repo, viewID string) (AtlasMeta, error) {
	var m AtlasMeta
	good, ok, err := a.Latest(repo, viewID)
	if err != nil {
		return AtlasMeta{}, err
	}
	if ok {
		m.HasDocument = true
		m.Version = good.Version
		m.Commit = good.Commit
		m.CostUSD = good.CostUSD
		m.GeneratedAt = good.CreatedAt
	}
	err = a.db.QueryRow(
		`SELECT error FROM atlas_documents WHERE repo = ? AND view_id = ? ORDER BY version DESC LIMIT 1`,
		repo, viewID,
	).Scan(&m.Error)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AtlasMeta{}, err
	}
	return m, nil
}

func (a *AtlasDocuments) one(query string, args ...any) (AtlasDocument, bool, error) {
	var d AtlasDocument
	err := a.db.QueryRow(query, args...).Scan(
		&d.Version, &d.Commit, &d.Document, &d.CostUSD, &d.Error, &d.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AtlasDocument{}, false, nil
	}
	if err != nil {
		return AtlasDocument{}, false, err
	}
	return d, true, nil
}

// pruneAtlas drops (repo, view) versions older than the retention window, keeping
// the newest atlasRetention. Fewer than that many rows is a no-op.
func pruneAtlas(tx *sql.Tx, repo, viewID string) error {
	var cutoff int
	err := tx.QueryRow(
		`SELECT version FROM atlas_documents WHERE repo = ? AND view_id = ?
		 ORDER BY version DESC LIMIT 1 OFFSET ?`,
		repo, viewID, atlasRetention,
	).Scan(&cutoff)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`DELETE FROM atlas_documents WHERE repo = ? AND view_id = ? AND version <= ?`,
		repo, viewID, cutoff,
	)
	return err
}
