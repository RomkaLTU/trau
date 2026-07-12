package hubstore

import (
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/state"
)

func testDrainOutcomes(t *testing.T) *DrainOutcomes {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStores(db.SQL(), nil, 0).DrainOutcomes()
}

func TestDrainOutcomeUpsertOneRemove(t *testing.T) {
	d := testDrainOutcomes(t)

	if _, found, err := d.One("/repo", "COD-1"); found || err != nil {
		t.Fatalf("One before any write = found %v err %v, want absent", found, err)
	}

	if err := d.Upsert("/repo", "COD-1", state.FailFaulted, "sub-issue faulted"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// A different repo/ticket must not bleed into COD-1's outcome.
	_ = d.Upsert("/other", "COD-1", state.FailGaveUp, "elsewhere")

	rep, found, err := d.One("/repo", "COD-1")
	if err != nil || !found {
		t.Fatalf("One(COD-1) = found %v err %v, want present", found, err)
	}
	if rep.Class != state.FailFaulted || rep.Reason != "sub-issue faulted" {
		t.Fatalf("outcome = %+v, want the faulted report", rep)
	}

	// A clean finish reports an empty class but must still read as present, so the
	// drain can tell a reported clean finish from a child that never reported.
	if err := d.Upsert("/repo", "COD-1", "", ""); err != nil {
		t.Fatalf("Upsert clean: %v", err)
	}
	rep, found, err = d.One("/repo", "COD-1")
	if err != nil || !found {
		t.Fatalf("One after clean upsert = found %v err %v, want present", found, err)
	}
	if rep.Class != "" || rep.Reason != "" {
		t.Fatalf("clean outcome = %+v, want empty class and reason", rep)
	}

	if err := d.Remove("/repo", "COD-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, found, _ := d.One("/repo", "COD-1"); found {
		t.Fatalf("One after Remove reported present")
	}
	// Removing an absent outcome is not an error.
	if err := d.Remove("/repo", "COD-1"); err != nil {
		t.Fatalf("Remove absent: %v", err)
	}
}
