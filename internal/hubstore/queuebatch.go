package hubstore

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/queue"
)

// Batch is a named, persistent subset of one repo's queue: a slug identifier
// unique per root, the display name it is shown under — which may be empty, and
// then surfaces fall back to when it was filed — and that stamp. Membership lives
// on the queue items themselves, so a batch groups the queue rather than holding
// a queue of its own, and dismissing one dissolves the grouping without moving
// any work.
type Batch struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

// Batches returns the repo's batches, oldest first. A batch whose members have
// all left the queue still lists until it is dismissed.
func (q *Queue) Batches() ([]Batch, error) {
	queueMu.Lock()
	defer queueMu.Unlock()
	return q.batches()
}

// Batch returns one batch, reporting queue.ErrBatchNotFound when the repo holds
// none under id.
func (q *Queue) Batch(id string) (Batch, error) {
	queueMu.Lock()
	defer queueMu.Unlock()
	return q.batch(id)
}

// CreateBatch files ids under a new batch and returns its identifier. Every id
// must be queued, still runnable, and held by no other batch; the first that is
// not refuses the whole create naming it, so a batch never lands half formed.
func (q *Queue) CreateBatch(name string, ids []string) (string, error) {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	id, err := q.freeBatchID(name)
	if err != nil {
		return "", err
	}
	if err := st.batchIn(id, ids); err != nil {
		return "", err
	}
	if _, err := q.db.Exec(
		`INSERT INTO queue_batches(root, id, name, created_at) VALUES(?, ?, ?, ?)`,
		q.root, id, name, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return "", err
	}
	if err := q.persist(st); err != nil {
		return "", err
	}
	return id, nil
}

// RenameBatch replaces a batch's display name, leaving its identifier and members
// untouched. An empty name is allowed: the batch falls back to its stamp.
func (q *Queue) RenameBatch(id, name string) error {
	queueMu.Lock()
	defer queueMu.Unlock()
	res, err := q.db.Exec(
		`UPDATE queue_batches SET name = ? WHERE root = ? AND id = ?`, strings.TrimSpace(name), q.root, id,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", id, queue.ErrBatchNotFound)
	}
	return nil
}

// AddToBatch takes ids into an existing batch under the rules a create applies,
// refusing the whole edit at the first id that cannot join.
func (q *Queue) AddToBatch(id string, ids []string) error {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return err
	}
	if _, err := q.batch(id); err != nil {
		return err
	}
	if err := st.batchIn(id, ids); err != nil {
		return err
	}
	return q.persist(st)
}

// RemoveFromBatch drops ids from the batch. A member always leaves — its status
// and the child it may be running are untouched, since leaving the grouping is
// not leaving the queue — and an id the batch does not hold is a no-op.
func (q *Queue) RemoveFromBatch(id string, ids []string) error {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return err
	}
	if _, err := q.batch(id); err != nil {
		return err
	}
	drop := make(map[string]bool, len(ids))
	for _, member := range ids {
		drop[member] = true
	}
	for i := range st.items {
		if st.items[i].Batch == id && drop[st.items[i].ID] {
			st.items[i].Batch = ""
		}
	}
	return q.persist(st)
}

// DismissBatch dissolves the grouping: every member loses its membership and the
// batch row goes. It never touches an item's status or the child one is running,
// so dismissing a batch mid-run leaves the work exactly where it stands.
func (q *Queue) DismissBatch(id string) error {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return err
	}
	if _, err := q.batch(id); err != nil {
		return err
	}
	for i := range st.items {
		if st.items[i].Batch == id {
			st.items[i].Batch = ""
		}
	}
	if err := q.persist(st); err != nil {
		return err
	}
	_, err = q.db.Exec(`DELETE FROM queue_batches WHERE root = ? AND id = ?`, q.root, id)
	return err
}

// ArmBatch starts draining this queue scoped to one batch: the drain runs its
// members in queue order and stops at the batch's boundary, leaving whatever else
// is queued pending. It is Arm with a scope — the same run-level knobs, the same
// stamp on an already-draining queue — except that a no-resume start restarts the
// members alone, and the skip fold covers those members alone. It refuses an
// unknown batch with queue.ErrBatchNotFound and a batch with nothing pending or
// paused in it with queue.ErrNoRunnableItems, writing nothing in either case.
func (q *Queue) ArmBatch(id string, noResume bool, onFault string, skips []string) error {
	queueMu.Lock()
	defer queueMu.Unlock()
	st, err := q.loadImported()
	if err != nil {
		return err
	}
	if _, err := q.batch(id); err != nil {
		return err
	}
	st.batch = id
	if !st.hasRunnable() {
		return queue.ErrNoRunnableItems
	}
	if noResume {
		st.restart()
	}
	st.addSkips(skips)
	st.noResume = noResume
	st.onFault = onFault
	st.arm()
	return q.persist(st)
}

// batchIn files every id under batch, refusing the first that is not queued, has
// already settled, or belongs to another batch. Nothing is written until every id
// has passed, so a refused edit leaves the queue exactly as it was.
func (st *queueState) batchIn(batch string, ids []string) error {
	members := make([]int, 0, len(ids))
	for _, id := range ids {
		i := st.indexOf(id)
		switch {
		case i < 0:
			return fmt.Errorf("%s: %w", id, queue.ErrNotQueued)
		case !queue.Runnable(st.items[i].Status):
			return fmt.Errorf("%s is %s: %w", id, st.items[i].Status, queue.ErrNotBatchable)
		case st.items[i].Batch != "" && st.items[i].Batch != batch:
			return fmt.Errorf("%s is already in batch %s: %w", id, st.items[i].Batch, queue.ErrAlreadyBatched)
		}
		members = append(members, i)
	}
	for _, i := range members {
		st.items[i].Batch = batch
	}
	return nil
}

func (st *queueState) indexOf(id string) int {
	for i := range st.items {
		if st.items[i].ID == id {
			return i
		}
	}
	return -1
}

// batches and batch read the batch table; both run with queueMu held, like every
// other read of a queue the same lock's writers rewrite.
func (q *Queue) batches() (out []Batch, err error) {
	rows, err := q.db.Query(
		`SELECT id, name, created_at FROM queue_batches WHERE root = ? ORDER BY created_at, id`, q.root,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out = []Batch{}
	for rows.Next() {
		var b Batch
		var createdAt string
		if err := rows.Scan(&b.ID, &b.Name, &createdAt); err != nil {
			return nil, err
		}
		if b.CreatedAt, err = parseBatchStamp(createdAt, b.ID); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (q *Queue) batch(id string) (Batch, error) {
	b := Batch{ID: id}
	var createdAt string
	err := q.db.QueryRow(
		`SELECT name, created_at FROM queue_batches WHERE root = ? AND id = ?`, q.root, id,
	).Scan(&b.Name, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Batch{}, fmt.Errorf("%s: %w", id, queue.ErrBatchNotFound)
	}
	if err != nil {
		return Batch{}, err
	}
	b.CreatedAt, err = parseBatchStamp(createdAt, id)
	return b, err
}

func parseBatchStamp(stamp, id string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse created_at for batch %s: %w", id, err)
	}
	return t, nil
}

// freeBatchID slugifies name and disambiguates it against the identifiers this
// repo's batches already hold.
func (q *Queue) freeBatchID(name string) (string, error) {
	base := batchSlug(name)
	for n := 1; ; n++ {
		id := base
		if n > 1 {
			id = base + "-" + strconv.Itoa(n)
		}
		var taken string
		err := q.db.QueryRow(`SELECT id FROM queue_batches WHERE root = ? AND id = ?`, q.root, id).Scan(&taken)
		if errors.Is(err, sql.ErrNoRows) {
			return id, nil
		}
		if err != nil {
			return "", err
		}
	}
}

var reBatchSlug = regexp.MustCompile(`[^a-z0-9]+`)

// batchSlug reduces a display name to an identifier. A batch filed without a name
// — or under one carrying no alphanumerics — yields "batch", which the caller
// disambiguates.
func batchSlug(name string) string {
	slug := strings.Trim(reBatchSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if slug == "" {
		return "batch"
	}
	return slug
}
