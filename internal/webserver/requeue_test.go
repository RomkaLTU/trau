package webserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/pipeline"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/state"
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
	writeRepoINI(t, root, "LINEAR_TEAM=COD\n")
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
	if err := store.Arm(false, queue.OnFaultHalt, nil); err != nil {
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

func postRunRequeue(t *testing.T, ts *httptest.Server, repo, ticket string) *http.Response {
	t.Helper()
	res, err := http.Post(ts.URL+APIPrefix+"/repos/"+repo+"/runs/"+ticket+"/requeue", "application/json", nil)
	if err != nil {
		t.Fatalf("POST run requeue: %v", err)
	}
	return res
}

// seedQuarantinedRun records the give-up a fix session diagnoses, so the run the
// receipt offers to requeue reads as failed.
func seedQuarantinedRun(t *testing.T, s *Server, root, ticket string) {
	t.Helper()
	if err := s.stores.Checkpoints().Upsert(root, ticket, map[string]string{
		"PHASE":          state.Quarantined,
		"FAILURE_REASON": "verify stayed red",
	}); err != nil {
		t.Fatalf("seed quarantined checkpoint: %v", err)
	}
}

// seedFailedRow settles ticket's queue row the way a give-up leaves it, which
// Runnable excludes — the row a requeue has to restore for the drain to re-pick it.
func seedFailedRow(t *testing.T, s *Server, root, ticket string) {
	t.Helper()
	store := s.stores.Queue(root)
	if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: ticket}); err != nil {
		t.Fatalf("Add %s: %v", ticket, err)
	}
	if err := store.MarkRunning(ticket, 1); err != nil {
		t.Fatalf("MarkRunning %s: %v", ticket, err)
	}
	if err := store.Finish(ticket, queue.StatusFailed, "gave up: verify stayed red"); err != nil {
		t.Fatalf("Finish %s: %v", ticket, err)
	}
}

// TestRunRequeueListsWhatChanged is the receipt's happy path: the hub drives a
// plain --requeue — never --force, the CLI keeps that escape hatch — and hands
// back the steps the recovery reported so the card can list them.
func TestRunRequeueListsWhatChanged(t *testing.T) {
	s, fake, root, ts := requeueServer(t, "acme")
	repo := filepath.Base(root)
	seedQuarantinedRun(t, s, root, "COD-50")
	seedFailedRow(t, s, root, "COD-50")
	fake.captureOut = []byte(strings.Join([]string{
		"12:00:00   ✓ tracker: dropped needs-human, restored ready-for-agent",
		"12:00:00   ✓ closed the attempt PR 42",
		"12:00:00   ✓ deleted branch feature/COD-50-retry",
		"12:00:00   ✓ cleared the saved checkpoint (was building)",
		"12:00:00   COD-50 is eligible again",
		"",
	}, "\n"))

	res := postRunRequeue(t, ts, repo, "COD-50")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("requeue = %d, want 200", res.StatusCode)
	}
	var got RunRequeueResult
	decodeBody(t, res, &got)
	want := []string{
		"tracker: dropped needs-human, restored ready-for-agent",
		"closed the attempt PR 42",
		"deleted branch feature/COD-50-retry",
		"cleared the saved checkpoint (was building)",
	}
	if !slices.Equal(got.Changed, want) {
		t.Errorf("changed = %v, want %v", got.Changed, want)
	}
	if len(fake.captures) != 1 {
		t.Fatalf("captures = %d, want 1", len(fake.captures))
	}
	spec := fake.captures[0]
	if spec.Dir != root {
		t.Errorf("capture dir = %q, want %q", spec.Dir, root)
	}
	wantArgs := []string{"--repo", root, "--requeue", "COD-50", "--no-tui"}
	if !slices.Equal(spec.Args, wantArgs) {
		t.Errorf("capture args = %v, want %v", spec.Args, wantArgs)
	}
	items, err := s.stores.Queue(root).Load()
	if err != nil {
		t.Fatalf("load queue: %v", err)
	}
	if items[0].Status != queue.StatusPending {
		t.Errorf("queue row = %q, want pending", items[0].Status)
	}
}

// TestRunRequeueRefusesMergedAttempt keeps the merged escape hatch on the CLI: the
// child's refusal comes back as a conflict the card shows, not a gateway failure.
// The stderr is built the way the CLI writes it and cut the way Capture reads it,
// so rewording the refusal fails here instead of quietly downgrading the conflict
// across the process boundary.
func TestRunRequeueRefusesMergedAttempt(t *testing.T) {
	s, fake, root, ts := requeueServer(t, "acme")
	repo := filepath.Base(root)
	seedQuarantinedRun(t, s, root, "COD-60")
	refusal := console.ErrorPrefix + console.FormatActionable(pipeline.RefuseShipped("requeue", "COD-60", "PR 42 is merged"))
	firstLine, _, _ := strings.Cut(refusal, "\n")
	fake.captureErr = fmt.Errorf("exit status 1: %s", firstLine)

	res := postRunRequeue(t, ts, repo, "COD-60")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("merged attempt = %d, want 409", res.StatusCode)
	}
	var body map[string]string
	decodeBody(t, res, &body)
	if body["error"] != "COD-60 is already shipped (PR 42 is merged)" {
		t.Errorf("error = %q, want the child's refusal unprefixed", body["error"])
	}
}

// TestRunRequeueReportsOtherFailures separates a partial recovery — a remote branch
// the forge refused to drop — from the merged refusal: it is a gateway failure, and
// the card keeps the button so the idempotent retry is one click away.
func TestRunRequeueReportsOtherFailures(t *testing.T) {
	s, fake, root, ts := requeueServer(t, "acme")
	repo := filepath.Base(root)
	seedQuarantinedRun(t, s, root, "COD-61")
	fake.captureErr = errors.New("exit status 1: trau: requeue COD-61: delete origin/feature/COD-61: permission denied")

	res := postRunRequeue(t, ts, repo, "COD-61")
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("partial failure = %d, want 502", res.StatusCode)
	}
	var body map[string]string
	decodeBody(t, res, &body)
	if !strings.Contains(body["error"], "permission denied") {
		t.Errorf("error = %q, want the joined error text", body["error"])
	}
}

// TestRunRequeueRefusesTicketInFlight guards the ticket alone: a row the drain is
// still holding or about to launch refuses before anything spawns, while another
// repo's live loop is no reason to withhold the recovery.
func TestRunRequeueRefusesTicketInFlight(t *testing.T) {
	s, fake, root, ts := requeueServer(t, "acme")
	repo := filepath.Base(root)
	seedQuarantinedRun(t, s, root, "COD-70")
	store := s.stores.Queue(root)
	if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-70"}); err != nil {
		t.Fatalf("Add COD-70: %v", err)
	}

	if res := postRunRequeue(t, ts, repo, "COD-70"); res.StatusCode != http.StatusConflict {
		t.Fatalf("queued ticket = %d, want 409", res.StatusCode)
	}
	if err := store.MarkRunning("COD-70", 1); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if res := postRunRequeue(t, ts, repo, "COD-70"); res.StatusCode != http.StatusConflict {
		t.Fatalf("running ticket = %d, want 409", res.StatusCode)
	}
	if len(fake.captures) != 0 {
		t.Errorf("captures = %d, want none", len(fake.captures))
	}
}

// TestRunRequeueNothingToDo covers the receipt reopened after a CLI requeue already
// ran: the checkpoint it was compiled from is gone, so there is nothing to drive —
// but the give-up outlived it in the queue, and the settled line only tells the
// truth once that row is back in front of the drain.
func TestRunRequeueNothingToDo(t *testing.T) {
	s, fake, root, ts := requeueServer(t, "acme")
	repo := filepath.Base(root)
	seedFailedRow(t, s, root, "COD-80")

	res := postRunRequeue(t, ts, repo, "COD-80")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("requeue = %d, want 200", res.StatusCode)
	}
	var got RunRequeueResult
	decodeBody(t, res, &got)
	if got.Ticket != "COD-80" || len(got.Changed) != 0 {
		t.Errorf("result = %+v, want COD-80 with nothing changed", got)
	}
	if len(fake.captures) != 0 {
		t.Errorf("captures = %d, want none", len(fake.captures))
	}
	items, err := s.stores.Queue(root).Load()
	if err != nil {
		t.Fatalf("load queue: %v", err)
	}
	if items[0].Status != queue.StatusPending {
		t.Errorf("queue row = %q, want pending", items[0].Status)
	}
	if items[0].Reason != "" {
		t.Errorf("reason = %q, want cleared", items[0].Reason)
	}
}

// TestRunRequeueGates covers the cheap refusals: only POST answers, only an
// allowlisted repo runs the binary, and only a plausible ticket id reaches it.
func TestRunRequeueGates(t *testing.T) {
	_, fake, root, ts := requeueServer(t, "acme")
	repo := filepath.Base(root)

	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/runs/COD-1/requeue")
	if err != nil {
		t.Fatalf("GET requeue: %v", err)
	}
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d, want 405", res.StatusCode)
	}
	if res := postRunRequeue(t, ts, "elsewhere", "COD-1"); res.StatusCode != http.StatusForbidden {
		t.Errorf("unlisted repo = %d, want 403", res.StatusCode)
	}
	if res := postRunRequeue(t, ts, repo, "not%20a%20ticket"); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad id = %d, want 400", res.StatusCode)
	}
	if len(fake.captures) != 0 {
		t.Errorf("captures = %d, want none", len(fake.captures))
	}
}
