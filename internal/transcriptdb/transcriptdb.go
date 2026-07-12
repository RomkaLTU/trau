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
	"net/url"
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

// Health describes the transcript database's state for `trau doctor`.
type Health struct {
	Path      string
	Exists    bool
	Version   int
	SizeBytes int64
	Err       error
}

// CheckHealth reports the transcript database's path, applied schema version,
// on-disk size, and integrity under home without creating or migrating anything.
// It opens the file read-only and runs SQLite's quick_check (ADR 0008 §5). A
// missing file is not an error: it holds only transcripts and the hub recreates it
// empty on next open.
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
	h.SizeBytes = dbSize(h.Path)
	db, err := openReadOnly(h.Path)
	if err != nil {
		h.Err = err
		return h
	}
	version, readErr := readVersion(db)
	if err := errors.Join(readErr, integrityCheck(db), db.Close()); err != nil {
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

// dbSize sums the database file and its WAL/SHM sidecars — the on-disk footprint
// doctor reports. A missing sidecar contributes nothing.
func dbSize(path string) int64 {
	var total int64
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if info, err := os.Stat(p); err == nil {
			total += info.Size()
		}
	}
	return total
}

// integrityCheck runs SQLite's quick_check and returns an error unless it reports
// "ok" — the corruption probe ADR 0008 §5 gives doctor over the databases.
func integrityCheck(db *sql.DB) error {
	var result string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity check: %s", result)
	}
	return nil
}
