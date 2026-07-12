package hubstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func testLessons(t *testing.T) *Lessons {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStores(db.SQL()).Lessons()
}

func TestLessonAppendAllNewestFirst(t *testing.T) {
	l := testLessons(t)
	first := Lesson{Ticket: "COD-1", FailureType: "migration", Lesson: "older", Tags: []string{"migration"}, Evidence: []string{"a", "b"}}
	second := Lesson{Ticket: "COD-2", FailureType: "test", Lesson: "newer", Tags: []string{"test"}}
	if err := l.Append("/repo", first); err != nil {
		t.Fatalf("Append first: %v", err)
	}
	if err := l.Append("/repo", second); err != nil {
		t.Fatalf("Append second: %v", err)
	}
	// A different repo must not bleed into /repo's ledger.
	_ = l.Append("/other", Lesson{Ticket: "COD-9", Lesson: "elsewhere"})

	got, err := l.All("/repo")
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("All returned %d, want 2: %+v", len(got), got)
	}
	if got[0].Lesson != "newer" || got[1].Lesson != "older" {
		t.Fatalf("All order = [%q, %q], want newest first [newer, older]", got[0].Lesson, got[1].Lesson)
	}
	if len(got[1].Evidence) != 2 || got[1].Tags[0] != "migration" {
		t.Fatalf("evidence/tags not round-tripped: %+v", got[1])
	}
}

func TestLessonAllEmpty(t *testing.T) {
	l := testLessons(t)
	got, err := l.All("/repo")
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("All on empty ledger = %+v, want an empty (non-nil) slice", got)
	}
}

func seedLegacyLedger(t *testing.T, runsDir, content string) {
	t.Helper()
	dir := filepath.Join(runsDir, "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lessons.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatalf("seed lessons.jsonl: %v", err)
	}
}

func TestLessonImportLegacyFoldsFileSkippingMalformed(t *testing.T) {
	l := testLessons(t)
	runsDir := t.TempDir()
	seedLegacyLedger(t, runsDir, `{"ticket":"COD-1","lesson":"first","failure_type":"migration","tags":["migration"]}
not json at all

{"ticket":"COD-2","lesson":"","failure_type":"build"}
{broken
{"ticket":"COD-3","lesson":"second","evidence":["ev"]}
`)

	if err := l.ImportLegacy("/repo", runsDir); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	got, _ := l.All("/repo")
	// The blank, non-JSON, empty-lesson, and broken lines are dropped; the two real
	// records land, and All returns them newest first (import preserves file order).
	if len(got) != 2 {
		t.Fatalf("imported %d records, want 2 (malformed dropped): %+v", len(got), got)
	}
	if got[0].Lesson != "second" || got[1].Lesson != "first" {
		t.Fatalf("import order = [%q, %q], want newest first [second, first]", got[0].Lesson, got[1].Lesson)
	}
	if _, err := os.Stat(filepath.Join(runsDir, "memory", "lessons.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("legacy ledger survived import (err=%v)", err)
	}
}

func TestLessonImportLegacyIsIdempotent(t *testing.T) {
	l := testLessons(t)
	runsDir := t.TempDir()
	seedLegacyLedger(t, runsDir, `{"ticket":"COD-1","lesson":"only one"}
`)
	if err := l.ImportLegacy("/repo", runsDir); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	if got, _ := l.All("/repo"); len(got) != 1 {
		t.Fatalf("first import produced %d records, want 1", len(got))
	}
	if _, err := os.Stat(filepath.Join(runsDir, "memory", "lessons.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("import left the ledger file in place (err=%v)", err)
	}

	// Across a serve restart the removed file leaves nothing to re-fold, so a fresh
	// guard does not duplicate the ledger.
	fresh := NewLessons(l.db)
	if err := fresh.ImportLegacy("/repo", runsDir); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if got, _ := fresh.All("/repo"); len(got) != 1 {
		t.Fatalf("re-import produced %d records, want 1 (no duplication): %+v", len(got), got)
	}

	// Once imported this lifetime, a later touch is a no-op even if a new file
	// appears — the child records through the hub now, not files.
	seedLegacyLedger(t, runsDir, `{"ticket":"COD-2","lesson":"appeared later"}
`)
	if err := l.ImportLegacy("/repo", runsDir); err != nil {
		t.Fatalf("guarded ImportLegacy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runsDir, "memory", "lessons.jsonl")); err != nil {
		t.Fatalf("guarded ImportLegacy touched disk; file should remain: %v", err)
	}
}

func TestLessonImportLegacyMissingLedger(t *testing.T) {
	l := testLessons(t)
	if err := l.ImportLegacy("/repo", filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("ImportLegacy(missing dir) = %v, want nil", err)
	}
	if err := l.ImportLegacy("/repo2", ""); err != nil {
		t.Fatalf("ImportLegacy(empty dir) = %v, want nil", err)
	}
	if got, _ := l.All("/repo"); len(got) != 0 {
		t.Fatalf("missing ledger imported %d records, want 0", len(got))
	}
}
