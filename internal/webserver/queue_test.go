package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/queue"
)

// queueServer builds a server whose allowlist holds one Registered repo whose
// base name resolves it, with a fake supervisor standing in for the epic
// preview capture. It returns the fake, the repo root, and the server.
func queueServer(t *testing.T, name string) (*fakeSupervisor, string, *httptest.Server) {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	s := New("1.2.3", "127.0.0.1", "", []string{root}, false)
	s.home = t.TempDir()
	fake := &fakeSupervisor{}
	s.sup = fake
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return fake, root, ts
}

func getQueue(t *testing.T, ts *httptest.Server, repo string) (*http.Response, QueueResponse) {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/queue")
	if err != nil {
		t.Fatalf("GET queue: %v", err)
	}
	var out QueueResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode queue: %v", err)
		}
	}
	return res, out
}

func TestQueueViewEmpty(t *testing.T) {
	_, _, ts := queueServer(t, "acme")
	res, out := getQueue(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if out.Repo != "acme" {
		t.Errorf("repo = %q, want acme", out.Repo)
	}
	if out.Items == nil {
		t.Error("items serialized as null, want an empty array")
	}
	if len(out.Items) != 0 {
		t.Errorf("items = %d, want 0 for a fresh queue", len(out.Items))
	}
}

func TestEnqueueTicket(t *testing.T) {
	_, _, ts := queueServer(t, "acme")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "ticket", ID: "  COD-11  ", Title: "A ticket"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	var out QueueResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(out.Items))
	}
	item := out.Items[0]
	if item.Position != 1 || item.Kind != "ticket" || item.ID != "COD-11" {
		t.Errorf("item = %+v, want the COD-11 run-once at position 1", item)
	}
	if item.Title != "A ticket" {
		t.Errorf("title = %q, want %q", item.Title, "A ticket")
	}
	if item.Status != "pending" {
		t.Errorf("status = %q, want pending", item.Status)
	}
	if item.QueuedAt == "" {
		t.Error("queued_at not stamped")
	}

	_, view := getQueue(t, ts, "acme")
	if len(view.Items) != 1 || view.Items[0].ID != "COD-11" {
		t.Errorf("queue view = %+v, want the COD-11 item", view.Items)
	}
}

func TestEnqueueEpicCarriesSubIssues(t *testing.T) {
	fake, root, ts := queueServer(t, "acme")
	fake.captureOut = []byte(`[{"id":"COD-12","title":"First","state":"todo"},{"id":"COD-13","title":"Second","state":"done"}]`)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "epic", ID: "COD-10", Title: "An epic"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	var out QueueResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(out.Items))
	}
	epic := out.Items[0]
	if epic.Kind != "epic" || epic.ID != "COD-10" {
		t.Errorf("item = %+v, want the COD-10 epic", epic)
	}
	if len(epic.SubIssues) != 2 {
		t.Fatalf("sub_issues = %d, want 2 carried from the preview", len(epic.SubIssues))
	}
	if epic.SubIssues[0].ID != "COD-12" || epic.SubIssues[1].State != "done" {
		t.Errorf("sub_issues = %+v, want COD-12/todo then COD-13/done", epic.SubIssues)
	}
	if len(fake.captures) != 1 {
		t.Fatalf("captures = %d, want 1 epic preview", len(fake.captures))
	}
	assertArgs(t, fake.captures[0].Args, []string{"--repo", root, "--list-epic", "COD-10", "--json", "--no-tui"})
}

func TestEnqueueEpicPreviewFailureIsBadGateway(t *testing.T) {
	fake, _, ts := queueServer(t, "acme")
	fake.captureErr = errStub("boom")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "epic", ID: "COD-10"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 when the epic preview fails", res.StatusCode)
	}
	if _, out := getQueue(t, ts, "acme"); len(out.Items) != 0 {
		t.Errorf("items = %d, want 0 — a failed preview must not queue the epic", len(out.Items))
	}
}

func TestEnqueueDedupeRefused(t *testing.T) {
	_, _, ts := queueServer(t, "acme")

	first := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "ticket", ID: "COD-11"})
	_ = first.Body.Close()
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first enqueue = %d, want 201", first.StatusCode)
	}

	again := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "ticket", ID: "COD-11"})
	defer func() { _ = again.Body.Close() }()
	if again.StatusCode != http.StatusConflict {
		t.Fatalf("re-enqueue = %d, want 409", again.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(again.Body).Decode(&body)
	if body["error"] == "" {
		t.Error("409 body missing a clear message")
	}
	if _, out := getQueue(t, ts, "acme"); len(out.Items) != 1 {
		t.Errorf("items = %d, want 1 — the dupe must not be appended", len(out.Items))
	}
}

func TestEnqueueRefusedForObserveOnlyRepo(t *testing.T) {
	_, _, ts := queueServer(t, "acme")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/stranger/queue", QueueRequest{Kind: "ticket", ID: "COD-1"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an observe-only repo", res.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body["error"] == "" {
		t.Error("403 body missing an observe-only hint")
	}
}

func TestEnqueueRejectsBadInput(t *testing.T) {
	_, _, ts := queueServer(t, "acme")
	tests := []struct {
		name string
		req  QueueRequest
	}{
		{name: "malformed id", req: QueueRequest{Kind: "ticket", ID: "not-a-ticket!"}},
		{name: "empty id", req: QueueRequest{Kind: "ticket", ID: "  "}},
		{name: "unknown kind", req: QueueRequest{Kind: "story", ID: "COD-1"}},
		{name: "empty kind", req: QueueRequest{Kind: "", ID: "COD-1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", tc.req)
			_ = res.Body.Close()
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", res.StatusCode)
			}
		})
	}
}

func TestDequeueRemovesItem(t *testing.T) {
	_, _, ts := queueServer(t, "acme")
	for _, id := range []string{"COD-1", "COD-2", "COD-3"} {
		res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "ticket", ID: id})
		_ = res.Body.Close()
	}

	res, body := deleteReq(t, ts, APIPrefix+"/repos/acme/queue/COD-2")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200 (body %q)", res.StatusCode, body)
	}
	var out QueueResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Items) != 2 || out.Items[0].ID != "COD-1" || out.Items[1].ID != "COD-3" {
		t.Errorf("remaining = %+v, want COD-1 then COD-3 with order kept", out.Items)
	}
	if out.Items[1].Position != 2 {
		t.Errorf("position = %d, want the surviving items renumbered", out.Items[1].Position)
	}
}

func TestDequeueUnknownItem(t *testing.T) {
	_, _, ts := queueServer(t, "acme")
	res, _ := deleteReq(t, ts, APIPrefix+"/repos/acme/queue/COD-404")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an item not in the queue", res.StatusCode)
	}
}

// TestDequeueRunningRefused proves the backend, not just the disabled UI button,
// rejects removing an item the hub is draining — so a Remove that races the
// drainer promoting the item to running cannot orphan the just-spawned child.
func TestDequeueRunningRefused(t *testing.T) {
	_, root, ts := queueServer(t, "acme")
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "ticket", ID: "COD-1"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("enqueue = %d, want 201", res.StatusCode)
	}
	if err := queue.NewStore(root).MarkRunning("COD-1", 4242); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	del, body := deleteReq(t, ts, APIPrefix+"/repos/acme/queue/COD-1")
	if del.StatusCode != http.StatusConflict {
		t.Fatalf("delete running = %d, want 409 (body %q)", del.StatusCode, body)
	}
	if _, out := getQueue(t, ts, "acme"); len(out.Items) != 1 || out.Items[0].ID != "COD-1" {
		t.Errorf("queue = %+v, want the running COD-1 kept", out.Items)
	}
}

// TestQueuePersistsAcrossServers proves the queue survives a serve restart: a
// second server, sharing only the repo root on disk, reads the item the first
// one registered.
func TestQueuePersistsAcrossServers(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	first := New("1.2.3", "127.0.0.1", "", []string{root}, false)
	first.home = t.TempDir()
	first.sup = &fakeSupervisor{}
	ts1 := httptest.NewServer(first.Handler())
	defer ts1.Close()

	res := postJSON(t, ts1.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "ticket", ID: "COD-11"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("enqueue = %d, want 201", res.StatusCode)
	}

	second := New("1.2.3", "127.0.0.1", "", []string{root}, false)
	second.home = t.TempDir()
	second.sup = &fakeSupervisor{}
	ts2 := httptest.NewServer(second.Handler())
	defer ts2.Close()

	res2, out := getQueue(t, ts2, "acme")
	defer func() { _ = res2.Body.Close() }()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("view on the restarted hub = %d, want 200", res2.StatusCode)
	}
	if len(out.Items) != 1 || out.Items[0].ID != "COD-11" {
		t.Errorf("restarted queue = %+v, want the persisted COD-11", out.Items)
	}
}

func TestQueueViewUnknownRepo(t *testing.T) {
	_, _, ts := queueServer(t, "acme")
	res, _ := getQueue(t, ts, "nope")
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown repo", res.StatusCode)
	}
}

func TestQueueRejectsUnsupportedMethod(t *testing.T) {
	_, _, ts := queueServer(t, "acme")
	req, err := http.NewRequest(http.MethodPut, ts.URL+APIPrefix+"/repos/acme/queue", nil)
	if err != nil {
		t.Fatalf("new PUT: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT queue: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("PUT status = %d, want 405", res.StatusCode)
	}
}

func TestQueueRequiresTokenWhenExposed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	s := New("1.2.3", "0.0.0.0", "s3cret", []string{root}, false)
	s.sup = &fakeSupervisor{}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "ticket", ID: "COD-1"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated enqueue = %d, want 401 on an exposed bind", res.StatusCode)
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }
