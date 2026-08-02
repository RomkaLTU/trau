package hubstore

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb/hubdbtest"
	"github.com/RomkaLTU/trau/internal/registry"
)

func testProjects(t *testing.T, home string) *Projects {
	t.Helper()
	db, err := hubdbtest.Open(home)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewProjects(db.SQL())
}

func mustCreateProject(t *testing.T, p *Projects, name string) Project {
	t.Helper()
	proj, err := p.Create(name)
	if err != nil {
		t.Fatalf("Create(%q): %v", name, err)
	}
	return proj
}

func mustAddRepo(t *testing.T, p *Projects, id, root string) Project {
	t.Helper()
	proj, err := p.AddRepo(id, root)
	if err != nil {
		t.Fatalf("AddRepo(%q, %q): %v", id, root, err)
	}
	return proj
}

func projectNames(projects []Project) []string {
	names := make([]string, 0, len(projects))
	for _, proj := range projects {
		names = append(names, proj.Name)
	}
	return names
}

func TestCreateAllocatesDistinctSlugs(t *testing.T) {
	p := testProjects(t, t.TempDir())

	cases := []struct {
		name   string
		wantID string
	}{
		{"Acme Platform", "acme-platform"},
		{"Acme Platform", "acme-platform-2"},
		{"acme---platform", "acme-platform-3"},
		{"日本語", "project"},
	}
	for _, tc := range cases {
		proj := mustCreateProject(t, p, tc.name)
		if proj.ID != tc.wantID {
			t.Errorf("Create(%q).ID = %q, want %q", tc.name, proj.ID, tc.wantID)
		}
		if proj.Name != tc.name {
			t.Errorf("Create(%q).Name = %q, want the display name verbatim", tc.name, proj.Name)
		}
		if len(proj.Repos) != 0 {
			t.Errorf("Create(%q).Repos = %v, want empty", tc.name, proj.Repos)
		}
	}
}

func TestCreateRejectsBlankName(t *testing.T) {
	p := testProjects(t, t.TempDir())
	if _, err := p.Create("   "); !errors.Is(err, ErrProjectNameEmpty) {
		t.Fatalf("Create(blank) = %v, want ErrProjectNameEmpty", err)
	}
}

func TestAddRepoGroupsInOrderAndMovesBetweenProjects(t *testing.T) {
	p := testProjects(t, t.TempDir())
	group := mustCreateProject(t, p, "Platform")
	other := mustCreateProject(t, p, "Other")

	mustAddRepo(t, p, group.ID, "/repos/api")
	proj := mustAddRepo(t, p, group.ID, "/repos/web")
	if want := []string{"/repos/api", "/repos/web"}; !reflect.DeepEqual(proj.Repos, want) {
		t.Fatalf("members = %v, want %v in add order", proj.Repos, want)
	}

	moved := mustAddRepo(t, p, other.ID, "/repos/web")
	if want := []string{"/repos/web"}; !reflect.DeepEqual(moved.Repos, want) {
		t.Fatalf("moved project members = %v, want %v", moved.Repos, want)
	}
	left, err := p.Get(group.ID)
	if err != nil {
		t.Fatalf("Get after move: %v", err)
	}
	if want := []string{"/repos/api"}; !reflect.DeepEqual(left.Repos, want) {
		t.Fatalf("source project members = %v, want %v — a root belongs to one project", left.Repos, want)
	}
}

func TestAddRepoIsIdempotentAndKeepsPosition(t *testing.T) {
	p := testProjects(t, t.TempDir())
	group := mustCreateProject(t, p, "Platform")
	mustAddRepo(t, p, group.ID, "/repos/api")
	mustAddRepo(t, p, group.ID, "/repos/web")

	proj := mustAddRepo(t, p, group.ID, "/repos/api")
	if want := []string{"/repos/api", "/repos/web"}; !reflect.DeepEqual(proj.Repos, want) {
		t.Fatalf("re-add reordered members: got %v, want %v", proj.Repos, want)
	}
}

func TestAddRepoPrunesTheProjectItEmpties(t *testing.T) {
	p := testProjects(t, t.TempDir())
	if err := p.EnsureRoots([]string{"/repos/api", "/repos/web"}); err != nil {
		t.Fatalf("EnsureRoots: %v", err)
	}
	group := mustCreateProject(t, p, "Platform")

	mustAddRepo(t, p, group.ID, "/repos/api")
	mustAddRepo(t, p, group.ID, "/repos/web")

	projects, err := p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := []string{"Platform"}; !reflect.DeepEqual(projectNames(projects), want) {
		t.Fatalf("projects = %v, want only %v — the vacated single-member projects must not linger", projectNames(projects), want)
	}
}

func TestAddRepoUnknownProject(t *testing.T) {
	p := testProjects(t, t.TempDir())
	if _, err := p.AddRepo("nope", "/repos/api"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("AddRepo on unknown project = %v, want ErrProjectNotFound", err)
	}
}

func TestRemoveRepoLeavesProjectStanding(t *testing.T) {
	p := testProjects(t, t.TempDir())
	group := mustCreateProject(t, p, "Platform")
	mustAddRepo(t, p, group.ID, "/repos/api")
	mustAddRepo(t, p, group.ID, "/repos/web")

	proj, err := p.RemoveRepo(group.ID, "/repos/api")
	if err != nil {
		t.Fatalf("RemoveRepo: %v", err)
	}
	if want := []string{"/repos/web"}; !reflect.DeepEqual(proj.Repos, want) {
		t.Fatalf("members = %v, want %v", proj.Repos, want)
	}
	if _, err := p.RemoveRepo(group.ID, "/repos/api"); !errors.Is(err, ErrProjectRepoNotFound) {
		t.Fatalf("re-remove = %v, want ErrProjectRepoNotFound", err)
	}
	if _, err := p.RemoveRepo("nope", "/repos/web"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("remove from unknown project = %v, want ErrProjectNotFound", err)
	}
}

func TestRenameKeepsIdentifierAndMembers(t *testing.T) {
	p := testProjects(t, t.TempDir())
	group := mustCreateProject(t, p, "Platform")
	mustAddRepo(t, p, group.ID, "/repos/api")

	renamed, err := p.Rename(group.ID, "Core")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if renamed.ID != group.ID {
		t.Errorf("rename changed the identifier: %q -> %q", group.ID, renamed.ID)
	}
	if renamed.Name != "Core" {
		t.Errorf("name = %q, want Core", renamed.Name)
	}
	if want := []string{"/repos/api"}; !reflect.DeepEqual(renamed.Repos, want) {
		t.Errorf("members = %v, want %v", renamed.Repos, want)
	}
	if _, err := p.Rename(group.ID, " "); !errors.Is(err, ErrProjectNameEmpty) {
		t.Errorf("Rename(blank) = %v, want ErrProjectNameEmpty", err)
	}
	if _, err := p.Rename("nope", "Core"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("Rename(unknown) = %v, want ErrProjectNotFound", err)
	}
}

func TestDeleteDropsGroupingOnly(t *testing.T) {
	home := t.TempDir()
	db, err := hubdbtest.Open(home)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p := NewProjects(db.SQL())
	regs := NewRegistrations(db.SQL())

	for _, root := range []string{"/repos/api", "/repos/web"} {
		if err := regs.Register(root); err != nil {
			t.Fatalf("register %s: %v", root, err)
		}
	}
	group := mustCreateProject(t, p, "Platform")
	mustAddRepo(t, p, group.ID, "/repos/api")
	mustAddRepo(t, p, group.ID, "/repos/web")

	if err := p.Delete(group.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	projects, err := p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("projects after delete = %v, want none", projects)
	}
	registered, err := regs.Registered()
	if err != nil {
		t.Fatalf("Registered: %v", err)
	}
	if want := []string{"/repos/api", "/repos/web"}; !reflect.DeepEqual(registered, want) {
		t.Fatalf("registered = %v, want %v — deleting a project must not unregister its repos", registered, want)
	}
	if err := p.Delete(group.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("re-Delete = %v, want ErrProjectNotFound", err)
	}
}

func TestEnsureRootsMigratesEachRegistrationOnceAndIsIdempotent(t *testing.T) {
	p := testProjects(t, t.TempDir())
	roots := []string{
		filepath.Join(string(filepath.Separator), "repos", "api"),
		filepath.Join(string(filepath.Separator), "repos", "web"),
	}

	if err := p.EnsureRoots(append(roots, "")); err != nil {
		t.Fatalf("EnsureRoots: %v", err)
	}
	first, err := p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(first) != len(roots) {
		t.Fatalf("projects = %v, want one per registration", projectNames(first))
	}
	for _, proj := range first {
		if len(proj.Repos) != 1 {
			t.Fatalf("project %q members = %v, want exactly one", proj.Name, proj.Repos)
		}
		if want := filepath.Base(proj.Repos[0]); proj.Name != want {
			t.Errorf("project name = %q, want the repo directory %q", proj.Name, want)
		}
	}

	if err := p.EnsureRoots(roots); err != nil {
		t.Fatalf("second EnsureRoots: %v", err)
	}
	second, err := p.List()
	if err != nil {
		t.Fatalf("List after re-run: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("re-run changed the store: %v -> %v", first, second)
	}
}

func TestEnsureRootsLeavesGroupedRepoWhereItIs(t *testing.T) {
	p := testProjects(t, t.TempDir())
	group := mustCreateProject(t, p, "Platform")
	mustAddRepo(t, p, group.ID, "/repos/api")

	if err := p.EnsureRoots([]string{"/repos/api", "/repos/web"}); err != nil {
		t.Fatalf("EnsureRoots: %v", err)
	}
	proj, err := p.Get(group.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if want := []string{"/repos/api"}; !reflect.DeepEqual(proj.Repos, want) {
		t.Fatalf("grouped repo moved: members = %v, want %v", proj.Repos, want)
	}
	projects, err := p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := []string{"Platform", "web"}; !reflect.DeepEqual(projectNames(projects), want) {
		t.Fatalf("projects = %v, want %v", projectNames(projects), want)
	}
}

func TestEnsureProjectsCoversTheReposTheHubTracks(t *testing.T) {
	home := t.TempDir()
	db, err := hubdbtest.Open(home)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	stores := NewStores(home, db.SQL(), nil, Retention{})

	if err := stores.Registrations().Register("/repos/api"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := stores.Registrations().Remember([]registry.Repo{
		{Name: "web", Root: "/repos/web", RunsDir: "/repos/web/runs"},
	}); err != nil {
		t.Fatalf("remember: %v", err)
	}

	if err := stores.EnsureProjects(); err != nil {
		t.Fatalf("EnsureProjects: %v", err)
	}
	projects, err := stores.Projects().List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := []string{"api", "web"}; !reflect.DeepEqual(projectNames(projects), want) {
		t.Fatalf("projects = %v, want %v", projectNames(projects), want)
	}
}

func TestForgetRootDropsMembershipAndVacatedProject(t *testing.T) {
	p := testProjects(t, t.TempDir())
	group := mustCreateProject(t, p, "Platform")
	mustAddRepo(t, p, group.ID, "/repos/api")
	mustAddRepo(t, p, group.ID, "/repos/web")
	solo := mustCreateProject(t, p, "Solo")
	mustAddRepo(t, p, solo.ID, "/repos/tools")

	if err := p.ForgetRoot("/repos/api"); err != nil {
		t.Fatalf("ForgetRoot: %v", err)
	}
	proj, err := p.Get(group.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if want := []string{"/repos/web"}; !reflect.DeepEqual(proj.Repos, want) {
		t.Fatalf("members = %v, want %v", proj.Repos, want)
	}

	if err := p.ForgetRoot("/repos/tools"); err != nil {
		t.Fatalf("ForgetRoot last member: %v", err)
	}
	if _, err := p.Get(solo.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("Get(emptied project) = %v, want ErrProjectNotFound", err)
	}
	if err := p.ForgetRoot("/repos/nowhere"); err != nil {
		t.Fatalf("ForgetRoot for an ungrouped root: %v", err)
	}
}
