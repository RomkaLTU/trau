package hubstore

import (
	"errors"
	"testing"

	"github.com/RomkaLTU/trau/internal/queue"
)

// mustBatch files ids under a batch and returns its identifier.
func mustBatch(t *testing.T, q *Queue, name string, ids ...string) string {
	t.Helper()
	id, err := q.CreateBatch(name, ids)
	if err != nil {
		t.Fatalf("CreateBatch(%q): %v", name, err)
	}
	return id
}

func batchOf(t *testing.T, q *Queue, id string) string {
	t.Helper()
	items, err := q.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, it := range items {
		if it.ID == id {
			return it.Batch
		}
	}
	t.Fatalf("item %s missing from queue", id)
	return ""
}

func TestCreateBatchSlugsNamesAndFilesMembers(t *testing.T) {
	q := testQueue(t)
	for _, id := range []string{"COD-1", "COD-2", "COD-3"} {
		mustAdd(t, q, id)
	}

	first := mustBatch(t, q, "Ship it", "COD-1", "COD-3")
	if first != "ship-it" {
		t.Errorf("id = %q, want the slugified name", first)
	}
	second := mustBatch(t, q, "Ship it")
	if second != "ship-it-2" {
		t.Errorf("id = %q, want the collision disambiguated", second)
	}
	unnamed := mustBatch(t, q, "")
	if unnamed != "batch" {
		t.Errorf("id = %q, want %q for a batch filed without a name", unnamed, "batch")
	}

	if got := batchOf(t, q, "COD-1"); got != first {
		t.Errorf("COD-1 batch = %q, want %q", got, first)
	}
	if got := batchOf(t, q, "COD-2"); got != "" {
		t.Errorf("COD-2 batch = %q, want none — it was never filed", got)
	}

	batches, err := q.Batches()
	if err != nil {
		t.Fatalf("Batches: %v", err)
	}
	if len(batches) != 3 {
		t.Fatalf("batches = %d, want 3", len(batches))
	}
	if batches[0].Name != "Ship it" || batches[0].CreatedAt.IsZero() {
		t.Errorf("batch = %+v, want the name and a stamp", batches[0])
	}
}

func TestCreateBatchRefusals(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
		want error
	}{
		{name: "not queued", ids: []string{"COD-9"}, want: queue.ErrNotQueued},
		{name: "already settled", ids: []string{"COD-3"}, want: queue.ErrNotBatchable},
		{name: "running", ids: []string{"COD-4"}, want: queue.ErrNotBatchable},
		{name: "already batched", ids: []string{"COD-2"}, want: queue.ErrAlreadyBatched},
		{name: "one bad id refuses the rest", ids: []string{"COD-1", "COD-9"}, want: queue.ErrNotQueued},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := testQueue(t)
			for _, id := range []string{"COD-1", "COD-2", "COD-3", "COD-4"} {
				mustAdd(t, q, id)
			}
			if err := q.Finish("COD-3", queue.StatusDone, ""); err != nil {
				t.Fatalf("Finish: %v", err)
			}
			if err := q.MarkRunning("COD-4", 42); err != nil {
				t.Fatalf("MarkRunning: %v", err)
			}
			held := mustBatch(t, q, "held", "COD-2")

			_, err := q.CreateBatch("wanted", tc.ids)
			if !errors.Is(err, tc.want) {
				t.Fatalf("CreateBatch = %v, want %v", err, tc.want)
			}
			if got := batchOf(t, q, "COD-1"); got != "" {
				t.Errorf("COD-1 batch = %q, want none — a refused create files nobody", got)
			}
			if got := batchOf(t, q, "COD-2"); got != held {
				t.Errorf("COD-2 batch = %q, want %q left alone", got, held)
			}
		})
	}
}

func TestBatchMembershipEdits(t *testing.T) {
	q := testQueue(t)
	for _, id := range []string{"COD-1", "COD-2", "COD-3"} {
		mustAdd(t, q, id)
	}
	bid := mustBatch(t, q, "wave one", "COD-1")

	if err := q.AddToBatch(bid, []string{"COD-2"}); err != nil {
		t.Fatalf("AddToBatch: %v", err)
	}
	if got := batchOf(t, q, "COD-2"); got != bid {
		t.Errorf("COD-2 batch = %q, want %q", got, bid)
	}
	if err := q.Finish("COD-3", queue.StatusDone, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := q.AddToBatch(bid, []string{"COD-3"}); !errors.Is(err, queue.ErrNotBatchable) {
		t.Fatalf("AddToBatch settled = %v, want ErrNotBatchable", err)
	}
	if err := q.AddToBatch("nope", []string{"COD-2"}); !errors.Is(err, queue.ErrBatchNotFound) {
		t.Fatalf("AddToBatch unknown = %v, want ErrBatchNotFound", err)
	}

	if err := q.MarkRunning("COD-1", 7); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := q.RemoveFromBatch(bid, []string{"COD-1", "COD-9"}); err != nil {
		t.Fatalf("RemoveFromBatch: %v", err)
	}
	if got := batchOf(t, q, "COD-1"); got != "" {
		t.Errorf("COD-1 batch = %q, want it dropped — a member always leaves", got)
	}
	items, _ := q.Load()
	if items[0].Status != queue.StatusRunning || items[0].PID != 7 {
		t.Errorf("COD-1 = %+v, want its run untouched by leaving the batch", items[0])
	}

	if err := q.RenameBatch(bid, "  wave two  "); err != nil {
		t.Fatalf("RenameBatch: %v", err)
	}
	batches, _ := q.Batches()
	if batches[0].Name != "wave two" {
		t.Errorf("name = %q, want the trimmed rename", batches[0].Name)
	}
	if err := q.RenameBatch("nope", "x"); !errors.Is(err, queue.ErrBatchNotFound) {
		t.Fatalf("RenameBatch unknown = %v, want ErrBatchNotFound", err)
	}
}

func TestDismissBatchDissolvesTheGroupingOnly(t *testing.T) {
	q := testQueue(t)
	for _, id := range []string{"COD-1", "COD-2"} {
		mustAdd(t, q, id)
	}
	bid := mustBatch(t, q, "wave", "COD-1", "COD-2")
	if err := q.MarkRunning("COD-1", 7); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	if err := q.DismissBatch(bid); err != nil {
		t.Fatalf("DismissBatch: %v", err)
	}
	items, err := q.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Status != queue.StatusRunning || items[0].PID != 7 {
		t.Errorf("COD-1 = %+v, want its running child untouched", items[0])
	}
	if items[1].Status != queue.StatusPending {
		t.Errorf("COD-2 = %q, want it still pending", items[1].Status)
	}
	for _, it := range items {
		if it.Batch != "" {
			t.Errorf("%s batch = %q, want it cleared", it.ID, it.Batch)
		}
	}
	if batches, _ := q.Batches(); len(batches) != 0 {
		t.Errorf("batches = %d, want the row gone", len(batches))
	}
	if err := q.DismissBatch(bid); !errors.Is(err, queue.ErrBatchNotFound) {
		t.Fatalf("DismissBatch twice = %v, want ErrBatchNotFound", err)
	}
}

func TestEmptyBatchStillListsUntilDismissed(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	bid := mustBatch(t, q, "wave", "COD-1")
	if _, err := q.Remove("COD-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	batches, err := q.Batches()
	if err != nil {
		t.Fatalf("Batches: %v", err)
	}
	if len(batches) != 1 || batches[0].ID != bid {
		t.Fatalf("batches = %+v, want the emptied batch still listed", batches)
	}
}

func TestArmBatchScopesTheDrain(t *testing.T) {
	q := testQueue(t)
	for _, id := range []string{"COD-1", "COD-2", "COD-3"} {
		mustAdd(t, q, id)
	}
	bid := mustBatch(t, q, "wave", "COD-2")

	if err := q.ArmBatch("nope", false, queue.OnFaultHalt); !errors.Is(err, queue.ErrBatchNotFound) {
		t.Fatalf("ArmBatch unknown = %v, want ErrBatchNotFound", err)
	}
	if err := q.ArmBatch(bid, false, queue.OnFaultSkip); err != nil {
		t.Fatalf("ArmBatch: %v", err)
	}
	_, meta, err := q.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !meta.Draining || meta.Batch != bid || meta.OnFault != queue.OnFaultSkip {
		t.Fatalf("meta = %+v, want armed on the batch scope", meta)
	}
	if meta.DrainingSince.IsZero() {
		t.Error("DrainingSince is zero, want the run stamped")
	}
}

func TestArmBatchRefusesABatchWithNothingRunnable(t *testing.T) {
	q := testQueue(t)
	for _, id := range []string{"COD-1", "COD-2"} {
		mustAdd(t, q, id)
	}
	bid := mustBatch(t, q, "wave", "COD-1")
	if err := q.Finish("COD-1", queue.StatusDone, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	if err := q.ArmBatch(bid, false, queue.OnFaultHalt); !errors.Is(err, queue.ErrNoRunnableItems) {
		t.Fatalf("ArmBatch = %v, want ErrNoRunnableItems even though COD-2 is pending", err)
	}
	if _, meta, _ := q.Snapshot(); meta.Draining || meta.Batch != "" {
		t.Errorf("meta = %+v, want untouched by the refused start", meta)
	}
}

func TestArmBatchNoResumeRestartsMembersOnly(t *testing.T) {
	q := testQueue(t)
	for _, id := range []string{"COD-1", "COD-2", "COD-3"} {
		mustAdd(t, q, id)
	}
	bid := mustBatch(t, q, "wave", "COD-1", "COD-2")
	if err := q.Finish("COD-1", queue.StatusFailed, "boom"); err != nil {
		t.Fatalf("Finish COD-1: %v", err)
	}
	if err := q.Finish("COD-3", queue.StatusDone, "shipped"); err != nil {
		t.Fatalf("Finish COD-3: %v", err)
	}

	if err := q.ArmBatch(bid, true, queue.OnFaultHalt); err != nil {
		t.Fatalf("ArmBatch: %v", err)
	}
	items, _ := q.Load()
	if items[0].Status != queue.StatusPending || items[0].Reason != "" {
		t.Errorf("COD-1 = %+v, want the member restarted", items[0])
	}
	if items[2].Status != queue.StatusDone {
		t.Errorf("COD-3 = %q, want a non-member left settled", items[2].Status)
	}
}

func TestScopeAwareFinishDraining(t *testing.T) {
	tests := []struct {
		name     string
		settle   map[string]string
		pause    string
		running  string
		wantDone bool
	}{
		{
			name:     "disarms with non-members still pending",
			settle:   map[string]string{"COD-1": queue.StatusDone},
			wantDone: true,
		},
		{
			name:   "refuses while a member is paused",
			pause:  "COD-1",
			settle: map[string]string{},
		},
		{
			name:    "refuses while a member is running",
			running: "COD-1",
			settle:  map[string]string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := testQueue(t)
			for _, id := range []string{"COD-1", "COD-2"} {
				mustAdd(t, q, id)
			}
			bid := mustBatch(t, q, "wave", "COD-1")
			if err := q.ArmBatch(bid, false, queue.OnFaultHalt); err != nil {
				t.Fatalf("ArmBatch: %v", err)
			}
			for id, status := range tc.settle {
				if err := q.Finish(id, status, ""); err != nil {
					t.Fatalf("Finish %s: %v", id, err)
				}
			}
			if tc.running != "" {
				if err := q.MarkRunning(tc.running, 7); err != nil {
					t.Fatalf("MarkRunning: %v", err)
				}
			}
			if tc.pause != "" {
				if err := q.MarkRunning(tc.pause, 7); err != nil {
					t.Fatalf("MarkRunning: %v", err)
				}
				if err := q.Pause(tc.pause, "walled"); err != nil {
					t.Fatalf("Pause: %v", err)
				}
			}

			done, err := q.FinishDraining()
			if err != nil {
				t.Fatalf("FinishDraining: %v", err)
			}
			if done != tc.wantDone {
				t.Fatalf("FinishDraining = %v, want %v", done, tc.wantDone)
			}
			_, meta, _ := q.Snapshot()
			if tc.wantDone && meta.Batch != "" {
				t.Errorf("scope = %q, want it cleared with the flag", meta.Batch)
			}
			if !tc.wantDone && meta.Batch != bid {
				t.Errorf("scope = %q, want %q kept", meta.Batch, bid)
			}
			if got := statusOfItem(t, q, "COD-2"); got != queue.StatusPending {
				t.Errorf("COD-2 = %q, want the non-member left pending", got)
			}
		})
	}
}

func TestScopeSurvivesPauseAndRearmAndClearsOnWholeQueueArm(t *testing.T) {
	q := testQueue(t)
	for _, id := range []string{"COD-1", "COD-2"} {
		mustAdd(t, q, id)
	}
	bid := mustBatch(t, q, "wave", "COD-1")
	if err := q.ArmBatch(bid, false, queue.OnFaultHalt); err != nil {
		t.Fatalf("ArmBatch: %v", err)
	}
	if err := q.MarkRunning("COD-1", 7); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	if err := q.Pause("COD-1", "provider walled"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	_, meta, _ := q.Snapshot()
	if meta.Draining {
		t.Error("Pause left the queue armed")
	}
	if meta.Batch != bid {
		t.Fatalf("scope = %q, want %q preserved across the park", meta.Batch, bid)
	}

	if err := q.Rearm(); err != nil {
		t.Fatalf("Rearm: %v", err)
	}
	if _, meta, _ = q.Snapshot(); !meta.Draining || meta.Batch != bid {
		t.Fatalf("meta = %+v, want re-armed on the same scope", meta)
	}

	if err := q.Arm(false, queue.OnFaultHalt); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if _, meta, _ = q.Snapshot(); !meta.Draining || meta.Batch != "" {
		t.Fatalf("meta = %+v, want a whole-queue start to clear the scope", meta)
	}
}

func TestRearmRefusesWhenTheBatchRanDry(t *testing.T) {
	q := testQueue(t)
	for _, id := range []string{"COD-1", "COD-2"} {
		mustAdd(t, q, id)
	}
	bid := mustBatch(t, q, "wave", "COD-1")
	if err := q.ArmBatch(bid, false, queue.OnFaultHalt); err != nil {
		t.Fatalf("ArmBatch: %v", err)
	}
	if err := q.Finish("COD-1", queue.StatusDone, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := q.SetDraining(false); err != nil {
		t.Fatalf("SetDraining: %v", err)
	}

	if err := q.Rearm(); !errors.Is(err, queue.ErrNoRunnableItems) {
		t.Fatalf("Rearm = %v, want ErrNoRunnableItems with only a non-member left", err)
	}
}

func TestItemBatchRoundTripsThroughLoadAndPersist(t *testing.T) {
	db := testDB(t)
	q := NewQueue(db, "/repo/acme")
	mustAdd(t, q, "COD-1")
	bid := mustBatch(t, q, "wave", "COD-1")
	if _, err := q.Move("COD-1", 1); err != nil {
		t.Fatalf("Move: %v", err)
	}

	if got := batchOf(t, NewQueue(db, "/repo/acme"), "COD-1"); got != bid {
		t.Errorf("batch = %q, want %q to survive a rewrite of the queue", got, bid)
	}
}

func statusOfItem(t *testing.T, q *Queue, id string) string {
	t.Helper()
	items, err := q.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, it := range items {
		if it.ID == id {
			return it.Status
		}
	}
	t.Fatalf("item %s missing from queue", id)
	return ""
}
