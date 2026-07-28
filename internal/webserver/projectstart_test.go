package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// startRepoHub builds a hub whose workspace holds the named roots, with enqueue's
// tracker validation stubbed unavailable so queue writes stay hermetic. It
// returns the hub, the roots in the order they were named, and the server.
func startRepoHub(t *testing.T, names ...string) (*Server, []string, *httptest.Server) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	roots := make([]string, 0, len(names))
	for _, name := range names {
		roots = append(roots, filepath.Join(base, name))
	}
	s := New("1.2.3", "127.0.0.1", "", roots, false, testStores(t))
	s.home = t.TempDir()
	s.newReader = func(config.Config) (tracker.Reader, error) { return nil, tracker.ErrReaderUnavailable }
	s.sup = &fakeSupervisor{}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, roots, ts
}

func groupRoots(t *testing.T, ts *httptest.Server, roots []string) string {
	t.Helper()
	project := createProjectReq(t, ts, "Platform")
	for _, root := range roots {
		if res, body := addProjectRepo(t, ts, project.ID, root); res.StatusCode != http.StatusOK {
			t.Fatalf("add %s = %d (%s)", root, res.StatusCode, body)
		}
	}
	return project.ID
}

func startRepo(t *testing.T, ts *httptest.Server, project, ticket string) ProjectStartResponse {
	t.Helper()
	path := APIPrefix + "/projects/" + url.PathEscape(project) + "/start-repo?id=" + url.QueryEscape(ticket)
	res, body := get(t, ts, path)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("start-repo = %d (%s)", res.StatusCode, body)
	}
	var resp ProjectStartResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode start-repo: %v (body %q)", err, body)
	}
	return resp
}

func queueTicket(t *testing.T, ts *httptest.Server, repo, id string) {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/queue", QueueRequest{Kind: "ticket", ID: id})
	if body := readBody(t, res); res.StatusCode != http.StatusCreated {
		t.Fatalf("queue %s in %s = %d (%s)", id, repo, res.StatusCode, body)
	}
}

func TestProjectStartRepoSuggestsNamedMember(t *testing.T) {
	s, roots, ts := startRepoHub(t, "api", "image-service", "web")
	project := groupRoots(t, ts, roots)
	if _, _, err := s.stores.Issues().Upsert(roots[0], "linear", []hubstore.Issue{
		{Identifier: "COD-1", Title: "fix image-service upload"},
	}); err != nil {
		t.Fatalf("seed issue: %v", err)
	}

	resp := startRepo(t, ts, project, "COD-1")
	if resp.Suggested != "image-service" || resp.Reason != startRepoNamed {
		t.Fatalf("suggested = %q (%s), want image-service named", resp.Suggested, resp.Reason)
	}
	if len(resp.Repos) != 3 || resp.Repos[0].Name != "api" || resp.Repos[0].Root != roots[0] {
		t.Fatalf("members = %+v, want the three roots in project order", resp.Repos)
	}
}

// TestProjectStartRepoFallsBackWithoutMatch walks the rest of the hint ladder: a
// ticket naming no member opens on the first one until the project has started
// work somewhere, and on that member afterwards.
func TestProjectStartRepoFallsBackWithoutMatch(t *testing.T) {
	s, roots, ts := startRepoHub(t, "api", "image-service", "web")
	project := groupRoots(t, ts, roots)
	if _, _, err := s.stores.Issues().Upsert(roots[0], "linear", []hubstore.Issue{
		{Identifier: "COD-2", Title: "tidy up the release notes"},
		{Identifier: "COD-3", Title: "drop the dead flag"},
	}); err != nil {
		t.Fatalf("seed issues: %v", err)
	}

	if resp := startRepo(t, ts, project, "COD-2"); resp.Suggested != "api" || resp.Reason != startRepoFirst {
		t.Fatalf("cold suggestion = %q (%s), want api first", resp.Suggested, resp.Reason)
	}

	queueTicket(t, ts, "web", "COD-2")

	if resp := startRepo(t, ts, project, "COD-3"); resp.Suggested != "web" || resp.Reason != startRepoRecent {
		t.Fatalf("suggestion after a run = %q (%s), want web recent", resp.Suggested, resp.Reason)
	}
}

// TestProjectStartRepoRemembersTicketRepo is the promise a re-start makes: the
// repo a ticket was queued in outranks the name its title happens to carry.
func TestProjectStartRepoRemembersTicketRepo(t *testing.T) {
	s, roots, ts := startRepoHub(t, "api", "image-service", "web")
	project := groupRoots(t, ts, roots)
	if _, _, err := s.stores.Issues().Upsert(roots[0], "linear", []hubstore.Issue{
		{Identifier: "COD-4", Title: "fix image-service upload"},
	}); err != nil {
		t.Fatalf("seed issue: %v", err)
	}

	queueTicket(t, ts, "web", "COD-4")

	if resp := startRepo(t, ts, project, "COD-4"); resp.Suggested != "web" || resp.Reason != startRepoRemembered {
		t.Fatalf("re-start suggestion = %q (%s), want web remembered", resp.Suggested, resp.Reason)
	}
}

// TestProjectStartRepoKeepsInternalTicketHome holds the picker to the repos that
// could actually take the ticket: an internal one lives in a single member's
// store, so no other member is offered however its title reads.
func TestProjectStartRepoKeepsInternalTicketHome(t *testing.T) {
	s, roots, ts := startRepoHub(t, "api", "image-service", "web")
	project := groupRoots(t, ts, roots)
	iss, err := s.stores.Issues().CreateInternal(roots[1], "IMG", hubstore.InternalDraft{
		Title: "fix api upload",
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	resp := startRepo(t, ts, project, iss.Identifier)
	if len(resp.Repos) != 1 || resp.Repos[0].Name != "image-service" {
		t.Fatalf("members = %+v, want image-service alone", resp.Repos)
	}
	if resp.Suggested != "image-service" {
		t.Fatalf("suggested = %q, want image-service", resp.Suggested)
	}
}

func TestProjectStartRepoRejectsUnknownProject(t *testing.T) {
	_, _, ts := startRepoHub(t, "api")

	res, body := get(t, ts, APIPrefix+"/projects/ghost/start-repo?id=COD-1")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown project = %d (%s), want 404", res.StatusCode, body)
	}
}

func TestNamedMember(t *testing.T) {
	members := []registry.Repo{
		{Name: "api", Root: "/src/api"},
		{Name: "image-service", Root: "/src/image-service"},
		{Name: "service", Root: "/src/service"},
		{Name: "web", Root: "/work/web-app"},
	}
	cases := []struct {
		name string
		text string
		want string
	}{
		{name: "hyphenated name in the title", text: "fix image-service upload", want: "image-service"},
		{name: "spelled out in prose", text: "the image service drops large files", want: "image-service"},
		{name: "underscores read the same", text: "patch image_service retries", want: "image-service"},
		{name: "the most specific name wins", text: "image-service is the only service at fault", want: "image-service"},
		{name: "root basename counts", text: "web-app renders twice", want: "web"},
		{name: "case is ignored", text: "API returns 500", want: "api"},
		{name: "no match inside a longer word", text: "rapids are unrelated"},
		{name: "no member named at all", text: "tidy up the release notes"},
		{name: "empty text", text: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, ok := namedMember(members, tc.text)
			if ok != (tc.want != "") {
				t.Fatalf("namedMember(%q) matched = %v, want %v", tc.text, ok, tc.want != "")
			}
			if repo.Name != tc.want {
				t.Fatalf("namedMember(%q) = %q, want %q", tc.text, repo.Name, tc.want)
			}
		})
	}
}
