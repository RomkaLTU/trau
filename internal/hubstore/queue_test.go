package hubstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/queue"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db.SQL()
}

func testQueue(t *testing.T) *Queue {
	t.Helper()
	return NewQueue(testDB(t), "/repo/acme")
}

func ids(items []queue.Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func mustAdd(t *testing.T, q *Queue, id string) {
	t.Helper()
	if _, err := q.Add(queue.Item{Kind: queue.KindTicket, ID: id}); err != nil {
		t.Fatalf("Add(%s): %v", id, err)
	}
}

func TestAddOrdersDedupsAndStamps(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	mustAdd(t, q, "COD-2")

	if _, err := q.Add(queue.Item{ID: "COD-1"}); err != queue.ErrAlreadyQueued {
		t.Fatalf("re-add = %v, want ErrAlreadyQueued", err)
	}

	items, err := q.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := ids(items); !reflect.DeepEqual(got, []string{"COD-1", "COD-2"}) {
		t.Fatalf("order = %v, want [COD-1 COD-2]", got)
	}
	if items[0].Status != queue.StatusPending {
		t.Errorf("status = %q, want pending", items[0].Status)
	}
	if items[0].QueuedAt.IsZero() {
		t.Error("QueuedAt not stamped on add")
	}
}

func TestAddFrontInsertsAtFirstPending(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	mustAdd(t, q, "COD-2")
	if err := q.MarkRunning("COD-1", 4242); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	items, moved, err := q.AddFront(queue.Item{Kind: queue.KindTicket, ID: "COD-3", Provider: "codex"})
	if err != nil {
		t.Fatalf("AddFront: %v", err)
	}
	if moved {
		t.Error("moved = true, want a fresh insert")
	}
	if got := ids(items); !reflect.DeepEqual(got, []string{"COD-1", "COD-3", "COD-2"}) {
		t.Fatalf("order = %v, want COD-3 first pending, behind running COD-1", got)
	}
	if items[1].Status != queue.StatusPending || items[1].QueuedAt.IsZero() {
		t.Errorf("front insert not stamped: %+v", items[1])
	}
	if items[1].Provider != "codex" {
		t.Errorf("provider = %q, want codex", items[1].Provider)
	}
}

func TestAddFrontMovesPendingToFront(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	mustAdd(t, q, "COD-2")
	mustAdd(t, q, "COD-3")

	items, moved, err := q.AddFront(queue.Item{Kind: queue.KindTicket, ID: "COD-3", Provider: "codex"})
	if err != nil {
		t.Fatalf("AddFront: %v", err)
	}
	if !moved {
		t.Error("moved = false, want a move-to-front of the pending item")
	}
	if got := ids(items); !reflect.DeepEqual(got, []string{"COD-3", "COD-1", "COD-2"}) {
		t.Fatalf("order = %v, want [COD-3 COD-1 COD-2]", got)
	}
	if items[0].Provider != "codex" {
		t.Errorf("provider = %q, want the incoming override adopted", items[0].Provider)
	}
	if items[0].QueuedAt.IsZero() {
		t.Error("QueuedAt lost on move")
	}
}

func TestAddFrontGuardsNonPending(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	mustAdd(t, q, "COD-2")
	if err := q.MarkRunning("COD-1", 4242); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := q.Pause("COD-2", "faulted"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	if _, _, err := q.AddFront(queue.Item{ID: "COD-1"}); err != queue.ErrAlreadyQueued {
		t.Fatalf("front re-add of running = %v, want ErrAlreadyQueued", err)
	}
	if _, _, err := q.AddFront(queue.Item{ID: "COD-2"}); err != queue.ErrAlreadyQueued {
		t.Fatalf("front re-add of paused = %v, want ErrAlreadyQueued", err)
	}
	items, err := q.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := ids(items); !reflect.DeepEqual(got, []string{"COD-1", "COD-2"}) {
		t.Fatalf("order = %v, want untouched [COD-1 COD-2]", got)
	}
}

func TestProviderPersistsAcrossStores(t *testing.T) {
	db := testDB(t)
	first := NewQueue(db, "/repo/acme")
	if _, err := first.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-1", Provider: "codex"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	mustAdd(t, first, "COD-2")

	items, err := NewQueue(db, "/repo/acme").Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Provider != "codex" {
		t.Errorf("COD-1 provider = %q, want codex round-tripped", items[0].Provider)
	}
	if items[1].Provider != "" {
		t.Errorf("COD-2 provider = %q, want empty for the config default", items[1].Provider)
	}
}

func TestPersistsAcrossStores(t *testing.T) {
	db := testDB(t)
	first := NewQueue(db, "/repo/acme")
	mustAdd(t, first, "COD-1")
	if err := first.Arm(true, queue.OnFaultSkip); err != nil {
		t.Fatalf("Arm: %v", err)
	}

	second := NewQueue(db, "/repo/acme")
	items, meta, err := second.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !meta.Draining || len(items) != 1 || items[0].ID != "COD-1" {
		t.Fatalf("reopened snapshot = %v draining=%v, want the persisted COD-1 armed", ids(items), meta.Draining)
	}
	if !meta.NoResume || meta.OnFault != queue.OnFaultSkip {
		t.Fatalf("meta = %+v, want no-resume + on-fault=skip", meta)
	}
}

func TestQueuesAreIsolatedByRoot(t *testing.T) {
	db := testDB(t)
	a := NewQueue(db, "/repo/a")
	b := NewQueue(db, "/repo/b")
	mustAdd(t, a, "COD-1")
	mustAdd(t, b, "COD-2")

	if items, _ := a.Load(); len(items) != 1 || items[0].ID != "COD-1" {
		t.Fatalf("repo a = %v, want just COD-1", ids(items))
	}
	if items, _ := b.Load(); len(items) != 1 || items[0].ID != "COD-2" {
		t.Fatalf("repo b = %v, want just COD-2", ids(items))
	}
}

func TestRemoveKeepsOrderAndGuardsRunning(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	mustAdd(t, q, "COD-2")
	mustAdd(t, q, "COD-3")

	if _, err := q.Remove("COD-9"); err != queue.ErrNotQueued {
		t.Fatalf("remove absent = %v, want ErrNotQueued", err)
	}
	if err := q.MarkRunning("COD-2", 7); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if _, err := q.Remove("COD-2"); err != queue.ErrRunning {
		t.Fatalf("remove running = %v, want ErrRunning", err)
	}
	items, err := q.Remove("COD-1")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := ids(items); !reflect.DeepEqual(got, []string{"COD-2", "COD-3"}) {
		t.Fatalf("after remove = %v, want [COD-2 COD-3]", got)
	}
}

func TestForceRemoveDropsARunningRow(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	mustAdd(t, q, "COD-2")
	if err := q.MarkRunning("COD-1", 7); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	if _, err := q.ForceRemove("COD-9"); err != queue.ErrNotQueued {
		t.Fatalf("force remove absent = %v, want ErrNotQueued", err)
	}
	items, err := q.ForceRemove("COD-1")
	if err != nil {
		t.Fatalf("ForceRemove: %v", err)
	}
	if got := ids(items); !reflect.DeepEqual(got, []string{"COD-2"}) {
		t.Fatalf("after force remove = %v, want [COD-2]", got)
	}
}

func TestPromoteSwapsChildrenForTheEpicInPlace(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	mustAdd(t, q, "COD-2")
	mustAdd(t, q, "COD-3")
	mustAdd(t, q, "COD-4")

	epic := queue.Item{
		Kind:      queue.KindEpic,
		ID:        "COD-10",
		SubIssues: []queue.SubIssue{{ID: "COD-2"}, {ID: "COD-3"}},
	}
	items, err := q.Promote(epic, []string{"COD-3", "COD-2"})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if got := ids(items); !reflect.DeepEqual(got, []string{"COD-1", "COD-10", "COD-4"}) {
		t.Fatalf("after promote = %v, want the epic where its first child sat", got)
	}
	if items[1].Status != queue.StatusPending || items[1].QueuedAt.IsZero() {
		t.Errorf("epic = %+v, want a pending row stamped with its queue time", items[1])
	}
	if len(items[1].SubIssues) != 2 {
		t.Errorf("sub_issues = %+v, want the two children it covers", items[1].SubIssues)
	}
}

func TestPromoteGuardsRowsItCannotSwap(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, q *Queue)
		ids     []string
		want    error
	}{
		{
			name: "child no longer queued",
			ids:  []string{"COD-1", "COD-9"},
			want: queue.ErrNotQueued,
		},
		{
			name: "child already running",
			prepare: func(t *testing.T, q *Queue) {
				if err := q.MarkRunning("COD-2", 11); err != nil {
					t.Fatalf("MarkRunning: %v", err)
				}
			},
			ids:  []string{"COD-1", "COD-2"},
			want: queue.ErrRunning,
		},
		{
			name: "child already settled",
			prepare: func(t *testing.T, q *Queue) {
				if err := q.MarkSkipped("COD-2", "duplicate"); err != nil {
					t.Fatalf("MarkSkipped: %v", err)
				}
			},
			ids:  []string{"COD-1", "COD-2"},
			want: queue.ErrNotQueued,
		},
		{
			name: "epic already queued",
			prepare: func(t *testing.T, q *Queue) {
				if _, err := q.Add(queue.Item{Kind: queue.KindEpic, ID: "COD-10"}); err != nil {
					t.Fatalf("Add epic: %v", err)
				}
			},
			ids:  []string{"COD-1"},
			want: queue.ErrAlreadyQueued,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := testQueue(t)
			mustAdd(t, q, "COD-1")
			mustAdd(t, q, "COD-2")
			if tt.prepare != nil {
				tt.prepare(t, q)
			}

			if _, err := q.Promote(queue.Item{Kind: queue.KindEpic, ID: "COD-10"}, tt.ids); !errors.Is(err, tt.want) {
				t.Fatalf("Promote = %v, want %v", err, tt.want)
			}
			items, err := q.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			for _, it := range items {
				if it.ID == "COD-1" && it.Status != queue.StatusPending {
					t.Errorf("COD-1 = %q, want a refused promote to write nothing", it.Status)
				}
			}
		})
	}
}

func TestMoveReorders(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	mustAdd(t, q, "COD-2")
	mustAdd(t, q, "COD-3")

	if items, err := q.Move("COD-3", -1); err != nil || !reflect.DeepEqual(ids(items), []string{"COD-1", "COD-3", "COD-2"}) {
		t.Fatalf("move up = %v (%v), want [COD-1 COD-3 COD-2]", ids(items), err)
	}
	if items, err := q.Move("COD-1", -1); err != nil || !reflect.DeepEqual(ids(items), []string{"COD-1", "COD-3", "COD-2"}) {
		t.Fatalf("move past front = %v (%v), want unchanged", ids(items), err)
	}
}

func TestMoveGuardsRunningItem(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	mustAdd(t, q, "COD-2")
	if err := q.MarkRunning("COD-1", 7); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if _, err := q.Move("COD-1", 1); err != queue.ErrRunning {
		t.Fatalf("move running = %v, want ErrRunning", err)
	}
	if items, err := q.Move("COD-2", -1); err != nil || !reflect.DeepEqual(ids(items), []string{"COD-1", "COD-2"}) {
		t.Fatalf("jumping the running item = %v (%v), want unchanged", ids(items), err)
	}
}

func TestMoveToFrontPromotesBehindTheRunningItem(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	mustAdd(t, q, "COD-2")
	if _, err := q.Add(queue.Item{
		Kind:      queue.KindEpic,
		ID:        "COD-10",
		Provider:  "codex",
		SubIssues: []queue.SubIssue{{ID: "COD-11", Title: "First", State: "todo"}},
	}); err != nil {
		t.Fatalf("Add epic: %v", err)
	}
	if err := q.MarkRunning("COD-1", 4242); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	before, err := q.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	items, err := q.MoveToFront("COD-10")
	if err != nil {
		t.Fatalf("MoveToFront: %v", err)
	}
	if got := ids(items); !reflect.DeepEqual(got, []string{"COD-1", "COD-10", "COD-2"}) {
		t.Fatalf("order = %v, want COD-10 first pending, behind running COD-1", got)
	}
	promoted := items[1]
	if promoted.Provider != "codex" {
		t.Errorf("provider = %q, want the per-run override preserved", promoted.Provider)
	}
	if !promoted.QueuedAt.Equal(before[2].QueuedAt) {
		t.Errorf("queued_at = %v, want the original stamp %v", promoted.QueuedAt, before[2].QueuedAt)
	}
	if !reflect.DeepEqual(promoted.SubIssues, before[2].SubIssues) {
		t.Errorf("sub_issues = %+v, want %+v", promoted.SubIssues, before[2].SubIssues)
	}
}

// TestMoveToFrontResumesAPausedItemAhead drives the running view's Resume: the
// paused row sits behind pending work, and promoting it makes it the first
// runnable item so arming the drain resumes that ticket and not the one in front.
func TestMoveToFrontResumesAPausedItemAhead(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	mustAdd(t, q, "COD-2")
	if err := q.Pause("COD-2", "needs re-auth"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	items, err := q.MoveToFront("COD-2")
	if err != nil {
		t.Fatalf("MoveToFront: %v", err)
	}
	if got := ids(items); !reflect.DeepEqual(got, []string{"COD-2", "COD-1"}) {
		t.Fatalf("order = %v, want the resumed pause first", got)
	}
	if items[0].Status != queue.StatusPaused {
		t.Errorf("status = %q, want the pause kept so the run resumes from its checkpoint", items[0].Status)
	}
}

// TestMoveToFrontPromotesAheadOfAPausedItem drives the running view's Run next
// with a pause sitting at the head of the remaining work: the promoted row lands
// where the drain launches next, not merely ahead of the pending rows behind it.
func TestMoveToFrontPromotesAheadOfAPausedItem(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	mustAdd(t, q, "COD-2")
	mustAdd(t, q, "COD-3")
	if err := q.Pause("COD-1", "needs re-auth"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	items, err := q.MoveToFront("COD-3")
	if err != nil {
		t.Fatalf("MoveToFront: %v", err)
	}
	if got := ids(items); !reflect.DeepEqual(got, []string{"COD-3", "COD-1", "COD-2"}) {
		t.Fatalf("order = %v, want the promoted item ahead of the pause", got)
	}
}

func TestMoveToFrontGuardsRowsItCannotPromote(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, q *Queue)
		id      string
		want    error
	}{
		{
			name: "item not queued",
			id:   "COD-9",
			want: queue.ErrNotQueued,
		},
		{
			name: "item already running",
			prepare: func(t *testing.T, q *Queue) {
				if err := q.MarkRunning("COD-2", 11); err != nil {
					t.Fatalf("MarkRunning: %v", err)
				}
			},
			id:   "COD-2",
			want: queue.ErrRunning,
		},
		{
			name: "item already settled",
			prepare: func(t *testing.T, q *Queue) {
				if err := q.Finish("COD-2", queue.StatusDone, ""); err != nil {
					t.Fatalf("Finish: %v", err)
				}
			},
			id:   "COD-2",
			want: queue.ErrNotPending,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := testQueue(t)
			mustAdd(t, q, "COD-1")
			mustAdd(t, q, "COD-2")
			if tt.prepare != nil {
				tt.prepare(t, q)
			}

			if _, err := q.MoveToFront(tt.id); !errors.Is(err, tt.want) {
				t.Fatalf("MoveToFront(%s) = %v, want %v", tt.id, err, tt.want)
			}
			items, err := q.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := ids(items); !reflect.DeepEqual(got, []string{"COD-1", "COD-2"}) {
				t.Fatalf("order = %v, want a refused promote to write nothing", got)
			}
		})
	}
}

func TestPauseParksAndStopsDraining(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	if err := q.SetDraining(true); err != nil {
		t.Fatalf("SetDraining: %v", err)
	}
	if err := q.MarkRunning("COD-1", 7); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := q.Pause("COD-1", "needs re-auth"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	items, meta, err := q.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if meta.Draining {
		t.Error("Pause left the queue draining")
	}
	if items[0].Status != queue.StatusPaused || items[0].Reason != "needs re-auth" || items[0].PID != 0 {
		t.Fatalf("paused item = %+v, want paused with reason and no pid", items[0])
	}
}

func TestFinishDoneSettlesSubIssues(t *testing.T) {
	q := testQueue(t)
	if _, err := q.Add(queue.Item{
		Kind: queue.KindEpic,
		ID:   "COD-1",
		SubIssues: []queue.SubIssue{
			{ID: "COD-2", Title: "child", State: "todo"},
			{ID: "COD-3", Title: "other", State: "in_progress"},
		},
	}); err != nil {
		t.Fatalf("Add epic: %v", err)
	}
	if err := q.MarkRunning("COD-1", 7); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := q.Finish("COD-1", queue.StatusDone, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	items, _ := q.Load()
	for _, sub := range items[0].SubIssues {
		if sub.State != "done" {
			t.Fatalf("sub %s state = %q, want done after a clean epic finish", sub.ID, sub.State)
		}
	}
}

func TestFinishOtherOutcomeLeavesSubIssues(t *testing.T) {
	q := testQueue(t)
	if _, err := q.Add(queue.Item{
		Kind:      queue.KindEpic,
		ID:        "COD-1",
		SubIssues: []queue.SubIssue{{ID: "COD-2", State: "todo"}},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Finish("COD-1", queue.StatusFailed, "boom"); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	items, _ := q.Load()
	if items[0].SubIssues[0].State != "todo" {
		t.Fatalf("sub state = %q, want left at todo on a non-done finish", items[0].SubIssues[0].State)
	}
}

func TestFinishDrainingClearsWhenDry(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	if err := q.SetDraining(true); err != nil {
		t.Fatalf("SetDraining: %v", err)
	}

	if done, _ := q.FinishDraining(); done {
		t.Fatal("FinishDraining cleared while an item was still pending")
	}
	if err := q.Finish("COD-1", queue.StatusDone, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	done, err := q.FinishDraining()
	if err != nil {
		t.Fatalf("FinishDraining: %v", err)
	}
	if !done {
		t.Fatal("FinishDraining did not clear a dry queue")
	}
	if _, meta, _ := q.Snapshot(); meta.Draining || !meta.DrainingSince.IsZero() {
		t.Errorf("meta = %+v, want disarmed and unstamped after FinishDraining", meta)
	}
}

func TestFinishDrainingDisarmsAQueueWithNothingRunnable(t *testing.T) {
	tests := []struct {
		name  string
		items []queue.Item
	}{
		{name: "empty"},
		{name: "settled only", items: []queue.Item{
			{ID: "COD-1", Status: queue.StatusDone},
			{ID: "COD-2", Status: queue.StatusFailed},
			{ID: "COD-3", Status: queue.StatusSkipped},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := testQueue(t)
			for _, it := range tc.items {
				mustAdd(t, q, it.ID)
				if err := q.Finish(it.ID, it.Status, ""); err != nil {
					t.Fatalf("Finish %s: %v", it.ID, err)
				}
			}
			if err := q.SetDraining(true); err != nil {
				t.Fatalf("SetDraining: %v", err)
			}

			done, err := q.FinishDraining()
			if err != nil {
				t.Fatalf("FinishDraining: %v", err)
			}
			if !done {
				t.Fatal("FinishDraining left the queue armed over work it cannot run")
			}
			if _, meta, _ := q.Snapshot(); meta.Draining || !meta.DrainingSince.IsZero() {
				t.Errorf("meta = %+v, want disarmed and unstamped", meta)
			}
		})
	}
}

func TestArmRefusesAQueueWithNothingRunnable(t *testing.T) {
	tests := []struct {
		name   string
		settle map[string]string
	}{
		{name: "empty"},
		{name: "settled only", settle: map[string]string{"COD-1": queue.StatusDone, "COD-2": queue.StatusSkipped}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := testQueue(t)
			for id, status := range tc.settle {
				mustAdd(t, q, id)
				if err := q.Finish(id, status, ""); err != nil {
					t.Fatalf("Finish %s: %v", id, err)
				}
			}

			if err := q.Arm(true, queue.OnFaultSkip); !errors.Is(err, queue.ErrNoRunnableItems) {
				t.Fatalf("Arm = %v, want ErrNoRunnableItems", err)
			}
			items, meta, err := q.Snapshot()
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			if meta.Draining || !meta.DrainingSince.IsZero() || meta.NoResume || meta.OnFault != "" {
				t.Errorf("meta = %+v, want untouched by the refused arm", meta)
			}
			for _, it := range items {
				if it.Status != tc.settle[it.ID] {
					t.Errorf("%s = %q, want %q — a refused arm resets nothing", it.ID, it.Status, tc.settle[it.ID])
				}
			}
		})
	}
}

func TestArmStartsOnPendingOrPaused(t *testing.T) {
	tests := []struct {
		name   string
		paused bool
	}{
		{name: "pending item"},
		{name: "paused item", paused: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := testQueue(t)
			mustAdd(t, q, "COD-1")
			if tc.paused {
				if err := q.Pause("COD-1", "faulted"); err != nil {
					t.Fatalf("Pause: %v", err)
				}
			}

			before := time.Now().UTC()
			if err := q.Arm(false, queue.OnFaultHalt); err != nil {
				t.Fatalf("Arm: %v", err)
			}
			_, meta, err := q.Snapshot()
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			if !meta.Draining || meta.DrainingSince.Before(before) {
				t.Fatalf("meta = %+v, want armed and stamped at or after %v", meta, before)
			}
			if meta.OnFault != queue.OnFaultHalt || meta.NoResume {
				t.Errorf("meta = %+v, want the start's options recorded", meta)
			}

			if err := q.Arm(false, queue.OnFaultHalt); err != nil {
				t.Fatalf("re-arm: %v", err)
			}
			if _, again, _ := q.Snapshot(); !again.DrainingSince.Equal(meta.DrainingSince) {
				t.Errorf("draining since = %v after a re-arm, want the original %v", again.DrainingSince, meta.DrainingSince)
			}
		})
	}
}

func TestArmWithNoResumeRestartsTheQueue(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	mustAdd(t, q, "COD-2")
	mustAdd(t, q, "COD-3")
	if err := q.Finish("COD-1", queue.StatusFailed, "boom"); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := q.MarkRunning("COD-2", 7); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	if err := q.Arm(true, queue.OnFaultSkip); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	items, meta, err := q.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if items[0].Status != queue.StatusPending || items[0].Reason != "" {
		t.Errorf("COD-1 = %+v, want reset to pending by the skip-resume start", items[0])
	}
	if items[1].Status != queue.StatusRunning {
		t.Errorf("COD-2 = %q, want the running item left alone", items[1].Status)
	}
	if !meta.NoResume || meta.OnFault != queue.OnFaultSkip {
		t.Errorf("meta = %+v, want the start's options recorded with the arm", meta)
	}
}

func TestSetDrainingStampsTheRunItArms(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	if _, meta, _ := q.Snapshot(); !meta.DrainingSince.IsZero() {
		t.Fatalf("draining since = %v on an idle queue, want zero", meta.DrainingSince)
	}

	before := time.Now().UTC()
	if err := q.SetDraining(true); err != nil {
		t.Fatalf("SetDraining: %v", err)
	}
	_, armed, err := q.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if armed.DrainingSince.Before(before) {
		t.Fatalf("draining since = %v, want at or after the arm at %v", armed.DrainingSince, before)
	}

	if err := q.SetDraining(true); err != nil {
		t.Fatalf("re-arm: %v", err)
	}
	if _, meta, _ := q.Snapshot(); !meta.DrainingSince.Equal(armed.DrainingSince) {
		t.Errorf("draining since = %v after a re-arm, want the original %v", meta.DrainingSince, armed.DrainingSince)
	}

	if err := q.SetDraining(false); err != nil {
		t.Fatalf("disarm: %v", err)
	}
	if _, meta, _ := q.Snapshot(); !meta.DrainingSince.IsZero() {
		t.Errorf("draining since = %v after a disarm, want zero", meta.DrainingSince)
	}
}

// TestConcurrentAddsPreserveEveryItem proves the store's per-mutation lock keeps
// concurrent adds from clobbering item order: every distinct id lands exactly
// once and the persisted order is stable across a re-read.
func TestConcurrentAddsPreserveEveryItem(t *testing.T) {
	db := testDB(t)
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "COD-" + string(rune('A'+i/26)) + string(rune('a'+i%26))
			if _, err := NewQueue(db, "/repo/acme").Add(queue.Item{ID: id}); err != nil {
				t.Errorf("Add(%s): %v", id, err)
			}
		}(i)
	}
	wg.Wait()

	items, err := NewQueue(db, "/repo/acme").Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(items) != n {
		t.Fatalf("len = %d, want %d — a concurrent add was lost", len(items), n)
	}
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.ID] {
			t.Fatalf("duplicate id %s after concurrent adds", it.ID)
		}
		seen[it.ID] = true
	}
	if again, _ := NewQueue(db, "/repo/acme").Load(); !reflect.DeepEqual(ids(again), ids(items)) {
		t.Fatal("order not stable across re-read")
	}
}

func TestImportLegacyQueuePreservesOrderAndSettings(t *testing.T) {
	root := t.TempDir()
	queuedAt := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	writeLegacyQueue(t, root, legacyQueue{
		Draining: true,
		NoResume: true,
		OnFault:  queue.OnFaultSkip,
		Items: []queue.Item{
			{Kind: queue.KindEpic, ID: "COD-1", Status: queue.StatusPending, QueuedAt: queuedAt, SubIssues: []queue.SubIssue{{ID: "COD-9", State: "todo"}}},
			{Kind: queue.KindTicket, ID: "COD-2", Status: queue.StatusPaused, Reason: "was faulted", Provider: "codex", QueuedAt: queuedAt},
		},
	})

	q := NewQueue(testDB(t), root)
	if err := q.ImportLegacy(); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}

	items, meta, err := q.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !meta.Draining {
		t.Error("draining flag not imported")
	}
	if got := ids(items); !reflect.DeepEqual(got, []string{"COD-1", "COD-2"}) {
		t.Fatalf("imported order = %v, want [COD-1 COD-2]", got)
	}
	if items[0].Kind != queue.KindEpic || len(items[0].SubIssues) != 1 || items[0].SubIssues[0].ID != "COD-9" {
		t.Fatalf("epic sub-issues not imported: %+v", items[0])
	}
	if !items[0].QueuedAt.Equal(queuedAt) {
		t.Fatalf("QueuedAt = %v, want %v preserved", items[0].QueuedAt, queuedAt)
	}
	if items[1].Status != queue.StatusPaused || items[1].Reason != "was faulted" {
		t.Fatalf("paused item not imported: %+v", items[1])
	}
	if items[1].Provider != "codex" {
		t.Fatalf("provider = %q, want codex imported from queue.json", items[1].Provider)
	}
	if _, present := LegacyQueueFile(root); present {
		t.Error("legacy queue.json still present after a committed import")
	}
}

func TestImportLegacyQueueIsLazyOnFirstTouch(t *testing.T) {
	root := t.TempDir()
	writeLegacyQueue(t, root, legacyQueue{Items: []queue.Item{{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusPending}}})

	q := NewQueue(testDB(t), root)
	items, _, err := q.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(items) != 1 || items[0].ID != "COD-1" {
		t.Fatalf("first-touch snapshot = %v, want the imported COD-1", ids(items))
	}
	if _, present := LegacyQueueFile(root); present {
		t.Error("legacy queue.json still present after first-touch import")
	}
}

func TestImportLegacyQueueAbortsOnMalformedJSON(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".trau"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := legacyQueuePath(root)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write malformed: %v", err)
	}

	q := NewQueue(testDB(t), root)
	err := q.ImportLegacy()
	if err == nil {
		t.Fatal("ImportLegacy = nil, want an error on malformed file")
	}
	if _, present := LegacyQueueFile(root); !present {
		t.Error("malformed queue.json was removed despite a failed import")
	}
}

func TestImportLegacyQueueFreshInstallDoesNothing(t *testing.T) {
	root := t.TempDir()
	q := NewQueue(testDB(t), root)
	if err := q.ImportLegacy(); err != nil {
		t.Fatalf("ImportLegacy on fresh install: %v", err)
	}
	if _, present := LegacyQueueFile(root); present {
		t.Error("fresh install created a legacy queue.json")
	}
	if items, _ := q.Load(); len(items) != 0 {
		t.Fatalf("fresh install has items: %v", ids(items))
	}
}

func TestLegacyQueueFileReportsPresence(t *testing.T) {
	root := t.TempDir()
	if _, present := LegacyQueueFile(root); present {
		t.Fatal("fresh root reports a legacy queue file")
	}
	writeLegacyQueue(t, root, legacyQueue{Items: []queue.Item{}})
	if path, present := LegacyQueueFile(root); !present || filepath.Base(path) != legacyQueueFilename {
		t.Fatalf("LegacyQueueFile = (%q, %v), want the queue.json present", path, present)
	}
}

func writeLegacyQueue(t *testing.T, root string, f legacyQueue) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".trau"), 0o755); err != nil {
		t.Fatalf("mkdir .trau: %v", err)
	}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal legacy queue: %v", err)
	}
	if err := os.WriteFile(legacyQueuePath(root), data, 0o644); err != nil {
		t.Fatalf("write legacy queue: %v", err)
	}
}
