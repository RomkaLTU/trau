package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// queuedLabelServer builds a hub with one registered repo carrying ini as its
// project config, a recording tracker Writer, and the deterministic drain probes
// drainServer uses, so label writes are asserted without touching a real tracker.
// The repo is bound to a Linear team ahead of ini, so its COD-<n> tickets read as
// this repo's own external ids; an ini naming TRACKER_PROVIDER still wins.
func queuedLabelServer(t *testing.T, ini string) (*Server, *fakeWriter, *fakeSupervisor, string, *httptest.Server) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "acme")
	writeRepoINI(t, root, "LINEAR_TEAM=COD\n"+ini)
	s := New("1.2.3", "127.0.0.1", "", []string{root}, false, testStores(t))
	s.home = t.TempDir()
	writer := newFakeWriter()
	s.newWriter = func(config.Config) (tracker.Writer, error) { return writer, nil }
	s.newReader = func(config.Config) (tracker.Reader, error) { return nil, tracker.ErrReaderUnavailable }
	sup := &fakeSupervisor{}
	s.sup = sup
	s.drain.busyPIDs = func(string) []int { return nil }
	s.drain.alive = func(int) bool { return false }
	s.drain.outcome = func(string, queue.Item) (string, string) { return "", "" }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, writer, sup, root, ts
}

func seedQueuedTicket(t *testing.T, s *Server, root, id string) {
	t.Helper()
	seedSyncedIssue(t, s.stores.Issues(), root, hubstore.Issue{Identifier: id, Title: id, StatusGroup: "backlog"})
}

func storedLabels(t *testing.T, s *Server, root, id string) []string {
	t.Helper()
	iss, found, err := s.stores.Issues().Find(root, id)
	if err != nil || !found {
		t.Fatalf("find %s: found=%v err=%v", id, found, err)
	}
	return iss.Labels
}

func TestEnqueueTicketAddsQueuedLabel(t *testing.T) {
	s, writer, _, root, ts := queuedLabelServer(t, "QUEUED_LABEL=queued\n")
	seedQueuedTicket(t, s, root, "COD-11")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{ID: "COD-11"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	want := []labelCall{{id: "COD-11", add: []string{"queued"}}}
	if !reflect.DeepEqual(writer.labels, want) {
		t.Errorf("label writes = %+v, want %+v", writer.labels, want)
	}
	if got := storedLabels(t, s, root, "COD-11"); !reflect.DeepEqual(got, []string{"queued"}) {
		t.Errorf("stored labels = %v, want [queued]", got)
	}
}

func TestEnqueueEpicLabelsEpicAndSubIssues(t *testing.T) {
	_, writer, sup, _, ts := queuedLabelServer(t, "QUEUED_LABEL=queued\n")
	sup.captureOut = []byte(`[{"id":"COD-12","title":"First","state":"todo"},{"id":"COD-13","title":"Second","state":"todo"}]`)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: "epic", ID: "COD-10"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	want := []labelCall{
		{id: "COD-10", add: []string{"queued"}},
		{id: "COD-12", add: []string{"queued"}},
		{id: "COD-13", add: []string{"queued"}},
	}
	if !reflect.DeepEqual(writer.labels, want) {
		t.Errorf("label writes = %+v, want the epic and both sub-issues labelled", writer.labels)
	}
}

func TestEnqueueInternalIssueLabelsStoreRow(t *testing.T) {
	s, writer, _, root, ts := queuedLabelServer(t, "QUEUED_LABEL=queued\n")
	iss, err := s.stores.Issues().CreateInternal(root, "ACME", hubstore.InternalDraft{Title: "Internal work"})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{ID: iss.Identifier})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	if len(writer.labels) != 0 {
		t.Errorf("label writes = %+v, want none — an internal issue never reaches the tracker", writer.labels)
	}
	if got := storedLabels(t, s, root, iss.Identifier); !reflect.DeepEqual(got, []string{"queued"}) {
		t.Errorf("stored labels = %v, want [queued]", got)
	}
}

// A repo on the internal provider has no direct writer to build at all, which
// must not cost its issues the label. The real writer factory stands in for the
// fake so the provider's own refusal is the one under test.
func TestEnqueueInternalProviderRepoLabelsStoreRow(t *testing.T) {
	s, _, _, root, ts := queuedLabelServer(t, "TRACKER_PROVIDER=internal\nISSUE_PREFIX=ACME\nQUEUED_LABEL=queued\n")
	s.newWriter = defaultWriter
	iss, err := s.stores.Issues().CreateInternal(root, "ACME", hubstore.InternalDraft{Title: "Internal work"})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{ID: iss.Identifier})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	if got := storedLabels(t, s, root, iss.Identifier); !reflect.DeepEqual(got, []string{"queued"}) {
		t.Fatalf("stored labels = %v, want [queued]", got)
	}

	res = doReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/acme/queue/"+iss.Identifier, nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("dequeue status = %d, want 200", res.StatusCode)
	}
	if got := storedLabels(t, s, root, iss.Identifier); len(got) != 0 {
		t.Errorf("stored labels = %v, want none once the item left the queue", got)
	}
}

func TestDrainRunStartRemovesQueuedLabelFromItemOnly(t *testing.T) {
	s, writer, _, root, _ := queuedLabelServer(t, "QUEUED_LABEL=queued\n")
	seedQueue(t, s, root, true, queue.Item{
		Kind:      queue.KindEpic,
		ID:        "COD-10",
		SubIssues: []queue.SubIssue{{ID: "COD-12"}, {ID: "COD-13"}},
	})

	if act, err := s.drain.tick(root); err != nil || act != drainSpawn {
		t.Fatalf("tick = %q, %v, want spawn", act, err)
	}
	want := []labelCall{{id: "COD-10", remove: []string{"queued"}}}
	if !reflect.DeepEqual(writer.labels, want) {
		t.Errorf("label writes = %+v, want only the epic's own label removed", writer.labels)
	}
}

func TestQueueRunItemRemovesQueuedLabel(t *testing.T) {
	s, writer, _, root, ts := queuedLabelServer(t, "QUEUED_LABEL=queued\n")
	seedQueue(t, s, root, false, queue.Item{ID: "COD-11"})

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue/COD-11/run", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	want := []labelCall{{id: "COD-11", remove: []string{"queued"}}}
	if !reflect.DeepEqual(writer.labels, want) {
		t.Errorf("label writes = %+v, want %+v", writer.labels, want)
	}
}

func TestDequeueStripsQueuedLabelFromItemAndSubIssues(t *testing.T) {
	s, writer, _, root, ts := queuedLabelServer(t, "QUEUED_LABEL=queued\n")
	seedQueue(t, s, root, false, queue.Item{
		Kind:      queue.KindEpic,
		ID:        "COD-10",
		SubIssues: []queue.SubIssue{{ID: "COD-12"}},
	})

	res := doReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/acme/queue/COD-10", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	want := []labelCall{
		{id: "COD-10", remove: []string{"queued"}},
		{id: "COD-12", remove: []string{"queued"}},
	}
	if !reflect.DeepEqual(writer.labels, want) {
		t.Errorf("label writes = %+v, want the epic and its sub-issue stripped", writer.labels)
	}
}

func TestDrainSettleStripsQueuedLabel(t *testing.T) {
	s, writer, _, root, _ := queuedLabelServer(t, "QUEUED_LABEL=queued\n")
	seedQueue(t, s, root, true, queue.Item{
		Kind:      queue.KindEpic,
		ID:        "COD-10",
		Status:    queue.StatusRunning,
		PID:       4242,
		SubIssues: []queue.SubIssue{{ID: "COD-12"}},
	})
	if err := s.stores.DrainOutcomes().Upsert(root, "COD-10", "", ""); err != nil {
		t.Fatalf("seed clean drain outcome: %v", err)
	}

	if act, err := s.drain.tick(root); err != nil || act != drainReconcile {
		t.Fatalf("tick = %q, %v, want reconcile", act, err)
	}
	if got := statusOf(t, s, root, "COD-10"); got != queue.StatusDone {
		t.Fatalf("status = %q, want done", got)
	}
	want := []labelCall{
		{id: "COD-10", remove: []string{"queued"}},
		{id: "COD-12", remove: []string{"queued"}},
	}
	if !reflect.DeepEqual(writer.labels, want) {
		t.Errorf("label writes = %+v, want the settled epic and its sub-issue stripped", writer.labels)
	}
}

// An item the drain parked is still queued for a resume, so it keeps its label.
func TestDrainPausedItemKeepsQueuedLabel(t *testing.T) {
	s, writer, _, root, _ := queuedLabelServer(t, "QUEUED_LABEL=queued\n")
	seedQueue(t, s, root, true, queue.Item{
		Kind:      queue.KindEpic,
		ID:        "COD-10",
		Status:    queue.StatusRunning,
		PID:       4242,
		SubIssues: []queue.SubIssue{{ID: "COD-12"}},
	})
	s.drain.outcome = func(string, queue.Item) (string, string) { return "faulted", "build failed" }

	if act, err := s.drain.tick(root); err != nil || act != drainReconcile {
		t.Fatalf("tick = %q, %v, want reconcile", act, err)
	}
	if len(writer.labels) != 0 {
		t.Errorf("label writes = %+v, want none for a parked item", writer.labels)
	}
}

// Archiving prunes an issue's pending queue entry, so the label has to come off
// with it; the running entry an archive leaves in place keeps its own.
func TestArchiveStripsQueuedLabelFromPrunedEntriesOnly(t *testing.T) {
	s, writer, _, root, ts := queuedLabelServer(t, "QUEUED_LABEL=queued\n")
	seedSyncedIssue(t, s.stores.Issues(), root, hubstore.Issue{Identifier: "COD-10", Title: "Epic", StatusGroup: "backlog", HasChildren: true})
	seedSyncedIssue(t, s.stores.Issues(), root, hubstore.Issue{Identifier: "COD-11", Title: "Child", StatusGroup: "backlog", Parent: "COD-10"})
	seedQueue(t, s, root, false,
		queue.Item{Kind: queue.KindEpic, ID: "COD-10", SubIssues: []queue.SubIssue{{ID: "COD-12"}}},
		queue.Item{ID: "COD-11", Status: queue.StatusRunning, PID: 4242},
	)

	res := putJSON(t, ts.URL+APIPrefix+"/repos/acme/issues/COD-10/archive", ArchiveRequest{Archived: true})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	want := []labelCall{
		{id: "COD-10", remove: []string{"queued"}},
		{id: "COD-12", remove: []string{"queued"}},
	}
	if !reflect.DeepEqual(writer.labels, want) {
		t.Errorf("label writes = %+v, want the archived epic and its sub-issue stripped, the running child untouched", writer.labels)
	}
}

func TestArchiveClearsQueuedLabelOnStoreRow(t *testing.T) {
	s, _, _, root, ts := queuedLabelServer(t, "QUEUED_LABEL=queued\n")
	seedQueuedTicket(t, s, root, "COD-11")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{ID: "COD-11"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("enqueue status = %d, want 201", res.StatusCode)
	}
	res = putJSON(t, ts.URL+APIPrefix+"/repos/acme/issues/COD-11/archive", ArchiveRequest{Archived: true})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("archive status = %d, want 200", res.StatusCode)
	}
	var out ArchiveResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Labels) != 0 {
		t.Errorf("response labels = %v, want none — the UI seeds its issue cache from this payload", out.Labels)
	}
	if got := storedLabels(t, s, root, "COD-11"); len(got) != 0 {
		t.Errorf("stored labels = %v, want none once the archive drops the queue entry", got)
	}
}

func TestQueuedLabelEmptyWritesNothing(t *testing.T) {
	s, writer, _, root, ts := queuedLabelServer(t, "QUEUED_LABEL=\n")
	seedQueuedTicket(t, s, root, "COD-11")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{ID: "COD-11"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	res = doReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/acme/queue/COD-11", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("dequeue status = %d, want 200", res.StatusCode)
	}
	if len(writer.labels) != 0 {
		t.Errorf("label writes = %+v, want none with QUEUED_LABEL unset", writer.labels)
	}
	if got := storedLabels(t, s, root, "COD-11"); len(got) != 0 {
		t.Errorf("stored labels = %v, want none", got)
	}
}

func TestQueuedLabelWriteFailureLeavesEnqueueIntact(t *testing.T) {
	s, writer, _, root, ts := queuedLabelServer(t, "QUEUED_LABEL=queued\n")
	seedQueuedTicket(t, s, root, "COD-11")
	writer.labelErr = errStub("linear said no")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{ID: "COD-11"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 despite the failed label write", res.StatusCode)
	}
	items, _, err := s.stores.Queue(root).Snapshot()
	if err != nil || len(items) != 1 || items[0].ID != "COD-11" {
		t.Fatalf("queue = %+v (%v), want the COD-11 item registered", items, err)
	}
	if got := storedLabels(t, s, root, "COD-11"); len(got) != 0 {
		t.Errorf("stored labels = %v, want none — a refused tracker write is not mirrored", got)
	}
}

// A repo with no direct tracker credentials still gets the board's queued marker.
func TestQueuedLabelWithoutWriterMirrorsStoreRow(t *testing.T) {
	s, _, _, root, ts := queuedLabelServer(t, "QUEUED_LABEL=queued\n")
	s.newWriter = func(config.Config) (tracker.Writer, error) { return nil, tracker.ErrWriterUnavailable }
	seedQueuedTicket(t, s, root, "COD-11")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{ID: "COD-11"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	if got := storedLabels(t, s, root, "COD-11"); !reflect.DeepEqual(got, []string{"queued"}) {
		t.Errorf("stored labels = %v, want [queued]", got)
	}
}

func TestClearQueuedIssueUnknownRepoIsNoOp(t *testing.T) {
	s, writer, _, _, _ := queuedLabelServer(t, "QUEUED_LABEL=queued\n")
	s.clearQueuedIssue(context.Background(), filepath.Join(t.TempDir(), "gone"), "COD-11")
	if len(writer.labels) != 0 {
		t.Errorf("label writes = %+v, want none for a repo the hub cannot resolve", writer.labels)
	}
}
