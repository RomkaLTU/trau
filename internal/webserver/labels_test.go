package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

func getLabels(t *testing.T, ts *httptest.Server, repo string) (*http.Response, LabelsResponse) {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/labels")
	if err != nil {
		t.Fatalf("GET labels: %v", err)
	}
	var out LabelsResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode labels: %v", err)
		}
	}
	return res, out
}

func TestLabelsServesCaseFoldedCounts(t *testing.T) {
	_, ts, root, store := backlogServer(t, nil, nil)
	if _, _, err := store.Upsert(root, "linear", []hubstore.Issue{
		{Identifier: "COD-1", Title: "a", StatusGroup: "backlog", Labels: []string{"Feature", "bug"}},
		{Identifier: "COD-2", Title: "b", StatusGroup: "unstarted", Labels: []string{"feature"}},
		{Identifier: "COD-3", Title: "c", StatusGroup: "started", Labels: []string{"stale"}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := store.Reconcile(root, []string{"COD-1", "COD-2"}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	res, out := getLabels(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	want := []LabelFacet{
		{Name: "bug", Count: 1},
		{Name: "Feature", Count: 2},
	}
	if !reflect.DeepEqual(out.Labels, want) {
		t.Fatalf("labels = %+v, want case-folded groups with counts and the tombstoned stale label excluded %+v", out.Labels, want)
	}
}

func TestLabelsUnknownRepo(t *testing.T) {
	_, ts, _, _ := backlogServer(t, nil, nil)
	res, _ := getLabels(t, ts, "nope")
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown repo", res.StatusCode)
	}
}

func TestLabelsRejectsNonGET(t *testing.T) {
	_, ts, _, _ := backlogServer(t, nil, nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/labels", map[string]string{})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", res.StatusCode)
	}
}
