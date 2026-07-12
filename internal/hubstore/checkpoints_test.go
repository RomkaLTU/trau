package hubstore

import (
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/state"
)

func testCheckpoints(t *testing.T) *Checkpoints {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStores(db.SQL()).Checkpoints()
}

func TestCheckpointUpsertProjectsColumns(t *testing.T) {
	c := testCheckpoints(t)
	data := map[string]string{"PHASE": "built", "TITLE": "Do it", "BRANCH": "feature/x", "UPDATED": "2026-07-12 10:00:00"}
	if err := c.Upsert("/repo", "COD-1", data); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	row, ok, err := c.One("/repo", "COD-1")
	if err != nil || !ok {
		t.Fatalf("One ok=%v err=%v", ok, err)
	}
	if row.Phase != "built" || row.Title != "Do it" || row.Branch != "feature/x" || row.UpdatedAt != "2026-07-12 10:00:00" {
		t.Fatalf("projected columns = %+v", row)
	}
	if row.Data != `{"BRANCH":"feature/x","PHASE":"built","TITLE":"Do it","UPDATED":"2026-07-12 10:00:00"}` {
		t.Fatalf("data blob = %q", row.Data)
	}
	if got := c.Phase("/repo", "COD-1"); got != "built" {
		t.Fatalf("Phase = %q, want built", got)
	}

	if err := c.Upsert("/repo", "COD-1", map[string]string{"PHASE": "merged"}); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	if got := c.Phase("/repo", "COD-1"); got != "merged" {
		t.Fatalf("Phase after update = %q, want merged", got)
	}
}

func TestCheckpointAbsentAndRemove(t *testing.T) {
	c := testCheckpoints(t)
	if _, ok, err := c.One("/repo", "nope"); ok || err != nil {
		t.Fatalf("One(absent) ok=%v err=%v; want false, nil", ok, err)
	}
	if got := c.Phase("/repo", "nope"); got != "" {
		t.Fatalf("Phase(absent) = %q, want empty", got)
	}
	if err := c.Upsert("/repo", "COD-1", map[string]string{"PHASE": "built"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := c.Remove("/repo", "COD-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok, _ := c.One("/repo", "COD-1"); ok {
		t.Fatalf("checkpoint still present after Remove")
	}
	if err := c.Remove("/repo", "COD-1"); err != nil {
		t.Fatalf("Remove(absent) = %v, want nil", err)
	}
}

func TestCheckpointAllOrderedByTicket(t *testing.T) {
	c := testCheckpoints(t)
	for _, id := range []string{"COD-2", "COD-1"} {
		if err := c.Upsert("/repo", id, map[string]string{"PHASE": "built"}); err != nil {
			t.Fatalf("Upsert %s: %v", id, err)
		}
	}
	_ = c.Upsert("/other", "COD-9", map[string]string{"PHASE": "built"})
	rows, err := c.All("/repo")
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(rows) != 2 || rows[0].Ticket != "COD-1" || rows[1].Ticket != "COD-2" {
		t.Fatalf("All = %+v, want COD-1 then COD-2 (repo-scoped)", rows)
	}
}

func TestCheckpointImportLegacyIsIdempotent(t *testing.T) {
	c := testCheckpoints(t)
	runsDir := t.TempDir()
	root := "/repo"

	seed := func() {
		fs := state.NewStore(runsDir)
		if err := fs.Set("COD-1", "PHASE", state.Verified); err != nil {
			t.Fatalf("seed state: %v", err)
		}
		if err := fs.Set("COD-1", "BRANCH", "feature/COD-1"); err != nil {
			t.Fatalf("seed state: %v", err)
		}
	}
	seed()

	if err := c.ImportLegacy(root, runsDir); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	row, ok, _ := c.One(root, "COD-1")
	if !ok || row.Phase != state.Verified || row.Branch != "feature/COD-1" {
		t.Fatalf("imported row = %+v, ok=%v", row, ok)
	}
	if _, _, _, exists := state.NewStore(runsDir).Load("COD-1"); exists {
		t.Fatalf("legacy state file was not removed after import")
	}

	// A re-import after an interrupt that left the file behind must not duplicate
	// or clobber: a fresh store (fresh guard) re-seeded with the same file lands
	// exactly one row.
	seed()
	fresh := NewCheckpoints(c.db)
	if err := fresh.ImportLegacy(root, runsDir); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	rows, err := fresh.All(root)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(rows) != 1 || rows[0].Ticket != "COD-1" {
		t.Fatalf("re-import produced %d rows, want 1", len(rows))
	}

	// Once a repo is imported this lifetime, a later touch is a no-op even if a
	// new file appears — the child writes through the hub, not files.
	seed()
	if err := c.ImportLegacy(root, runsDir); err != nil {
		t.Fatalf("guarded ImportLegacy: %v", err)
	}
	if _, _, _, exists := state.NewStore(runsDir).Load("COD-1"); !exists {
		t.Fatalf("guarded ImportLegacy touched disk; file should remain")
	}
}

func TestCheckpointImportLegacyDoesNotClobberProgressed(t *testing.T) {
	c := testCheckpoints(t)
	runsDir := t.TempDir()
	root := "/repo"

	fs := state.NewStore(runsDir)
	if err := fs.Set("COD-1", "PHASE", state.Verified); err != nil {
		t.Fatalf("seed stale state: %v", err)
	}

	if err := c.Upsert(root, "COD-1", map[string]string{"PHASE": state.Merged}); err != nil {
		t.Fatalf("progress hub row: %v", err)
	}

	if err := c.ImportLegacy(root, runsDir); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	if got := c.Phase(root, "COD-1"); got != state.Merged {
		t.Fatalf("import clobbered a progressed checkpoint: phase = %q, want %q", got, state.Merged)
	}
	if _, _, _, exists := state.NewStore(runsDir).Load("COD-1"); exists {
		t.Fatalf("superseded legacy state file was not removed")
	}
}
