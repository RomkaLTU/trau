package hubstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/registry"
)

func testStore(t *testing.T, home string) *Registrations {
	t.Helper()
	db, err := hubdb.Open(home)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewRegistrations(db.SQL())
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
