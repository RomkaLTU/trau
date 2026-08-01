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
	draining      bool
	drainingSince time.Time
	noResume      bool
	onFault       string
	items         []queue.Item
}

// disarm stops the drain and drops its stamp, so a queue that is not draining
// never reports a loop run in flight.
func (st *queueState) disarm() {
	st.draining = false
	st.drainingSince = time.Time{}
}

// arm starts the drain, stamping when it began. An already-draining queue keeps
// its stamp, so a re-arm mid-run never restarts the clock.
func (st *queueState) arm() {
	if st.draining {
		return
	}
	st.draining = true
	st.drainingSince = time.Now().UTC()
}

// restart returns every item that is not currently running to pending with its
// reason and child pid cleared, so the next drain runs from the top.
func (st *queueState) restart() {
	for i := range st.items {
		if st.items[i].Status == queue.StatusRunning {
			continue
		}
		st.items[i].Status = queue.StatusPending
		st.items[i].Reason = ""
		st.items[i].PID = 0
	}
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

// Snapshot returns the queue in registration order along with its run-level
// metadata, the two halves the Queue view and the drainer both read. One load
// serves both, so a caller never pairs items from one write with metadata from
// the next.
func (q *Queue) Snapshot() ([]queue.Item, queue.Meta, error) {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return nil, queue.Meta{}, err
	}
	return st.items, queue.Meta{
		Draining:      st.draining,
		DrainingSince: st.drainingSince,
		NoResume:      st.noResume,
		OnFault:       st.onFault,
	}, nil
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

// AddFront inserts item at the front of the run order — behind the running item,
// so it never displaces one — stamping it pending like Add, and returns the
// resulting queue. When the same id is already queued pending it is
// moved to that position instead of refused, adopting the incoming Provider
// override so the latest gesture describes the run; moved reports which case
// ran. Any other already-queued status keeps Add's ErrAlreadyQueued refusal.
func (q *Queue) AddFront(item queue.Item) (items []queue.Item, moved bool, err error) {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return nil, false, err
	}
	for i := range st.items {
		if st.items[i].ID != item.ID {
			continue
		}
		if st.items[i].Status != queue.StatusPending {
			return nil, false, queue.ErrAlreadyQueued
		}
		existing := st.items[i]
		existing.Provider = item.Provider
		st.items = append(st.items[:i], st.items[i+1:]...)
		st.items = insertAtFront(st.items, existing)
		if err := q.persist(st); err != nil {
			return nil, false, err
		}
		return st.items, true, nil
	}
	item.Status = queue.StatusPending
	item.QueuedAt = time.Now().UTC()
	st.items = insertAtFront(st.items, item)
	if err := q.persist(st); err != nil {
		return nil, false, err
	}
	return st.items, false, nil
}

// insertAtFront places item at the slot the drain launches from next, appending
// when nothing runnable is left, so a front insert lands ahead of every waiting
// and paused row but behind anything running or settled. The gesture names what
// runs next whether it promotes pending work or resumes a pause, because the
// drain launches whichever runnable row comes first.
func insertAtFront(items []queue.Item, item queue.Item) []queue.Item {
	idx := len(items)
	for i := range items {
		if queue.Runnable(items[i].Status) {
			idx = i
			break
		}
	}
	items = append(items, queue.Item{})
	copy(items[idx+1:], items[idx:])
	items[idx] = item
	return items
}

// Remove drops the queued item with id, keeping the order of the rest, and
// returns the resulting queue. It reports ErrNotQueued when nothing matches and
// ErrRunning when the item is currently being drained.
func (q *Queue) Remove(id string) ([]queue.Item, error) {
	return q.remove(id, false)
}

// ForceRemove drops the queued item with id whatever its status, for a caller
// that has already stopped the item's child itself and so cannot orphan one. It
// still reports ErrNotQueued when nothing matches.
func (q *Queue) ForceRemove(id string) ([]queue.Item, error) {
	return q.remove(id, true)
}

func (q *Queue) remove(id string, force bool) ([]queue.Item, error) {
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
			if !force && it.Status == queue.StatusRunning {
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

// Promote swaps the queued rows in ids for item, landing it where the first of
// them sat so the epic inherits the place its children held. Every id must still
// be queued pending — a row that has started or settled since the caller looked
// reports ErrRunning or ErrNotQueued and writes nothing — and item's own id must
// not already be queued.
func (q *Queue) Promote(item queue.Item, ids []string) ([]queue.Item, error) {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return nil, err
	}
	swap := make(map[string]bool, len(ids))
	for _, id := range ids {
		swap[id] = true
	}
	matched := 0
	for _, it := range st.items {
		if it.ID == item.ID {
			return nil, queue.ErrAlreadyQueued
		}
		if !swap[it.ID] {
			continue
		}
		if it.Status == queue.StatusRunning {
			return nil, queue.ErrRunning
		}
		if it.Status != queue.StatusPending {
			return nil, queue.ErrNotQueued
		}
		matched++
	}
	if matched != len(swap) {
		return nil, queue.ErrNotQueued
	}
	item.Status = queue.StatusPending
	item.QueuedAt = time.Now().UTC()
	kept := make([]queue.Item, 0, len(st.items))
	promoted := false
	for _, it := range st.items {
		if !swap[it.ID] {
			kept = append(kept, it)
			continue
		}
		if !promoted {
			kept = append(kept, item)
			promoted = true
		}
	}
	st.items = kept
	if err := q.persist(st); err != nil {
		return nil, err
	}
	return st.items, nil
}

// Arm starts draining this queue for a fresh run and records the run-level knobs
// that go with it: noResume returns every non-running item to pending so the
// queue re-runs from the top, and onFault decides what a fault does to the rest.
// It refuses a queue with nothing pending or paused with queue.ErrNoRunnableItems
// and writes nothing in that case. Validation, the reset, the options and the arm
// share one lock and one persist, so a concurrent enqueue or settlement can
// neither slip past the check nor observe a half-armed queue. Arming an
// already-draining queue keeps its stamp, so a re-arm mid-run never restarts the
// clock.
func (q *Queue) Arm(noResume bool, onFault string) error {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return err
	}
	if !queue.HasRunnable(st.items) {
		return queue.ErrNoRunnableItems
	}
	if noResume {
		st.restart()
	}
	st.noResume = noResume
	st.onFault = onFault
	st.arm()
	return q.persist(st)
}

// Rearm resumes a queue the drain disarmed, keeping the knobs the run was armed
// with. It is Arm without the reset: a re-attempt of one parked item must leave
// the items that already settled alone, which replaying a no-resume arm would
// not — that returns every settled item to pending and runs shipped work again.
// A queue with nothing runnable left reports queue.ErrNoRunnableItems.
func (q *Queue) Rearm() error {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return err
	}
	if !queue.HasRunnable(st.items) {
		return queue.ErrNoRunnableItems
	}
	st.arm()
	return q.persist(st)
}

// FinishDraining clears the draining flag once the queue has nothing left to run
// — no pending, paused, or running item — and reports whether it did, so a
// completed queue reads stopped instead of idling armed. An empty or fully
// settled queue disarms the same way, which self-heals a row persisted armed
// before a start required runnable work. The check and the write share one lock,
// so an item queued after the drain's last snapshot keeps the queue armed rather
// than being stranded.
func (q *Queue) FinishDraining() (bool, error) {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return false, err
	}
	if !st.draining {
		return false, nil
	}
	for _, it := range st.items {
		if queue.Runnable(it.Status) || it.Status == queue.StatusRunning {
			return false, nil
		}
	}
	st.disarm()
	if err := q.persist(st); err != nil {
		return false, err
	}
	return true, nil
}

// SetDraining records whether the hub is draining this queue, stamping when the
// drain was armed so the run in flight can be timed from it. Arming an
// already-draining queue is a no-op, so a re-arm mid-run never restarts the
// clock. It survives a serve restart with the rest of the row, so a resumed hub
// picks draining back up where it left off.
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
	if draining {
		st.arm()
	} else {
		st.disarm()
	}
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
			st.disarm()
			return q.persist(st)
		}
	}
	return queue.ErrNotQueued
}

// SetSubIssueStates advances the recorded states of an item's sub-issues,
// leaving every id states does not name alone and writing nothing when none of
// them moves. It is how the drain reports a child that settled while the item
// carrying it is still running, ahead of the wholesale stamp its own settle
// makes.
func (q *Queue) SetSubIssueStates(id string, states map[string]string) error {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return err
	}
	for i := range st.items {
		if st.items[i].ID != id {
			continue
		}
		changed := false
		for j := range st.items[i].SubIssues {
			sub := &st.items[i].SubIssues[j]
			next, ok := states[sub.ID]
			if !ok || next == sub.State {
				continue
			}
			sub.State = next
			changed = true
		}
		if !changed {
			return nil
		}
		return q.persist(st)
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

// MoveToFront repositions the item with id at the front of the run order —
// behind anything running or settled, ahead of every row the drain would still
// launch — and returns the resulting queue, so the drain picks it as soon as the
// run in flight settles. The item keeps every field it carries, its Provider
// override and QueuedAt stamp included, unlike the AddFront path which adopts
// the incoming request's. A settled item cannot be promoted.
func (q *Queue) MoveToFront(id string) ([]queue.Item, error) {
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
	if !queue.Runnable(st.items[idx].Status) {
		return nil, queue.ErrNotPending
	}
	item := st.items[idx]
	st.items = append(st.items[:idx], st.items[idx+1:]...)
	st.items = insertAtFront(st.items, item)
	if err := q.persist(st); err != nil {
		return nil, err
	}
	return st.items, nil
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
	var drainingSince string
	err = q.db.QueryRow(
		`SELECT draining, no_resume, on_fault, draining_since FROM queue_repos WHERE root = ?`, q.root,
	).Scan(&draining, &noResume, &st.onFault, &drainingSince)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return queueState{}, err
	}
	st.draining = draining != 0
	st.noResume = noResume != 0
	if drainingSince != "" {
		t, perr := time.Parse(time.RFC3339Nano, drainingSince)
		if perr != nil {
			return queueState{}, fmt.Errorf("parse draining_since for %s: %w", q.root, perr)
		}
		st.drainingSince = t
	}

	subs, err := q.loadSubIssues()
	if err != nil {
		return queueState{}, err
	}

	rows, err := q.db.Query(
		`SELECT id, kind, title, source, provider, status, reason, pid, queued_at FROM queue_items WHERE root = ? ORDER BY position`,
		q.root,
	)
	if err != nil {
		return queueState{}, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var it queue.Item
		var kind, queuedAt string
		if scanErr := rows.Scan(&it.ID, &kind, &it.Title, &it.Source, &it.Provider, &it.Status, &it.Reason, &it.PID, &queuedAt); scanErr != nil {
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
	drainingSince := ""
	if !st.drainingSince.IsZero() {
		drainingSince = st.drainingSince.UTC().Format(time.RFC3339Nano)
	}
	if _, err := tx.Exec(
		`INSERT INTO queue_repos(root, draining, no_resume, on_fault, draining_since) VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(root) DO UPDATE SET draining = excluded.draining, no_resume = excluded.no_resume, on_fault = excluded.on_fault, draining_since = excluded.draining_since`,
		q.root, boolToInt(st.draining), boolToInt(st.noResume), st.onFault, drainingSince,
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
			`INSERT INTO queue_items(root, position, id, kind, title, source, provider, status, reason, pid, queued_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			q.root, pos, it.ID, string(it.Kind), it.Title, it.Source, it.Provider, it.Status, it.Reason, it.PID, queuedAt,
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
