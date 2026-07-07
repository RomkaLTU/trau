package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// fakeReader stands in for a direct tracker client so the backlog endpoint is
// asserted without any network.
type fakeReader struct {
	items []tracker.BacklogItem
	err   error
}

func (f *fakeReader) Backlog(context.Context) ([]tracker.BacklogItem, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

// backlogServer builds a server with one exited repo ("acme") and a Reader
// factory returning fake (or readerErr when set).
func backlogServer(t *testing.T, fake tracker.Reader, readerErr error) *httptest.Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	seedRepo(t, home, "acme")
	s := New("1.2.3", "127.0.0.1", "", nil, false)
	s.home = home
	s.newReader = func(config.Config) (tracker.Reader, error) {
		if readerErr != nil {
			return nil, readerErr
		}
		return fake, nil
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func getBacklog(t *testing.T, ts *httptest.Server, repo string) (*http.Response, BacklogResponse) {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/backlog")
	if err != nil {
		t.Fatalf("GET backlog: %v", err)
	}
	var out BacklogResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode backlog: %v", err)
		}
	}
	return res, out
}

func TestBacklogListsItems(t *testing.T) {
	fake := &fakeReader{items: []tracker.BacklogItem{
		{ID: "COD-10", Title: "Epic", Status: "Backlog", Group: tracker.StatusGroupBacklog, HasChildren: true},
		{ID: "COD-11", Title: "Child", Status: "Todo", Group: tracker.StatusGroupUnstarted, Parent: "COD-10", Labels: []string{"ready-for-agent"}, Ready: true},
	}}
	ts := backlogServer(t, fake, nil)

	res, out := getBacklog(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if out.Provider != "linear" {
		t.Errorf("provider = %q, want linear (the resolved default)", out.Provider)
	}
	if len(out.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(out.Items))
	}
	epic := out.Items[0]
	if epic.ID != "COD-10" || epic.Group != "backlog" || !epic.HasChildren {
		t.Errorf("epic entry = %+v, want the COD-10 backlog epic", epic)
	}
	if epic.Labels == nil {
		t.Error("labels serialized as null, want an empty array")
	}
	child := out.Items[1]
	if child.Parent != "COD-10" || !child.Ready || child.Group != "unstarted" {
		t.Errorf("child entry = %+v, want ready unstarted child of COD-10", child)
	}
}

func TestBacklogUnknownRepo(t *testing.T) {
	ts := backlogServer(t, &fakeReader{}, nil)
	res, _ := getBacklog(t, ts, "nope")
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown repo", res.StatusCode)
	}
}

func TestBacklogWithoutCredentials(t *testing.T) {
	ts := backlogServer(t, nil, tracker.ErrReaderUnavailable)
	res, _ := getBacklog(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 when the repo has no direct tracker credentials", res.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body["error"] == "" {
		t.Error("422 body missing a config hint")
	}
}

func TestBacklogTrackerFailureIsBadGateway(t *testing.T) {
	ts := backlogServer(t, &fakeReader{err: errors.New("linear: 500")}, nil)
	res, _ := getBacklog(t, ts, "acme")
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 when the tracker API errors", res.StatusCode)
	}
}

func TestBacklogRejectsNonGET(t *testing.T) {
	ts := backlogServer(t, &fakeReader{}, nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/backlog", map[string]string{})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", res.StatusCode)
	}
}

func TestBacklogRequiresTokenWhenExposed(t *testing.T) {
	s := New("1.2.3", "0.0.0.0", "s3cret", nil, false)
	fake := &fakeReader{}
	s.newReader = func(config.Config) (tracker.Reader, error) { return fake, nil }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/backlog")
	if err != nil {
		t.Fatalf("GET backlog: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated backlog = %d, want 401 on an exposed bind", res.StatusCode)
	}
}
