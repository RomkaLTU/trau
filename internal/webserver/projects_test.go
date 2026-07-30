package webserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
)

func decodeProject(t *testing.T, body string) ProjectView {
	t.Helper()
	var view ProjectView
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatalf("decode project: %v (body %q)", err, body)
	}
	return view
}

func listProjects(t *testing.T, ts *httptest.Server) []ProjectView {
	t.Helper()
	res, body := get(t, ts, APIPrefix+"/projects")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list projects = %d (%s)", res.StatusCode, body)
	}
	var resp ProjectsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode projects: %v (body %q)", err, body)
	}
	return resp.Projects
}

func createProjectReq(t *testing.T, ts *httptest.Server, name string) ProjectView {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/projects", ProjectRequest{Name: name})
	body := readBody(t, res)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create project = %d (%s)", res.StatusCode, body)
	}
	return decodeProject(t, body)
}

func addProjectRepo(t *testing.T, ts *httptest.Server, id, repo string) (*http.Response, string) {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/projects/"+url.PathEscape(id)+"/repos", ProjectRepoRequest{Repo: repo})
	return res, readBody(t, res)
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(data)
}

func TestProjectCreateGroupsRegisteredRepos(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	web := gitRepo(t, base, "web", "dir")
	_, ts := controlServer(t, home, nil)

	for _, root := range []string{api, web} {
		res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: root})
		if body := readBody(t, res); res.StatusCode != http.StatusCreated {
			t.Fatalf("register %s = %d (%s)", root, res.StatusCode, body)
		}
	}

	project := createProjectReq(t, ts, "Platform")
	if project.ID != "platform" || len(project.Repos) != 0 {
		t.Fatalf("created project = %+v, want a slug id and no members", project)
	}

	for _, ident := range []string{"api", web} {
		res, body := addProjectRepo(t, ts, project.ID, ident)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("add %s = %d (%s)", ident, res.StatusCode, body)
		}
	}

	projects := listProjects(t, ts)
	if len(projects) != 1 {
		t.Fatalf("projects = %+v, want one group", projects)
	}
	if want := []string{api, web}; !reflect.DeepEqual(projects[0].Repos, want) {
		t.Fatalf("members = %v, want %v", projects[0].Repos, want)
	}

	if allowed := allowedRepoNames(t, ts); !allowed["api"] || !allowed["web"] {
		t.Errorf("grouping changed the startable set: %v", allowed)
	}
}

func TestProjectAddRejectsUnknownRepo(t *testing.T) {
	_, ts := controlServer(t, t.TempDir(), nil)
	project := createProjectReq(t, ts, "Platform")

	res, body := addProjectRepo(t, ts, project.ID, "ghost")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("add unknown repo = %d (%s), want 404", res.StatusCode, body)
	}
}

func TestProjectCreateRejectsBlankName(t *testing.T) {
	_, ts := controlServer(t, t.TempDir(), nil)

	res := postJSON(t, ts.URL+APIPrefix+"/projects", ProjectRequest{Name: "  "})
	body := readBody(t, res)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("create blank = %d (%s), want 400", res.StatusCode, body)
	}
}

func TestProjectRenameAndRemoveRepo(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	web := gitRepo(t, base, "web", "dir")
	_, ts := controlServer(t, home, nil)
	for _, root := range []string{api, web} {
		res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: root})
		_ = readBody(t, res)
	}

	project := createProjectReq(t, ts, "Platform")
	for _, ident := range []string{"api", "web"} {
		if res, body := addProjectRepo(t, ts, project.ID, ident); res.StatusCode != http.StatusOK {
			t.Fatalf("add %s = %d (%s)", ident, res.StatusCode, body)
		}
	}

	res, body := authReq(t, http.MethodPatch, ts.URL+APIPrefix+"/projects/"+project.ID, "", ProjectRequest{Name: "Core"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("rename = %d (%s)", res.StatusCode, body)
	}
	renamed := decodeProject(t, body)
	if renamed.ID != project.ID || renamed.Name != "Core" {
		t.Fatalf("renamed = %+v, want the same id under Core", renamed)
	}

	res, body = deleteReq(t, ts, APIPrefix+"/projects/"+project.ID+"/repos/api")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("remove repo = %d (%s)", res.StatusCode, body)
	}
	if want := []string{web}; !reflect.DeepEqual(decodeProject(t, body).Repos, want) {
		t.Fatalf("members after removal = %v, want %v", decodeProject(t, body).Repos, want)
	}

	res, body = deleteReq(t, ts, APIPrefix+"/projects/"+project.ID+"/repos/api")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("re-remove = %d (%s), want 404", res.StatusCode, body)
	}
	res, body = authReq(t, http.MethodPatch, ts.URL+APIPrefix+"/projects/ghost", "", ProjectRequest{Name: "Core"})
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("rename unknown = %d (%s), want 404", res.StatusCode, body)
	}
}

// TestProjectDeleteLeavesReposRegistered is the promise a delete makes: the group
// goes, every member stays registered and startable.
func TestProjectDeleteLeavesReposRegistered(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	web := gitRepo(t, base, "web", "dir")
	_, ts := controlServer(t, home, nil)
	for _, root := range []string{api, web} {
		res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: root})
		_ = readBody(t, res)
	}

	project := createProjectReq(t, ts, "Platform")
	for _, ident := range []string{"api", "web"} {
		if res, body := addProjectRepo(t, ts, project.ID, ident); res.StatusCode != http.StatusOK {
			t.Fatalf("add %s = %d (%s)", ident, res.StatusCode, body)
		}
	}

	res, body := deleteReq(t, ts, APIPrefix+"/projects/"+project.ID)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d (%s)", res.StatusCode, body)
	}
	if want := []string{api, web}; !reflect.DeepEqual(decodeProject(t, body).Repos, want) {
		t.Fatalf("delete body = %s, want the group it dropped", body)
	}
	if projects := listProjects(t, ts); len(projects) != 0 {
		t.Fatalf("projects after delete = %+v, want none", projects)
	}

	registered, _ := testStoresAt(t, home).Registrations().Registered()
	if want := []string{api, web}; !reflect.DeepEqual(registered, want) {
		t.Fatalf("registered = %v, want %v — deleting a project must not unregister its repos", registered, want)
	}
	if allowed := allowedRepoNames(t, ts); !allowed["api"] || !allowed["web"] {
		t.Errorf("repos stopped being startable after their project was deleted: %v", allowed)
	}

	res, body = deleteReq(t, ts, APIPrefix+"/projects/"+project.ID)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("re-delete = %d (%s), want 404", res.StatusCode, body)
	}
}

func removalNames(repos []ProjectRemovalRepo) []string {
	names := make([]string, 0, len(repos))
	for _, repo := range repos {
		names = append(names, repo.Name)
	}
	return names
}

// TestProjectForgetReposClearsTheFolder is the one-action removal: every member the
// hub will let go leaves it, and a member it refuses stays registered holding the
// group rather than failing the whole call.
func TestProjectForgetReposClearsTheFolder(t *testing.T) {
	cases := []struct {
		name        string
		live        string
		wantRemoved []string
		wantBlocked []string
		wantReason  string
	}{
		{
			name:        "every member leaves the hub",
			wantRemoved: []string{"api", "web"},
			wantBlocked: []string{},
		},
		{
			name:        "a live member stays behind with its group",
			live:        "web",
			wantRemoved: []string{"api"},
			wantBlocked: []string{"web"},
			wantReason:  "stop it",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			base := t.TempDir()
			roots := map[string]string{
				"api": gitRepo(t, base, "api", "dir"),
				"web": gitRepo(t, base, "web", "dir"),
			}
			if tc.live != "" {
				writeEntry(t, home, registry.Entry{
					PID:          os.Getpid(),
					RepoRoot:     roots[tc.live],
					SessionState: registry.StateIdle,
					StartedAt:    time.Now(),
					Heartbeat:    time.Now(),
				})
			}
			_, ts := controlServer(t, home, nil)
			for _, name := range []string{"api", "web"} {
				res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: roots[name]})
				if body := readBody(t, res); res.StatusCode != http.StatusCreated {
					t.Fatalf("register %s = %d (%s)", name, res.StatusCode, body)
				}
			}
			project := createProjectReq(t, ts, "Acme")
			for _, name := range []string{"api", "web"} {
				if res, body := addProjectRepo(t, ts, project.ID, roots[name]); res.StatusCode != http.StatusOK {
					t.Fatalf("add %s = %d (%s)", name, res.StatusCode, body)
				}
			}

			res, body := deleteReq(t, ts, APIPrefix+"/projects/"+project.ID+"?forget=1")
			if res.StatusCode != http.StatusOK {
				t.Fatalf("forget = %d (%s)", res.StatusCode, body)
			}
			var out ProjectRemoval
			if err := json.Unmarshal([]byte(body), &out); err != nil {
				t.Fatalf("decode removal: %v (body %q)", err, body)
			}
			if want := []string{roots["api"], roots["web"]}; !reflect.DeepEqual(out.Project.Repos, want) {
				t.Errorf("snapshot = %v, want the group it acted on %v", out.Project.Repos, want)
			}
			if got := removalNames(out.Removed); !reflect.DeepEqual(got, tc.wantRemoved) {
				t.Errorf("removed = %v, want %v", got, tc.wantRemoved)
			}
			if got := removalNames(out.Blocked); !reflect.DeepEqual(got, tc.wantBlocked) {
				t.Errorf("blocked = %v, want %v", got, tc.wantBlocked)
			}
			if len(out.Blocked) > 0 && !strings.Contains(out.Blocked[0].Reason, tc.wantReason) {
				t.Errorf("blocked reason = %q, want it to name %q", out.Blocked[0].Reason, tc.wantReason)
			}
			if want := len(tc.wantBlocked) == 0; out.ProjectDeleted != want {
				t.Errorf("project_deleted = %v, want %v", out.ProjectDeleted, want)
			}

			registered, _ := testStoresAt(t, home).Registrations().Registered()
			for _, name := range tc.wantRemoved {
				if slices.Contains(registered, roots[name]) {
					t.Errorf("removed %s is still registered: %v", name, registered)
				}
			}
			for _, name := range tc.wantBlocked {
				if !slices.Contains(registered, roots[name]) {
					t.Errorf("blocked %s lost its registration: %v", name, registered)
				}
			}

			projects := listProjects(t, ts)
			if len(tc.wantBlocked) == 0 {
				if len(projects) != 0 {
					t.Fatalf("projects after a clean removal = %+v, want none", projects)
				}
				return
			}
			if len(projects) != 1 {
				t.Fatalf("projects = %+v, want the group to survive its blocked member", projects)
			}
			want := make([]string, 0, len(tc.wantBlocked))
			for _, name := range tc.wantBlocked {
				want = append(want, roots[name])
			}
			if !reflect.DeepEqual(projects[0].Repos, want) {
				t.Errorf("members = %v, want %v", projects[0].Repos, want)
			}
		})
	}
}

// TestProjectMigrationIsIdempotentAndAdditive covers the upgrade path: a hub that
// predates projects backfills one single-member project per repo it tracks,
// leaves the registrations byte-for-byte alone, and re-running it changes nothing.
func TestProjectMigrationIsIdempotentAndAdditive(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	web := gitRepo(t, base, "web", "dir")
	stores := testStoresAt(t, home)
	for _, root := range []string{api, web} {
		if err := stores.Registrations().Register(root); err != nil {
			t.Fatalf("register %s: %v", root, err)
		}
	}

	if err := stores.EnsureProjects(); err != nil {
		t.Fatalf("EnsureProjects: %v", err)
	}
	first, err := stores.Projects().List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("projects = %+v, want one per registration", first)
	}
	for _, proj := range first {
		if len(proj.Repos) != 1 {
			t.Fatalf("project %q = %v, want a single member", proj.Name, proj.Repos)
		}
	}

	if err := stores.EnsureProjects(); err != nil {
		t.Fatalf("second EnsureProjects: %v", err)
	}
	second, err := stores.Projects().List()
	if err != nil {
		t.Fatalf("List after re-run: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("re-running the migration changed the store: %+v -> %+v", first, second)
	}

	registered, _ := stores.Registrations().Registered()
	if want := []string{api, web}; !reflect.DeepEqual(registered, want) {
		t.Fatalf("registered = %v, want %v — the migration must not rewrite registrations", registered, want)
	}
}

// TestProjectMigrationKeepsQueueAndRuns pins the "zero behavior change" half of
// the upgrade: a repo carrying queued work keeps it across the backfill.
func TestProjectMigrationKeepsQueueAndRuns(t *testing.T) {
	home := t.TempDir()
	repo := gitRepo(t, t.TempDir(), "api", "dir")
	stores := testStoresAt(t, home)
	if err := stores.Registrations().Register(repo); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := stores.Queue(repo).Add(queue.Item{ID: "COD-1", Title: "queued before the upgrade", Status: "pending"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if err := stores.EnsureProjects(); err != nil {
		t.Fatalf("EnsureProjects: %v", err)
	}

	items, err := stores.Queue(repo).Load()
	if err != nil {
		t.Fatalf("load queue: %v", err)
	}
	if len(items) != 1 || items[0].ID != "COD-1" {
		t.Fatalf("queue after migration = %+v, want the pre-upgrade item", items)
	}
}

// TestProjectExposureGate keeps every project write under the same bind × token ×
// SERVE_ALLOW_REGISTER rule the registration writes follow.
func TestProjectExposureGate(t *testing.T) {
	cases := []struct {
		name          string
		bind          string
		serverToken   string
		reqToken      string
		allowRegister bool
		wantCreate    int
		namesKey      bool
	}{
		{"loopback open without key", "127.0.0.1", "", "", false, http.StatusCreated, false},
		{"exposed token, key off, refused", "0.0.0.0", "s3cret", "s3cret", false, http.StatusForbidden, true},
		{"exposed token, key on, allowed", "0.0.0.0", "s3cret", "s3cret", true, http.StatusCreated, false},
		{"exposed missing token", "0.0.0.0", "s3cret", "", false, http.StatusUnauthorized, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			store := testStoresAt(t, home)
			s := New("1.2.3", tc.bind, tc.serverToken, nil, tc.allowRegister, store)
			s.home = home
			s.sup = &fakeSupervisor{}
			ts := httptest.NewServer(s.Handler())
			t.Cleanup(ts.Close)

			res, body := authReq(t, http.MethodPost, ts.URL+APIPrefix+"/projects", tc.reqToken, ProjectRequest{Name: "Platform"})
			if res.StatusCode != tc.wantCreate {
				t.Fatalf("create = %d, want %d (%s)", res.StatusCode, tc.wantCreate, body)
			}
			if tc.namesKey && !strings.Contains(body, "SERVE_ALLOW_REGISTER") {
				t.Errorf("create refusal %q does not name SERVE_ALLOW_REGISTER", body)
			}
			projects, _ := store.Projects().List()
			if got := len(projects) > 0; got != (tc.wantCreate == http.StatusCreated) {
				t.Errorf("project persisted = %v after create status %d", got, tc.wantCreate)
			}

			res, body = authReq(t, http.MethodDelete, ts.URL+APIPrefix+"/projects/platform", tc.reqToken, nil)
			wantDelete := tc.wantCreate
			if wantDelete == http.StatusCreated {
				wantDelete = http.StatusOK
			}
			if res.StatusCode != wantDelete {
				t.Fatalf("delete = %d, want %d (%s)", res.StatusCode, wantDelete, body)
			}
			if tc.namesKey && !strings.Contains(body, "SERVE_ALLOW_REGISTER") {
				t.Errorf("delete refusal %q does not name SERVE_ALLOW_REGISTER", body)
			}
		})
	}
}

func TestProjectMethodNotAllowed(t *testing.T) {
	_, ts := controlServer(t, t.TempDir(), nil)
	project := createProjectReq(t, ts, "Platform")

	cases := []struct {
		path      string
		method    string
		wantAllow string
	}{
		{APIPrefix + "/projects", http.MethodDelete, "GET, POST"},
		{APIPrefix + "/projects/" + project.ID, http.MethodGet, "PATCH, DELETE"},
		{APIPrefix + "/projects/" + project.ID + "/repos", http.MethodGet, http.MethodPost},
		{APIPrefix + "/projects/" + project.ID + "/repos/api", http.MethodPost, http.MethodDelete},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			res, _ := authReq(t, tc.method, ts.URL+tc.path, "", nil)
			if res.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405", res.StatusCode)
			}
			if allow := res.Header.Get("Allow"); allow != tc.wantAllow {
				t.Errorf("Allow = %q, want %q", allow, tc.wantAllow)
			}
		})
	}
}

// TestForgetRepoDropsProjectMembership keeps a group from referencing a root the
// hub no longer knows.
func TestForgetRepoDropsProjectMembership(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	web := gitRepo(t, base, "web", "dir")
	_, ts := controlServer(t, home, nil)
	for _, root := range []string{api, web} {
		res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: root})
		_ = readBody(t, res)
	}

	project := createProjectReq(t, ts, "Platform")
	for _, ident := range []string{"api", "web"} {
		if res, body := addProjectRepo(t, ts, project.ID, ident); res.StatusCode != http.StatusOK {
			t.Fatalf("add %s = %d (%s)", ident, res.StatusCode, body)
		}
	}

	res, body := deleteReq(t, ts, APIPrefix+"/repos/api?forget=1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("forget = %d (%s)", res.StatusCode, body)
	}

	projects := listProjects(t, ts)
	if len(projects) != 1 {
		t.Fatalf("projects = %+v, want the group to survive", projects)
	}
	if want := []string{web}; !reflect.DeepEqual(projects[0].Repos, want) {
		t.Fatalf("members = %v, want %v", projects[0].Repos, want)
	}
}
