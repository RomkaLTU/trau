package hubstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func testPhaseLogs(t *testing.T) *PhaseLogs {
	t.Helper()
	home := t.TempDir()
	db, err := hubdb.Open(home)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStores(home, db.SQL(), nil, Retention{}).PhaseLogs()
}

func TestPhaseLogsUpsertListRemove(t *testing.T) {
	l := testPhaseLogs(t)

	// Written oldest-first; the list must come back newest-first.
	if err := l.upsertAt("/repo", "COD-1", "build", "build output", 100); err != nil {
		t.Fatalf("upsert build: %v", err)
	}
	if err := l.upsertAt("/repo", "COD-1", "verify", "verify output", 200); err != nil {
		t.Fatalf("upsert verify: %v", err)
	}
	// A different repo/ticket must not bleed in.
	_ = l.upsertAt("/other", "COD-1", "build", "elsewhere", 300)

	logs, err := l.List("/repo", "COD-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("List returned %d logs, want 2", len(logs))
	}
	if logs[0].Phase != "verify" || logs[1].Phase != "build" {
		t.Fatalf("order = [%s, %s], want verify newest-first", logs[0].Phase, logs[1].Phase)
	}
	if logs[0].Content != "verify output" {
		t.Fatalf("verify content = %q", logs[0].Content)
	}

	// Upsert replaces content in place.
	if err := l.upsertAt("/repo", "COD-1", "build", "rebuilt", 400); err != nil {
		t.Fatalf("upsert rebuild: %v", err)
	}
	logs, _ = l.List("/repo", "COD-1")
	if logs[0].Phase != "build" || logs[0].Content != "rebuilt" {
		t.Fatalf("after rebuild, newest = %+v, want build/rebuilt", logs[0])
	}

	if err := l.Remove("/repo", "COD-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if logs, _ := l.List("/repo", "COD-1"); len(logs) != 0 {
		t.Fatalf("List after Remove returned %d logs, want 0", len(logs))
	}
}

func TestPhaseLogsImportLegacy(t *testing.T) {
	l := testPhaseLogs(t)
	runsDir := t.TempDir()
	ticketDir := filepath.Join(runsDir, "COD-7")
	if err := os.MkdirAll(ticketDir, 0o755); err != nil {
		t.Fatalf("mkdir ticket: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ticketDir, "build.log"), []byte("legacy build"), 0o644); err != nil {
		t.Fatalf("seed build.log: %v", err)
	}
	// The transcript family must be skipped, not imported as a phase log.
	resultsDir := filepath.Join(runsDir, "_agent-results")
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatalf("mkdir results: %v", err)
	}
	if err := os.WriteFile(filepath.Join(resultsDir, "123-build.pty.log"), []byte("raw pty"), 0o644); err != nil {
		t.Fatalf("seed pty log: %v", err)
	}

	if err := l.ImportLegacy("/repo", runsDir); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}

	logs, err := l.List("/repo", "COD-7")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(logs) != 1 || logs[0].Phase != "build" || logs[0].Content != "legacy build" {
		t.Fatalf("imported logs = %+v, want a single build log", logs)
	}
	if _, err := os.Stat(filepath.Join(ticketDir, "build.log")); !os.IsNotExist(err) {
		t.Fatalf("legacy build.log survived import (err=%v)", err)
	}
	// The _agent-results dir was skipped, so no bogus "_agent-results" ticket.
	if logs, _ := l.List("/repo", "_agent-results"); len(logs) != 0 {
		t.Fatalf("transcript dir imported as a phase log: %+v", logs)
	}
}
