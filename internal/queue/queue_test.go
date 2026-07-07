package queue

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

func mustAdd(t *testing.T, s *Store, item Item) {
	t.Helper()
	if _, err := s.Add(item); err != nil {
		t.Fatalf("Add(%s): %v", item.ID, err)
	}
}

func ids(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func equalIDs(a, b []string) bool {
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

// TestRoundTrip queues a ticket and an epic, then reloads through a fresh Store
// on the same root — the "survives a serve restart" case — and asserts every
// field, including the epic's carried sub-issues, comes back intact.
func TestRoundTrip(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	mustAdd(t, s, Item{Kind: KindTicket, ID: "COD-11", Title: "A ticket"})
	mustAdd(t, s, Item{
		Kind:  KindEpic,
		ID:    "COD-10",
		Title: "An epic",
		SubIssues: []SubIssue{
			{ID: "COD-12", Title: "First child", State: "todo"},
			{ID: "COD-13", Title: "Second child", State: "done"},
		},
	})

	if _, err := os.Stat(filepath.Join(root, ".trau", "queue.json")); err != nil {
		t.Fatalf("queue.json not written: %v", err)
	}

	items, err := NewStore(root).Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}

	ticket := items[0]
	if ticket.Kind != KindTicket || ticket.ID != "COD-11" || ticket.Title != "A ticket" {
		t.Errorf("ticket = %+v, want the COD-11 run-once", ticket)
	}
	if ticket.Status != StatusPending {
		t.Errorf("ticket status = %q, want %q", ticket.Status, StatusPending)
	}
	if ticket.QueuedAt.IsZero() {
		t.Error("ticket queued_at not stamped")
	}
	if len(ticket.SubIssues) != 0 {
		t.Errorf("ticket sub_issues = %v, want none", ticket.SubIssues)
	}

	epic := items[1]
	if epic.Kind != KindEpic || epic.ID != "COD-10" {
		t.Errorf("epic = %+v, want the COD-10 epic", epic)
	}
	if len(epic.SubIssues) != 2 {
		t.Fatalf("epic sub_issues = %d, want 2", len(epic.SubIssues))
	}
	if epic.SubIssues[0].ID != "COD-12" || epic.SubIssues[0].State != "todo" {
		t.Errorf("sub_issues[0] = %+v, want COD-12/todo", epic.SubIssues[0])
	}
	if epic.SubIssues[1].ID != "COD-13" || epic.SubIssues[1].State != "done" {
		t.Errorf("sub_issues[1] = %+v, want COD-13/done", epic.SubIssues[1])
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	items, err := newStore(t).Load()
	if err != nil {
		t.Fatalf("Load on a fresh repo: %v", err)
	}
	if items == nil {
		t.Fatal("Load returned nil, want an empty slice")
	}
	if len(items) != 0 {
		t.Errorf("items = %d, want 0", len(items))
	}
}

// TestOrdering asserts the queue preserves registration order across reloads.
func TestOrdering(t *testing.T) {
	tests := []struct {
		name string
		add  []string
		want []string
	}{
		{name: "single", add: []string{"COD-1"}, want: []string{"COD-1"}},
		{name: "registration order kept", add: []string{"COD-3", "COD-1", "COD-2"}, want: []string{"COD-3", "COD-1", "COD-2"}},
		{name: "not sorted", add: []string{"COD-30", "COD-4", "COD-100"}, want: []string{"COD-30", "COD-4", "COD-100"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			s := NewStore(root)
			for _, id := range tc.add {
				mustAdd(t, s, Item{Kind: KindTicket, ID: id})
			}
			items, err := NewStore(root).Load()
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			if got := ids(items); !equalIDs(got, tc.want) {
				t.Errorf("order = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDedupe asserts re-queuing an item already present is refused regardless of
// kind, and that the refused Add never grows the queue.
func TestDedupe(t *testing.T) {
	tests := []struct {
		name  string
		first Item
		again Item
	}{
		{
			name:  "same ticket twice",
			first: Item{Kind: KindTicket, ID: "COD-1"},
			again: Item{Kind: KindTicket, ID: "COD-1"},
		},
		{
			name:  "same epic twice",
			first: Item{Kind: KindEpic, ID: "COD-2"},
			again: Item{Kind: KindEpic, ID: "COD-2"},
		},
		{
			name:  "queued as epic, again as ticket",
			first: Item{Kind: KindEpic, ID: "COD-3"},
			again: Item{Kind: KindTicket, ID: "COD-3"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			mustAdd(t, s, tc.first)
			if _, err := s.Add(tc.again); !errors.Is(err, ErrAlreadyQueued) {
				t.Fatalf("re-Add err = %v, want ErrAlreadyQueued", err)
			}
			items, err := s.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(items) != 1 {
				t.Errorf("items = %d, want 1 (dupe never appended)", len(items))
			}
		})
	}
}

// TestRemove asserts removal drops the named item, preserves the order of the
// rest, persists, and reports ErrNotQueued for an item the queue does not hold.
func TestRemove(t *testing.T) {
	tests := []struct {
		name    string
		seed    []string
		remove  string
		wantErr error
		want    []string
	}{
		{name: "middle", seed: []string{"COD-1", "COD-2", "COD-3"}, remove: "COD-2", want: []string{"COD-1", "COD-3"}},
		{name: "first", seed: []string{"COD-1", "COD-2"}, remove: "COD-1", want: []string{"COD-2"}},
		{name: "last", seed: []string{"COD-1", "COD-2"}, remove: "COD-2", want: []string{"COD-1"}},
		{name: "only", seed: []string{"COD-1"}, remove: "COD-1", want: []string{}},
		{name: "absent", seed: []string{"COD-1"}, remove: "COD-9", wantErr: ErrNotQueued, want: []string{"COD-1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			s := NewStore(root)
			for _, id := range tc.seed {
				mustAdd(t, s, Item{Kind: KindTicket, ID: id})
			}
			_, err := s.Remove(tc.remove)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Remove err = %v, want %v", err, tc.wantErr)
			}
			items, err := NewStore(root).Load()
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			if got := ids(items); !equalIDs(got, tc.want) {
				t.Errorf("remaining = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRemoveRunningRefused asserts a running item cannot be dequeued — the guard
// that keeps a Remove race from orphaning a just-spawned child — while pending
// and terminal items around it stay removable.
func TestRemoveRunningRefused(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	mustAdd(t, s, Item{Kind: KindTicket, ID: "COD-1"})
	mustAdd(t, s, Item{Kind: KindTicket, ID: "COD-2"})
	if err := s.MarkRunning("COD-2", 4242); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	if _, err := s.Remove("COD-2"); !errors.Is(err, ErrRunning) {
		t.Fatalf("Remove running = %v, want ErrRunning", err)
	}
	if got := ids(mustLoad(t, root)); !equalIDs(got, []string{"COD-1", "COD-2"}) {
		t.Errorf("queue = %v, want the running item kept", got)
	}

	if _, err := s.Remove("COD-1"); err != nil {
		t.Fatalf("Remove pending alongside a running item: %v", err)
	}
	if err := s.Finish("COD-2", StatusDone, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if _, err := s.Remove("COD-2"); err != nil {
		t.Fatalf("Remove after finish = %v, want a settled item removable", err)
	}
}

func mustLoad(t *testing.T, root string) []Item {
	t.Helper()
	items, err := NewStore(root).Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	return items
}

func status(t *testing.T, s *Store, id string) string {
	t.Helper()
	items, _, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, it := range items {
		if it.ID == id {
			return it.Status
		}
	}
	t.Fatalf("item %s not in queue", id)
	return ""
}

// TestDrainingSurvivesReloadAndMutation proves the draining flag persists like
// the items do — across a fresh Store on the same root (a serve restart) — and
// is not clobbered when the queue is otherwise mutated.
func TestDrainingSurvivesReloadAndMutation(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	mustAdd(t, s, Item{Kind: KindTicket, ID: "COD-1"})

	if _, draining, err := s.Snapshot(); err != nil || draining {
		t.Fatalf("fresh queue draining = %v (err %v), want false", draining, err)
	}
	if err := s.SetDraining(true); err != nil {
		t.Fatalf("SetDraining: %v", err)
	}

	if _, draining, err := NewStore(root).Snapshot(); err != nil || !draining {
		t.Fatalf("reloaded draining = %v (err %v), want true after a restart", draining, err)
	}

	mustAdd(t, s, Item{Kind: KindTicket, ID: "COD-2"})
	if _, err := s.Remove("COD-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, draining, err := s.Snapshot(); err != nil || !draining {
		t.Fatalf("draining = %v after add/remove, want it preserved", draining)
	}
}

// TestStatusTransitions walks an item pending → running → terminal, asserting
// the running child's pid is recorded then cleared, and that the transition
// persists across a reload.
func TestStatusTransitions(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	mustAdd(t, s, Item{Kind: KindTicket, ID: "COD-1"})

	if got := status(t, s, "COD-1"); got != StatusPending {
		t.Fatalf("new item status = %q, want pending", got)
	}

	if err := s.MarkRunning("COD-1", 4242); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	items, _, _ := NewStore(root).Snapshot()
	if items[0].Status != StatusRunning || items[0].PID != 4242 {
		t.Fatalf("running item = %+v, want running with pid 4242", items[0])
	}

	if err := s.Finish("COD-1", StatusDone, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	items, _, _ = NewStore(root).Snapshot()
	if items[0].Status != StatusDone || items[0].PID != 0 {
		t.Fatalf("finished item = %+v, want done with the pid cleared", items[0])
	}
}

// TestPauseParksItemAndStopsDraining proves a paused item survives a reload
// carrying its reason, that pausing clears the draining flag in the same write,
// and that a re-attempt via MarkRunning drops the stale reason.
func TestPauseParksItemAndStopsDraining(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	mustAdd(t, s, Item{Kind: KindTicket, ID: "COD-1"})
	mustAdd(t, s, Item{Kind: KindTicket, ID: "COD-2"})
	if err := s.SetDraining(true); err != nil {
		t.Fatalf("SetDraining: %v", err)
	}
	if err := s.MarkRunning("COD-1", 4242); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	if err := s.Pause("COD-1", "claude needs re-authentication"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	items, draining, err := NewStore(root).Snapshot()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if draining {
		t.Error("draining still set after a pause, want it cleared in the same write")
	}
	if items[0].Status != StatusPaused || items[0].Reason != "claude needs re-authentication" || items[0].PID != 0 {
		t.Fatalf("paused item = %+v, want paused with reason and no pid", items[0])
	}
	if items[1].Status != StatusPending {
		t.Errorf("COD-2 status = %q, want it left pending behind the paused item", items[1].Status)
	}

	if err := s.MarkRunning("COD-1", 99); err != nil {
		t.Fatalf("re-attempt MarkRunning: %v", err)
	}
	items, _, _ = NewStore(root).Snapshot()
	if items[0].Status != StatusRunning || items[0].Reason != "" {
		t.Errorf("re-attempted item = %+v, want running with the stale reason cleared", items[0])
	}
}

// TestFinishRecordsReason proves a give-up outcome settles an item failed while
// carrying the reason the Queue view surfaces.
func TestFinishRecordsReason(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	mustAdd(t, s, Item{Kind: KindTicket, ID: "COD-1"})
	if err := s.MarkRunning("COD-1", 7); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := s.Finish("COD-1", StatusFailed, "verify never went green"); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	items, _, _ := NewStore(root).Snapshot()
	if items[0].Status != StatusFailed || items[0].Reason != "verify never went green" {
		t.Fatalf("failed item = %+v, want failed carrying its reason", items[0])
	}
}

func TestSetStatusUnknownItem(t *testing.T) {
	s := newStore(t)
	if err := s.MarkRunning("COD-404", 1); !errors.Is(err, ErrNotQueued) {
		t.Fatalf("MarkRunning unknown = %v, want ErrNotQueued", err)
	}
	if err := s.Finish("COD-404", StatusDone, ""); !errors.Is(err, ErrNotQueued) {
		t.Fatalf("Finish unknown = %v, want ErrNotQueued", err)
	}
	if err := s.Pause("COD-404", "x"); !errors.Is(err, ErrNotQueued) {
		t.Fatalf("Pause unknown = %v, want ErrNotQueued", err)
	}
}
