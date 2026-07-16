package hubstore

import (
	"database/sql"
	"encoding/json"
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
	if err := first.SetOptions(true, queue.OnFaultSkip); err != nil {
		t.Fatalf("SetOptions: %v", err)
	}
	if err := first.SetDraining(true); err != nil {
		t.Fatalf("SetDraining: %v", err)
	}

	second := NewQueue(db, "/repo/acme")
	items, draining, err := second.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !draining || len(items) != 1 || items[0].ID != "COD-1" {
		t.Fatalf("reopened snapshot = %v draining=%v, want the persisted COD-1 armed", ids(items), draining)
	}
	meta, err := second.Meta()
	if err != nil {
		t.Fatalf("Meta: %v", err)
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
	items, draining, err := q.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if draining {
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
	if _, draining, _ := q.Snapshot(); draining {
		t.Error("queue still reads draining after FinishDraining")
	}
}

func TestRestartResetsNonRunning(t *testing.T) {
	q := testQueue(t)
	mustAdd(t, q, "COD-1")
	mustAdd(t, q, "COD-2")
	if err := q.Finish("COD-1", queue.StatusFailed, "boom"); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := q.MarkRunning("COD-2", 7); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := q.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	items, _ := q.Load()
	if items[0].Status != queue.StatusPending || items[0].Reason != "" {
		t.Fatalf("COD-1 = %+v, want reset to pending", items[0])
	}
	if items[1].Status != queue.StatusRunning {
		t.Fatalf("COD-2 = %q, want the running item left alone", items[1].Status)
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

	items, draining, err := q.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !draining {
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
