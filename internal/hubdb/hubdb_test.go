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

func TestOpenAddsPromptOverridesToExisting(t *testing.T) {
	home := t.TempDir()

	seed, err := sql.Open("sqlite", Path(home))
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	seedVersion(t, seed, 25)
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	db, err := Open(home)
	if err != nil {
		t.Fatalf("Open over a pre-prompt-overrides database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if want := currentVersion(t); db.Version() != want {
		t.Fatalf("version = %d, want %d", db.Version(), want)
	}
	if _, err := db.SQL().Exec(
		`INSERT INTO prompt_overrides(name, repo, body) VALUES('build', '', 'custom body')`,
	); err != nil {
		t.Fatalf("insert into migrated prompt_overrides: %v", err)
	}
}

// TestOpenBackfillsQAAccountSource covers the upgrade path for the provenance
// column: a QA account stored before the migration must come back as manual, not
// as an empty string the settings surface would have to interpret.
func TestOpenBackfillsQAAccountSource(t *testing.T) {
	home := t.TempDir()

	seed, err := sql.Open("sqlite", Path(home))
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	seedVersion(t, seed, 33)
	if _, err := seed.Exec(
		`INSERT INTO qa_accounts(repo, label, username, secret) VALUES('/repos/acme', 'admin', 'admin@example.test', 'pw')`,
	); err != nil {
		t.Fatalf("seed qa account: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	db, err := Open(home)
	if err != nil {
		t.Fatalf("Open over a pre-source database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var source string
	if err := db.SQL().QueryRow(`SELECT source FROM qa_accounts WHERE label = 'admin'`).Scan(&source); err != nil {
		t.Fatalf("read migrated source: %v", err)
	}
	if source != "manual" {
		t.Errorf("pre-existing account source = %q, want %q", source, "manual")
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

// The 2026-07-16 incident: two epics developed in parallel both claimed version
// 20; the merge renumbered one to 22, and a database that had followed that
// branch skipped the real 20 (its counter already said 20) and died applying 21.
// Key tracking must apply exactly the missing migrations and recognize the
// renumbered one as already run.
func TestMigrateRenumberedParallelBranches(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	base := migration{version: 1, name: "0001_base.sql", key: "base", sql: `
		CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT;
		CREATE TABLE t (id INTEGER PRIMARY KEY);`}
	atlas := migration{version: 2, name: "0002_atlas.sql", key: "atlas", sql: `CREATE TABLE atlas (id INTEGER PRIMARY KEY);`}
	if _, err := migrateAll(db, []migration{base, atlas}); err != nil {
		t.Fatalf("branch migrate: %v", err)
	}

	assignees := migration{version: 2, name: "0002_assignees.sql", key: "assignees", sql: `ALTER TABLE t ADD COLUMN assignee TEXT;`}
	fts := migration{version: 3, name: "0003_fts.sql", key: "fts", sql: `CREATE INDEX t_assignee ON t(assignee);`}
	atlas.version, atlas.name = 4, "0004_atlas.sql"
	v, err := migrateAll(db, []migration{base, assignees, fts, atlas})
	if err != nil {
		t.Fatalf("merged migrate: %v", err)
	}
	if v != 4 {
		t.Fatalf("version = %d, want 4", v)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('t') WHERE name = 'assignee'`).Scan(&n); err != nil {
		t.Fatalf("read columns: %v", err)
	}
	if n != 1 {
		t.Fatalf("assignee columns = %d, want 1", n)
	}
	var stored string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, schemaVersionKey).Scan(&stored); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if stored != "4" {
		t.Fatalf("stored schema_version = %s, want 4", stored)
	}
}

func TestMigrateBackfillsLegacyVersionCounter(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	migs := []migration{
		{version: 1, name: "0001_base.sql", key: "base", sql: `CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT;`},
		{version: 2, name: "0002_second.sql", key: "second", sql: `CREATE TABLE second (id INTEGER PRIMARY KEY);`},
	}
	if _, err := migrateAll(db, migs); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// A database migrated before key tracking has no schema_migrations table.
	if _, err := db.Exec(`DROP TABLE schema_migrations`); err != nil {
		t.Fatalf("drop: %v", err)
	}

	v, err := migrateAll(db, migs)
	if err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	if v != 2 {
		t.Fatalf("version = %d, want 2", v)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count keys: %v", err)
	}
	if n != 2 {
		t.Fatalf("backfilled keys = %d, want 2", n)
	}
}

func TestMigrateRejectsCollidingMigrations(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	sameVersion := []migration{
		{version: 1, name: "0001_a.sql", key: "a", sql: `SELECT 1;`},
		{version: 1, name: "0001_b.sql", key: "b", sql: `SELECT 1;`},
	}
	if _, err := migrateAll(db, sameVersion); err == nil || !strings.Contains(err.Error(), "share version") {
		t.Fatalf("same-version error = %v, want share version", err)
	}

	sameKey := []migration{
		{version: 1, name: "0001_a.sql", key: "a", sql: `SELECT 1;`},
		{version: 2, name: "0002_a.sql", key: "a", sql: `SELECT 1;`},
	}
	if _, err := migrateAll(db, sameKey); err == nil || !strings.Contains(err.Error(), "share name") {
		t.Fatalf("same-key error = %v, want share name", err)
	}
}

// TestOpenAddsIssueProviderToExisting covers the upgrade path: an issue stored before
// the migration must come back unpinned, not with a NULL the store's scan cannot read.
func TestOpenAddsIssueProviderToExisting(t *testing.T) {
	home := t.TempDir()

	seed, err := sql.Open("sqlite", Path(home))
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	seedVersion(t, seed, 36)
	if _, err := seed.Exec(
		`INSERT INTO issues(repo, source, identifier, title) VALUES('/repos/acme', 'linear', 'COD-1', 'Fix')`,
	); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	db, err := Open(home)
	if err != nil {
		t.Fatalf("Open over a pre-provider-pin database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var provider string
	if err := db.SQL().QueryRow(
		`SELECT provider FROM issues WHERE identifier = 'COD-1'`,
	).Scan(&provider); err != nil {
		t.Fatalf("read migrated provider: %v", err)
	}
	if provider != "" {
		t.Fatalf("provider = %q, want an existing issue to migrate in unpinned", provider)
	}
}
