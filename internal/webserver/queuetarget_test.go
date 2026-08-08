package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
)

// internalQueueHub builds a hub with one Registered repo that tracks its issues
// internally — the state a switch to internal leaves behind. There is no tracker
// reader, so the queue target check has the identifier alone to go on. The repo's
// internal prefix derives from its name: ACME.
func internalQueueHub(t *testing.T) (*Server, string, *httptest.Server) {
	t.Helper()
	s, _, root, ts := queueHubINI(t, "acme", "TRACKER_PROVIDER=internal\n")
	return s, root, ts
}

// TestEnqueueInternalRepoTakesItsOwnIssue is the case the guard must leave alone:
// an internal-provider repo queueing an issue its own store holds.
func TestEnqueueInternalRepoTakesItsOwnIssue(t *testing.T) {
	s, root, ts := internalQueueHub(t)
	iss, err := s.stores.Issues().CreateInternal(root, "ACME", hubstore.InternalDraft{Title: "Internal work"})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{ID: iss.Identifier})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	var out QueueResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].ID != iss.Identifier {
		t.Fatalf("items = %+v, want the %s row", out.Items, iss.Identifier)
	}
}

// TestEnqueueInternalRepoRejectsStaleExternalID is the hole the guard closes: with no
// reader for the internal provider the target check used to no-op, and the id went into
// the queue to be run by tracker.Internal, whose lookup is scoped to the store's
// internal rows. The leftover synced row proves the mirror is no rescue: the tracker
// that owns JIRA-42 is one this repo no longer talks to.
func TestEnqueueInternalRepoRejectsStaleExternalID(t *testing.T) {
	s, root, ts := internalQueueHub(t)
	seedSyncedIssue(t, s.stores.Issues(), root, hubstore.Issue{Identifier: "JIRA-42", Title: "Old work"})

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{ID: "JIRA-42"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	msg := errorOf(t, res)
	for _, want := range []string{"JIRA-42", "no longer connected", "ACME-"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to mention %q", msg, want)
		}
	}

	_, view := getQueue(t, ts, "acme")
	if len(view.Items) != 0 {
		t.Errorf("queue = %+v, want the refused id left out", view.Items)
	}
}

// TestEnqueueInternalRepoTakesPrefixedID keeps the guard on the identifier's own
// terms: an id carrying the repo's internal prefix is routed internally by the
// StoreBacked convention whether or not the store holds a row for it yet.
func TestEnqueueInternalRepoTakesPrefixedID(t *testing.T) {
	_, _, ts := internalQueueHub(t)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "ticket", ID: "ACME-99"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d (%s), want 201", res.StatusCode, errorOf(t, res))
	}
}

// TestEnqueueInternalRepoRejectsRetiredPrefixedID is the collision the prefix rule
// alone cannot see: the tracker the repo left numbered its tickets under the same
// prefix the internal tracker mints under, so a stale id looks exactly like one of
// the repo's own. The raised sequence is what separates them — every number below
// it the store does not hold went with the dropped mirror.
func TestEnqueueInternalRepoRejectsRetiredPrefixedID(t *testing.T) {
	s, root, ts := internalQueueHub(t)
	seedSyncedIssue(t, s.stores.Issues(), root, hubstore.Issue{Identifier: "ACME-100", Title: "Old work"})
	if _, err := s.stores.Issues().RaiseInternalSeq(root, "ACME"); err != nil {
		t.Fatalf("raise seq: %v", err)
	}
	if err := s.stores.Issues().DropSynced(root); err != nil {
		t.Fatalf("drop mirror: %v", err)
	}

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "ticket", ID: "ACME-100"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d (%s), want 409", res.StatusCode, errorOf(t, res))
	}
	if msg := errorOf(t, res); !strings.Contains(msg, "ACME-100") || !strings.Contains(msg, "no longer connected") {
		t.Errorf("error = %q, want the disconnected-tracker refusal naming ACME-100", msg)
	}

	_, view := getQueue(t, ts, "acme")
	if len(view.Items) != 0 {
		t.Errorf("queue = %+v, want the refused id left out", view.Items)
	}

	// The number the sequence hands out next is the repo's own work, not the old
	// tracker's, so it still queues.
	iss, err := s.stores.Issues().CreateInternal(root, "ACME", hubstore.InternalDraft{Title: "Fresh"})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}
	if iss.Identifier != "ACME-101" {
		t.Fatalf("minted %s, want ACME-101", iss.Identifier)
	}
	fresh := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "ticket", ID: iss.Identifier})
	defer func() { _ = fresh.Body.Close() }()
	if fresh.StatusCode != http.StatusCreated {
		t.Fatalf("enqueue %s = %d (%s), want 201", iss.Identifier, fresh.StatusCode, errorOf(t, fresh))
	}
}

// TestEnqueueExternalRepoKeepsUncheckedIDs is the hybrid regression: a repo bound to
// an external tracker but carrying no direct credentials still queues whatever it is
// handed — an internal-prefixed id included — because there is a reader for that
// provider to resolve it later, and nothing here can say the id is wrong.
func TestEnqueueExternalRepoKeepsUncheckedIDs(t *testing.T) {
	_, _, ts := queueServer(t, "acme")

	for _, id := range []string{"COD-11", "ACME-7"} {
		res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "ticket", ID: id})
		if res.StatusCode != http.StatusCreated {
			t.Errorf("enqueue %s = %d (%s), want 201", id, res.StatusCode, errorOf(t, res))
			continue
		}
		_ = res.Body.Close()
	}
}

// seedSettledRow stages the row a give-up leaves behind, so a requeue has something
// to restore.
func seedSettledRow(t *testing.T, s *Server, root string, item queue.Item) {
	t.Helper()
	store := s.stores.Queue(root)
	if _, err := store.Add(item); err != nil {
		t.Fatalf("Add %s: %v", item.ID, err)
	}
	if err := store.MarkRunning(item.ID, 1); err != nil {
		t.Fatalf("MarkRunning %s: %v", item.ID, err)
	}
	if err := store.Finish(item.ID, queue.StatusFailed, "gave up: CI red"); err != nil {
		t.Fatalf("Finish %s: %v", item.ID, err)
	}
}

// TestIssueRequeueInternalRepoRejectsStaleExternalRow closes the same hole from the
// other side: a row queued before the switch names an id no reader answers for, so
// making it eligible again would only schedule a run that dies at ticket lookup.
func TestIssueRequeueInternalRepoRejectsStaleExternalRow(t *testing.T) {
	s, root, ts := internalQueueHub(t)
	seedSettledRow(t, s, root, queue.Item{Kind: queue.KindTicket, ID: "JIRA-20", Source: "jira"})

	res := postRequeue(t, ts, "acme", "JIRA-20")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("requeue = %d, want 409", res.StatusCode)
	}
	if msg := errorOf(t, res); !strings.Contains(msg, "no longer connected") {
		t.Errorf("error = %q, want the disconnected-tracker refusal", msg)
	}

	_, view := getQueue(t, ts, "acme")
	row, found := queueViewByID(view, "JIRA-20")
	if !found || row.Status != queue.StatusFailed {
		t.Errorf("row = %+v (found %v), want it left settled", row, found)
	}
}

// TestRunRequeueInternalRepoRejectsStaleExternalRow pins the guard on the run
// endpoint too — both requeue routes settle their target the same way.
func TestRunRequeueInternalRepoRejectsStaleExternalRow(t *testing.T) {
	s, root, ts := internalQueueHub(t)
	seedSettledRow(t, s, root, queue.Item{Kind: queue.KindTicket, ID: "JIRA-21", Source: "jira"})

	res := postRunRequeue(t, ts, "acme", "JIRA-21")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("run requeue = %d, want 409", res.StatusCode)
	}
	if msg := errorOf(t, res); !strings.Contains(msg, "no longer connected") {
		t.Errorf("error = %q, want the disconnected-tracker refusal", msg)
	}
}

// TestIssueRequeueInternalRepoRestoresItsOwnRow is the no-regression half: the
// repo's own internal ticket still requeues.
func TestIssueRequeueInternalRepoRestoresItsOwnRow(t *testing.T) {
	s, root, ts := internalQueueHub(t)
	iss, err := s.stores.Issues().CreateInternal(root, "ACME", hubstore.InternalDraft{Title: "Internal work"})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}
	seedSettledRow(t, s, root, queue.Item{Kind: queue.KindTicket, ID: iss.Identifier, Source: hubstore.SourceInternal})

	res := postRequeue(t, ts, "acme", iss.Identifier)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("requeue = %d (%s), want 200", res.StatusCode, errorOf(t, res))
	}
	var q QueueResponse
	decodeBody(t, res, &q)
	row, found := queueViewByID(q, iss.Identifier)
	if !found || row.Status != queue.StatusPending {
		t.Fatalf("row = %+v (found %v), want it pending again", row, found)
	}
}
