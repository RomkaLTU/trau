package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// queueServer builds a server whose allowlist holds one Registered repo whose
// base name resolves it, with a fake supervisor standing in for the epic
// preview capture. Its tracker reader is stubbed unavailable so enqueue's
// best-effort ticket validation stays hermetic — the repo resolves but carries
// no credentials, the fresh-registration case. It returns the fake, the repo
// root, and the server.
func queueServer(t *testing.T, name string) (*fakeSupervisor, string, *httptest.Server) {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	s := New("1.2.3", "127.0.0.1", "", []string{root}, false, testStores(t))
	s.home = t.TempDir()
	s.newReader = func(config.Config) (tracker.Reader, error) { return nil, tracker.ErrReaderUnavailable }
	fake := &fakeSupervisor{}
	s.sup = fake
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return fake, root, ts
}

// queueTrackerServer builds a hub with one Registered repo bound to a Linear team
// whose tracker reader answers from fake, so enqueue's tracker validation runs for
// real rather than degrading to the uncredentialed pass.
func queueTrackerServer(t *testing.T, fake tracker.Reader) *httptest.Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))
	writeRepoINI(t, root, "LINEAR_TEAM=COD\n")
	s := New("1.2.3", "127.0.0.1", "", []string{root}, false, testStoresAt(t, home))
	s.home = home
	s.newReader = func(config.Config) (tracker.Reader, error) { return fake, nil }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
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

// TestEnqueueAutoDetectsKind proves a request that omits kind is resolved by
// listing the id's children: no children makes it a ticket, any child makes it
// an epic carrying them — so the Loop card can add a bare id.
func TestEnqueueAutoDetectsKind(t *testing.T) {
	t.Run("no children is a ticket", func(t *testing.T) {
		_, _, ts := queueServer(t, "acme")
		res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{ID: "COD-1"})
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201", res.StatusCode)
		}
		var out QueueResponse
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(out.Items) != 1 || out.Items[0].Kind != "ticket" {
			t.Errorf("item = %+v, want a ticket", out.Items)
		}
	})
	t.Run("children make it an epic", func(t *testing.T) {
		fake, _, ts := queueServer(t, "acme")
		fake.captureOut = []byte(`[{"id":"COD-12","title":"First","state":"todo"}]`)
		res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{ID: "COD-10"})
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201", res.StatusCode)
		}
		var out QueueResponse
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(out.Items) != 1 || out.Items[0].Kind != "epic" || len(out.Items[0].SubIssues) != 1 {
			t.Errorf("item = %+v, want an epic carrying its one sub-issue", out.Items)
		}
	})
}

// TestQueueMoveReorders drives the reorder endpoint: a valid move returns the
// reordered queue, an unknown id 404s, and a bad direction 400s.
func TestEnqueueInternalTicketCarriesInternalSource(t *testing.T) {
	ts, _, _ := internalIssueServer(t, true)

	_, created := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Runnable"})
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{ID: created.ID})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}

	_, view := getQueue(t, ts, "acme")
	if len(view.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(view.Items))
	}
	if view.Items[0].Source != "internal" {
		t.Errorf("source = %q, want internal — the queue must badge an internal item after a reload", view.Items[0].Source)
	}
}

func TestEnqueueInternalEpicCarriesSourceAndSubIssues(t *testing.T) {
	ts, _, _ := internalIssueServer(t, true)

	_, epic := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Internal epic"})
	_, first := createInternal(t, ts, "acme", InternalIssueRequest{Title: "First child", Parent: epic.ID})
	_, second := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Second child", Parent: epic.ID})

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{ID: epic.ID})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}

	_, view := getQueue(t, ts, "acme")
	if len(view.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(view.Items))
	}
	item := view.Items[0]
	if item.Kind != "epic" || item.Source != "internal" {
		t.Errorf("kind/source = %q/%q, want epic/internal", item.Kind, item.Source)
	}
	if len(item.SubIssues) != 2 {
		t.Fatalf("sub_issues = %+v, want the epic's two internal children", item.SubIssues)
	}
	if item.SubIssues[0].ID != first.ID || item.SubIssues[1].ID != second.ID {
		t.Errorf("sub_issues = %+v, want %s then %s", item.SubIssues, first.ID, second.ID)
	}
}

func TestEnqueueTrackerTicketCarriesTrackerSource(t *testing.T) {
	ts := queueTrackerServer(t, &fakeReader{issue: tracker.IssueSummary{
		BacklogItem: tracker.BacklogItem{ID: "COD-11", Title: "A tracker ticket"},
		Project:     "trau",
		InProject:   true,
	}})

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "ticket", ID: "COD-11"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}

	_, view := getQueue(t, ts, "acme")
	if len(view.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(view.Items))
	}
	item := view.Items[0]
	if item.Source != "linear" {
		t.Errorf("source = %q, want linear — a tracker id carries its tracker's source, not internal", item.Source)
	}
	if item.Title != "A tracker ticket" {
		t.Errorf("title = %q, want the title resolved from the tracker", item.Title)
	}
}

func TestEnqueueCrossProjectTicketRefused(t *testing.T) {
	ts := queueTrackerServer(t, &fakeReader{issue: tracker.IssueSummary{
		BacklogItem: tracker.BacklogItem{ID: "COD-11", Title: "Someone else's ticket"},
		Project:     "other",
		InProject:   false,
	}})

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "ticket", ID: "COD-11"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — a cross-project tracker ticket is still refused", res.StatusCode)
	}

	_, view := getQueue(t, ts, "acme")
	if len(view.Items) != 0 {
		t.Errorf("items = %+v, want the refused ticket left unqueued", view.Items)
	}
}

func TestQueueMoveReorders(t *testing.T) {
	_, _, ts := queueServer(t, "acme")
	for _, id := range []string{"COD-1", "COD-2", "COD-3"} {
		res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "ticket", ID: id})
		_ = res.Body.Close()
	}

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue/COD-3/move", MoveRequest{Dir: -1})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("move status = %d, want 200", res.StatusCode)
	}
	var out QueueResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Items) != 3 || out.Items[1].ID != "COD-3" || out.Items[2].ID != "COD-2" {
		t.Errorf("order = %+v, want COD-1 COD-3 COD-2 after moving COD-3 up", out.Items)
	}

	miss := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue/COD-9/move", MoveRequest{Dir: -1})
	_ = miss.Body.Close()
	if miss.StatusCode != http.StatusNotFound {
		t.Errorf("move absent = %d, want 404", miss.StatusCode)
	}
	bad := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue/COD-1/move", MoveRequest{Dir: 0})
	_ = bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("move dir 0 = %d, want 400", bad.StatusCode)
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
	root := filepath.Join(t.TempDir(), "acme")
	s := New("1.2.3", "127.0.0.1", "", []string{root}, false, testStores(t))
	s.home = t.TempDir()
	s.newReader = func(config.Config) (tracker.Reader, error) { return nil, tracker.ErrReaderUnavailable }
	s.sup = &fakeSupervisor{}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "ticket", ID: "COD-1"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("enqueue = %d, want 201", res.StatusCode)
	}
	if err := s.stores.Queue(root).MarkRunning("COD-1", 4242); err != nil {
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
// second server, sharing the same hub database, reads the item the first one
// registered.
func TestQueuePersistsAcrossServers(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "acme")
	first := New("1.2.3", "127.0.0.1", "", []string{root}, false, testStoresAt(t, home))
	first.home = home
	first.newReader = func(config.Config) (tracker.Reader, error) { return nil, tracker.ErrReaderUnavailable }
	first.sup = &fakeSupervisor{}
	ts1 := httptest.NewServer(first.Handler())
	defer ts1.Close()

	res := postJSON(t, ts1.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "ticket", ID: "COD-11"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("enqueue = %d, want 201", res.StatusCode)
	}

	second := New("1.2.3", "127.0.0.1", "", []string{root}, false, testStoresAt(t, home))
	second.home = home
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
	s := New("1.2.3", "0.0.0.0", "s3cret", []string{root}, false, testStores(t))
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
