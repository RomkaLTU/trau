package hubstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func testArtifacts(t *testing.T) *Artifacts {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStores(db.SQL(), nil, 0).Artifacts()
}

func TestArtifactUpsertOneAll(t *testing.T) {
	a := testArtifacts(t)
	if err := a.Upsert("/repo", "COD-1", ArtifactHandoff, "brief body"); err != nil {
		t.Fatalf("Upsert handoff: %v", err)
	}
	if err := a.Upsert("/repo", "COD-1", ArtifactVerdict, `{"pass":true}`); err != nil {
		t.Fatalf("Upsert verdict: %v", err)
	}
	// A different repo/ticket must not bleed into COD-1's set.
	_ = a.Upsert("/other", "COD-1", ArtifactHandoff, "elsewhere")

	content, ok, err := a.One("/repo", "COD-1", ArtifactHandoff)
	if err != nil || !ok || content != "brief body" {
		t.Fatalf("One(handoff) = %q ok=%v err=%v", content, ok, err)
	}
	if _, ok, _ := a.One("/repo", "COD-1", ArtifactRubric); ok {
		t.Fatalf("One(rubric) reported present with no row")
	}

	all, err := a.All("/repo", "COD-1")
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 2 || all[ArtifactHandoff] != "brief body" || all[ArtifactVerdict] != `{"pass":true}` {
		t.Fatalf("All = %+v", all)
	}
}

func TestArtifactUpsertReplacesInPlace(t *testing.T) {
	a := testArtifacts(t)
	_ = a.Upsert("/repo", "COD-1", ArtifactRubric, "v1")
	if err := a.Upsert("/repo", "COD-1", ArtifactRubric, "v2"); err != nil {
		t.Fatalf("Upsert replace: %v", err)
	}
	content, _, _ := a.One("/repo", "COD-1", ArtifactRubric)
	if content != "v2" {
		t.Fatalf("content = %q, want v2 (rewrite replaces in place)", content)
	}
}

func TestArtifactEmptyContentIsPresent(t *testing.T) {
	a := testArtifacts(t)
	if err := a.Upsert("/repo", "COD-1", ArtifactHandoff, ""); err != nil {
		t.Fatalf("Upsert empty: %v", err)
	}
	content, ok, err := a.One("/repo", "COD-1", ArtifactHandoff)
	if err != nil || !ok || content != "" {
		t.Fatalf("One(empty) = %q ok=%v err=%v; want present-but-empty", content, ok, err)
	}
}

func TestArtifactRemoveDropsWholeTicket(t *testing.T) {
	a := testArtifacts(t)
	_ = a.Upsert("/repo", "COD-1", ArtifactHandoff, "b")
	_ = a.Upsert("/repo", "COD-1", ArtifactRubric, "r")
	_ = a.Upsert("/repo", "COD-2", ArtifactHandoff, "keep")

	if err := a.Remove("/repo", "COD-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	all, _ := a.All("/repo", "COD-1")
	if len(all) != 0 {
		t.Fatalf("COD-1 still has %d artifacts after Remove", len(all))
	}
	if _, ok, _ := a.One("/repo", "COD-2", ArtifactHandoff); !ok {
		t.Fatalf("Remove dropped a sibling ticket's artifact")
	}
	if err := a.Remove("/repo", "COD-1"); err != nil {
		t.Fatalf("Remove(absent) = %v, want nil", err)
	}
}

func TestValidArtifactKind(t *testing.T) {
	for _, k := range []string{ArtifactHandoff, ArtifactRubric, ArtifactVerdict, ArtifactBuildNotes} {
		if !ValidArtifactKind(k) {
			t.Errorf("ValidArtifactKind(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"", "handoff.md", "unknown", "HANDOFF"} {
		if ValidArtifactKind(k) {
			t.Errorf("ValidArtifactKind(%q) = true, want false", k)
		}
	}
}

func seedLegacyArtifacts(t *testing.T, runsDir, ticket string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(runsDir, ticket)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func TestArtifactImportLegacyFoldsFilesAndRemovesThem(t *testing.T) {
	a := testArtifacts(t)
	runsDir := t.TempDir()
	root := "/repo"
	seedLegacyArtifacts(t, runsDir, "COD-1", map[string]string{
		"handoff.md":    "the brief",
		"rubric.json":   `{"ticket":"COD-1"}`,
		"verdict.json":  `{"pass":false}`,
		"buildnotes.md": "notes",
	})

	if err := a.ImportLegacy(root, runsDir); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	all, _ := a.All(root, "COD-1")
	if all[ArtifactHandoff] != "the brief" || all[ArtifactRubric] != `{"ticket":"COD-1"}` ||
		all[ArtifactVerdict] != `{"pass":false}` || all[ArtifactBuildNotes] != "notes" {
		t.Fatalf("imported artifacts = %+v", all)
	}
	for _, name := range []string{"handoff.md", "rubric.json", "verdict.json", "buildnotes.md"} {
		if _, err := os.Stat(filepath.Join(runsDir, "COD-1", name)); !os.IsNotExist(err) {
			t.Fatalf("legacy file %s survived import (err=%v)", name, err)
		}
	}
}

func TestArtifactImportLegacyIsIdempotentAndGuarded(t *testing.T) {
	a := testArtifacts(t)
	runsDir := t.TempDir()
	root := "/repo"
	seedLegacyArtifacts(t, runsDir, "COD-1", map[string]string{"handoff.md": "the brief"})

	if err := a.ImportLegacy(root, runsDir); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}

	// A re-import after an interrupt re-seeded the same file must land exactly one
	// row and never clobber, verified through a fresh guard.
	seedLegacyArtifacts(t, runsDir, "COD-1", map[string]string{"handoff.md": "the brief"})
	fresh := NewArtifacts(a.db)
	if err := fresh.ImportLegacy(root, runsDir); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	all, _ := fresh.All(root, "COD-1")
	if len(all) != 1 || all[ArtifactHandoff] != "the brief" {
		t.Fatalf("re-import produced %+v, want a single handoff row", all)
	}

	// Once imported this lifetime, a later touch is a no-op even if a new file
	// appears — the child writes through the hub now, not files.
	seedLegacyArtifacts(t, runsDir, "COD-1", map[string]string{"handoff.md": "the brief"})
	if err := a.ImportLegacy(root, runsDir); err != nil {
		t.Fatalf("guarded ImportLegacy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runsDir, "COD-1", "handoff.md")); err != nil {
		t.Fatalf("guarded ImportLegacy touched disk; file should remain: %v", err)
	}
}

func TestArtifactImportLegacyDoesNotClobberHubRow(t *testing.T) {
	a := testArtifacts(t)
	runsDir := t.TempDir()
	root := "/repo"
	if err := a.Upsert(root, "COD-1", ArtifactHandoff, "fresh from hub"); err != nil {
		t.Fatalf("seed hub row: %v", err)
	}
	seedLegacyArtifacts(t, runsDir, "COD-1", map[string]string{"handoff.md": "stale file"})

	if err := a.ImportLegacy(root, runsDir); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	content, _, _ := a.One(root, "COD-1", ArtifactHandoff)
	if content != "fresh from hub" {
		t.Fatalf("import clobbered a hub-held artifact: %q", content)
	}
	if _, err := os.Stat(filepath.Join(runsDir, "COD-1", "handoff.md")); !os.IsNotExist(err) {
		t.Fatalf("superseded legacy file was not removed (err=%v)", err)
	}
}

func TestArtifactImportLegacyMissingRunsDir(t *testing.T) {
	a := testArtifacts(t)
	if err := a.ImportLegacy("/repo", filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("ImportLegacy(missing dir) = %v, want nil", err)
	}
	if err := a.ImportLegacy("/repo2", ""); err != nil {
		t.Fatalf("ImportLegacy(empty dir) = %v, want nil", err)
	}
}
