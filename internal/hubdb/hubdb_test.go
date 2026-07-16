package hubdb

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// seedVersion brings db up to schema version v through the real migrations, so a
// fixture standing in for an older database carries the schema that version
// actually shipped rather than a hand-written subset of it.
func seedVersion(t *testing.T, db *sql.DB, v int) {
	t.Helper()
	migs, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	for _, m := range migs {
		if m.version > v {
			return
		}
		if err := applyMigration(db, m); err != nil {
			t.Fatalf("apply %s: %v", m.name, err)
		}
	}
}

func currentVersion(t *testing.T) int {
	t.Helper()
	migs, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(migs) == 0 {
		t.Fatal("no embedded migrations")
	}
	return migs[len(migs)-1].version
}

func TestOpenCreatesAndMigrates(t *testing.T) {
	home := t.TempDir()
	db, err := Open(home)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if want := currentVersion(t); db.Version() != want {
		t.Fatalf("version = %d, want %d", db.Version(), want)
	}
	if db.Path() != filepath.Join(home, Filename) {
		t.Fatalf("path = %q, want %q", db.Path(), filepath.Join(home, Filename))
	}
	if _, err := os.Stat(db.Path()); err != nil {
		t.Fatalf("database file not created: %v", err)
	}

	var journal string
	if err := db.SQL().QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(journal, "wal") {
		t.Fatalf("journal_mode = %q, want wal", journal)
	}

	var fk int
	if err := db.SQL().QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d, want 1", fk)
	}

	var stored string
	if err := db.SQL().QueryRow(`SELECT value FROM meta WHERE key = ?`, schemaVersionKey).Scan(&stored); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if want := strconv.Itoa(currentVersion(t)); stored != want {
		t.Fatalf("meta schema_version = %q, want %q", stored, want)
	}
}

func TestOpenIdempotent(t *testing.T) {
	home := t.TempDir()

	first, err := Open(home)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	v1 := first.Version()
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := Open(home)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	if second.Version() != v1 {
		t.Fatalf("version drifted across opens: %d then %d", v1, second.Version())
	}

	var rows int
	if err := second.SQL().QueryRow(`SELECT count(*) FROM meta WHERE key = ?`, schemaVersionKey).Scan(&rows); err != nil {
		t.Fatalf("count meta: %v", err)
	}
	if rows != 1 {
		t.Fatalf("schema_version rows = %d, want 1", rows)
	}
}

func TestOpenOverExistingDerivedCheckpoints(t *testing.T) {
	home := t.TempDir()

	seed, err := sql.Open("sqlite", Path(home))
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	seedVersion(t, seed, 7)
	if _, err := seed.Exec(`CREATE TABLE checkpoints (
	     repo TEXT NOT NULL, ticket TEXT NOT NULL, phase TEXT NOT NULL DEFAULT '',
	     title TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '',
	     pr TEXT NOT NULL DEFAULT '', pr_url TEXT NOT NULL DEFAULT '',
	     failure_reason TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '',
	     data TEXT NOT NULL DEFAULT '{}', PRIMARY KEY (repo, ticket)
	 ) STRICT`); err != nil {
		t.Fatalf("seed derived checkpoints: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	db, err := Open(home)
	if err != nil {
		t.Fatalf("Open over a pre-existing derived checkpoints table: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if want := currentVersion(t); db.Version() != want {
		t.Fatalf("version = %d, want %d", db.Version(), want)
	}
	if _, err := db.SQL().Exec(`INSERT INTO checkpoints(repo, ticket) VALUES('/repo', 'COD-1')`); err != nil {
		t.Fatalf("insert into migrated checkpoints: %v", err)
	}
}

func ftsMatches(t *testing.T, db *sql.DB, term string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM issues_fts WHERE issues_fts MATCH ?`, term).Scan(&n); err != nil {
		t.Fatalf("fts match %q: %v", term, err)
	}
	return n
}

func TestOpenRepopulatesFTSWithAssigneeName(t *testing.T) {
	home := t.TempDir()

	seed, err := sql.Open("sqlite", Path(home))
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	seedVersion(t, seed, 20)
	if _, err := seed.Exec(
		`INSERT INTO issues(repo, source, identifier, title, assignee_id, assignee_name)
		 VALUES('/repo/acme', 'linear', 'COD-1', 'nothing special', 'u-1', 'Ada Lovelace')`,
	); err != nil {
		t.Fatalf("seed populated issue: %v", err)
	}
	if n := ftsMatches(t, seed, "lovelace"); n != 0 {
		t.Fatalf("pre-migration assignee matches = %d, want 0 while the column is unindexed", n)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	db, err := Open(home)
	if err != nil {
		t.Fatalf("Open over a populated pre-assignee-FTS database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if n := ftsMatches(t, db.SQL(), "lovelace"); n != 1 {
		t.Fatalf("post-migration assignee matches = %d, want the rebuild to index the existing row", n)
	}
}

func TestOpenCorrupt(t *testing.T) {
	home := t.TempDir()
	garbage := []byte(strings.Repeat("not a sqlite database ", 64))
	if err := os.WriteFile(Path(home), garbage, 0o644); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	db, err := Open(home)
	if err == nil {
		_ = db.Close()
		t.Fatal("Open succeeded on a corrupt file, want error")
	}
	if !strings.Contains(err.Error(), Path(home)) {
		t.Fatalf("error %q does not name the database path", err)
	}
}

func TestOpenNoHome(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("Open(\"\") succeeded, want error")
	}
}

func TestCheckHealth(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		home := t.TempDir()
		h := CheckHealth(home)
		if h.Err != nil {
			t.Fatalf("Err = %v, want nil", h.Err)
		}
		if h.Exists {
			t.Fatal("Exists = true, want false")
		}
		if h.Path != Path(home) {
			t.Fatalf("Path = %q, want %q", h.Path, Path(home))
		}
	})

	t.Run("healthy", func(t *testing.T) {
		home := t.TempDir()
		db, err := Open(home)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		_ = db.Close()

		h := CheckHealth(home)
		if h.Err != nil {
			t.Fatalf("Err = %v, want nil", h.Err)
		}
		if !h.Exists {
			t.Fatal("Exists = false, want true")
		}
		if want := currentVersion(t); h.Version != want {
			t.Fatalf("Version = %d, want %d", h.Version, want)
		}
	})

	t.Run("corrupt", func(t *testing.T) {
		home := t.TempDir()
		garbage := []byte(strings.Repeat("not a sqlite database ", 64))
		if err := os.WriteFile(Path(home), garbage, 0o644); err != nil {
			t.Fatalf("seed corrupt file: %v", err)
		}
		h := CheckHealth(home)
		if !h.Exists {
			t.Fatal("Exists = false, want true")
		}
		if h.Err == nil {
			t.Fatal("Err = nil, want a corruption error")
		}
	})
}
