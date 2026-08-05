package hubstore

import (
	"database/sql"
	"errors"
	"maps"
	"slices"

	"github.com/RomkaLTU/trau/internal/registry"
)

// Tracker returns the tracker config the project's member repos share, keyed by
// config key. An unconfigured project returns an empty map.
func (p *Projects) Tracker(id string) (keys map[string]string, err error) {
	rows, err := p.db.Query(`SELECT key, value FROM project_tracker WHERE project = ?`, id)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	keys = map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		keys[key] = value
	}
	return keys, rows.Err()
}

// SetTracker replaces the project's tracker config. Seeding it into the member
// repos is the caller's job — the store never touches a repo's files.
func (p *Projects) SetTracker(id string, keys map[string]string) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	if err := requireProject(tx, id); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if _, err := tx.Exec(`DELETE FROM project_tracker WHERE project = ?`, id); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	for _, key := range slices.Sorted(maps.Keys(keys)) {
		if _, err := tx.Exec(
			`INSERT INTO project_tracker(project, key, value) VALUES(?, ?, ?)`, id, key, keys[key],
		); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	return tx.Commit()
}

// SeededTrackerKeys returns the tracker keys root carries on its project's
// behalf. Every other key in its config file is the repo's own and outranks the
// project default.
func (p *Projects) SeededTrackerKeys(root string) (keys map[string]bool, err error) {
	rows, err := p.db.Query(`SELECT key FROM project_tracker_seeded WHERE root = ?`, registry.CanonicalRoot(root))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	keys = map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys[key] = true
	}
	return keys, rows.Err()
}

// MarkTrackerSeeded records the keys root now holds on its project's behalf,
// replacing whatever it held before.
func (p *Projects) MarkTrackerSeeded(root string, keys []string) error {
	root = registry.CanonicalRoot(root)
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	if err := clearSeeded(tx, root); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	for _, key := range keys {
		if _, err := tx.Exec(`INSERT INTO project_tracker_seeded(root, key) VALUES(?, ?)`, root, key); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	return tx.Commit()
}

// ReleaseTrackerKey hands one key back to the repo, for a root that sets it
// itself rather than inheriting it. A project edit leaves it alone from then on.
func (p *Projects) ReleaseTrackerKey(root, key string) error {
	_, err := p.db.Exec(`DELETE FROM project_tracker_seeded WHERE root = ? AND key = ?`, registry.CanonicalRoot(root), key)
	return err
}

// canonicalizeSeeded rewrites every seeded-key root to its canonical spelling,
// folding the rows two spellings of one directory left behind. Seeded keys are
// read canonically now, so a row a pre-canonical hub stored under the spelling the
// loop resolved is invisible: the hub stops believing the repo holds the keys its
// project seeded and a project edit walks past them.
func canonicalizeSeeded(tx *sql.Tx) error {
	roots, err := seededRoots(tx)
	if err != nil {
		return err
	}
	for _, root := range roots {
		canonical := registry.CanonicalRoot(root)
		if canonical == root {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO project_tracker_seeded(root, key)
			 SELECT ?, key FROM project_tracker_seeded WHERE root = ?
			 ON CONFLICT(root, key) DO NOTHING`, canonical, root,
		); err != nil {
			return err
		}
		if err := clearSeeded(tx, root); err != nil {
			return err
		}
	}
	return nil
}

// seededRoots returns the distinct roots carrying seeded keys. They are read out
// before the first rewrite because the transaction holds a single connection.
func seededRoots(tx *sql.Tx) (roots []string, err error) {
	rows, err := tx.Query(`SELECT DISTINCT root FROM project_tracker_seeded`)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, rows.Err()
}

// clearSeeded hands root's seeded keys back to the repo: the values stay in its
// config file, but they are now its own rather than a project default.
func clearSeeded(tx *sql.Tx, root string) error {
	_, err := tx.Exec(`DELETE FROM project_tracker_seeded WHERE root = ?`, root)
	return err
}

func dropTracker(tx *sql.Tx, id string) error {
	_, err := tx.Exec(`DELETE FROM project_tracker WHERE project = ?`, id)
	return err
}
