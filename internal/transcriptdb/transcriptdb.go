// Package transcriptdb owns the trau serve hub's transcript database. The file
// lives under the trau home as transcripts.db, separate from trau.db so the bulk
// PTY-transcript bytes never bloat the authoritative store or drag its
// backups/VACUUM (ADR 0008 §4). It is opened by the hub process alone, WAL, with
// incremental auto-vacuum so pruned chunks reclaim space. It is the one store
// safe to delete wholesale: it holds only transcripts, so a missing or corrupt
// file loses replay for past runs and nothing else — the hub recreates it empty
// on next open. The driver is pure-Go (modernc.org/sqlite) so CGO_ENABLED=0 is
// preserved.
package transcriptdb

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Filename is the transcript database's fixed name under the trau home.
const Filename = "transcripts.db"

// Path returns the transcript database path for the given trau home.
func Path(home string) string {
	return filepath.Join(home, Filename)
}

// DB is the hub's open handle to the transcript database.
type DB struct {
	sql     *sql.DB
	path    string
	version int
}

// Open opens the transcript database under home, creating the file if missing,
// with WAL journal mode, a busy timeout, and incremental auto-vacuum, then
// applies the embedded forward-only migrations. Deleting the file between opens
// is safe: Open recreates it empty. An unopenable or corrupt file is reported as
// an error naming the path.
func Open(home string) (*DB, error) {
	if home == "" {
		return nil, errors.New("no trau home resolved — set TRAU_HOME or a usable home directory")
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, fmt.Errorf("create trau home %s: %w", home, err)
	}
	path := Path(home)
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=auto_vacuum(2)")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	version, err := migrate(db)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, errors.Join(err, db.Close()))
	}
	return &DB{sql: db, path: path, version: version}, nil
}

// SQL exposes the underlying handle for the transcript store.
func (d *DB) SQL() *sql.DB { return d.sql }

// Path returns the file the database was opened from.
func (d *DB) Path() string { return d.path }

// Version returns the applied schema version.
func (d *DB) Version() int { return d.version }

// Close closes the database.
func (d *DB) Close() error { return d.sql.Close() }
