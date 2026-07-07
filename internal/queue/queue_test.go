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
