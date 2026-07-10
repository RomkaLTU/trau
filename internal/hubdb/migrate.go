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

//go:embed schema/*.sql
var schemaFS embed.FS

type migration struct {
	version int
	name    string
	sql     string
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
		num, _, ok := strings.Cut(name, "_")
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
		migs = append(migs, migration{version: v, name: name, sql: string(body)})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	return migs, nil
}

func migrate(db *sql.DB) (int, error) {
	migs, err := loadMigrations()
	if err != nil {
		return 0, err
	}
	current, err := readVersion(db)
	if err != nil {
		return 0, err
	}
	for _, m := range migs {
		if m.version <= current {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return current, fmt.Errorf("apply migration %s: %w", m.name, err)
		}
		current = m.version
	}
	return current, nil
}

func applyMigration(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(m.sql); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if _, err := tx.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		schemaVersionKey, strconv.Itoa(m.version),
	); err != nil {
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
