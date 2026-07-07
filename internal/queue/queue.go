// Package queue is the per-Repo execution queue: an ordered list of tickets and
// epics deliberately registered from the web for the hub to drain one run at a
// time. It persists to <root>/.trau/queue.json — filesystem, no SQLite (ADR
// 0003) — so a repo's queue survives serve restarts and is the same whether the
// hub is running or not.
package queue

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Kind distinguishes a single run-once ticket from an epic carrying its
// sub-issues.
type Kind string

const (
	KindTicket Kind = "ticket"
	KindEpic   Kind = "epic"
)

// StatusPending marks a queued item the hub has not started draining. It is the
// only status registration produces; draining lands the terminal states.
const StatusPending = "pending"

// SubIssue is one child an epic item carries, captured when the epic is queued
// so the queue records what an epic run will cover.
type SubIssue struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

// Item is one queued unit of work — a run-once ticket or an epic. Its position
// is implicit in the queue's order.
type Item struct {
	Kind      Kind       `json:"kind"`
	ID        string     `json:"id"`
	Title     string     `json:"title,omitempty"`
	Status    string     `json:"status"`
	SubIssues []SubIssue `json:"sub_issues,omitempty"`
	QueuedAt  time.Time  `json:"queued_at"`
}

var (
	// ErrAlreadyQueued is returned when the same ticket or epic is registered
	// twice, so work is never queued more than once.
	ErrAlreadyQueued = errors.New("already in the queue")
	// ErrNotQueued is returned when removing an item the queue does not hold.
	ErrNotQueued = errors.New("not in the queue")
)

var mu sync.Mutex

// Store reads and writes one Repo's queue at <root>/.trau/queue.json.
type Store struct {
	path string
}

// NewStore builds a Store for the queue file under a Repo root.
func NewStore(root string) *Store {
	return &Store{path: filepath.Join(root, ".trau", "queue.json")}
}

type file struct {
	Items []Item `json:"items"`
}

// Load returns the queue in registration order, empty when nothing has been
// queued yet.
func (s *Store) Load() ([]Item, error) {
	mu.Lock()
	defer mu.Unlock()
	return s.load()
}

// Add appends item to the end of the queue, stamping it pending and recording
// when it was queued, and returns the resulting queue. It refuses a ticket or
// epic already present with ErrAlreadyQueued.
func (s *Store) Add(item Item) ([]Item, error) {
	mu.Lock()
	defer mu.Unlock()
	items, err := s.load()
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.ID == item.ID {
			return nil, ErrAlreadyQueued
		}
	}
	item.Status = StatusPending
	item.QueuedAt = time.Now().UTC()
	items = append(items, item)
	if err := s.save(items); err != nil {
		return nil, err
	}
	return items, nil
}

// Remove drops the queued item with id, keeping the order of the rest, and
// returns the resulting queue. It reports ErrNotQueued when nothing matches.
func (s *Store) Remove(id string) ([]Item, error) {
	mu.Lock()
	defer mu.Unlock()
	items, err := s.load()
	if err != nil {
		return nil, err
	}
	kept := make([]Item, 0, len(items))
	found := false
	for _, it := range items {
		if it.ID == id {
			found = true
			continue
		}
		kept = append(kept, it)
	}
	if !found {
		return nil, ErrNotQueued
	}
	if err := s.save(kept); err != nil {
		return nil, err
	}
	return kept, nil
}

func (s *Store) load() ([]Item, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Item{}, nil
		}
		return nil, err
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if f.Items == nil {
		return []Item{}, nil
	}
	return f.Items, nil
}

func (s *Store) save(items []Item) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(file{Items: items}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".queue-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.path)
}
