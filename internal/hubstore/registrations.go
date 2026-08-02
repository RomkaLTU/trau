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
// untouched. New repos are added; it never overwrites an existing row. A repo
// whose root canonicalizes to one already known is skipped rather than inserted
// under its own spelling: the ON CONFLICT clause only catches a byte-identical
// root, and a second row for the same directory is a row every later lookup and
// removal has to reconcile.
func (r *Registrations) Remember(repos []registry.Repo) error {
	if len(repos) == 0 {
		return nil
	}
	known, err := r.Known()
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(known)+len(repos))
	for _, repo := range known {
		seen[filepath.Clean(repo.Root)] = true
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	for _, repo := range repos {
		canonical := filepath.Clean(repo.Root)
		if repo.Root == "" || seen[canonical] {
			continue
		}
		seen[canonical] = true
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
func (r *Registrations) Unregister(root string) (removed bool, err error) {
	tx, err := r.db.Begin()
	if err != nil {
		return false, err
	}
	removed, err = deleteRootsMatching(tx, "registered_repos", root)
	if err != nil {
		return false, errors.Join(err, tx.Rollback())
	}
	return removed, tx.Commit()
}

// Forget drops root from both the known set and the startable set, so a repo the
// hub only ever observed can leave the list instead of lingering forever. It
// reports whether any row went: a removal that matched nothing left the repo
// listed, and the caller must not report that as success.
func (r *Registrations) Forget(root string) (removed bool, err error) {
	tx, err := r.db.Begin()
	if err != nil {
		return false, err
	}
	for _, table := range []string{"known_repos", "registered_repos"} {
		went, err := deleteRootsMatching(tx, table, root)
		if err != nil {
			return false, errors.Join(err, tx.Rollback())
		}
		removed = removed || went
	}
	return removed, tx.Commit()
}

// deleteRootsMatching drops every row of table whose stored root canonicalizes to
// root, reporting whether any did. A root is stored exactly as the loop resolved
// it — `git rev-parse` hands back forward slashes on Windows — so a byte-equal
// DELETE misses the very row it was aimed at, and a store holding both spellings
// of one directory needs both gone in the same pass. The matches are collected
// before the first delete because the transaction holds a single connection.
func deleteRootsMatching(tx *sql.Tx, table, root string) (bool, error) {
	rows, err := tx.Query(`SELECT root FROM ` + table)
	if err != nil {
		return false, err
	}
	cleaned := filepath.Clean(root)
	var matches []string
	for rows.Next() {
		var stored string
		if err := rows.Scan(&stored); err != nil {
			return false, errors.Join(err, rows.Close())
		}
		if filepath.Clean(stored) == cleaned {
			matches = append(matches, stored)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, err
	}
	for _, stored := range matches {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE root = ?`, stored); err != nil {
			return false, err
		}
	}
	return len(matches) > 0, nil
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
