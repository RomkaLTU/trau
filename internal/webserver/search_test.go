package webserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// searchServer builds a server with one registered repo ("acme") whose local
// issue store the caller can seed, so the search endpoint is exercised without a
// tracker round-trip.
func searchServer(t *testing.T) (*httptest.Server, *hubstore.Stores, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "acme")
	repo := registry.Repo{Name: "acme", Root: root, RunsDir: filepath.Join(root, ".trau", "runs")}
	stores := testStoresAt(t, home)
	if err := stores.Registrations().Remember([]registry.Repo{repo}); err != nil {
		t.Fatalf("remember repo: %v", err)
	}
	s := New("1.2.3", "127.0.0.1", "", nil, false, stores)
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, stores, root
}

func getSearch(t *testing.T, ts *httptest.Server, repo, query string) (*http.Response, SearchResponse) {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/issues/search?q=" + url.QueryEscape(query))
	if err != nil {
		t.Fatalf("GET search: %v", err)
	}
	var out SearchResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode search: %v", err)
		}
	}
	return res, out
}

func TestSearchEndpointReturnsRankedMatches(t *testing.T) {
	ts, stores, root := searchServer(t)
	if _, _, err := stores.Issues().Upsert(root, "linear", []hubstore.Issue{
		{Identifier: "COD-1", Title: "widget dashboard", Status: "In Progress", StatusGroup: "started"},
		{Identifier: "COD-2", Title: "unrelated", Description: "a passing mention of widget"},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	res, out := getSearch(t, ts, "acme", "widget")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if out.Repo != "acme" || out.Query != "widget" {
		t.Fatalf("envelope = %+v, want repo acme / query widget", out)
	}
	ids := make([]string, len(out.Results))
	for i, r := range out.Results {
		ids[i] = r.ID
	}
	if !slices.Equal(ids, []string{"COD-1", "COD-2"}) {
		t.Fatalf("results = %v, want the title match COD-1 ranked first", ids)
	}
	if out.Results[0].Status != "In Progress" || out.Results[0].Source != "linear" {
		t.Fatalf("top result = %+v, want its stored status and source carried through", out.Results[0])
	}
}

func TestSearchEndpointEmptyQueryIsOK(t *testing.T) {
	ts, _, _ := searchServer(t)
	res, out := getSearch(t, ts, "acme", "   ")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if len(out.Results) != 0 {
		t.Fatalf("results = %v, want none for a blank query", out.Results)
	}
}

func TestSearchEndpointUnknownRepo(t *testing.T) {
	ts, _, _ := searchServer(t)
	res, _ := getSearch(t, ts, "ghost", "widget")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown repo", res.StatusCode)
	}
}

func TestSearchEndpointRejectsNonGet(t *testing.T) {
	ts, _, _ := searchServer(t)
	res, err := http.Post(ts.URL+APIPrefix+"/repos/acme/issues/search?q=widget", "application/json", nil)
	if err != nil {
		t.Fatalf("POST search: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", res.StatusCode)
	}
}

// globalSearchServer builds a server with two registered repos whose issue
// store, config file, and checkpoint table the caller can seed, so the
// cross-repo endpoint is exercised over a real multi-repo fixture.
func globalSearchServer(t *testing.T) (*httptest.Server, *hubstore.Stores, map[string]string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	stores := testStoresAt(t, home)
	roots := map[string]string{}
	repos := make([]registry.Repo, 0, 2)
	for _, name := range []string{"acme", "beta"} {
		root := filepath.Join(t.TempDir(), name)
		roots[name] = root
		repos = append(repos, registry.Repo{Name: name, Root: root, RunsDir: filepath.Join(root, ".trau", "runs")})
	}
	if err := stores.Registrations().Remember(repos); err != nil {
		t.Fatalf("remember repos: %v", err)
	}
	s := New("1.2.3", "127.0.0.1", "", nil, false, stores)
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, stores, roots
}

func getGlobalSearch(t *testing.T, ts *httptest.Server, query string) (*http.Response, GlobalSearchResponse, string) {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/search?q=" + url.QueryEscape(query))
	if err != nil {
		t.Fatalf("GET global search: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read global search: %v", err)
	}
	var out GlobalSearchResponse
	if res.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode global search: %v", err)
		}
	}
	return res, out, string(body)
}

func TestGlobalSearchTagsMatchesWithTheirRepo(t *testing.T) {
	ts, stores, roots := globalSearchServer(t)
	for name, root := range roots {
		if _, _, err := stores.Issues().Upsert(root, "linear", []hubstore.Issue{
			{Identifier: "COD-1", Title: name + " widget dashboard", Status: "In Progress"},
		}); err != nil {
			t.Fatalf("upsert %s issues: %v", name, err)
		}
		if err := stores.Checkpoints().Upsert(root, "COD-7", map[string]string{
			"PHASE": state.Merged, "TITLE": "widget ledger", "UPDATED": "2099-01-01 00:00:00",
		}); err != nil {
			t.Fatalf("seed %s checkpoint: %v", name, err)
		}
	}

	res, out, _ := getGlobalSearch(t, ts, "widget")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if out.Query != "widget" {
		t.Fatalf("query = %q, want widget", out.Query)
	}
	issueRepos := make([]string, 0, len(out.Results.Issues))
	for _, hit := range out.Results.Issues {
		if hit.ID != "COD-1" {
			t.Fatalf("issue = %+v, want the seeded COD-1", hit)
		}
		issueRepos = append(issueRepos, hit.Repo)
	}
	slices.Sort(issueRepos)
	if !slices.Equal(issueRepos, []string{"acme", "beta"}) {
		t.Fatalf("issue repos = %v, want a tagged hit from each repo", issueRepos)
	}
	runRepos := make([]string, 0, len(out.Results.Runs))
	for _, hit := range out.Results.Runs {
		if hit.Ticket != "COD-7" || hit.Phase != state.Merged {
			t.Fatalf("run = %+v, want the seeded merged COD-7", hit)
		}
		runRepos = append(runRepos, hit.Repo)
	}
	slices.Sort(runRepos)
	if !slices.Equal(runRepos, []string{"acme", "beta"}) {
		t.Fatalf("run repos = %v, want a tagged run from each repo", runRepos)
	}
}

func TestGlobalSearchMasksSecretSettings(t *testing.T) {
	ts, _, roots := globalSearchServer(t)
	if err := os.MkdirAll(roots["acme"], 0o755); err != nil {
		t.Fatalf("mkdir acme root: %v", err)
	}
	secret := []byte("LINEAR_API_KEY=lin_api_notsoserious\n")
	if err := os.WriteFile(config.ProjectConfigPath(roots["acme"]), secret, 0o644); err != nil {
		t.Fatalf("write acme config: %v", err)
	}

	res, out, body := getGlobalSearch(t, ts, "linear api key")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if strings.Contains(body, "lin_api_notsoserious") {
		t.Fatalf("response leaked the raw secret: %s", body)
	}
	var hit *GlobalSettingResult
	for i, row := range out.Results.Settings {
		if row.Key == "LINEAR_API_KEY" && row.Repo == "acme" {
			hit = &out.Results.Settings[i]
		}
	}
	if hit == nil {
		t.Fatalf("settings = %+v, want acme's LINEAR_API_KEY", out.Results.Settings)
	}
	if hit.Value != "••••••••" || hit.Group == "" {
		t.Fatalf("setting = %+v, want a masked value and its section", *hit)
	}
	for _, row := range out.Results.Settings {
		if row.Repo == "beta" && row.Value != "—" {
			t.Fatalf("beta setting = %+v, want no value where the key is unset", row)
		}
	}
}

func TestGlobalSearchCapsEachGroup(t *testing.T) {
	ts, stores, roots := globalSearchServer(t)
	for _, root := range roots {
		issues := make([]hubstore.Issue, 0, 8)
		for i := range 8 {
			issues = append(issues, hubstore.Issue{
				Identifier: fmt.Sprintf("COD-%d", i),
				Title:      "widget dashboard",
			})
		}
		if _, _, err := stores.Issues().Upsert(root, "linear", issues); err != nil {
			t.Fatalf("upsert issues: %v", err)
		}
	}

	_, out, _ := getGlobalSearch(t, ts, "widget")
	if len(out.Results.Issues) != 10 {
		t.Fatalf("issues = %d, want the group capped at 10", len(out.Results.Issues))
	}
	perRepo := map[string]int{}
	for _, hit := range out.Results.Issues {
		perRepo[hit.Repo]++
	}
	if perRepo["acme"] != 5 || perRepo["beta"] != 5 {
		t.Fatalf("per-repo counts = %v, want neither repo flooding the group", perRepo)
	}
}

func TestGlobalSearchEmptyQueryIsOK(t *testing.T) {
	ts, stores, roots := globalSearchServer(t)
	if _, _, err := stores.Issues().Upsert(roots["acme"], "linear", []hubstore.Issue{
		{Identifier: "COD-1", Title: "widget dashboard"},
	}); err != nil {
		t.Fatalf("upsert issues: %v", err)
	}

	for _, query := range []string{"   ", "!!!"} {
		res, out, _ := getGlobalSearch(t, ts, query)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status for %q = %d, want 200", query, res.StatusCode)
		}
		groups := out.Results
		if len(groups.Issues) != 0 || len(groups.Settings) != 0 || len(groups.Runs) != 0 {
			t.Fatalf("results for %q = %+v, want every group empty", query, groups)
		}
	}
}

func TestGlobalSearchRejectsNonGet(t *testing.T) {
	ts, _, _ := globalSearchServer(t)
	res, err := http.Post(ts.URL+APIPrefix+"/search?q=widget", "application/json", nil)
	if err != nil {
		t.Fatalf("POST global search: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", res.StatusCode)
	}
}
