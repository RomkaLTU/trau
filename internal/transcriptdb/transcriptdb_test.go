package transcriptdb

import (
	"os"
	"testing"
)

// TestOpenCreatesSchema checks a fresh open migrates the chunk table into place.
func TestOpenCreatesSchema(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if db.Version() < 1 {
		t.Errorf("schema version = %d, want >= 1", db.Version())
	}
	if _, err := db.SQL().Exec(
		`INSERT INTO transcript_chunks(repo, stem, seq, ts, cols, rows, data) VALUES('r', 's', 0, 1, 80, 24, x'00')`,
	); err != nil {
		t.Fatalf("insert into chunk table: %v", err)
	}
}

// TestDeleteAndReopenRecreatesEmpty checks the store is safe to delete wholesale:
// the hub recreates it empty on the next open without carrying stale rows.
func TestDeleteAndReopenRecreatesEmpty(t *testing.T) {
	home := t.TempDir()
	db, err := Open(home)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.SQL().Exec(
		`INSERT INTO transcript_chunks(repo, stem, seq, ts, cols, rows, data) VALUES('r', 's', 0, 1, 80, 24, x'00')`,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(Path(home) + suffix)
	}

	db2, err := Open(home)
	if err != nil {
		t.Fatalf("reopen after delete: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	var n int
	if err := db2.SQL().QueryRow(`SELECT COUNT(*) FROM transcript_chunks`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("recreated database has %d rows, want empty", n)
	}
}
