package hubstore

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb/hubdbtest"
	"github.com/RomkaLTU/trau/internal/registry"
)

// realHubHome stands in for the home of a hub that is not itself throwaway
// (~/.trau on a real machine). The guard reads home only to classify the hub, so
// the path never has to exist.
const realHubHome = "/srv/trau"

func testStore(t *testing.T, home string) *Registrations {
	t.Helper()
	return NewRegistrations(home, testHubDB(t, home))
}

func testHubDB(t *testing.T, home string) *sql.DB {
	t.Helper()
	db, err := hubdbtest.Open(home)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db.SQL()
}

func TestRegisterInOrderAndDedupes(t *testing.T) {
	s := testStore(t, t.TempDir())

	for _, root := range []string{"/repos/a", "/repos/b", "/repos/a"} {
		if err := s.Register(root); err != nil {
			t.Fatalf("Register(%q): %v", root, err)
		}
	}

	got, err := s.Registered()
	if err != nil {
		t.Fatalf("Registered: %v", err)
	}
	if want := []string{"/repos/a", "/repos/b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("registered = %v, want %v", got, want)
	}
}

func TestUnregisterReportsPresenceAndReappends(t *testing.T) {
	s := testStore(t, t.TempDir())
	for _, root := range []string{"/repos/a", "/repos/b", "/repos/c"} {
		if err := s.Register(root); err != nil {
			t.Fatalf("register %s: %v", root, err)
		}
	}

	found, err := s.Unregister("/repos/b")
	if err != nil || !found {
		t.Fatalf("Unregister(/repos/b) = (%v, %v), want (true, nil)", found, err)
	}
	if got, _ := s.Registered(); !reflect.DeepEqual(got, []string{"/repos/a", "/repos/c"}) {
		t.Fatalf("after unregister = %v", got)
	}

	found, err = s.Unregister("/repos/b")
	if err != nil || found {
		t.Fatalf("re-Unregister(/repos/b) = (%v, %v), want (false, nil)", found, err)
	}

	if err := s.Register("/repos/b"); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if got, _ := s.Registered(); !reflect.DeepEqual(got, []string{"/repos/a", "/repos/c", "/repos/b"}) {
		t.Fatalf("after re-register = %v, want b appended", got)
	}
}

func TestRememberAddsNewSortsAndDoesNotOverwrite(t *testing.T) {
	s := testStore(t, t.TempDir())

	if err := s.Remember([]registry.Repo{
		{Name: "beta", Root: "/repo/beta", RunsDir: "/repo/beta/runs"},
		{Name: "alpha", Root: "/repo/alpha", RunsDir: "/repo/alpha/runs"},
		{Root: ""},
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	if err := s.Remember([]registry.Repo{
		{Name: "alpha", Root: "/repo/alpha", RunsDir: "/changed"},
	}); err != nil {
		t.Fatalf("Remember again: %v", err)
	}

	known, err := s.Known()
	if err != nil {
		t.Fatalf("Known: %v", err)
	}
	want := []registry.Repo{
		{Name: "alpha", Root: "/repo/alpha", RunsDir: "/repo/alpha/runs"},
		{Name: "beta", Root: "/repo/beta", RunsDir: "/repo/beta/runs"},
	}
	if !reflect.DeepEqual(known, want) {
		t.Fatalf("known = %v, want %v (sorted by name, no overwrite)", known, want)
	}
}

func TestRememberSkipsScratchpadClonesOnARealHub(t *testing.T) {
	s := NewRegistrations(realHubHome, testHubDB(t, t.TempDir()))
	clone := t.TempDir()

	if err := s.Remember([]registry.Repo{
		{Name: "acme", Root: "/repos/acme", RunsDir: "/repos/acme/runs"},
		{Name: "acme", Root: clone, RunsDir: filepath.Join(clone, "runs")},
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	known, err := s.Known()
	if err != nil {
		t.Fatalf("Known: %v", err)
	}
	want := []registry.Repo{{Name: "acme", Root: "/repos/acme", RunsDir: "/repos/acme/runs"}}
	if !reflect.DeepEqual(known, want) {
		t.Fatalf("known = %v, want %v — a temp-dir clone must not be adopted", known, want)
	}
}

// TestRememberKeepsTempRootsOnAThrowawayHub pins the carve-out the isolated QA
// hub of AGENTS.md relies on: a hub whose own home is throwaway dies with its
// clones, so it still tracks them.
func TestRememberKeepsTempRootsOnAThrowawayHub(t *testing.T) {
	s := testStore(t, t.TempDir())
	clone := t.TempDir()

	if err := s.Remember([]registry.Repo{{Name: "acme", Root: clone, RunsDir: filepath.Join(clone, "runs")}}); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	known, err := s.Known()
	if err != nil {
		t.Fatalf("Known: %v", err)
	}
	if len(known) != 1 || known[0].Root != clone {
		t.Fatalf("known = %v, want the clone %s", known, clone)
	}
}

func TestUnderTempDirCoversBothTempTrees(t *testing.T) {
	cases := map[string]bool{
		filepath.Join(t.TempDir(), "clone"):  true,
		filepath.Join(os.TempDir(), "clone"): true,
		// macOS hands each user a TMPDIR under /var/folders, so a scratchpad clone
		// under /tmp is one os.TempDir() alone would miss.
		"/tmp/claude-501/scratchpad/repos/shipflock": true,
		"/repos/api":     false,
		"/tmpfiles/repo": false,
		"":               false,
	}
	for path, want := range cases {
		if got := underTempDir(path); got != want {
			t.Errorf("underTempDir(%q) = %v, want %v", path, got, want)
		}
	}
	// A root is stored as the loop resolved it, and macOS resolves /tmp to
	// /private/tmp.
	resolved, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Skipf("no /tmp to resolve: %v", err)
	}
	if !underTempDir(filepath.Join(resolved, "scratchpad", "repos", "shipflock")) {
		t.Errorf("underTempDir under resolved %s = false, want true", resolved)
	}
}

func TestPruneStaleDropsClonesAndVanishedRootsOnly(t *testing.T) {
	db := testHubDB(t, t.TempDir())
	// The package directory is the one real, non-temp root a test can point at.
	real, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	clone, registeredClone, vanished := t.TempDir(), t.TempDir(), "/repos/vanished"
	// A throwaway hub adopts every root, which is how the rows a real hub must now
	// prune reached the store on the builds before the guard.
	seed := NewRegistrations(t.TempDir(), db)
	for _, root := range []string{real, clone, registeredClone, vanished} {
		if err := seed.Remember([]registry.Repo{{Name: filepath.Base(root), Root: root, RunsDir: filepath.Join(root, "runs")}}); err != nil {
			t.Fatalf("seed %s: %v", root, err)
		}
	}
	if err := seed.Register(registeredClone); err != nil {
		t.Fatalf("register: %v", err)
	}

	s := NewRegistrations(realHubHome, db)
	pruned, err := s.PruneStale()
	if err != nil {
		t.Fatalf("PruneStale: %v", err)
	}
	sort.Strings(pruned)
	if want := sortedRoots(clone, vanished); !reflect.DeepEqual(pruned, want) {
		t.Fatalf("pruned = %v, want %v", pruned, want)
	}
	known, err := s.Known()
	if err != nil {
		t.Fatalf("Known: %v", err)
	}
	var kept []string
	for _, repo := range known {
		kept = append(kept, repo.Root)
	}
	sort.Strings(kept)
	if want := sortedRoots(real, registeredClone); !reflect.DeepEqual(kept, want) {
		t.Fatalf("known after prune = %v, want %v — a real repo and a registered one must survive", kept, want)
	}
}

func sortedRoots(roots ...string) []string {
	sort.Strings(roots)
	return roots
}

func TestForgetClearsBothSetsAndLeavesOthers(t *testing.T) {
	s := testStore(t, t.TempDir())
	if err := s.Remember([]registry.Repo{
		{Name: "gone", Root: "/repo/gone", RunsDir: "/repo/gone/runs"},
		{Name: "kept", Root: "/repo/kept", RunsDir: "/repo/kept/runs"},
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	for _, root := range []string{"/repo/gone", "/repo/kept"} {
		if err := s.Register(root); err != nil {
			t.Fatalf("register %s: %v", root, err)
		}
	}

	if removed, err := s.Forget("/repo/gone"); err != nil || !removed {
		t.Fatalf("Forget = (%v, %v), want (true, nil)", removed, err)
	}

	want := []registry.Repo{{Name: "kept", Root: "/repo/kept", RunsDir: "/repo/kept/runs"}}
	if known, _ := s.Known(); !reflect.DeepEqual(known, want) {
		t.Fatalf("known = %v, want %v", known, want)
	}
	if got, _ := s.Registered(); !reflect.DeepEqual(got, []string{"/repo/kept"}) {
		t.Fatalf("registered = %v, want only the kept repo", got)
	}
	if removed, err := s.Forget("/repo/gone"); err != nil || removed {
		t.Fatalf("re-Forget = (%v, %v), want (false, nil) for a root with no rows", removed, err)
	}
}

// TestForgetClearsEverySpellingOfARoot covers the store a removal used to give up
// on: rows written under whatever spelling the loop resolved — `git rev-parse`
// answers with forward slashes on Windows — where a byte-equal DELETE matched
// nothing and the repo stayed listed forever.
func TestForgetClearsEverySpellingOfARoot(t *testing.T) {
	s := testStore(t, t.TempDir())
	clean := filepath.Join(t.TempDir(), "gone")
	spellings := []string{clean, clean + string(filepath.Separator), filepath.ToSlash(clean)}
	for _, root := range spellings {
		if _, err := s.db.Exec(
			`INSERT INTO known_repos(root, name, runs_dir) VALUES(?, ?, ?) ON CONFLICT(root) DO NOTHING`,
			root, "gone", filepath.Join(root, ".trau", "runs"),
		); err != nil {
			t.Fatalf("seed known %q: %v", root, err)
		}
		if err := s.Register(root); err != nil {
			t.Fatalf("register %q: %v", root, err)
		}
	}

	if removed, err := s.Forget(filepath.ToSlash(clean)); err != nil || !removed {
		t.Fatalf("Forget = (%v, %v), want (true, nil)", removed, err)
	}

	if known, _ := s.Known(); len(known) != 0 {
		t.Errorf("known = %v, want every spelling gone", known)
	}
	if got, _ := s.Registered(); len(got) != 0 {
		t.Errorf("registered = %v, want every spelling gone", got)
	}
}

// TestRememberSkipsACanonicalTwin keeps the sweep and the post-unregister remember
// from adding a second row for a directory already known under another spelling.
func TestRememberSkipsACanonicalTwin(t *testing.T) {
	s := testStore(t, t.TempDir())
	root := filepath.Join(t.TempDir(), "acme")
	stored := root + string(filepath.Separator)
	if err := s.Remember([]registry.Repo{{Name: "acme", Root: stored, RunsDir: filepath.Join(stored, ".trau", "runs")}}); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	if err := s.Remember([]registry.Repo{{Name: "acme", Root: root, RunsDir: filepath.Join(root, ".trau", "runs")}}); err != nil {
		t.Fatalf("Remember canonical twin: %v", err)
	}

	known, err := s.Known()
	if err != nil {
		t.Fatalf("Known: %v", err)
	}
	if len(known) != 1 || known[0].Root != stored {
		t.Fatalf("known = %v, want only the root as first stored", known)
	}
}

func TestImportLegacyBackfillsAndDeletesFiles(t *testing.T) {
	home := t.TempDir()
	writeLegacyRepos(t, home, map[string]registry.Repo{
		"/repo/one": {Name: "one", Root: "/repo/one", RunsDir: "/repo/one/runs"},
	})
	writeLegacyWorkspace(t, home, []string{"/repo/one", "/repo/two"})

	s := testStore(t, home)
	if err := s.ImportLegacy(home); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}

	if known, _ := s.Known(); len(known) != 1 || known[0].Root != "/repo/one" {
		t.Fatalf("known after import = %v, want one", known)
	}
	if got, _ := s.Registered(); !reflect.DeepEqual(got, []string{"/repo/one", "/repo/two"}) {
		t.Fatalf("registered after import = %v", got)
	}
	if files := LegacyFiles(home); len(files) != 0 {
		t.Fatalf("legacy files still present after import: %v", files)
	}
}

func TestImportLegacyIsIdempotent(t *testing.T) {
	home := t.TempDir()
	writeLegacyWorkspace(t, home, []string{"/repo/one"})

	s := testStore(t, home)
	if err := s.ImportLegacy(home); err != nil {
		t.Fatalf("first import: %v", err)
	}
	writeLegacyWorkspace(t, home, []string{"/repo/one", "/repo/two"})
	if err := s.ImportLegacy(home); err != nil {
		t.Fatalf("second import: %v", err)
	}
	if got, _ := s.Registered(); !reflect.DeepEqual(got, []string{"/repo/one", "/repo/two"}) {
		t.Fatalf("registered after re-import = %v, want deduped union", got)
	}
}

func TestImportLegacyAbortsAndLeavesFileOnMalformedJSON(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "workspace.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write malformed: %v", err)
	}

	s := testStore(t, home)
	err := s.ImportLegacy(home)
	if err == nil {
		t.Fatal("ImportLegacy = nil, want error on malformed file")
	}
	if !strings.Contains(err.Error(), "workspace.json") {
		t.Errorf("error %q does not name the offending file", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("malformed file was removed despite failed import: %v", statErr)
	}
}

func TestImportLegacyFreshInstallCreatesNoFiles(t *testing.T) {
	home := t.TempDir()
	s := testStore(t, home)
	if err := s.ImportLegacy(home); err != nil {
		t.Fatalf("ImportLegacy on fresh install: %v", err)
	}
	if files := LegacyFiles(home); len(files) != 0 {
		t.Fatalf("fresh install created legacy files: %v", files)
	}
}

func TestLegacyFilesReportsPresent(t *testing.T) {
	home := t.TempDir()
	if files := LegacyFiles(home); len(files) != 0 {
		t.Fatalf("fresh home reports %v", files)
	}
	writeLegacyWorkspace(t, home, []string{"/repo/one"})
	files := LegacyFiles(home)
	if len(files) != 1 || filepath.Base(files[0]) != "workspace.json" {
		t.Fatalf("LegacyFiles = %v, want workspace.json", files)
	}
}

func writeLegacyRepos(t *testing.T, home string, repos map[string]registry.Repo) {
	t.Helper()
	data, err := json.Marshal(repos)
	if err != nil {
		t.Fatalf("marshal repos: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "repos.json"), data, 0o644); err != nil {
		t.Fatalf("write repos.json: %v", err)
	}
}

func writeLegacyWorkspace(t *testing.T, home string, roots []string) {
	t.Helper()
	data, err := json.Marshal(struct {
		Repos []string `json:"repos"`
	}{Repos: roots})
	if err != nil {
		t.Fatalf("marshal workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "workspace.json"), data, 0o644); err != nil {
		t.Fatalf("write workspace.json: %v", err)
	}
}
