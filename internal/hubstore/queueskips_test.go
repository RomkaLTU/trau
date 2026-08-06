package hubstore

import (
	"reflect"
	"testing"

	"github.com/RomkaLTU/trau/internal/queue"
)

func TestSkipsPersistAcrossStores(t *testing.T) {
	db := testDB(t)
	first := NewQueue(db, "/repo/acme")
	if _, err := first.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-1", Skips: []string{"verify", "merge"}}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	mustAdd(t, first, "COD-2")

	items, err := NewQueue(db, "/repo/acme").Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := items[0].Skips; !reflect.DeepEqual(got, []string{"verify", "merge"}) {
		t.Errorf("COD-1 skips = %v, want [verify merge] round-tripped", got)
	}
	if got := items[1].Skips; len(got) != 0 {
		t.Errorf("COD-2 skips = %v, want none for a run that skips nothing", got)
	}
}

func TestAddFrontAdoptsIncomingSkips(t *testing.T) {
	q := testQueue(t)
	if _, err := q.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-1", Skips: []string{"verify"}}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	mustAdd(t, q, "COD-2")

	items, moved, err := q.AddFront(queue.Item{Kind: queue.KindTicket, ID: "COD-1", Skips: []string{"ci", "merge"}})
	if err != nil {
		t.Fatalf("AddFront: %v", err)
	}
	if !moved {
		t.Error("moved = false, want a move-to-front of the pending item")
	}
	if got := items[0].Skips; !reflect.DeepEqual(got, []string{"ci", "merge"}) {
		t.Errorf("skips = %v, want the incoming set adopted", got)
	}

	cleared, _, err := q.AddFront(queue.Item{Kind: queue.KindTicket, ID: "COD-1"})
	if err != nil {
		t.Fatalf("AddFront clearing: %v", err)
	}
	if got := cleared[0].Skips; len(got) != 0 {
		t.Errorf("skips = %v, want cleared by a re-run that skips nothing", got)
	}
	reloaded, err := q.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := reloaded[0].Skips; len(got) != 0 {
		t.Errorf("stored skips = %v, want the cleared set persisted", got)
	}
}

// TestAddFrontAdoptsSkipsOnPausedItem proves a paused row answers the picker the
// same way a pending one does: Run next and the issue drawer's Run both re-land
// it on the set just confirmed — clearing it included — and it stays paused, so
// the resume still picks up from the checkpoint it parked at.
func TestAddFrontAdoptsSkipsOnPausedItem(t *testing.T) {
	q := testQueue(t)
	if _, err := q.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-1", Skips: []string{"verify", "merge"}}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Pause("COD-1", "handback"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	items, moved, err := q.AddFront(queue.Item{Kind: queue.KindTicket, ID: "COD-1"})
	if err != nil {
		t.Fatalf("AddFront: %v", err)
	}
	if !moved {
		t.Error("moved = false, want a move-to-front of the paused item")
	}
	if got := items[0].Skips; len(got) != 0 {
		t.Errorf("skips = %v, want cleared by a re-run that skips nothing", got)
	}
	if got := items[0].Status; got != queue.StatusPaused {
		t.Errorf("status = %q, want the pause kept so the resume reads its checkpoint", got)
	}

	again, _, err := q.AddFront(queue.Item{Kind: queue.KindTicket, ID: "COD-1", Skips: []string{"verify"}})
	if err != nil {
		t.Fatalf("AddFront re-skipping: %v", err)
	}
	if got := again[0].Skips; !reflect.DeepEqual(got, []string{"verify"}) {
		t.Errorf("skips = %v, want the incoming set adopted", got)
	}
}

func TestMoveToFrontKeepsSkips(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	if _, err := q.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-2", Skips: []string{"lintfix"}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	items, err := q.MoveToFront("COD-2")
	if err != nil {
		t.Fatalf("MoveToFront: %v", err)
	}
	if got := items[0].Skips; !reflect.DeepEqual(got, []string{"lintfix"}) {
		t.Errorf("skips = %v, want the per-run set preserved by a promote", got)
	}
}
