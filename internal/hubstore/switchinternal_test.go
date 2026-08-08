package hubstore

import "testing"

// TestRaiseInternalSeqClearsTheDroppedMirror is the numbering a switch to the
// internal tracker rests on when the tracker it leaves numbered its tickets under
// the same prefix: the sequence is lifted clear of the mirror's highest id before
// the mirror is dropped, and InternalSeq is how a later reader tells a retired id
// from one the internal tracker can still mint.
func TestRaiseInternalSeqClearsTheDroppedMirror(t *testing.T) {
	s := testIssues(t)
	const root = "/repo/save24"

	next, err := s.InternalSeq(root)
	if err != nil {
		t.Fatalf("seq before minting: %v", err)
	}
	if next != 1 {
		t.Fatalf("seq before minting = %d, want 1", next)
	}

	if _, _, err := s.Upsert(root, "jira", []Issue{
		{Identifier: "SAVE24-766", Title: "Mirrored"},
		{Identifier: "SAVE24-767", Title: "Mirrored, highest"},
	}); err != nil {
		t.Fatalf("seed mirror: %v", err)
	}

	raised, err := s.RaiseInternalSeq(root, "SAVE24")
	if err != nil {
		t.Fatalf("raise seq: %v", err)
	}
	if raised != 768 {
		t.Fatalf("raised = %d, want 768 — clear of SAVE24-767", raised)
	}

	if err := s.DropSynced(root); err != nil {
		t.Fatalf("drop mirror: %v", err)
	}
	got, err := s.InternalSeq(root)
	if err != nil {
		t.Fatalf("seq after the mirror is dropped: %v", err)
	}
	if got != raised {
		t.Fatalf("InternalSeq = %d, want the raised %d to outlive the mirror", got, raised)
	}
}
