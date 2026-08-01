package webserver

import (
	"net/http"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// A .trau.ini retargeted after the binding was cached must not keep syncing the
// old team forever: the sync notices the config no longer matches the target the
// cached ids were resolved from, re-resolves the binding, drops the stale
// team's rows, and pulls the new target clean — so the board stops showing
// another team's tickets without a manual force-resync.
func TestSyncRebindsWhenConfigTargetChanges(t *testing.T) {
	fake := &fakeReader{
		binding:     tracker.ProjectBinding{TeamID: "team-old"},
		synced:      []tracker.SyncedIssue{{ID: "OLD-1", Title: "Old team ticket", Group: tracker.StatusGroupBacklog, UpdatedAt: "2026-07-10T12:00:00Z"}},
		identifiers: []string{"OLD-1"},
	}
	ts, root, store := syncServer(t, fake)

	res, _ := postSync(t, ts, "acme")
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("first sync status = %d, want 200", res.StatusCode)
	}

	writeRepoINI(t, root, "LINEAR_TEAM=NEW\n")
	fake.binding = tracker.ProjectBinding{TeamID: "team-new"}
	fake.synced = []tracker.SyncedIssue{{ID: "NEW-1", Title: "New team ticket", Group: tracker.StatusGroupBacklog, UpdatedAt: "2026-06-01T09:00:00Z"}}
	fake.identifiers = []string{"NEW-1"}

	res, out := postSync(t, ts, "acme")
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("second sync status = %d, want 200", res.StatusCode)
	}
	if out.Issues != 1 {
		t.Fatalf("second sync wrote %d issues, want the new team's one", out.Issues)
	}
	if fake.syncSince != "" {
		t.Fatalf("since = %q, want a full pull after the rebind — the old team's cursor must not gate the new target", fake.syncSince)
	}

	stored, err := store.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stored) != 1 || stored[0].Identifier != "NEW-1" {
		t.Fatalf("store = %v, want only NEW-1 — the old team's rows must not survive the retarget", idents(stored))
	}
	st, err := store.SyncState(root)
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if st.Binding.TeamID != "team-new" {
		t.Fatalf("cached binding team = %q, want team-new", st.Binding.TeamID)
	}
}

// A binding cached before config targets were stamped (or whose key re-resolves
// to the same ids — a tracker-side rename) is restamped in place: one extra
// ResolveBinding call, then the cursor, rows, and incremental pull all survive.
// Nothing is dropped when the target's ids did not actually move.
func TestSyncRestampsSameTargetWithoutDropping(t *testing.T) {
	fake := &fakeReader{
		binding:     tracker.ProjectBinding{TeamID: "team-1"},
		synced:      syncedFixture(),
		identifiers: []string{"COD-1"},
	}
	ts, root, store := syncServer(t, fake)

	res, _ := postSync(t, ts, "acme")
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("first sync status = %d, want 200", res.StatusCode)
	}

	st, err := store.SyncState(root)
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if err := store.SaveBinding(root, hubstore.SyncBinding{
		TeamID:    st.Binding.TeamID,
		ProjectID: st.Binding.ProjectID,
		Project:   st.Binding.Project,
	}); err != nil {
		t.Fatalf("blank stored target: %v", err)
	}

	fresh := tracker.SyncedIssue{ID: "COD-2", Title: "Second", Group: tracker.StatusGroupUnstarted, UpdatedAt: "2026-07-11T08:00:00Z"}
	fake.synced = []tracker.SyncedIssue{fresh}
	fake.identifiers = []string{"COD-1", "COD-2"}

	res, _ = postSync(t, ts, "acme")
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("second sync status = %d, want 200", res.StatusCode)
	}
	if fake.bindingCalls != 2 {
		t.Fatalf("ResolveBinding called %d times, want 2 — the unstamped cache re-resolves once", fake.bindingCalls)
	}
	if fake.syncSince != "2026-07-10T12:00:00Z" {
		t.Fatalf("since = %q, want the stored cursor — same ids keep the incremental pull", fake.syncSince)
	}
	stored, err := store.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("store = %v, want both issues — a restamp must not drop rows", idents(stored))
	}
}

// An unchanged config keeps the cached binding a pure cache hit: no
// ResolveBinding round-trip on later syncs, and the incremental cursor holds.
func TestSyncKeepsBindingCacheHitWhenConfigUnchanged(t *testing.T) {
	fake := &fakeReader{
		binding:     tracker.ProjectBinding{TeamID: "team-1"},
		synced:      syncedFixture(),
		identifiers: []string{"COD-1"},
	}
	ts, _, _ := syncServer(t, fake)

	for i := 0; i < 2; i++ {
		res, _ := postSync(t, ts, "acme")
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("sync %d status = %d, want 200", i+1, res.StatusCode)
		}
	}
	if fake.bindingCalls != 1 {
		t.Fatalf("ResolveBinding called %d times, want 1 — an unchanged config must stay a cache hit", fake.bindingCalls)
	}
	if fake.syncSince != "2026-07-10T12:00:00Z" {
		t.Fatalf("since = %q, want the stored cursor on the second pull", fake.syncSince)
	}
}

func idents(issues []hubstore.Issue) []string {
	out := make([]string, 0, len(issues))
	for _, iss := range issues {
		out = append(out, iss.Identifier)
	}
	return out
}
