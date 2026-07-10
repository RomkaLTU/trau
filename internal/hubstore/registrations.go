// Package hubstore holds the hub-owned stores backed by the SQLite hub database
// (ADR 0007). Only the `trau serve` process opens the database, so these stores
// have single-writer semantics without cross-process locking. Registrations is
// the first: the known-repos set and the web-registered startable roots that
// used to live in repos.json and workspace.json.
package hubstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/RomkaLTU/trau/internal/registry"
)

const (
	legacyReposFile     = "repos.json"
	legacyWorkspaceFile = "workspace.json"
)

// Registrations is the hub's authoritative store for repo registration state:
// the known repos it has seen a loop run in, and the roots registered from the
// web as startable.
type Registrations struct {
	db *sql.DB
}

// NewRegistrations returns a store over db. The caller owns db's lifecycle.
func NewRegistrations(db *sql.DB) *Registrations {
	return &Registrations{db: db}
}

// Known returns the repos the hub has seen a loop run in, sorted by name.
func (r *Registrations) Known() (repos []registry.Repo, err error) {
	rows, err := r.db.Query(`SELECT root, name, runs_dir FROM known_repos ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	repos = []registry.Repo{}
	for rows.Next() {
		var repo registry.Repo
		if err := rows.Scan(&repo.Root, &repo.Name, &repo.RunsDir); err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, rows.Err()
}

// Remember folds repos into the known set, leaving any already-known repo
// untouched. New repos are added; it never overwrites an existing row.
func (r *Registrations) Remember(repos []registry.Repo) error {
	if len(repos) == 0 {
		return nil
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	for _, repo := range repos {
		if repo.Root == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO known_repos(root, name, runs_dir) VALUES(?, ?, ?) ON CONFLICT(root) DO NOTHING`,
			repo.Root, repo.Name, repo.RunsDir,
		); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	return tx.Commit()
}

// Registered returns the repo roots registered as startable from the web, in the
// order they were added.
func (r *Registrations) Registered() (roots []string, err error) {
	rows, err := r.db.Query(`SELECT root FROM registered_repos ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	roots = []string{}
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, rows.Err()
}

// Register adds root to the startable set, returning without error when it is
// already registered.
func (r *Registrations) Register(root string) error {
	_, err := r.db.Exec(
		`INSERT INTO registered_repos(root) VALUES(?) ON CONFLICT(root) DO NOTHING`, root,
	)
	return err
}

// Unregister removes root from the startable set, reporting whether it was
// present.
func (r *Registrations) Unregister(root string) (bool, error) {
	res, err := r.db.Exec(`DELETE FROM registered_repos WHERE root = ?`, root)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ImportLegacy backfills the store from any repos.json / workspace.json left by
// a file-era installation, then deletes each file. Import is transactional: a
// file is removed only after its rows commit; a failed import returns an error
// naming the file and leaves it in place so serve startup can abort without
// losing state. A fresh install has no files and imports nothing.
func (r *Registrations) ImportLegacy(home string) error {
	if home == "" {
		return nil
	}
	if err := r.importKnown(filepath.Join(home, legacyReposFile)); err != nil {
		return err
	}
	return r.importRegistered(filepath.Join(home, legacyWorkspaceFile))
}

func (r *Registrations) importKnown(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy %s: %w", path, err)
	}
	var known map[string]registry.Repo
	if err := json.Unmarshal(data, &known); err != nil {
		return fmt.Errorf("parse legacy %s: %w", path, err)
	}
	repos := make([]registry.Repo, 0, len(known))
	for root, repo := range known {
		if repo.Root == "" {
			repo.Root = root
		}
		repos = append(repos, repo)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Root < repos[j].Root })
	if err := r.Remember(repos); err != nil {
		return fmt.Errorf("import legacy %s: %w", path, err)
	}
	return os.Remove(path)
}

func (r *Registrations) importRegistered(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy %s: %w", path, err)
	}
	var ws struct {
		Repos []string `json:"repos"`
	}
	if err := json.Unmarshal(data, &ws); err != nil {
		return fmt.Errorf("parse legacy %s: %w", path, err)
	}
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("import legacy %s: %w", path, err)
	}
	for _, root := range ws.Repos {
		if root == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO registered_repos(root) VALUES(?) ON CONFLICT(root) DO NOTHING`, root,
		); err != nil {
			return fmt.Errorf("import legacy %s: %w", path, errors.Join(err, tx.Rollback()))
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("import legacy %s: %w", path, err)
	}
	return os.Remove(path)
}

// LegacyFiles returns the paths of any file-era registration files still present
// under home. A non-empty result means a half-completed upgrade that `trau
// doctor` surfaces.
func LegacyFiles(home string) []string {
	if home == "" {
		return nil
	}
	var found []string
	for _, name := range []string{legacyReposFile, legacyWorkspaceFile} {
		path := filepath.Join(home, name)
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		}
	}
	return found
}
