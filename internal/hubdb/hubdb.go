// Package hubdb owns the trau serve hub's SQLite database. The file lives under
// the trau home as trau.db and is opened by the hub process alone (ADR 0007);
// `trau doctor` inspects it read-only for its health report. No loop, Run once,
// or TUI code path references it. The driver is pure-Go (modernc.org/sqlite) so
// CGO_ENABLED=0 is preserved.
package hubdb

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Filename is the hub database's fixed name under the trau home.
const Filename = "trau.db"

// Path returns the hub database path for the given trau home.
func Path(home string) string {
	return filepath.Join(home, Filename)
}

// DB is the hub's open handle to the database.
type DB struct {
	sql     *sql.DB
	path    string
	version int
}

// Open opens the hub database under home, creating the file if missing, with
// WAL journal mode, a busy timeout, and foreign keys on, then applies the
// embedded forward-only migrations. It is idempotent: opening an already
// migrated database only re-reads its schema version. An unopenable or corrupt
// file is reported as an error naming the path.
func Open(home string) (*DB, error) {
	if home == "" {
		return nil, errors.New("no trau home resolved — set TRAU_HOME or a usable home directory")
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, fmt.Errorf("create trau home %s: %w", home, err)
	}
	path := Path(home)
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	version, err := migrate(db)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, errors.Join(err, db.Close()))
	}
	return &DB{sql: db, path: path, version: version}, nil
}

// SQL exposes the underlying handle for hub stores built on later slices.
func (d *DB) SQL() *sql.DB { return d.sql }

// Path returns the file the database was opened from.
func (d *DB) Path() string { return d.path }

// Version returns the applied schema version.
func (d *DB) Version() int { return d.version }

// Close closes the database.
func (d *DB) Close() error { return d.sql.Close() }

// Health describes the hub database's state for `trau doctor`.
type Health struct {
	Path    string
	Exists  bool
	Version int
	Err     error
}

// CheckHealth reports the hub database's path, applied schema version, and open
// health under home without creating or migrating anything. A missing file is
// not an error: the hub creates it at serve startup.
func CheckHealth(home string) Health {
	h := Health{Path: Path(home)}
	if home == "" {
		h.Err = errors.New("no trau home resolved")
		return h
	}
	if _, err := os.Stat(h.Path); err != nil {
		return h
	}
	h.Exists = true
	db, err := openReadOnly(h.Path)
	if err != nil {
		h.Err = err
		return h
	}
	version, readErr := readVersion(db)
	if err := errors.Join(readErr, db.Close()); err != nil {
		h.Err = err
		return h
	}
	h.Version = version
	return h
}

func openReadOnly(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(2000)"}
	return sql.Open("sqlite", u.String())
}
