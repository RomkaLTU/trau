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

// The statuses an item moves through as the hub drains the queue: registration
// lands it Pending, draining marks it Running, and the child's outcome settles
// it Done, Failed, or — when the run faults or a provider pauses — Paused, parked
// at the front for a resume to re-attempt.
const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusPaused  = "paused"
	StatusDone    = "done"
	StatusFailed  = "failed"
	// StatusSkipped marks an item the drain passed over without running: a
	// duplicate of work already claimed elsewhere in the same queue.
	StatusSkipped = "skipped"
)

// OnFault selects what a fault does to the rest of the queue: halt parks the
// item and stops the drain for a human, skip settles it failed and continues.
const (
	OnFaultHalt = "halt"
	OnFaultSkip = "skip"
)

// SubIssue is one child an epic item carries, captured when the epic is queued
// so the queue records what an epic run will cover.
type SubIssue struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

// Item is one queued unit of work — a run-once ticket or an epic. Its position
// is implicit in the queue's order. PID is the child the hub spawned to run it,
// set while Running so a resumed hub can tell whether that child is still alive.
type Item struct {
	Kind      Kind       `json:"kind"`
	ID        string     `json:"id"`
	Title     string     `json:"title,omitempty"`
	Status    string     `json:"status"`
	Reason    string     `json:"reason,omitempty"`
	PID       int        `json:"pid,omitempty"`
	SubIssues []SubIssue `json:"sub_issues,omitempty"`
	QueuedAt  time.Time  `json:"queued_at"`
}

var (
	// ErrAlreadyQueued is returned when the same ticket or epic is registered
	// twice, so work is never queued more than once.
	ErrAlreadyQueued = errors.New("already in the queue")
	// ErrNotQueued is returned when removing an item the queue does not hold.
	ErrNotQueued = errors.New("not in the queue")
	// ErrRunning is returned when removing an item the hub is currently
	// draining, so a running child is never orphaned by a dequeue.
	ErrRunning = errors.New("cannot remove a running item")
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
	Draining bool   `json:"draining,omitempty"`
	NoResume bool   `json:"no_resume,omitempty"`
	OnFault  string `json:"on_fault,omitempty"`
	Items    []Item `json:"items"`
}

// Meta is the queue's run-level configuration, read alongside its items to drive
// the drain: whether to ignore stored checkpoints and what a fault does.
type Meta struct {
	Draining bool
	NoResume bool
	OnFault  string
}

// Load returns the queue in registration order, empty when nothing has been
// queued yet.
func (s *Store) Load() ([]Item, error) {
	mu.Lock()
	defer mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	return f.Items, nil
}

// Snapshot returns the queue in registration order along with whether the hub is
// draining it, the two facts the Queue view and the drainer both read.
func (s *Store) Snapshot() ([]Item, bool, error) {
	mu.Lock()
	defer mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, false, err
	}
	return f.Items, f.Draining, nil
}

// Add appends item to the end of the queue, stamping it pending and recording
// when it was queued, and returns the resulting queue. It refuses a ticket or
// epic already present with ErrAlreadyQueued.
func (s *Store) Add(item Item) ([]Item, error) {
	mu.Lock()
	defer mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	for _, it := range f.Items {
		if it.ID == item.ID {
			return nil, ErrAlreadyQueued
		}
	}
	item.Status = StatusPending
	item.QueuedAt = time.Now().UTC()
	f.Items = append(f.Items, item)
	if err := s.save(f); err != nil {
		return nil, err
	}
	return f.Items, nil
}

// Remove drops the queued item with id, keeping the order of the rest, and
// returns the resulting queue. It reports ErrNotQueued when nothing matches and
// ErrRunning when the item is currently being drained.
func (s *Store) Remove(id string) ([]Item, error) {
	mu.Lock()
	defer mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	kept := make([]Item, 0, len(f.Items))
	found := false
	for _, it := range f.Items {
		if it.ID == id {
			if it.Status == StatusRunning {
				return nil, ErrRunning
			}
			found = true
			continue
		}
		kept = append(kept, it)
	}
	if !found {
		return nil, ErrNotQueued
	}
	f.Items = kept
	if err := s.save(f); err != nil {
		return nil, err
	}
	return f.Items, nil
}

// SetDraining records whether the hub is draining this queue. It survives a
// serve restart with the rest of the file, so a resumed hub picks draining back
// up where it left off.
func (s *Store) SetDraining(draining bool) error {
	mu.Lock()
	defer mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	if f.Draining == draining {
		return nil
	}
	f.Draining = draining
	return s.save(f)
}

// MarkRunning moves an item to running and records the child that runs it, so a
// resumed hub can probe whether that child is still alive. It clears any prior
// attempt's reason so a re-attempted item shows no stale fault while it runs.
func (s *Store) MarkRunning(id string, pid int) error {
	return s.settle(id, StatusRunning, "", pid)
}

// Finish settles a running item at its terminal status with the reason recorded
// on its checkpoint and clears its child pid.
func (s *Store) Finish(id, status, reason string) error {
	return s.settle(id, status, reason, 0)
}

// Pause parks a running item back at the front as paused, records the surfaced
// reason, and stops the drain — all in one write, so a reader never catches the
// item paused while the queue still reads as draining. A resume re-attempts it.
func (s *Store) Pause(id, reason string) error {
	mu.Lock()
	defer mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	for i := range f.Items {
		if f.Items[i].ID == id {
			f.Items[i].Status = StatusPaused
			f.Items[i].Reason = reason
			f.Items[i].PID = 0
			f.Draining = false
			return s.save(f)
		}
	}
	return ErrNotQueued
}

// Move shifts the item with id one slot toward the front (dir -1) or back
// (dir +1), returning the resulting queue. A running item cannot move and the
// running item cannot be jumped over, so a reorder never disturbs the run in
// flight. Moving past either end is a no-op, not an error.
func (s *Store) Move(id string, dir int) ([]Item, error) {
	mu.Lock()
	defer mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	idx := -1
	for i := range f.Items {
		if f.Items[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, ErrNotQueued
	}
	if f.Items[idx].Status == StatusRunning {
		return nil, ErrRunning
	}
	target := idx + dir
	if target < 0 || target >= len(f.Items) {
		return f.Items, nil
	}
	if f.Items[target].Status == StatusRunning {
		return f.Items, nil
	}
	f.Items[idx], f.Items[target] = f.Items[target], f.Items[idx]
	if err := s.save(f); err != nil {
		return nil, err
	}
	return f.Items, nil
}

// MarkSkipped settles an item as skipped with the reason it was passed over,
// clearing any child pid. The drain uses it to record a duplicate it did not
// run.
func (s *Store) MarkSkipped(id, reason string) error {
	return s.settle(id, StatusSkipped, reason, 0)
}

// Meta returns the queue's run-level configuration.
func (s *Store) Meta() (Meta, error) {
	mu.Lock()
	defer mu.Unlock()
	f, err := s.load()
	if err != nil {
		return Meta{}, err
	}
	return Meta{
		Draining: f.Draining,
		NoResume: f.NoResume,
		OnFault:  f.OnFault,
	}, nil
}

// SetOptions records the run-level knobs a drain start carries: whether children
// ignore stored checkpoints, and what a fault does to the rest of the queue.
func (s *Store) SetOptions(noResume bool, onFault string) error {
	mu.Lock()
	defer mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	f.NoResume = noResume
	f.OnFault = onFault
	return s.save(f)
}

// Restart resets the queue for a fresh drain from the top: every item that is
// not currently running returns to pending with its reason and child pid
// cleared. It backs skip-resume, which discards any stored position before
// draining.
func (s *Store) Restart() error {
	mu.Lock()
	defer mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	for i := range f.Items {
		if f.Items[i].Status == StatusRunning {
			continue
		}
		f.Items[i].Status = StatusPending
		f.Items[i].Reason = ""
		f.Items[i].PID = 0
	}
	return s.save(f)
}

func (s *Store) settle(id, status, reason string, pid int) error {
	mu.Lock()
	defer mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	for i := range f.Items {
		if f.Items[i].ID == id {
			f.Items[i].Status = status
			f.Items[i].Reason = reason
			f.Items[i].PID = pid
			return s.save(f)
		}
	}
	return ErrNotQueued
}

func (s *Store) load() (file, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return file{Items: []Item{}}, nil
		}
		return file{}, err
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return file{}, err
	}
	if f.Items == nil {
		f.Items = []Item{}
	}
	return f, nil
}

// DrainReport is what a headless queue-member child leaves for the hub drainer
// on exit: when the run ended on a fault or provider pause, the failure class
// and reason. It lets the drain settle an item — including an epic whose fault
// lives on a sub-issue's checkpoint, not the epic's — from the child's own
// outcome rather than the epic checkpoint, which never shows it.
type DrainReport struct {
	Class  string `json:"class,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// WriteReport writes a drain report to path.
func WriteReport(path string, r DrainReport) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ReadReport reads a drain report from path, reporting ok=false when the file is
// absent or unreadable so the drain falls back to checkpoint-derived outcome.
func ReadReport(path string) (DrainReport, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return DrainReport{}, false
	}
	var r DrainReport
	if err := json.Unmarshal(data, &r); err != nil {
		return DrainReport{}, false
	}
	return r, true
}

func (s *Store) save(f file) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
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
