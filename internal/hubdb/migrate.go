package hubdb

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

const schemaVersionKey = "schema_version"

const createSchemaMigrationsSQL = `CREATE TABLE IF NOT EXISTS schema_migrations (
	key TEXT PRIMARY KEY
) STRICT`

const upsertMetaSQL = `INSERT INTO meta(key, value) VALUES(?, ?)
	 ON CONFLICT(key) DO UPDATE SET value = excluded.value`

//go:embed schema/*.sql
var schemaFS embed.FS

type migration struct {
	version int
	name    string
	// key is the filename minus its version prefix and extension. It identifies
	// the migration across renumbering, which happens when parallel branches
	// claim the same version and the merge shifts one of them.
	key string
	sql string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(schemaFS, "schema")
	if err != nil {
		return nil, err
	}
	migs := make([]migration, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		num, rest, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("migration %q must be named <version>_<name>.sql", name)
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("migration %q has a non-numeric version: %w", name, err)
		}
		body, err := schemaFS.ReadFile("schema/" + name)
		if err != nil {
			return nil, err
		}
		migs = append(migs, migration{version: v, name: name, key: strings.TrimSuffix(rest, ".sql"), sql: string(body)})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	return migs, nil
}

func migrate(db *sql.DB) (int, error) {
	migs, err := loadMigrations()
	if err != nil {
		return 0, err
	}
	return migrateAll(db, migs)
}

// migrateAll applies every migration whose key is not yet recorded. Keys, not
// version numbers, decide whether a migration ran: a database that followed a
// branch's numbering must still receive exactly the migrations it is missing
// after the merge renumbers them. The version counter in meta is kept as a
// display-only high-water mark.
func migrateAll(db *sql.DB, migs []migration) (int, error) {
	byVersion := make(map[int]string, len(migs))
	byKey := make(map[string]string, len(migs))
	for _, m := range migs {
		if prev, ok := byVersion[m.version]; ok {
			return 0, fmt.Errorf("migrations %s and %s share version %d", prev, m.name, m.version)
		}
		if prev, ok := byKey[m.key]; ok {
			return 0, fmt.Errorf("migrations %s and %s share name %q", prev, m.name, m.key)
		}
		byVersion[m.version] = m.name
		byKey[m.key] = m.name
	}
	current, err := readVersion(db)
	if err != nil {
		return 0, err
	}
	applied, err := readApplied(db)
	if err != nil {
		return current, err
	}
	if len(applied) == 0 && current > 0 {
		if applied, err = backfillApplied(db, migs, current); err != nil {
			return current, err
		}
	}
	for _, m := range migs {
		if applied[m.key] {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return current, fmt.Errorf("apply migration %s: %w", m.name, err)
		}
		if m.version > current {
			current = m.version
		}
	}
	if len(migs) == 0 {
		return current, nil
	}
	last := migs[len(migs)-1].version
	if last != current {
		if _, err := db.Exec(upsertMetaSQL, schemaVersionKey, strconv.Itoa(last)); err != nil {
			return current, err
		}
	}
	return last, nil
}

func applyMigration(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(m.sql); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if _, err := tx.Exec(createSchemaMigrationsSQL); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(key) VALUES(?) ON CONFLICT(key) DO NOTHING`, m.key); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if _, err := tx.Exec(upsertMetaSQL, schemaVersionKey, strconv.Itoa(m.version)); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

// readVersion returns the applied schema version, or 0 before the meta table
// exists. A read that fails against an existing schema (a corrupt file) is
// returned as an error rather than masked as version 0.
func readVersion(db *sql.DB) (int, error) {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'meta'`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var value string
	err = db.QueryRow(`SELECT value FROM meta WHERE key = ?`, schemaVersionKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(value)
}

// readApplied returns the keys of recorded migrations, or an empty set before
// the schema_migrations table exists.
func readApplied(db *sql.DB) (map[string]bool, error) {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT key FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	applied := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		applied[k] = true
	}
	return applied, rows.Err()
}

// backfillApplied seeds key tracking on a database from before schema_migrations
// existed. Such a database carries only the version counter, which is trusted
// positionally: every migration numbered at or below it is marked applied.
func backfillApplied(db *sql.DB, migs []migration, version int) (map[string]bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(createSchemaMigrationsSQL); err != nil {
		return nil, errors.Join(err, tx.Rollback())
	}
	applied := make(map[string]bool)
	for _, m := range migs {
		if m.version > version {
			break
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(key) VALUES(?)`, m.key); err != nil {
			return nil, errors.Join(err, tx.Rollback())
		}
		applied[m.key] = true
	}
	return applied, tx.Commit()
}
