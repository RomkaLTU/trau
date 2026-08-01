package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/tracker"
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

func ptr[T any](v T) *T { return &v }

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

func TestTransitionInternalIssueAppliesStatusAndLabels(t *testing.T) {
	ts, _, _ := internalIssueServer(t, false)
	_, created := createInternal(t, ts, "acme", InternalIssueRequest{
		Title: "Runnable", State: "unstarted", Labels: []string{"ready-for-agent"},
	})

	url := ts.URL + APIPrefix + "/repos/acme/issues/internal/" + created.ID + "/transition"
	res := postJSON(t, url, InternalTransitionRequest{
		State:        "started",
		AddLabels:    []string{"needs-human"},
		RemoveLabels: []string{"ready-for-agent"},
		Comment:      "Trau loop stopped.",
	})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out InternalIssueResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.State != "started" || len(out.Labels) != 1 || out.Labels[0] != "needs-human" {
		t.Fatalf("transitioned = %+v, want started with only needs-human", out)
	}
}

func TestTransitionInternalIssueRejectsSyncedTicket(t *testing.T) {
	ts, root, store := internalIssueServer(t, false)
	if _, _, err := store.Upsert(root, "linear", []hubstore.Issue{{Identifier: "COD-1", Title: "Synced"}}); err != nil {
		t.Fatalf("seed synced: %v", err)
	}
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/issues/internal/COD-1/transition", InternalTransitionRequest{State: "done"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — a synced ticket is not transitioned here", res.StatusCode)
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

func TestInternalIssueRoundTripsPriorityDueDateAndRelations(t *testing.T) {
	ts, _, _ := internalIssueServer(t, false)
	_, blocker := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Blocker"})
	res, created := createInternal(t, ts, "acme", InternalIssueRequest{
		Title:     "Dependent",
		Priority:  ptr(2),
		DueDate:   ptr("2026-08-14"),
		BlockedBy: []string{blocker.ID},
	})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	if created.Priority != 2 || created.DueDate != "2026-08-14" {
		t.Fatalf("created = %+v, want priority 2 due 2026-08-14", created)
	}
	if !reflect.DeepEqual(created.BlockedBy, []string{blocker.ID}) {
		t.Fatalf("created blocked_by = %v, want [%s]", created.BlockedBy, blocker.ID)
	}

	got := getInternalIssueResponse(t, ts, created.ID)
	if got.Priority != 2 || got.DueDate != "2026-08-14" || !reflect.DeepEqual(got.BlockedBy, []string{blocker.ID}) {
		t.Fatalf("GET = %+v, want the created priority, due date and blocker", got)
	}

	patched := patchJSON(t, ts.URL+APIPrefix+"/repos/acme/issues/internal/"+created.ID, InternalIssueRequest{
		Title: "Dependent", Priority: ptr(4), DueDate: ptr("2026-09-30"),
	})
	defer func() { _ = patched.Body.Close() }()
	if patched.StatusCode != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200", patched.StatusCode)
	}
	var edited InternalIssueResponse
	if err := json.NewDecoder(patched.Body).Decode(&edited); err != nil {
		t.Fatalf("decode patched issue: %v", err)
	}
	if edited.Priority != 4 || edited.DueDate != "2026-09-30" {
		t.Fatalf("patched = %+v, want priority 4 due 2026-09-30", edited)
	}
	if !reflect.DeepEqual(edited.BlockedBy, []string{blocker.ID}) {
		t.Fatalf("patched blocked_by = %v, want the edge to survive an edit that names none", edited.BlockedBy)
	}

	form := patchJSON(t, ts.URL+APIPrefix+"/repos/acme/issues/internal/"+created.ID, InternalIssueRequest{
		Title: "Dependent", Description: "d", State: "started", Labels: []string{"x"},
	})
	defer func() { _ = form.Body.Close() }()
	if form.StatusCode != http.StatusOK {
		t.Fatalf("form PATCH status = %d, want 200", form.StatusCode)
	}
	kept := getInternalIssueResponse(t, ts, created.ID)
	if kept.Priority != 4 || kept.DueDate != "2026-09-30" {
		t.Fatalf("after an edit naming neither = %+v, want priority 4 due 2026-09-30 untouched", kept)
	}
}

func TestCreateInternalIssueRejectsBadDueDateAndUnknownBlocker(t *testing.T) {
	ts, _, _ := internalIssueServer(t, false)
	res, _ := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Bad date", DueDate: ptr("14/08/2026")})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("due date status = %d, want 400", res.StatusCode)
	}
	missing, _ := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Bad blocker", BlockedBy: []string{"ACME-99"}})
	defer func() { _ = missing.Body.Close() }()
	if missing.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown blocker status = %d, want 400", missing.StatusCode)
	}
	if msg := decodeError(t, missing); !strings.Contains(msg, "ACME-99") {
		t.Errorf("error = %q, want it to name the missing blocker", msg)
	}
}

func TestInternalProviderSkipsBlockedIssueUntilBlockerIsDone(t *testing.T) {
	ts, _, _ := internalIssueServer(t, false)
	ready := []string{"ready-for-agent"}
	_, blocker := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Blocker", State: "unstarted", Labels: ready})
	_, dependent := createInternal(t, ts, "acme", InternalIssueRequest{
		Title: "Dependent", State: "unstarted", Labels: ready, BlockedBy: []string{blocker.ID},
	})

	provider := &tracker.Internal{
		Hub:             hubclient.New(ts.URL, ""),
		Repo:            "acme",
		ReadyLabel:      "ready-for-agent",
		QuarantineLabel: "needs-human",
	}
	ctx := context.Background()
	picked, err := provider.Pick(ctx, tracker.Scope{})
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if picked != blocker.ID {
		t.Fatalf("pick = %q, want the blocker %s — the dependent is blocked", picked, blocker.ID)
	}
	if err := provider.SetStatus(ctx, blocker.ID, tracker.StageDone, ""); err != nil {
		t.Fatalf("finish blocker: %v", err)
	}
	picked, err = provider.Pick(ctx, tracker.Scope{})
	if err != nil {
		t.Fatalf("pick after unblock: %v", err)
	}
	if picked != dependent.ID {
		t.Fatalf("pick = %q, want %s once its blocker is done", picked, dependent.ID)
	}
}

func getInternalIssueResponse(t *testing.T, ts *httptest.Server, id string) InternalIssueResponse {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/issues/internal/" + id)
	if err != nil {
		t.Fatalf("GET %s: %v", id, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", id, res.StatusCode)
	}
	var out InternalIssueResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	return out
}
