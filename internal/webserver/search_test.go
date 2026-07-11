package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
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
