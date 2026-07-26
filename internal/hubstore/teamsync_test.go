package hubstore

import (
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func testTeamSync(t *testing.T) *TeamSync {
	t.Helper()
	home := t.TempDir()
	db, err := hubdb.Open(home)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStores(home, db.SQL(), nil, Retention{}).TeamSync()
}

func TestTeamSyncReplaceIsWholesaleAndPerRepo(t *testing.T) {
	store := testTeamSync(t)

	if err := store.Replace("/repo/a", []TeamRecord{
		{WriterID: "w1", AuthorName: "Ada", Payload: `{"version":1}`},
		{WriterID: "w2", AuthorName: "Grace", Payload: `{"version":1}`},
	}); err != nil {
		t.Fatalf("replace a: %v", err)
	}
	if err := store.Replace("/repo/b", []TeamRecord{{WriterID: "w3", Payload: `{"version":1}`}}); err != nil {
		t.Fatalf("replace b: %v", err)
	}

	// A writer who withdrew their ref disappears without a tombstone.
	if err := store.Replace("/repo/a", []TeamRecord{{WriterID: "w2", AuthorName: "Grace", Payload: `{"version":1}`}}); err != nil {
		t.Fatalf("replace a again: %v", err)
	}

	got, err := store.Records("/repo/a")
	if err != nil {
		t.Fatalf("records a: %v", err)
	}
	if len(got) != 1 || got[0].WriterID != "w2" {
		t.Fatalf("records a = %+v, want only w2", got)
	}

	other, err := store.Records("/repo/b")
	if err != nil {
		t.Fatalf("records b: %v", err)
	}
	if len(other) != 1 || other[0].WriterID != "w3" {
		t.Errorf("records b = %+v, want the other repo untouched", other)
	}
}

func TestTeamSyncRecordsEmptyRepo(t *testing.T) {
	got, err := testTeamSync(t).Records("/repo/none")
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("records = %+v, want empty", got)
	}
}

func TestTeamSyncStateRoundTrip(t *testing.T) {
	store := testTeamSync(t)

	zero, err := store.State("/repo/a")
	if err != nil {
		t.Fatalf("state before any sync: %v", err)
	}
	if zero != (TeamSyncState{}) {
		t.Errorf("state = %+v, want zero before any sync", zero)
	}

	failed := TeamSyncState{WriterID: "w1", LastSyncAt: "2026-07-26T10:00:00Z", LastError: "push rejected"}
	if err := store.SaveState("/repo/a", failed); err != nil {
		t.Fatalf("save failed state: %v", err)
	}
	got, err := store.State("/repo/a")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if got != failed {
		t.Errorf("state = %+v, want %+v", got, failed)
	}

	clean := TeamSyncState{WriterID: "w1", LastSyncAt: "2026-07-26T11:00:00Z"}
	if err := store.SaveState("/repo/a", clean); err != nil {
		t.Fatalf("save clean state: %v", err)
	}
	got, err = store.State("/repo/a")
	if err != nil {
		t.Fatalf("state after clean pass: %v", err)
	}
	if got != clean {
		t.Errorf("state = %+v, want the clean pass to clear the error", got)
	}
}
