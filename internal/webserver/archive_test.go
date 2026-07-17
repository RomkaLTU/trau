package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// archiveServer builds a hub with one Registered repo whose store already holds
// the given issues, so the archive endpoint can flip them without a tracker call.
func archiveServer(t *testing.T, issues []hubstore.Issue) (*Server, string, *httptest.Server) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "acme")
	s := New("1.2.3", "127.0.0.1", "", []string{root}, false, testStores(t))
	s.home = t.TempDir()
	s.newReader = func(config.Config) (tracker.Reader, error) { return nil, tracker.ErrReaderUnavailable }
	s.sup = &fakeSupervisor{}
	if _, _, err := s.stores.Issues().Upsert(root, "linear", issues); err != nil {
		t.Fatalf("seed issues: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, root, ts
}

func TestArchiveRemovesPendingQueueEntriesButNotRunning(t *testing.T) {
	s, root, ts := archiveServer(t, []hubstore.Issue{
		{Identifier: "COD-1", StatusGroup: "backlog", HasChildren: true},
		{Identifier: "COD-2", StatusGroup: "backlog", Parent: "COD-1"},
		{Identifier: "COD-3", StatusGroup: "backlog", Parent: "COD-1"},
	})
	q := s.stores.Queue(root)
	for _, id := range []string{"COD-1", "COD-2", "COD-3"} {
		if _, err := q.Add(queue.Item{ID: id, Kind: queue.KindTicket}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	if err := q.MarkRunning("COD-3", 4242); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	res := putJSON(t, ts.URL+APIPrefix+"/repos/acme/issues/COD-1/archive", ArchiveRequest{Archived: true})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out ArchiveResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID != "COD-1" || !out.Archived {
		t.Errorf("response = %+v, want COD-1 archived", out.IssueResponse)
	}
	if out.QueueRemoved != 2 {
		t.Errorf("queue_removed = %d, want 2 (the pending epic and its pending child)", out.QueueRemoved)
	}

	_, view := getQueue(t, ts, "acme")
	if len(view.Items) != 1 || view.Items[0].ID != "COD-3" {
		t.Fatalf("queue = %+v, want only the running COD-3 kept", view.Items)
	}
	if view.Items[0].Status != "running" {
		t.Errorf("COD-3 status = %q, want it left running", view.Items[0].Status)
	}
}

func TestArchiveUnknownIssueIsNotFound(t *testing.T) {
	_, _, ts := archiveServer(t, []hubstore.Issue{{Identifier: "COD-1", StatusGroup: "backlog"}})
	res := putJSON(t, ts.URL+APIPrefix+"/repos/acme/issues/COD-404/archive", ArchiveRequest{Archived: true})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown identifier", res.StatusCode)
	}
}
