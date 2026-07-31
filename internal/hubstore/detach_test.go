package hubstore

import (
	"errors"
	"testing"

	"github.com/RomkaLTU/trau/internal/queue"
)

// seedDetachFamily stores a synced epic with two children and the local rows that
// name them by identifier, the state a conversion has to carry over.
func seedDetachFamily(t *testing.T, s *Stores, root string) {
	t.Helper()
	if _, _, err := s.Issues().Upsert(root, "linear", []Issue{
		{Identifier: "COD-1", Title: "Epic", StatusGroup: "started"},
		{Identifier: "COD-2", Title: "Slice one", StatusGroup: "unstarted", Parent: "COD-1"},
		{Identifier: "COD-3", Title: "Slice two", StatusGroup: "unstarted", Parent: "COD-1"},
	}); err != nil {
		t.Fatalf("seed issues: %v", err)
	}
	if err := s.Issues().AddRelation(root, "COD-2", "COD-3"); err != nil {
		t.Fatalf("add relation: %v", err)
	}
}

func TestDetachToInternalConvertsSubtree(t *testing.T) {
	s, repo := purgeStores(t)
	root := repo.Root
	seedDetachFamily(t, s, root)
	sess, err := s.Grill().Create(NewGrillSession{Repo: root, IssueID: "COD-1"})
	if err != nil {
		t.Fatalf("create grill session: %v", err)
	}

	res, found, err := s.Issues().DetachToInternal(root, "ACME", "COD-1")
	if err != nil || !found {
		t.Fatalf("detach: found=%v err=%v", found, err)
	}
	if res.From != "COD-1" || res.To != "ACME-1" {
		t.Fatalf("detach root = %s → %s, want COD-1 → ACME-1", res.From, res.To)
	}
	if len(res.Converted) != 3 {
		t.Fatalf("converted = %+v, want the epic and both children", res.Converted)
	}

	for _, old := range []string{"COD-1", "COD-2", "COD-3"} {
		if _, found, err := s.Issues().Get(root, old); err != nil || found {
			t.Errorf("%s still in the backlog (found=%v err=%v)", old, found, err)
		}
		if n := countRows(t, s, `SELECT count(*) FROM issue_tombstones WHERE repo = ? AND identifier = ?`, root, old); n != 1 {
			t.Errorf("%s tombstones = %d, want 1 so a sync cannot re-import it", old, n)
		}
	}

	epic, found, err := s.Issues().Internal(root, "ACME-1")
	if err != nil || !found {
		t.Fatalf("converted epic: found=%v err=%v", found, err)
	}
	if epic.Title != "Epic" || epic.StatusGroup != "started" || epic.Status != "In Progress" {
		t.Errorf("converted epic = %+v, want the title and workflow state carried over", epic)
	}
	kids, err := s.Issues().InternalChildren(root, "ACME-1")
	if err != nil || len(kids) != 2 {
		t.Fatalf("converted children = %+v (err=%v), want both re-parented", kids, err)
	}
	blockers, err := s.Issues().Blockers(root, kids[1].Identifier)
	if err != nil || len(blockers) != 1 || blockers[0] != kids[0].Identifier {
		t.Errorf("blockers of %s = %v (err=%v), want the re-keyed sibling", kids[1].Identifier, blockers, err)
	}

	moved, _, err := s.Grill().Session(sess.ID)
	if err != nil || moved.IssueID != "ACME-1" {
		t.Errorf("session anchor = %q (err=%v), want the converted epic", moved.IssueID, err)
	}

	if _, _, err := s.Issues().Upsert(root, "linear", []Issue{
		{Identifier: "COD-1", Title: "Epic", StatusGroup: "started"},
	}); err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	if _, found, _ := s.Issues().Get(root, "COD-1"); found {
		t.Error("a later sync re-imported the retired ticket beside the internal issue it became")
	}
}

func TestDetachToInternalRefusesBusyMember(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		settle func(*Queue) error
	}{
		{name: "pending", status: queue.StatusPending},
		{
			name:   "running",
			status: queue.StatusRunning,
			settle: func(q *Queue) error { return q.MarkRunning("COD-2", 1234) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo := purgeStores(t)
			root := repo.Root
			seedDetachFamily(t, s, root)
			q := s.Queue(root)
			if _, err := q.Add(queue.Item{ID: "COD-2", Kind: queue.KindTicket}); err != nil {
				t.Fatalf("enqueue: %v", err)
			}
			if tc.settle != nil {
				if err := tc.settle(q); err != nil {
					t.Fatalf("settle queue item: %v", err)
				}
			}

			_, found, err := s.Issues().DetachToInternal(root, "ACME", "COD-1")
			var busy *IssueBusyError
			if !errors.As(err, &busy) {
				t.Fatalf("detach err = %v (found=%v), want an IssueBusyError", err, found)
			}
			if busy.Identifier != "COD-2" || busy.Status != tc.status {
				t.Errorf("refusal = %+v, want COD-2 named as %s", busy, tc.status)
			}
			if _, found, _ := s.Issues().Get(root, "COD-1"); !found {
				t.Error("the refused detach still converted the epic")
			}
			if n := countRows(t, s, `SELECT count(*) FROM issue_tombstones WHERE repo = ?`, root); n != 0 {
				t.Errorf("tombstones = %d, want none after a refusal", n)
			}
		})
	}
}

func TestDetachToInternalUnknownIssue(t *testing.T) {
	s, repo := purgeStores(t)
	if _, found, err := s.Issues().DetachToInternal(repo.Root, "ACME", "COD-9"); found || err != nil {
		t.Fatalf("detach of an unknown issue = found=%v err=%v, want found=false", found, err)
	}
}
