package hubstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/queue"
)

const legacyQueueFilename = "queue.json"

// queueMu serializes every queue mutation across all repos, so a load-mutate-
// persist cycle is atomic and concurrent writers cannot corrupt item order. The
// hub is the only process that opens the database, so a process-level lock is
// enough — it mirrors the single global lock the file-era queue held.
var queueMu sync.Mutex

// Queue is the hub's authoritative store for one Repo's execution queue: the
// ordered items, the drain flag, and the run-level fault policy that used to
// live in <root>/.trau/queue.json. It is bound to a repo root; the caller owns
// the database's lifecycle.
type Queue struct {
	db   *sql.DB
	root string
}

// NewQueue returns a queue store for root over db.
func NewQueue(db *sql.DB, root string) *Queue {
	return &Queue{db: db, root: root}
}

// queueState is one repo's whole queue in memory: the file-era `file` shape,
// loaded, mutated, and written back as a unit.
type queueState struct {
	draining bool
	noResume bool
	onFault  string
	items    []queue.Item
}

// Load returns the queue in registration order, empty when nothing has been
// queued yet.
func (q *Queue) Load() ([]queue.Item, error) {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return nil, err
	}
	return st.items, nil
}

// Snapshot returns the queue in registration order along with whether the hub is
// draining it, the two facts the Queue view and the drainer both read.
func (q *Queue) Snapshot() ([]queue.Item, bool, error) {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return nil, false, err
	}
	return st.items, st.draining, nil
}

// Add appends item to the end of the queue, stamping it pending and recording
// when it was queued, and returns the resulting queue. It refuses a ticket or
// epic already present with ErrAlreadyQueued.
func (q *Queue) Add(item queue.Item) ([]queue.Item, error) {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return nil, err
	}
	for _, it := range st.items {
		if it.ID == item.ID {
			return nil, queue.ErrAlreadyQueued
		}
	}
	item.Status = queue.StatusPending
	item.QueuedAt = time.Now().UTC()
	st.items = append(st.items, item)
	if err := q.persist(st); err != nil {
		return nil, err
	}
	return st.items, nil
}

// Remove drops the queued item with id, keeping the order of the rest, and
// returns the resulting queue. It reports ErrNotQueued when nothing matches and
// ErrRunning when the item is currently being drained.
func (q *Queue) Remove(id string) ([]queue.Item, error) {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return nil, err
	}
	kept := make([]queue.Item, 0, len(st.items))
	found := false
	for _, it := range st.items {
		if it.ID == id {
			if it.Status == queue.StatusRunning {
				return nil, queue.ErrRunning
			}
			found = true
			continue
		}
		kept = append(kept, it)
	}
	if !found {
		return nil, queue.ErrNotQueued
	}
	st.items = kept
	if err := q.persist(st); err != nil {
		return nil, err
	}
	return st.items, nil
}

// FinishDraining clears the draining flag once the queue has run dry — at least
// one item, none of them pending, paused, or running — and reports whether it
// did, so a completed queue reads stopped instead of idling armed. An armed
// empty queue is left waiting for items. The check and the write share one lock,
// so an item queued after the drain's last snapshot keeps the queue armed rather
// than being stranded.
func (q *Queue) FinishDraining() (bool, error) {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return false, err
	}
	if !st.draining || len(st.items) == 0 {
		return false, nil
	}
	for _, it := range st.items {
		switch it.Status {
		case queue.StatusPending, queue.StatusPaused, queue.StatusRunning:
			return false, nil
		}
	}
	st.draining = false
	if err := q.persist(st); err != nil {
		return false, err
	}
	return true, nil
}

// SetDraining records whether the hub is draining this queue. It survives a
// serve restart with the rest of the row, so a resumed hub picks draining back
// up where it left off.
func (q *Queue) SetDraining(draining bool) error {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return err
	}
	if st.draining == draining {
		return nil
	}
	st.draining = draining
	return q.persist(st)
}

// MarkRunning moves an item to running and records the child that runs it, so a
// resumed hub can probe whether that child is still alive. It clears any prior
// attempt's reason so a re-attempted item shows no stale fault while it runs.
func (q *Queue) MarkRunning(id string, pid int) error {
	return q.settle(id, queue.StatusRunning, "", pid)
}

// Finish settles a running item at its terminal status with the reason recorded
// on its checkpoint and clears its child pid. Settling an item done also settles
// its carried sub-issues done — a clean epic finish means the run drained them
// all — while any other outcome leaves their enqueue-time states alone.
func (q *Queue) Finish(id, status, reason string) error {
	return q.settle(id, status, reason, 0)
}

// MarkSkipped settles an item as skipped with the reason it was passed over,
// clearing any child pid. The drain uses it to record a duplicate it did not
// run.
func (q *Queue) MarkSkipped(id, reason string) error {
	return q.settle(id, queue.StatusSkipped, reason, 0)
}

// Pause parks a running item back at the front as paused, records the surfaced
// reason, and stops the drain — all in one write, so a reader never catches the
// item paused while the queue still reads as draining. A resume re-attempts it.
func (q *Queue) Pause(id, reason string) error {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return err
	}
	for i := range st.items {
		if st.items[i].ID == id {
			st.items[i].Status = queue.StatusPaused
			st.items[i].Reason = reason
			st.items[i].PID = 0
			st.draining = false
			return q.persist(st)
		}
	}
	return queue.ErrNotQueued
}

// Move shifts the item with id one slot toward the front (dir -1) or back
// (dir +1), returning the resulting queue. A running item cannot move and the
// running item cannot be jumped over, so a reorder never disturbs the run in
// flight. Moving past either end is a no-op, not an error.
func (q *Queue) Move(id string, dir int) ([]queue.Item, error) {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return nil, err
	}
	idx := -1
	for i := range st.items {
		if st.items[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, queue.ErrNotQueued
	}
	if st.items[idx].Status == queue.StatusRunning {
		return nil, queue.ErrRunning
	}
	target := idx + dir
	if target < 0 || target >= len(st.items) {
		return st.items, nil
	}
	if st.items[target].Status == queue.StatusRunning {
		return st.items, nil
	}
	st.items[idx], st.items[target] = st.items[target], st.items[idx]
	if err := q.persist(st); err != nil {
		return nil, err
	}
	return st.items, nil
}

// Meta returns the queue's run-level configuration.
func (q *Queue) Meta() (queue.Meta, error) {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return queue.Meta{}, err
	}
	return queue.Meta{Draining: st.draining, NoResume: st.noResume, OnFault: st.onFault}, nil
}

// SetOptions records the run-level knobs a drain start carries: whether children
// ignore stored checkpoints, and what a fault does to the rest of the queue.
func (q *Queue) SetOptions(noResume bool, onFault string) error {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return err
	}
	st.noResume = noResume
	st.onFault = onFault
	return q.persist(st)
}

// Restart resets the queue for a fresh drain from the top: every item that is
// not currently running returns to pending with its reason and child pid
// cleared. It backs skip-resume, which discards any stored position before
// draining.
func (q *Queue) Restart() error {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return err
	}
	for i := range st.items {
		if st.items[i].Status == queue.StatusRunning {
			continue
		}
		st.items[i].Status = queue.StatusPending
		st.items[i].Reason = ""
		st.items[i].PID = 0
	}
	return q.persist(st)
}

func (q *Queue) settle(id, status, reason string, pid int) error {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return err
	}
	for i := range st.items {
		if st.items[i].ID == id {
			st.items[i].Status = status
			st.items[i].Reason = reason
			st.items[i].PID = pid
			if status == queue.StatusDone {
				for j := range st.items[i].SubIssues {
					st.items[i].SubIssues[j].State = "done"
				}
			}
			return q.persist(st)
		}
	}
	return queue.ErrNotQueued
}

// ImportLegacy imports this repo's file-era <root>/.trau/queue.json into the
// queue tables and removes the file, doing nothing when no file is present. The
// import is transactional: the file is deleted only after its rows commit, and a
// failed import returns an error naming the file and leaves it in place so serve
// startup can abort without losing the queue.
func (q *Queue) ImportLegacy() error {
	queueMu.Lock()
	defer queueMu.Unlock()
	return q.importLocked()
}

// loadImported imports any leftover legacy file, then loads the queue. It runs
// with queueMu held; every operation calls it so a repo registered after the
// upgrade imports its queue.json on first touch.
func (q *Queue) loadImported() (queueState, error) {
	if err := q.importLocked(); err != nil {
		return queueState{}, err
	}
	return q.load()
}

type legacyQueue struct {
	Draining bool         `json:"draining,omitempty"`
	NoResume bool         `json:"no_resume,omitempty"`
	OnFault  string       `json:"on_fault,omitempty"`
	Items    []queue.Item `json:"items"`
}

func (q *Queue) importLocked() error {
	path := legacyQueuePath(q.root)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy %s: %w", path, err)
	}
	var f legacyQueue
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse legacy %s: %w", path, err)
	}
	st := queueState{
		draining: f.Draining,
		noResume: f.NoResume,
		onFault:  f.OnFault,
		items:    f.Items,
	}
	if st.items == nil {
		st.items = []queue.Item{}
	}
	if err := q.persist(st); err != nil {
		return fmt.Errorf("import legacy %s: %w", path, err)
	}
	return os.Remove(path)
}

func (q *Queue) load() (st queueState, err error) {
	st.items = []queue.Item{}
	var draining, noResume int
	err = q.db.QueryRow(
		`SELECT draining, no_resume, on_fault FROM queue_repos WHERE root = ?`, q.root,
	).Scan(&draining, &noResume, &st.onFault)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return queueState{}, err
	}
	st.draining = draining != 0
	st.noResume = noResume != 0

	subs, err := q.loadSubIssues()
	if err != nil {
		return queueState{}, err
	}

	rows, err := q.db.Query(
		`SELECT id, kind, title, status, reason, pid, queued_at FROM queue_items WHERE root = ? ORDER BY position`,
		q.root,
	)
	if err != nil {
		return queueState{}, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var it queue.Item
		var kind, queuedAt string
		if scanErr := rows.Scan(&it.ID, &kind, &it.Title, &it.Status, &it.Reason, &it.PID, &queuedAt); scanErr != nil {
			return queueState{}, scanErr
		}
		it.Kind = queue.Kind(kind)
		if queuedAt != "" {
			t, perr := time.Parse(time.RFC3339Nano, queuedAt)
			if perr != nil {
				return queueState{}, fmt.Errorf("parse queued_at for %s: %w", it.ID, perr)
			}
			it.QueuedAt = t
		}
		it.SubIssues = subs[it.ID]
		st.items = append(st.items, it)
	}
	return st, rows.Err()
}

func (q *Queue) loadSubIssues() (subs map[string][]queue.SubIssue, err error) {
	rows, err := q.db.Query(
		`SELECT item_id, id, title, state FROM queue_sub_issues WHERE root = ? ORDER BY item_id, position`,
		q.root,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	subs = map[string][]queue.SubIssue{}
	for rows.Next() {
		var itemID string
		var sub queue.SubIssue
		if scanErr := rows.Scan(&itemID, &sub.ID, &sub.Title, &sub.State); scanErr != nil {
			return nil, scanErr
		}
		subs[itemID] = append(subs[itemID], sub)
	}
	return subs, rows.Err()
}

func (q *Queue) persist(st queueState) error {
	tx, err := q.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO queue_repos(root, draining, no_resume, on_fault) VALUES(?, ?, ?, ?)
		 ON CONFLICT(root) DO UPDATE SET draining = excluded.draining, no_resume = excluded.no_resume, on_fault = excluded.on_fault`,
		q.root, boolToInt(st.draining), boolToInt(st.noResume), st.onFault,
	); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if _, err := tx.Exec(`DELETE FROM queue_sub_issues WHERE root = ?`, q.root); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if _, err := tx.Exec(`DELETE FROM queue_items WHERE root = ?`, q.root); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	for pos, it := range st.items {
		queuedAt := ""
		if !it.QueuedAt.IsZero() {
			queuedAt = it.QueuedAt.UTC().Format(time.RFC3339Nano)
		}
		if _, err := tx.Exec(
			`INSERT INTO queue_items(root, position, id, kind, title, status, reason, pid, queued_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			q.root, pos, it.ID, string(it.Kind), it.Title, it.Status, it.Reason, it.PID, queuedAt,
		); err != nil {
			return errors.Join(err, tx.Rollback())
		}
		for subPos, sub := range it.SubIssues {
			if _, err := tx.Exec(
				`INSERT INTO queue_sub_issues(root, item_id, position, id, title, state) VALUES(?, ?, ?, ?, ?, ?)`,
				q.root, it.ID, subPos, sub.ID, sub.Title, sub.State,
			); err != nil {
				return errors.Join(err, tx.Rollback())
			}
		}
	}
	return tx.Commit()
}

func legacyQueuePath(root string) string {
	return filepath.Join(root, ".trau", legacyQueueFilename)
}

// LegacyQueueFile reports a repo's file-era queue.json path and whether it is
// still present. A present file under a repo the hub already tracks means a
// half-completed upgrade that `trau doctor` surfaces; the hub imports and
// removes it on first touch.
func LegacyQueueFile(root string) (path string, present bool) {
	path = legacyQueuePath(root)
	if _, err := os.Stat(path); err == nil {
		return path, true
	}
	return path, false
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
