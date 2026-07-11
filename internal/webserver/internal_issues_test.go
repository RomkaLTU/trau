package webserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

// internalIssueServer builds a hub with one known repo ("acme") and no tracker
// wiring, plus a second store handle at the same home so a test can read what the
// handlers wrote. When allowlist is set the repo root is placed on the workspace
// allowlist so the queue endpoints accept it.
func internalIssueServer(t *testing.T, allowlist bool) (*httptest.Server, string, *hubstore.Issues) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))
	var workspace []string
	if allowlist {
		workspace = []string{root}
	}
	s := New("1.2.3", "127.0.0.1", "", workspace, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, root, testStoresAt(t, home).Issues()
}

func patchJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", url, err)
	}
	return res
}

func createInternal(t *testing.T, ts *httptest.Server, repo string, body InternalIssueRequest) (*http.Response, InternalIssueResponse) {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/issues/internal", body)
	var out InternalIssueResponse
	if res.StatusCode == http.StatusCreated {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode created issue: %v", err)
		}
	}
	return res, out
}

func TestCreateInternalIssuePersistsAndAppearsOnBacklog(t *testing.T) {
	ts, root, store := internalIssueServer(t, false)
	res, out := createInternal(t, ts, "acme", InternalIssueRequest{
		Title: "Write docs", Description: "body", State: "started", Labels: []string{"ready-for-agent"},
	})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	if out.ID != "ACME-1" || out.Source != "internal" || out.State != "started" {
		t.Fatalf("created = %+v, want ACME-1 internal in the started state", out)
	}

	items, err := store.Backlog(root)
	if err != nil {
		t.Fatalf("backlog: %v", err)
	}
	if len(items) != 1 || items[0].Identifier != "ACME-1" || items[0].Source != "internal" {
		t.Fatalf("backlog = %+v, want the new internal issue immediately", items)
	}
}

func TestCreateInternalIssueRequiresTitle(t *testing.T) {
	ts, _, _ := internalIssueServer(t, false)
	res, _ := createInternal(t, ts, "acme", InternalIssueRequest{Title: "   "})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a blank title", res.StatusCode)
	}
}

func TestUpdateInternalIssueEditsContent(t *testing.T) {
	ts, _, _ := internalIssueServer(t, false)
	_, created := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Old", State: "backlog"})

	url := ts.URL + APIPrefix + "/repos/acme/issues/internal/" + created.ID
	res := patchJSON(t, url, InternalIssueRequest{Title: "New", Description: "d", State: "done", Labels: []string{"x"}})
	if res.StatusCode != http.StatusOK {
		_ = res.Body.Close()
		t.Fatalf("patch status = %d, want 200", res.StatusCode)
	}
	var updated InternalIssueResponse
	if err := json.NewDecoder(res.Body).Decode(&updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_ = res.Body.Close()
	if updated.Title != "New" || updated.State != "done" {
		t.Fatalf("updated = %+v, want the edited title and state", updated)
	}

	g, err := http.Get(url)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var got InternalIssueResponse
	if err := json.NewDecoder(g.Body).Decode(&got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	_ = g.Body.Close()
	if got.Title != "New" || got.Description != "d" || got.State != "done" {
		t.Fatalf("got = %+v, want the persisted edit", got)
	}
}

func TestUpdateInternalIssueRejectsSyncedTicket(t *testing.T) {
	ts, root, store := internalIssueServer(t, false)
	if _, _, err := store.Upsert(root, "linear", []hubstore.Issue{{Identifier: "COD-1", Title: "Synced"}}); err != nil {
		t.Fatalf("seed synced: %v", err)
	}
	res := patchJSON(t, ts.URL+APIPrefix+"/repos/acme/issues/internal/COD-1", InternalIssueRequest{Title: "hijack"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — a synced ticket is not an internal issue", res.StatusCode)
	}
}

func TestGetInternalIssueNotFound(t *testing.T) {
	ts, _, _ := internalIssueServer(t, false)
	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/issues/internal/ACME-99")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown internal issue", res.StatusCode)
	}
}

func TestEnqueueInternalIssueSkipsTracker(t *testing.T) {
	ts, _, _ := internalIssueServer(t, true)

	_, created := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Runnable"})
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{ID: created.ID})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("enqueue status = %d, want 201 — an internal issue queues without a tracker", res.StatusCode)
	}
	var q QueueResponse
	if err := json.NewDecoder(res.Body).Decode(&q); err != nil {
		t.Fatalf("decode queue: %v", err)
	}
	if len(q.Items) != 1 || q.Items[0].ID != created.ID || q.Items[0].Kind != "ticket" {
		t.Fatalf("queue = %+v, want the internal issue queued as a ticket", q.Items)
	}
}
