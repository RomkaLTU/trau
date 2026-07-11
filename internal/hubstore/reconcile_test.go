package hubstore

import (
	"testing"
)

const reconcileRepo = "/repo/acme"

func secondIssue() Issue {
	iss := sampleIssue()
	iss.Identifier = "COD-2"
	iss.Title = "Second"
	iss.ExternalID = "iss-2"
	iss.Comments = nil
	return iss
}

func seedSynced(t *testing.T, s *Issues, issues ...Issue) {
	t.Helper()
	if _, _, err := s.Upsert(reconcileRepo, "linear", issues); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}

func TestReconcileTombstonesMissingSynced(t *testing.T) {
	s := testIssues(t)
	seedSynced(t, s, sampleIssue(), secondIssue())

	tombstoned, err := s.Reconcile(reconcileRepo, []string{"COD-2"})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(tombstoned) != 1 || tombstoned[0] != "COD-1" {
		t.Fatalf("tombstoned = %v, want [COD-1]", tombstoned)
	}

	gone, ok, err := s.Get(reconcileRepo, "COD-1")
	if err != nil || !ok {
		t.Fatalf("Get COD-1 = (%v, %v)", ok, err)
	}
	if gone.DeletedAt == "" {
		t.Fatal("COD-1 should carry a tombstone timestamp")
	}
	kept, _, _ := s.Get(reconcileRepo, "COD-2")
	if kept.DeletedAt != "" {
		t.Fatalf("COD-2 should stay live, got deleted_at %q", kept.DeletedAt)
	}
}

func TestReconcileExcludesTombstonedFromBoardAndSearch(t *testing.T) {
	s := testIssues(t)
	seedSynced(t, s, sampleIssue(), secondIssue())
	if _, err := s.Reconcile(reconcileRepo, []string{"COD-2"}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	board, err := s.Backlog(reconcileRepo)
	if err != nil {
		t.Fatalf("Backlog: %v", err)
	}
	if len(board) != 1 || board[0].Identifier != "COD-2" {
		t.Fatalf("board = %+v, want only the live COD-2", board)
	}

	hits, err := s.Search(reconcileRepo, "First", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("search returned tombstoned issue: %+v", hits)
	}
}

func TestReconcileRevivesReturnedIssue(t *testing.T) {
	s := testIssues(t)
	seedSynced(t, s, sampleIssue(), secondIssue())
	if _, err := s.Reconcile(reconcileRepo, []string{"COD-2"}); err != nil {
		t.Fatalf("Reconcile tombstone: %v", err)
	}

	tombstoned, err := s.Reconcile(reconcileRepo, []string{"COD-1", "COD-2"})
	if err != nil {
		t.Fatalf("Reconcile revive: %v", err)
	}
	if len(tombstoned) != 0 {
		t.Fatalf("revival tombstoned %v, want none", tombstoned)
	}
	revived, _, _ := s.Get(reconcileRepo, "COD-1")
	if revived.DeletedAt != "" {
		t.Fatalf("COD-1 should be revived, got deleted_at %q", revived.DeletedAt)
	}
}

func TestReconcileLeavesInternalIssuesUntouched(t *testing.T) {
	s := testIssues(t)
	seedSynced(t, s, sampleIssue())
	internal, err := s.CreateInternal(reconcileRepo, "LOOP", InternalDraft{Title: "hand-filed"})
	if err != nil {
		t.Fatalf("CreateInternal: %v", err)
	}

	if _, err := s.Reconcile(reconcileRepo, nil); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, ok, err := s.Get(reconcileRepo, internal.Identifier)
	if err != nil || !ok {
		t.Fatalf("Get internal = (%v, %v)", ok, err)
	}
	if got.DeletedAt != "" {
		t.Fatal("an internal issue must never be tombstoned by reconcile")
	}
}

func TestUpsertRevivesTombstonedIssue(t *testing.T) {
	s := testIssues(t)
	seedSynced(t, s, sampleIssue(), secondIssue())
	if _, err := s.Reconcile(reconcileRepo, []string{"COD-2"}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	seedSynced(t, s, sampleIssue())
	revived, _, _ := s.Get(reconcileRepo, "COD-1")
	if revived.DeletedAt != "" {
		t.Fatalf("re-syncing COD-1 should clear its tombstone, got %q", revived.DeletedAt)
	}
}

func TestDropSyncedRemovesSyncedKeepsInternalAndResetsCursor(t *testing.T) {
	s := testIssues(t)
	seedSynced(t, s, sampleIssue(), secondIssue())
	internal, err := s.CreateInternal(reconcileRepo, "LOOP", InternalDraft{Title: "hand-filed"})
	if err != nil {
		t.Fatalf("CreateInternal: %v", err)
	}
	if err := s.RecordResult(reconcileRepo, SyncResult{Issues: 2, Cursor: "2026-07-10T12:00:00Z", SyncedAt: "2026-07-10T12:00:01Z"}); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}

	if err := s.DropSynced(reconcileRepo); err != nil {
		t.Fatalf("DropSynced: %v", err)
	}

	if _, ok, _ := s.Get(reconcileRepo, "COD-1"); ok {
		t.Fatal("synced COD-1 should be gone after DropSynced")
	}
	if _, ok, _ := s.Get(reconcileRepo, internal.Identifier); !ok {
		t.Fatal("internal issue should survive DropSynced")
	}
	st, err := s.SyncState(reconcileRepo)
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if st.Cursor != "" || st.LastSyncedAt != "" || st.LastIssues != 0 {
		t.Fatalf("cursor not reset: %+v", st)
	}
}

func TestGetUnknownIssueReportsNotFound(t *testing.T) {
	s := testIssues(t)
	if _, ok, err := s.Get(reconcileRepo, "COD-404"); err != nil || ok {
		t.Fatalf("Get missing = (%v, %v), want (false, nil)", ok, err)
	}
}
