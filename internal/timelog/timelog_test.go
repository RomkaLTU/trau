package timelog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHeuristicMinutesDeterministicLadder(t *testing.T) {
	cases := []struct {
		name    string
		stats   DiffStats
		commits int
		want    int
	}{
		{"one-line fix", DiffStats{Files: 1, Additions: 1, Deletions: 0}, 1, 20},
		{"empty diff floors non-zero", DiffStats{}, 0, 20},
		{"small single-file bug fix", DiffStats{Files: 1, Additions: 40, Deletions: 5}, 1, 45},
		{"bug fix with tests, few files", DiffStats{Files: 3, Additions: 90, Deletions: 20}, 2, 90},
		{"small feature", DiffStats{Files: 6, Additions: 250, Deletions: 40}, 3, 180},
		{"refactor across many files", DiffStats{Files: 18, Additions: 500, Deletions: 120}, 3, 300},
		{"feature spanning layers", DiffStats{Files: 30, Additions: 1200, Deletions: 200}, 3, 480},
		{"architectural change", DiffStats{Files: 60, Additions: 3000, Deletions: 800}, 3, 720},
		{"commit nudge bumps mid tier", DiffStats{Files: 6, Additions: 250, Deletions: 40}, 5, 210},
		{"heavy commit nudge", DiffStats{Files: 6, Additions: 250, Deletions: 40}, 9, 240},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := HeuristicMinutes(c.stats, c.commits)
			if got != c.want {
				t.Fatalf("HeuristicMinutes(%+v, %d) = %d, want %d", c.stats, c.commits, got, c.want)
			}
			// Determinism: a second call yields the same number.
			if again := HeuristicMinutes(c.stats, c.commits); again != got {
				t.Fatalf("non-deterministic: %d then %d", got, again)
			}
			if got <= 0 {
				t.Fatalf("estimate must be non-zero, got %d", got)
			}
		})
	}
}

func TestRecordIdempotentSameCommits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "COD-1.json")
	meta := Log{TicketID: "COD-1", TicketTitle: "Add a thing", Branch: "feature/COD-1-add"}
	entry := Entry{
		Date:      "2026-06-28",
		Minutes:   90,
		Summary:   "Add a thing",
		DiffStats: DiffStats{Files: 3, Additions: 90, Deletions: 20},
		Commits:   []string{"a1b2c3d", "e4f5g6h"},
	}

	if err := Record(path, meta, entry, "S", "C1"); err != nil {
		t.Fatalf("first Record: %v", err)
	}
	// Re-run after merge: same commits (even reordered) must not duplicate.
	reordered := entry
	reordered.Commits = []string{"e4f5g6h", "a1b2c3d"}
	if err := Record(path, meta, reordered, "S2", "C2"); err != nil {
		t.Fatalf("second Record: %v", err)
	}

	got := mustRead(t, path)
	if len(got.Entries) != 1 {
		t.Fatalf("expected 1 entry after idempotent re-run, got %d", len(got.Entries))
	}
	if got.TotalMinutes != 90 {
		t.Fatalf("totalMinutes = %d, want 90", got.TotalMinutes)
	}
	if got.Started != "S" {
		t.Fatalf("started should be first-write-wins, got %q", got.Started)
	}
	if got.Completed != "C2" {
		t.Fatalf("completed should refresh, got %q", got.Completed)
	}
}

func TestRecordAppendsDistinctWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "COD-2.json")
	meta := Log{TicketID: "COD-2", TicketTitle: "Two days", Branch: "feature/COD-2"}
	day1 := Entry{Date: "2026-06-27", Minutes: 45, Commits: []string{"aaa"}}
	day2 := Entry{Date: "2026-06-28", Minutes: 90, Commits: []string{"bbb"}}

	if err := Record(path, meta, day1, "S", "C1"); err != nil {
		t.Fatalf("Record day1: %v", err)
	}
	if err := Record(path, meta, day2, "S", "C2"); err != nil {
		t.Fatalf("Record day2: %v", err)
	}

	got := mustRead(t, path)
	if len(got.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got.Entries))
	}
	if got.TotalMinutes != 135 {
		t.Fatalf("totalMinutes = %d, want 135", got.TotalMinutes)
	}
}

// TestRecordSchemaRoundTrip guards the on-disk contract: the persisted JSON must
// carry exactly the keys downstream collectors read, in the documented shape.
func TestRecordSchemaRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "COD-3.json")
	meta := Log{TicketID: "COD-3", TicketTitle: "Schema check", Branch: "feature/COD-3"}
	entry := Entry{
		Date:      "2026-06-28",
		Minutes:   135,
		Summary:   "What was built.",
		DiffStats: DiffStats{Files: 8, Additions: 234, Deletions: 12},
		Commits:   []string{"a1b2c3d"},
	}
	if err := Record(path, meta, entry, "2026-06-28T09:00:00Z", "2026-06-28T11:15:00Z"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"ticketId", "ticketTitle", "branch", "started", "completed", "entries", "totalMinutes"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("missing top-level key %q in %s", k, raw)
		}
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(m["entries"], &entries); err != nil || len(entries) != 1 {
		t.Fatalf("entries shape wrong: %v (%s)", err, m["entries"])
	}
	for _, k := range []string{"date", "minutes", "summary", "diffStats", "commits"} {
		if _, ok := entries[0][k]; !ok {
			t.Fatalf("missing entry key %q", k)
		}
	}
	var ds map[string]json.RawMessage
	if err := json.Unmarshal(entries[0]["diffStats"], &ds); err != nil {
		t.Fatalf("diffStats: %v", err)
	}
	for _, k := range []string{"files", "additions", "deletions"} {
		if _, ok := ds[k]; !ok {
			t.Fatalf("missing diffStats key %q", k)
		}
	}
}

func TestPathStorageModes(t *testing.T) {
	if got := Path(StorageNone, "/repo", "COD-1"); got != "" {
		t.Fatalf("none storage should be empty, got %q", got)
	}
	if got := Path(StorageRepo, "", "COD-1"); got != "" {
		t.Fatalf("repo storage with empty root should be empty, got %q", got)
	}
	if got := Path(StorageRepo, "/repo", " "); got != "" {
		t.Fatalf("blank ticket should be empty, got %q", got)
	}
	if got := Path(StorageRepo, "/repo/app", "COD-1"); got != "/repo/app/.trau/time/COD-1.json" {
		t.Fatalf("repo path = %q", got)
	}

	t.Setenv("HOME", "/home/dev")
	got := Path(StorageUser, "/work/salonradar", "COD-9")
	want := "/home/dev/.trau/time/salonradar/COD-9.json"
	if got != want {
		t.Fatalf("user path = %q, want %q", got, want)
	}
}

// TestMigrateLegacy guards the .dev-flow/time -> .trau/time rename: legacy logs
// move to the new location so Record keeps appending to them, a log already
// written at the new location is never clobbered (its legacy counterpart stays
// put), and emptied legacy directories are pruned.
func TestMigrateLegacy(t *testing.T) {
	repo := t.TempDir()
	legacy := filepath.Join(repo, ".dev-flow", "time")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "COD-1.json"), []byte(`{"ticketId":"COD-1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "COD-2.json"), []byte(`{"ticketId":"legacy"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	newDir := filepath.Join(repo, ".trau", "time")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "COD-2.json"), []byte(`{"ticketId":"new"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacy(StorageRepo, repo); err != nil {
		t.Fatalf("MigrateLegacy: %v", err)
	}

	if got := mustReadFile(t, filepath.Join(newDir, "COD-1.json")); got != `{"ticketId":"COD-1"}` {
		t.Fatalf("COD-1 not migrated, got %q", got)
	}
	if got := mustReadFile(t, filepath.Join(newDir, "COD-2.json")); got != `{"ticketId":"new"}` {
		t.Fatalf("existing new-location log clobbered, got %q", got)
	}
	if got := mustReadFile(t, filepath.Join(legacy, "COD-2.json")); got != `{"ticketId":"legacy"}` {
		t.Fatalf("conflicting legacy log should stay put, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(legacy, "COD-1.json")); !os.IsNotExist(err) {
		t.Fatalf("migrated legacy file should be gone: %v", err)
	}

	// Second run with the conflict resolved: the dir empties out and is pruned.
	if err := os.Remove(filepath.Join(legacy, "COD-2.json")); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacy(StorageRepo, repo); err != nil {
		t.Fatalf("second MigrateLegacy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".dev-flow")); !os.IsNotExist(err) {
		t.Fatalf("emptied .dev-flow should be pruned: %v", err)
	}
	// No legacy dir at all is a no-op, not an error.
	if err := MigrateLegacy(StorageRepo, repo); err != nil {
		t.Fatalf("MigrateLegacy without legacy dir: %v", err)
	}
}

func TestEnsureGitignore(t *testing.T) {
	dir := t.TempDir()
	gi := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gi, []byte("node_modules\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := EnsureGitignore(dir); err != nil {
		t.Fatalf("EnsureGitignore: %v", err)
	}
	content := mustReadFile(t, gi)
	if !strings.Contains(content, ".trau/time/") {
		t.Fatalf("expected .trau/time/ ignored, got:\n%s", content)
	}
	if strings.Contains(content, "\n.trau\n") || content == ".trau\n" {
		t.Fatalf("must not blanket-ignore .trau/, got:\n%s", content)
	}

	// Idempotent: a second call adds nothing.
	if err := EnsureGitignore(dir); err != nil {
		t.Fatalf("second EnsureGitignore: %v", err)
	}
	if again := mustReadFile(t, gi); again != content {
		t.Fatalf("EnsureGitignore not idempotent:\n%s\n---\n%s", content, again)
	}
}

func TestEnsureGitignoreRespectsExistingCoverage(t *testing.T) {
	dir := t.TempDir()
	gi := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gi, []byte(".trau/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGitignore(dir); err != nil {
		t.Fatalf("EnsureGitignore: %v", err)
	}
	if content := mustReadFile(t, gi); content != ".trau/\n" {
		t.Fatalf("should leave existing .trau/ coverage untouched, got:\n%s", content)
	}
}

// A legacy .dev-flow entry no longer covers the new location: EnsureGitignore
// must still add .trau/time/.
func TestEnsureGitignoreLegacyEntryDoesNotCover(t *testing.T) {
	dir := t.TempDir()
	gi := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gi, []byte(".dev-flow/time/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGitignore(dir); err != nil {
		t.Fatalf("EnsureGitignore: %v", err)
	}
	if content := mustReadFile(t, gi); !strings.Contains(content, ".trau/time/") {
		t.Fatalf("expected .trau/time/ added alongside legacy entry, got:\n%s", content)
	}
}

func mustRead(t *testing.T, path string) Log {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var l Log
	if err := json.Unmarshal(data, &l); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return l
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
