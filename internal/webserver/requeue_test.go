package webserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/queue"
)

func decodeBody(t *testing.T, res *http.Response, into any) {
	t.Helper()
	defer func() { _ = res.Body.Close() }()
	if err := json.NewDecoder(res.Body).Decode(into); err != nil {
		t.Fatalf("decode body: %v", err)
	}
}

func requeueServer(t *testing.T, name string) (*Server, *fakeSupervisor, string, *httptest.Server) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), name)
	s := New("1.2.3", "127.0.0.1", "", []string{root}, false, testStoresAt(t, home))
	s.home = home
	fake := &fakeSupervisor{}
	s.sup = fake
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, fake, root, ts
}

func postRequeue(t *testing.T, ts *httptest.Server, repo, id string) *http.Response {
	t.Helper()
	res, err := http.Post(ts.URL+APIPrefix+"/repos/"+repo+"/issues/"+id+"/requeue", "application/json", nil)
	if err != nil {
		t.Fatalf("POST requeue: %v", err)
	}
	return res
}

// TestIssueRequeueRepairsEpicSubIssue is the salonradar shape: an epic parked
// unfinalized over a quarantined child. The requeue drives a fresh trau with
// --requeue in the repo root and resets the child's recorded state to todo, so
// the epic stops reporting a quarantine while staying parked and re-runnable.
func TestIssueRequeueRepairsEpicSubIssue(t *testing.T) {
	s, fake, root, ts := requeueServer(t, "acme")
	repo := filepath.Base(root)
	store := s.stores.Queue(root)
	if _, err := store.Add(queue.Item{
		Kind: queue.KindEpic, ID: "COD-10",
		SubIssues: []queue.SubIssue{
			{ID: "COD-11", State: queue.SubIssueQuarantined},
			{ID: "COD-12", State: queue.SubIssueTodo},
		},
	}); err != nil {
		t.Fatalf("Add COD-10: %v", err)
	}
	if err := store.MarkRunning("COD-10", 1); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := store.Pause("COD-10", "epic COD-10 unfinalized — waiting on COD-11"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	res := postRequeue(t, ts, repo, "COD-11")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("requeue = %d, want 200", res.StatusCode)
	}
	var q QueueResponse
	decodeBody(t, res, &q)

	if len(fake.captures) != 1 {
		t.Fatalf("captures = %d, want 1", len(fake.captures))
	}
	spec := fake.captures[0]
	if spec.Dir != root {
		t.Errorf("capture dir = %q, want %q", spec.Dir, root)
	}
	want := []string{"--repo", root, "--requeue", "COD-11", "--no-tui"}
	if strings.Join(spec.Args, " ") != strings.Join(want, " ") {
		t.Errorf("capture args = %v, want %v", spec.Args, want)
	}

	view, found := queueViewByID(q, "COD-10")
	if !found {
		t.Fatalf("COD-10 missing from response queue")
	}
	if view.Status != queue.StatusPaused {
		t.Errorf("epic status = %q, want still paused", view.Status)
	}
	states := map[string]string{}
	for _, sub := range view.SubIssues {
		states[sub.ID] = sub.State
	}
	if states["COD-11"] != queue.SubIssueTodo {
		t.Errorf("COD-11 state = %q, want todo", states["COD-11"])
	}
	if states["COD-12"] != queue.SubIssueTodo {
		t.Errorf("COD-12 state = %q, want untouched todo", states["COD-12"])
	}
}

// TestIssueRequeueRestoresFailedRow covers the standalone ticket: a give-up
// settles its row failed, which Runnable excludes, so without this repair the
// row could never be re-picked from the UI however clean the tracker is.
func TestIssueRequeueRestoresFailedRow(t *testing.T) {
	s, fake, root, ts := requeueServer(t, "acme")
	repo := filepath.Base(root)
	store := s.stores.Queue(root)
	if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-20"}); err != nil {
		t.Fatalf("Add COD-20: %v", err)
	}
	if err := store.MarkRunning("COD-20", 1); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := store.Finish("COD-20", queue.StatusFailed, "gave up: CI red"); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	res := postRequeue(t, ts, repo, "COD-20")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("requeue = %d, want 200", res.StatusCode)
	}
	var q QueueResponse
	decodeBody(t, res, &q)
	view, found := queueViewByID(q, "COD-20")
	if !found {
		t.Fatalf("COD-20 missing from response queue")
	}
	if view.Status != queue.StatusPending {
		t.Errorf("status = %q, want pending", view.Status)
	}
	if view.Reason != "" {
		t.Errorf("reason = %q, want cleared", view.Reason)
	}
	if len(fake.captures) != 1 {
		t.Errorf("captures = %d, want 1", len(fake.captures))
	}
}

// TestIssueRequeueRefusedWhileDraining keeps the recovery out from under a live
// loop: a requeue rewrites tracker state and deletes branches a running child
// could be standing on, so an armed queue refuses before anything spawns.
func TestIssueRequeueRefusedWhileDraining(t *testing.T) {
	s, fake, root, ts := requeueServer(t, "acme")
	repo := filepath.Base(root)
	store := s.stores.Queue(root)
	if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-30", Status: queue.StatusFailed}); err != nil {
		t.Fatalf("Add COD-30: %v", err)
	}
	if err := store.Arm(false, queue.OnFaultHalt); err != nil {
		t.Fatalf("Arm: %v", err)
	}

	res := postRequeue(t, ts, repo, "COD-30")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("requeue while draining = %d, want 409", res.StatusCode)
	}
	if len(fake.captures) != 0 {
		t.Errorf("captures = %d, want none", len(fake.captures))
	}
}

// TestIssueRequeueReportsCaptureFailure surfaces the child's refusal — a merged
// attempt, a tracker outage — instead of repairing a queue the requeue never
// actually touched.
func TestIssueRequeueReportsCaptureFailure(t *testing.T) {
	s, fake, root, ts := requeueServer(t, "acme")
	repo := filepath.Base(root)
	store := s.stores.Queue(root)
	if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-40"}); err != nil {
		t.Fatalf("Add COD-40: %v", err)
	}
	if err := store.MarkRunning("COD-40", 1); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := store.Finish("COD-40", queue.StatusFailed, "gave up"); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	fake.captureErr = errors.New("exit status 1: COD-40 is already shipped (PR 7 is merged)")

	res := postRequeue(t, ts, repo, "COD-40")
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("requeue = %d, want 502", res.StatusCode)
	}
	var body map[string]string
	decodeBody(t, res, &body)
	if !strings.Contains(body["error"], "already shipped") {
		t.Errorf("error = %q, want the child's refusal surfaced", body["error"])
	}
	_, q := getQueue(t, ts, repo)
	if view, _ := queueViewByID(q, "COD-40"); view.Status != queue.StatusFailed {
		t.Errorf("status after failed requeue = %q, want failed untouched", view.Status)
	}
}

// TestIssueRequeueGates covers the cheap refusals: only POST answers, only an
// allowlisted repo runs the binary, and only a plausible ticket id reaches it.
func TestIssueRequeueGates(t *testing.T) {
	_, fake, root, ts := requeueServer(t, "acme")
	repo := filepath.Base(root)

	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/issues/COD-1/requeue")
	if err != nil {
		t.Fatalf("GET requeue: %v", err)
	}
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d, want 405", res.StatusCode)
	}
	if res := postRequeue(t, ts, "elsewhere", "COD-1"); res.StatusCode != http.StatusForbidden {
		t.Errorf("unlisted repo = %d, want 403", res.StatusCode)
	}
	if res := postRequeue(t, ts, repo, "not%20a%20ticket"); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad id = %d, want 400", res.StatusCode)
	}
	if len(fake.captures) != 0 {
		t.Errorf("captures = %d, want none", len(fake.captures))
	}
}
