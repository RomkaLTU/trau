package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// reconcileServer builds a hub over a fresh home with one exited repo ("acme") and
// a Reader factory returning fake, and returns the server plus the repo root so a
// test can seed the store, run reconcileRepo/forceResync directly, and assert.
func reconcileServer(t *testing.T, fake tracker.Reader) (*Server, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))
	writeRepoINI(t, root, "LINEAR_TEAM=COD\n")
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	s.newReader = func(config.Config) (tracker.Reader, error) { return fake, nil }
	return s, root
}

func syncedIssue(id string) tracker.SyncedIssue {
	return tracker.SyncedIssue{
		ID:         id,
		ExternalID: "ext-" + id,
		Title:      id,
		Status:     "Todo",
		Group:      tracker.StatusGroupUnstarted,
		UpdatedAt:  "2026-07-10T12:00:00Z",
	}
}

func deletedAt(t *testing.T, s *Server, root, id string) string {
	t.Helper()
	iss, ok, err := s.stores.Issues().Get(root, id)
	if err != nil || !ok {
		t.Fatalf("Get %s = (%v, %v), want found", id, ok, err)
	}
	return iss.DeletedAt
}

func TestReconcileSweepTombstonesAndDropsFromQueue(t *testing.T) {
	fake := &fakeReader{synced: []tracker.SyncedIssue{syncedIssue("COD-1"), syncedIssue("COD-2")}}
	s, root := reconcileServer(t, fake)
	repo := workspaceRepo(root)
	if _, err := s.syncRepo(context.Background(), repo); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	for _, id := range []string{"COD-1", "COD-2"} {
		if _, err := s.stores.Queue(root).Add(queue.Item{Kind: queue.KindTicket, ID: id}); err != nil {
			t.Fatalf("queue %s: %v", id, err)
		}
	}

	fake.identifiers = []string{"COD-2"}
	if err := s.reconcileRepo(context.Background(), repo); err != nil {
		t.Fatalf("reconcileRepo: %v", err)
	}

	if deletedAt(t, s, root, "COD-1") == "" {
		t.Fatal("COD-1 should be tombstoned after it left the tracker")
	}
	if deletedAt(t, s, root, "COD-2") != "" {
		t.Fatal("COD-2 is still in the tracker and must stay live")
	}

	items, err := s.stores.Queue(root).Load()
	if err != nil {
		t.Fatalf("queue load: %v", err)
	}
	if len(items) != 1 || items[0].ID != "COD-2" {
		t.Fatalf("queue = %+v, want only COD-2 left", items)
	}
}

func TestReconcileEmptyIdentifierSetIsNoOp(t *testing.T) {
	fake := &fakeReader{synced: []tracker.SyncedIssue{syncedIssue("COD-1")}}
	s, root := reconcileServer(t, fake)
	repo := workspaceRepo(root)
	if _, err := s.syncRepo(context.Background(), repo); err != nil {
		t.Fatalf("seed sync: %v", err)
	}

	fake.identifiers = nil
	if err := s.reconcileRepo(context.Background(), repo); err != nil {
		t.Fatalf("reconcileRepo: %v", err)
	}
	if deletedAt(t, s, root, "COD-1") != "" {
		t.Fatal("an empty tracker result must not tombstone the whole store")
	}
}

func TestReconcileTrackerErrorRecordsAndBacksOff(t *testing.T) {
	fake := &fakeReader{synced: []tracker.SyncedIssue{syncedIssue("COD-1")}}
	s, root := reconcileServer(t, fake)
	repo := workspaceRepo(root)
	if _, err := s.syncRepo(context.Background(), repo); err != nil {
		t.Fatalf("seed sync: %v", err)
	}

	fake.identifiersErr = errors.New("linear: 500")
	if err := s.reconcileRepo(context.Background(), repo); err == nil {
		t.Fatal("reconcileRepo should surface the tracker error")
	}
	st, err := s.stores.Issues().SyncState(root)
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if st.LastError == "" {
		t.Fatal("a sweep failure should record on the sync error surface")
	}
	if deletedAt(t, s, root, "COD-1") != "" {
		t.Fatal("a failed sweep must not tombstone anything")
	}
}

func TestForceResyncDropsAndRepullsPreservingInternal(t *testing.T) {
	fake := &fakeReader{synced: []tracker.SyncedIssue{syncedIssue("COD-1"), syncedIssue("COD-2")}}
	s, root := reconcileServer(t, fake)
	repo := workspaceRepo(root)
	if _, err := s.syncRepo(context.Background(), repo); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	internal, err := s.stores.Issues().CreateInternal(root, "LOOP", hubstore.InternalDraft{Title: "hand-filed"})
	if err != nil {
		t.Fatalf("CreateInternal: %v", err)
	}

	fake.synced = []tracker.SyncedIssue{syncedIssue("COD-1")}
	resp, err := s.forceResync(context.Background(), repo)
	if err != nil {
		t.Fatalf("forceResync: %v", err)
	}
	if resp.Issues != 1 {
		t.Fatalf("resync pulled %d issues, want 1", resp.Issues)
	}

	if _, ok, _ := s.stores.Issues().Get(root, "COD-2"); ok {
		t.Fatal("COD-2 left the tracker and should be gone after a clean resync")
	}
	if _, ok, _ := s.stores.Issues().Get(root, "COD-1"); !ok {
		t.Fatal("COD-1 should be re-pulled by the resync")
	}
	if _, ok, _ := s.stores.Issues().Get(root, internal.Identifier); !ok {
		t.Fatal("internal issues must survive a force resync")
	}
}

func TestForceResyncWithoutCredentialsKeepsStore(t *testing.T) {
	s, root := reconcileServer(t, nil)
	s.newReader = func(config.Config) (tracker.Reader, error) { return nil, tracker.ErrReaderUnavailable }
	if _, _, err := s.stores.Issues().Upsert(root, "linear", []hubstore.Issue{{Identifier: "COD-1", Title: "kept"}}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	if _, err := s.forceResync(context.Background(), workspaceRepo(root)); !errors.Is(err, tracker.ErrReaderUnavailable) {
		t.Fatalf("forceResync err = %v, want ErrReaderUnavailable", err)
	}
	if _, ok, _ := s.stores.Issues().Get(root, "COD-1"); !ok {
		t.Fatal("a resync refused for lack of credentials must not drop the store")
	}
}

func TestResyncEndpoint(t *testing.T) {
	fake := &fakeReader{synced: []tracker.SyncedIssue{syncedIssue("COD-1")}}
	ts, _, _ := syncServer(t, fake)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/resync", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out SyncResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Issues != 1 {
		t.Fatalf("resync response = %+v, want one issue", out)
	}

	unknown := postJSON(t, ts.URL+APIPrefix+"/repos/ghost/resync", nil)
	defer func() { _ = unknown.Body.Close() }()
	if unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown repo status = %d, want 404", unknown.StatusCode)
	}

	get, err := http.Get(ts.URL + APIPrefix + "/repos/acme/resync")
	if err != nil {
		t.Fatalf("GET resync: %v", err)
	}
	defer func() { _ = get.Body.Close() }()
	if get.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", get.StatusCode)
	}
}

func TestRunDetailMarksRemovedTicket(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))
	seedCheckpoint(t, runsDir, "COD-1", map[string]string{"PHASE": state.Building, "TITLE": "gone upstream"})

	store := testStoresAt(t, home).Issues()
	if _, _, err := store.Upsert(root, "linear", []hubstore.Issue{{Identifier: "COD-1", Title: "gone upstream"}}); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if _, err := store.Reconcile(root, nil); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	ts := instancesServer(t, home)
	if d := getRunDetail(t, ts, "acme", "COD-1"); !d.Removed {
		t.Fatalf("run detail = %+v, want removed=true for a tombstoned ticket", d)
	}
}
