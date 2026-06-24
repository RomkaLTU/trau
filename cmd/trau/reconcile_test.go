package main

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// fakeStatuser answers IssueStatus from a fixed map and records which ids were
// queried, so a test can assert that terminal/unknown local phases are never
// cross-checked against the tracker.
type fakeStatuser struct {
	status  map[string]tracker.IssueStatus
	errs    map[string]error
	queried []string
}

func (f *fakeStatuser) IssueStatus(_ context.Context, id string) (tracker.IssueStatus, error) {
	f.queried = append(f.queried, id)
	if err := f.errs[id]; err != nil {
		return tracker.StatusUnknown, err
	}
	return f.status[id], nil
}

func TestReconcileWith(t *testing.T) {
	store := state.NewStore(t.TempDir())
	set := func(id, phase string) {
		if err := store.Set(id, "PHASE", phase); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	set("COD-1", state.Quarantined) // quarantined + Done   → cleared
	set("COD-2", state.Built)       // in-flight  + Done     → cleared
	set("COD-3", state.Built)       // in-flight  + still open → kept
	set("COD-4", state.Merged)      // merged (not reconcilable) → never queried
	set("COD-5", state.PROpen)      // in-flight  + query error  → kept

	f := &fakeStatuser{
		status: map[string]tracker.IssueStatus{
			"COD-1": tracker.StatusDone,
			"COD-2": tracker.StatusCanceled,
			"COD-3": tracker.StatusOpen,
		},
		errs: map[string]error{"COD-5": errors.New("network")},
	}

	cleared := reconcileWith(context.Background(), store, f)

	gotIDs := make([]string, 0, len(cleared))
	for _, c := range cleared {
		gotIDs = append(gotIDs, c.ID)
	}
	sort.Strings(gotIDs)
	if want := []string{"COD-1", "COD-2"}; !equalStrings(gotIDs, want) {
		t.Errorf("cleared = %v, want %v", gotIDs, want)
	}

	// Cleared tickets are gone; everything else is left intact.
	remaining := map[string]bool{}
	for _, id := range store.Tickets() {
		remaining[id] = true
	}
	for _, id := range []string{"COD-1", "COD-2"} {
		if remaining[id] {
			t.Errorf("%s should have been cleared but is still present", id)
		}
	}
	for _, id := range []string{"COD-3", "COD-4", "COD-5"} {
		if !remaining[id] {
			t.Errorf("%s should have been left intact but is gone", id)
		}
	}

	// A merged (non-reconcilable) checkpoint must never hit the tracker.
	for _, q := range f.queried {
		if q == "COD-4" {
			t.Errorf("merged COD-4 should not be queried against the tracker")
		}
	}
}

func TestHasReconcileCandidate(t *testing.T) {
	store := state.NewStore(t.TempDir())
	if hasReconcileCandidate(store) {
		t.Error("empty store should have no reconcile candidates")
	}
	if err := store.Set("COD-9", "PHASE", state.Merged); err != nil {
		t.Fatal(err)
	}
	if hasReconcileCandidate(store) {
		t.Error("a merged-only store should have no reconcile candidates")
	}
	if err := store.Set("COD-10", "PHASE", state.Quarantined); err != nil {
		t.Fatal(err)
	}
	if !hasReconcileCandidate(store) {
		t.Error("a quarantined checkpoint should be a reconcile candidate")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
