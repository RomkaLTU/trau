package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/queue"
)

func batchURL(ts *httptest.Server, repo string, rest ...string) string {
	return ts.URL + APIPrefix + "/repos/" + repo + "/queue/batches" + strings.Join(rest, "")
}

// createBatch files a batch over the REST surface and answers with the status and
// the refusal reason, which is empty on success.
func createBatch(t *testing.T, ts *httptest.Server, repo, name string, ids ...string) (*http.Response, string) {
	t.Helper()
	res := postJSON(t, batchURL(ts, repo), BatchRequest{Name: name, IDs: ids})
	return res, errorReason(t, res)
}

func errorReason(t *testing.T, res *http.Response) string {
	t.Helper()
	if res.StatusCode == http.StatusOK {
		return ""
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body.Error
}

func TestCreateBatchAnswersWithTheLabelledQueue(t *testing.T) {
	s, _, root, ts := queueHub(t, "acme")
	seedQueue(t, s, root, false,
		queue.Item{Kind: queue.KindTicket, ID: "COD-1"},
		queue.Item{Kind: queue.KindTicket, ID: "COD-2"},
	)

	res, _ := createBatch(t, ts, "acme", "Wave one", "COD-2")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out QueueResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode queue: %v", err)
	}
	if len(out.Batches) != 1 || out.Batches[0].ID != "wave-one" || out.Batches[0].Name != "Wave one" {
		t.Fatalf("batches = %+v, want the new batch listed", out.Batches)
	}
	if out.Batches[0].CreatedAt == "" {
		t.Error("batch carries no created_at, want the stamp surfaces fall back to")
	}
	if out.Items[0].Batch != "" || out.Items[1].Batch != "wave-one" {
		t.Errorf("item batches = %q/%q, want only COD-2 labelled", out.Items[0].Batch, out.Items[1].Batch)
	}
	if out.DrainingBatch != "" {
		t.Errorf("draining_batch = %q, want empty while nothing is draining", out.DrainingBatch)
	}
}

func TestCreateBatchRefusals(t *testing.T) {
	cases := []struct {
		name   string
		ids    []string
		status int
		reason string
	}{
		{name: "not queued", ids: []string{"COD-9"}, status: http.StatusConflict, reason: "COD-9"},
		{name: "already settled", ids: []string{"COD-3"}, status: http.StatusConflict, reason: "COD-3 is done"},
		{name: "already batched", ids: []string{"COD-1"}, status: http.StatusConflict, reason: "COD-1 is already in batch held"},
		{name: "no ids", ids: nil, status: http.StatusBadRequest, reason: "at least one queued item"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, root, ts := queueHub(t, "acme")
			seedQueue(t, s, root, false,
				queue.Item{Kind: queue.KindTicket, ID: "COD-1"},
				queue.Item{Kind: queue.KindTicket, ID: "COD-2"},
				queue.Item{Kind: queue.KindTicket, ID: "COD-3", Status: queue.StatusDone},
			)
			seedBatch(t, s, root, "held", "COD-1")

			res, reason := createBatch(t, ts, "acme", "wanted", tc.ids...)
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.status)
			}
			if !strings.Contains(reason, tc.reason) {
				t.Errorf("reason = %q, want it to name %q", reason, tc.reason)
			}
			if batches, _ := s.stores.Queue(root).Batches(); len(batches) != 1 {
				t.Errorf("batches = %d, want the refused create to file nothing", len(batches))
			}
		})
	}
}

func TestCreateBatchRefusedForObserveOnlyRepo(t *testing.T) {
	_, _, ts := queueServer(t, "acme")
	res, _ := createBatch(t, ts, "stranger", "wave", "COD-1")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an observe-only repo", res.StatusCode)
	}
}

func TestEditBatchRenamesAndMovesMembership(t *testing.T) {
	s, _, root, ts := queueHub(t, "acme")
	seedQueue(t, s, root, false,
		queue.Item{Kind: queue.KindTicket, ID: "COD-1"},
		queue.Item{Kind: queue.KindTicket, ID: "COD-2"},
		queue.Item{Kind: queue.KindTicket, ID: "COD-3", Status: queue.StatusDone},
	)
	bid := seedBatch(t, s, root, "wave", "COD-1")

	res := patchJSON(t, batchURL(ts, "acme", "/", bid), BatchEditRequest{
		Name:   ptr("Wave two"),
		Add:    []string{"COD-2"},
		Remove: []string{"COD-1"},
	})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out QueueResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode queue: %v", err)
	}
	if out.Batches[0].Name != "Wave two" {
		t.Errorf("name = %q, want the rename", out.Batches[0].Name)
	}
	if out.Items[0].Batch != "" || out.Items[1].Batch != bid {
		t.Errorf("item batches = %q/%q, want membership moved from COD-1 to COD-2", out.Items[0].Batch, out.Items[1].Batch)
	}

	res = patchJSON(t, batchURL(ts, "acme", "/", bid), BatchEditRequest{Add: []string{"COD-3"}})
	reason := errorReason(t, res)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusConflict || !strings.Contains(reason, "COD-3 is done") {
		t.Fatalf("add settled = %d %q, want 409 naming it", res.StatusCode, reason)
	}

	res = patchJSON(t, batchURL(ts, "acme", "/ghost"), BatchEditRequest{Name: ptr("x")})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown batch", res.StatusCode)
	}
}

func TestDismissBatchLeavesTheWorkAlone(t *testing.T) {
	s, _, root, ts := queueHub(t, "acme")
	seedQueue(t, s, root, false,
		queue.Item{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusRunning, PID: 4242},
		queue.Item{Kind: queue.KindTicket, ID: "COD-2"},
	)
	bid := seedBatch(t, s, root, "wave", "COD-2")

	res, _ := deleteReq(t, ts, APIPrefix+"/repos/acme/queue/batches/"+bid)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusRunning {
		t.Errorf("COD-1 = %q, want its run untouched by the dismiss", got)
	}
	if got := statusOf(t, s, root, "COD-2"); got != queue.StatusPending {
		t.Errorf("COD-2 = %q, want it still pending", got)
	}
	if batches, _ := s.stores.Queue(root).Batches(); len(batches) != 0 {
		t.Errorf("batches = %d, want the grouping dissolved", len(batches))
	}

	res, _ = deleteReq(t, ts, APIPrefix+"/repos/acme/queue/batches/"+bid)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 dismissing it twice", res.StatusCode)
	}
}

func TestStartBatchArmsTheScopedDrain(t *testing.T) {
	s, _, root, ts := runOneServer(t)
	seedQueue(t, s, root, false,
		queue.Item{Kind: queue.KindTicket, ID: "COD-1"},
		queue.Item{Kind: queue.KindTicket, ID: "COD-2"},
	)
	bid := seedBatch(t, s, root, "wave", "COD-2")

	res := postJSON(t, batchURL(ts, "acme", "/", bid, "/start"), BatchStartRequest{OnFault: queue.OnFaultSkip})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out QueueResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode queue: %v", err)
	}
	if !out.Draining || out.DrainingBatch != bid {
		t.Fatalf("view = draining %v batch %q, want the run labelled with the batch", out.Draining, out.DrainingBatch)
	}
	_, meta, err := s.stores.Queue(root).Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if meta.OnFault != queue.OnFaultSkip {
		t.Errorf("on_fault = %q, want the normalized knob stored", meta.OnFault)
	}
	s.drain.mu.Lock()
	_, active := s.drain.active[root]
	s.drain.mu.Unlock()
	if !active {
		t.Error("the start did not launch a drain loop for the repo")
	}
}

func TestStartBatchGuards(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, s *Server, root string)
		bid    string
		status int
		reason string
	}{
		{
			name: "draining queue",
			setup: func(t *testing.T, s *Server, root string) {
				if err := s.stores.Queue(root).Arm(false, queue.OnFaultHalt); err != nil {
					t.Fatalf("arm: %v", err)
				}
			},
			status: http.StatusConflict,
			reason: "the queue is draining",
		},
		{
			name: "another item running",
			setup: func(t *testing.T, s *Server, root string) {
				if err := s.stores.Queue(root).MarkRunning("COD-1", 4242); err != nil {
					t.Fatalf("mark running: %v", err)
				}
			},
			status: http.StatusConflict,
			reason: "COD-1 is already running",
		},
		{
			name: "live instance in the repo",
			setup: func(t *testing.T, s *Server, _ string) {
				s.drain.repoLive = func(string) bool { return true }
			},
			status: http.StatusConflict,
			reason: "a loop is already running",
		},
		{
			name:   "unknown batch",
			bid:    "ghost",
			status: http.StatusNotFound,
			reason: "ghost",
		},
		{
			name: "no runnable member",
			setup: func(t *testing.T, s *Server, root string) {
				if err := s.stores.Queue(root).Finish("COD-2", queue.StatusDone, ""); err != nil {
					t.Fatalf("finish: %v", err)
				}
			},
			status: http.StatusConflict,
			reason: "no runnable items",
		},
		{
			name: "member blocked from outside the batch",
			setup: func(t *testing.T, s *Server, root string) {
				if err := s.stores.Issues().AddRelation(root, "COD-1", "COD-2"); err != nil {
					t.Fatalf("add relation: %v", err)
				}
			},
			status: http.StatusConflict,
			reason: "blocked by COD-1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, fake, root, ts := runOneServer(t)
			seedQueue(t, s, root, false,
				queue.Item{Kind: queue.KindTicket, ID: "COD-1"},
				queue.Item{Kind: queue.KindTicket, ID: "COD-2"},
			)
			bid := seedBatch(t, s, root, "wave", "COD-2")
			if tc.setup != nil {
				tc.setup(t, s, root)
			}
			if tc.bid != "" {
				bid = tc.bid
			}

			res := postJSON(t, batchURL(ts, "acme", "/", bid, "/start"), BatchStartRequest{})
			reason := errorReason(t, res)
			_ = res.Body.Close()
			if res.StatusCode != tc.status {
				t.Fatalf("status = %d (%s), want %d", res.StatusCode, reason, tc.status)
			}
			if !strings.Contains(reason, tc.reason) {
				t.Errorf("reason = %q, want it to name %q", reason, tc.reason)
			}
			if got := drainingBatchOf(t, s, root); got != "" {
				t.Errorf("scope = %q, want nothing armed by a refused start", got)
			}
			if len(fake.spawns) != 0 {
				t.Errorf("spawns = %d, want none — a refused start launches nothing", len(fake.spawns))
			}
		})
	}
}

// TestStartBatchAllowsBlockersInsideTheBatch proves the outside-blocker guard is
// exactly that: a member held back by another member is fine, since the drain runs
// the blocker first.
func TestStartBatchAllowsBlockersInsideTheBatch(t *testing.T) {
	s, _, root, ts := runOneServer(t)
	seedQueue(t, s, root, false,
		queue.Item{Kind: queue.KindTicket, ID: "COD-1"},
		queue.Item{Kind: queue.KindTicket, ID: "COD-2"},
	)
	if err := s.stores.Issues().AddRelation(root, "COD-1", "COD-2"); err != nil {
		t.Fatalf("add relation: %v", err)
	}
	bid := seedBatch(t, s, root, "wave", "COD-1", "COD-2")

	res := postJSON(t, batchURL(ts, "acme", "/", bid, "/start"), BatchStartRequest{})
	reason := errorReason(t, res)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200 — the batch carries its own blocker", res.StatusCode, reason)
	}
}

// TestWholeQueueStartClearsTheBatchScope proves Start stays batch-blind: it drops
// whatever scope a batch start left behind and drains everything, members included.
func TestWholeQueueStartClearsTheBatchScope(t *testing.T) {
	s, _, root, ts := runOneServer(t)
	seedQueue(t, s, root, false,
		queue.Item{Kind: queue.KindTicket, ID: "COD-1"},
		queue.Item{Kind: queue.KindTicket, ID: "COD-2"},
	)
	bid := seedBatch(t, s, root, "wave", "COD-2")
	if err := s.stores.Queue(root).ArmBatch(bid, false, queue.OnFaultHalt); err != nil {
		t.Fatalf("arm batch: %v", err)
	}
	if err := s.stores.Queue(root).SetDraining(false); err != nil {
		t.Fatalf("disarm: %v", err)
	}

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue/drain", DrainRequest{Draining: true})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out QueueResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode queue: %v", err)
	}
	if !out.Draining || out.DrainingBatch != "" {
		t.Fatalf("view = draining %v batch %q, want a whole-queue run", out.Draining, out.DrainingBatch)
	}
}

func TestBatchRoutesRejectUnsupportedMethods(t *testing.T) {
	cases := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/repos/acme/queue/batches"},
		{method: http.MethodPost, path: "/repos/acme/queue/batches/wave"},
		{method: http.MethodGet, path: "/repos/acme/queue/batches/wave/start"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			_, _, ts := queueServer(t, "acme")
			req, err := http.NewRequest(tc.method, ts.URL+APIPrefix+tc.path, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", tc.method, err)
			}
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405", res.StatusCode)
			}
		})
	}
}

// TestQueueItemActionsStillRoute guards the wildcard the batch routes forced onto
// the per-item actions: move and run keep working and an action neither names is
// a 404 rather than a misdirected move.
func TestQueueItemActionsStillRoute(t *testing.T) {
	s, _, root, ts := runOneServer(t)
	seedQueue(t, s, root, false,
		queue.Item{Kind: queue.KindTicket, ID: "COD-1"},
		queue.Item{Kind: queue.KindTicket, ID: "COD-2"},
	)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue/COD-2/move", MoveRequest{Dir: -1})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("move = %d, want 200", res.StatusCode)
	}
	if items := snapshot(t, s, root); items[0].ID != "COD-2" {
		t.Errorf("queue head = %q, want COD-2 moved up", items[0].ID)
	}

	res = postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue/COD-2/bogus", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown action = %d, want 404", res.StatusCode)
	}
}
