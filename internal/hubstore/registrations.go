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
	"strings"

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
	// throwawayHub is set when the hub's own home lives under the temp dir — an
	// isolated QA hub or a test hub, whose whole state dies with the directory. It
	// may adopt temp-dir repo roots; the real hub refuses them.
	throwawayHub bool
}

// NewRegistrations returns a store over db for the hub at home. The caller owns
// db's lifecycle.
func NewRegistrations(home string, db *sql.DB) *Registrations {
	return &Registrations{db: db, throwawayHub: underTempDir(home)}
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
// untouched. New repos are added; it never overwrites an existing row. Every root
// is stored canonicalized, and a repo whose root canonicalizes to one already
// known is skipped rather than inserted under its own spelling: the ON CONFLICT
// clause only catches a byte-identical root, and a second row for the same
// directory is a row every later lookup and removal has to reconcile. Throwaway
// roots are never folded in at all.
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
		seen[registry.CanonicalRoot(repo.Root)] = true
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	for _, repo := range repos {
		canonical := registry.CanonicalRoot(repo.Root)
		if canonical == "" || seen[canonical] || r.throwaway(canonical) {
			continue
		}
		seen[canonical] = true
		if _, err := tx.Exec(
			`INSERT INTO known_repos(root, name, runs_dir) VALUES(?, ?, ?) ON CONFLICT(root) DO NOTHING`,
			canonical, repo.Name, repo.RunsDir,
		); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	return tx.Commit()
}

// throwaway reports whether root is a clone the hub must not adopt permanently:
// a directory under the temp dir, where QA and verify sessions run scratchpad
// clones that vanish with the session. A loop running in one still shows in the
// repos list for as long as it is live — its root just never reaches known_repos.
func (r *Registrations) throwaway(root string) bool {
	return !r.throwawayHub && underTempDir(root)
}

// PruneStale drops the known-repo rows the hub should never have kept: throwaway
// roots under the temp dir, and roots whose directory is gone from disk. It
// returns the roots it dropped, so the caller can drop what hung off them. Roots
// registered from the web are exempt — they leave only through Unregister and
// Forget.
func (r *Registrations) PruneStale() (pruned []string, err error) {
	known, err := r.Known()
	if err != nil {
		return nil, err
	}
	registered, err := r.Registered()
	if err != nil {
		return nil, err
	}
	exempt := make(map[string]bool, len(registered))
	for _, root := range registered {
		exempt[registry.CanonicalRoot(root)] = true
	}
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	for _, repo := range known {
		usable := !r.throwaway(repo.Root) && dirExists(repo.Root)
		if usable || exempt[registry.CanonicalRoot(repo.Root)] {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM known_repos WHERE root = ?`, repo.Root); err != nil {
			return nil, errors.Join(err, tx.Rollback())
		}
		pruned = append(pruned, repo.Root)
	}
	return pruned, tx.Commit()
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

// Register adds root to the startable set under its canonical spelling,
// returning without error when it is already registered. Registering the same
// directory under a second spelling is the no-op it reads as, not a second row:
// every stored root is canonical, so the conflict clause catches it. An empty
// root registers nothing.
func (r *Registrations) Register(root string) error {
	canonical := registry.CanonicalRoot(root)
	if canonical == "" {
		return nil
	}
	_, err := r.db.Exec(
		`INSERT INTO registered_repos(root) VALUES(?) ON CONFLICT(root) DO NOTHING`, canonical,
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

// Canonicalize rewrites every stored root to its canonical spelling, merging the
// rows two spellings of one directory left behind. A store written before roots
// were canonicalized on the way in holds both `C:\Users\x\y` and `C:/Users/x/y`
// for one repo, which lists it twice with its flags split across the rows. Serve
// startup runs the merge once instead of every read papering over it. Rows already
// canonical are left where they are, so a hub with nothing to fix keeps its
// registration order.
func (r *Registrations) Canonicalize() error {
	known, err := r.Known()
	if err != nil {
		return err
	}
	registered, err := r.Registered()
	if err != nil {
		return err
	}
	knownMerges := rootsToMerge(known, func(repo registry.Repo) string { return repo.Root })
	registeredMerges := rootsToMerge(registered, func(root string) string { return root })
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	for _, group := range knownMerges {
		survivor := group[0]
		canonical := registry.CanonicalRoot(survivor.Root)
		if _, err := deleteRootsMatching(tx, "known_repos", canonical); err != nil {
			return errors.Join(err, tx.Rollback())
		}
		if _, err := tx.Exec(
			`INSERT INTO known_repos(root, name, runs_dir) VALUES(?, ?, ?)`,
			canonical, survivor.Name, survivor.RunsDir,
		); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	for _, group := range registeredMerges {
		canonical := registry.CanonicalRoot(group[0])
		if _, err := deleteRootsMatching(tx, "registered_repos", canonical); err != nil {
			return errors.Join(err, tx.Rollback())
		}
		if _, err := tx.Exec(`INSERT INTO registered_repos(root) VALUES(?)`, canonical); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	return tx.Commit()
}

// rootsToMerge groups stored rows by the canonical spelling of the root each
// carries, and returns every group a merge has to rewrite: a group holding more
// than one row, or a lone row stored under something other than its canonical
// spelling. A group already down to its canonical row is left out so nothing
// moves. Groups keep the order stored gave them, so the first row of a group is
// the first the store took.
func rootsToMerge[T any](stored []T, rootOf func(T) string) [][]T {
	groups := make(map[string][]T, len(stored))
	var order []string
	for _, row := range stored {
		canonical := registry.CanonicalRoot(rootOf(row))
		if _, ok := groups[canonical]; !ok {
			order = append(order, canonical)
		}
		groups[canonical] = append(groups[canonical], row)
	}
	var merge [][]T
	for _, canonical := range order {
		if group := groups[canonical]; len(group) > 1 || rootOf(group[0]) != canonical {
			merge = append(merge, group)
		}
	}
	return merge
}

// deleteRootsMatching drops every row of table whose stored root canonicalizes to
// root, reporting whether any did. Roots reach the store canonicalized now, but a
// hub upgraded from a build that stored them as the loop resolved them — `git
// rev-parse` hands back forward slashes on Windows — still holds rows a byte-equal
// DELETE would miss, and both spellings of one directory have to go in the same
// pass. The matches are collected before the first delete because the transaction
// holds a single connection.
func deleteRootsMatching(tx *sql.Tx, table, root string) (bool, error) {
	rows, err := tx.Query(`SELECT root FROM ` + table)
	if err != nil {
		return false, err
	}
	canonical := registry.CanonicalRoot(root)
	var matches []string
	for rows.Next() {
		var stored string
		if err := rows.Scan(&stored); err != nil {
			return false, errors.Join(err, rows.Close())
		}
		if registry.CanonicalRoot(stored) == canonical {
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

// underTempDir reports whether path lives in a throwaway tree: the OS temp dir or
// the conventional /tmp, which are not the same directory on macOS — each user
// gets a TMPDIR under /var/folders while agents write under /tmp. Both sides are
// compared in their literal and their symlink-resolved spelling, since /tmp
// resolves to /private/tmp and a root is stored exactly as the loop resolved it.
func underTempDir(path string) bool {
	if path == "" {
		return false
	}
	trees := append(pathSpellings(os.TempDir()), pathSpellings("/tmp")...)
	for _, spelling := range pathSpellings(path) {
		for _, tree := range trees {
			if spelling == tree || strings.HasPrefix(spelling, tree+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// pathSpellings returns path canonicalized, plus its symlink-resolved form when
// the path exists and resolves elsewhere. Both sides of the containment test come
// through here, so a scratchpad clone the loop reported as `c:\...\Temp\x` still
// prefix-matches the temp dir the OS spells `C:\...\Temp`.
func pathSpellings(path string) []string {
	canonical := registry.CanonicalRoot(path)
	resolved, err := filepath.EvalSymlinks(canonical)
	if err != nil || registry.CanonicalRoot(resolved) == canonical {
		return []string{canonical}
	}
	return []string{canonical, registry.CanonicalRoot(resolved)}
}

func dirExists(root string) bool {
	info, err := os.Stat(root)
	return err == nil && info.IsDir()
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
		canonical := registry.CanonicalRoot(root)
		if canonical == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO registered_repos(root) VALUES(?) ON CONFLICT(root) DO NOTHING`, canonical,
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
