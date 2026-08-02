package hubstore

import (
	"errors"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb/hubdbtest"
)

func testAppURLs(t *testing.T) *AppURLs {
	t.Helper()
	db, err := hubdbtest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewAppURLs(db.SQL())
}

func TestAppURLCRUD(t *testing.T) {
	a := testAppURLs(t)
	const repo = "/repos/acme"

	created, err := a.Create(repo, AppURLInput{Label: "storefront", URL: "http://localhost:3000"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == 0 || created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Fatalf("create returned unstamped entry: %+v", created)
	}
	if created.Workspace != "" {
		t.Errorf("workspace = %q, want blank — a workspaceless entry is the repo default", created.Workspace)
	}

	got, found, err := a.Get(repo, created.ID)
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got.URL != "http://localhost:3000" || got.Label != "storefront" {
		t.Errorf("get returned %+v", got)
	}

	updated, err := a.Update(repo, created.ID, AppURLInput{Label: "storefront", URL: "http://localhost:4000", Workspace: "web"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.URL != "http://localhost:4000" || updated.Workspace != "web" {
		t.Errorf("update returned %+v", updated)
	}

	deleted, err := a.Delete(repo, created.ID)
	if err != nil || !deleted {
		t.Fatalf("delete: deleted=%v err=%v", deleted, err)
	}
	if _, found, _ := a.Get(repo, created.ID); found {
		t.Error("entry still present after delete")
	}
}

func TestAppURLUpdateMissing(t *testing.T) {
	a := testAppURLs(t)
	in := AppURLInput{URL: "http://localhost:3000"}
	if _, err := a.Update("/repos/acme", 999, in); !errors.Is(err, ErrAppURLNotFound) {
		t.Fatalf("update missing err = %v, want ErrAppURLNotFound", err)
	}
}

// TestAppURLRequiresURL pins the one validated field: an entry with no URL gives
// browser verify nothing to drive, so neither write accepts it.
func TestAppURLRequiresURL(t *testing.T) {
	a := testAppURLs(t)
	const repo = "/repos/acme"

	if _, err := a.Create(repo, AppURLInput{Label: "blank"}); !errors.Is(err, ErrAppURLRequired) {
		t.Fatalf("create without a url err = %v, want ErrAppURLRequired", err)
	}
	if _, err := a.Create(repo, AppURLInput{URL: "   "}); !errors.Is(err, ErrAppURLRequired) {
		t.Fatalf("create with a whitespace-only url err = %v, want ErrAppURLRequired", err)
	}
	created, err := a.Create(repo, AppURLInput{URL: "http://localhost:3000"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := a.Update(repo, created.ID, AppURLInput{Label: "blanked"}); !errors.Is(err, ErrAppURLRequired) {
		t.Fatalf("update without a url err = %v, want ErrAppURLRequired", err)
	}
}

// TestAppURLWorkspaceUniqueness covers both constraints the table enforces
// through (repo, workspace): one entry per workspace, and one default per repo.
// Either write surfaces a violation as ErrAppURLWorkspaceTaken rather than the
// driver's text, since a caller that pre-checked can still lose the race.
// Another repo's entries are unaffected.
func TestAppURLWorkspaceUniqueness(t *testing.T) {
	a := testAppURLs(t)
	const repo = "/repos/acme"

	if _, err := a.Create(repo, AppURLInput{URL: "http://localhost:3000"}); err != nil {
		t.Fatalf("create default: %v", err)
	}
	if _, err := a.Create(repo, AppURLInput{URL: "http://localhost:3001"}); !errors.Is(err, ErrAppURLWorkspaceTaken) {
		t.Errorf("second default err = %v, want ErrAppURLWorkspaceTaken", err)
	}
	if _, err := a.Create(repo, AppURLInput{URL: "http://localhost:3002", Workspace: "api"}); err != nil {
		t.Fatalf("create workspace entry: %v", err)
	}
	if _, err := a.Create(repo, AppURLInput{URL: "http://localhost:3003", Workspace: "api"}); !errors.Is(err, ErrAppURLWorkspaceTaken) {
		t.Errorf("duplicate workspace err = %v, want ErrAppURLWorkspaceTaken", err)
	}
	web, err := a.Create(repo, AppURLInput{URL: "http://localhost:3005", Workspace: "web"})
	if err != nil {
		t.Fatalf("create web entry: %v", err)
	}
	if _, err := a.Update(repo, web.ID, AppURLInput{URL: "http://localhost:3005", Workspace: "api"}); !errors.Is(err, ErrAppURLWorkspaceTaken) {
		t.Errorf("re-scope onto a taken workspace err = %v, want ErrAppURLWorkspaceTaken", err)
	}
	if _, err := a.Create("/repos/other", AppURLInput{URL: "http://localhost:3004", Workspace: "api"}); err != nil {
		t.Errorf("another repo's api workspace rejected: %v", err)
	}
}

func TestAppURLByWorkspace(t *testing.T) {
	a := testAppURLs(t)
	const repo = "/repos/acme"
	if _, err := a.Create(repo, AppURLInput{URL: "http://localhost:3000"}); err != nil {
		t.Fatalf("create default: %v", err)
	}
	if _, ok, err := a.ByWorkspace(repo, ""); err != nil || !ok {
		t.Fatalf("ByWorkspace(default): ok=%v err=%v", ok, err)
	}
	if _, ok, err := a.ByWorkspace(repo, "api"); err != nil || ok {
		t.Fatalf("ByWorkspace(api): ok=%v err=%v", ok, err)
	}
}

// TestAppURLListScopedAndOrdered pins the read the loop depends on: the repo's
// own entries only, default first so a reader can take it without a second query.
func TestAppURLListScopedAndOrdered(t *testing.T) {
	a := testAppURLs(t)
	for _, in := range []AppURLInput{
		{URL: "http://localhost:3001", Workspace: "web"},
		{URL: "http://localhost:3002", Workspace: "api"},
		{URL: "http://localhost:3000"},
	} {
		if _, err := a.Create("/repos/acme", in); err != nil {
			t.Fatalf("create %+v: %v", in, err)
		}
	}
	if _, err := a.Create("/repos/other", AppURLInput{URL: "http://localhost:9999"}); err != nil {
		t.Fatalf("create other-repo entry: %v", err)
	}

	list, err := a.List("/repos/acme")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("list returned %d entries, want 3 (repo-scoped)", len(list))
	}
	if list[0].Workspace != "" || list[1].Workspace != "api" || list[2].Workspace != "web" {
		t.Errorf("list not ordered default-first by workspace: %+v", list)
	}
}
