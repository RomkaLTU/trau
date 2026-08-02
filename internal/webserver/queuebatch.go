package webserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
)

// BatchRequest is the body of POST /repos/{repo}/queue/batches: an optional
// display name and the queued ids to file under it. Every id must still be
// runnable and held by no other batch.
type BatchRequest struct {
	Name string   `json:"name"`
	IDs  []string `json:"ids"`
}

// BatchEditRequest is the body of PATCH /repos/{repo}/queue/batches/{bid}: an
// optional rename and the membership to take in or let go. A name left out of the
// body leaves the batch's own alone; sent empty it clears the name back to the
// stamp surfaces fall back to.
type BatchEditRequest struct {
	Name   *string  `json:"name"`
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

// BatchStartRequest is the body of POST /repos/{repo}/queue/batches/{bid}/start:
// the same run-level knobs a whole-queue drain start carries.
type BatchStartRequest struct {
	NoResume bool   `json:"no_resume,omitempty"`
	OnFault  string `json:"on_fault,omitempty"`
}

// BatchView is one batch as the Queue view reads it. Name may be empty, in which
// case a surface labels the batch by CreatedAt.
type BatchView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// handleQueueBatches files a new batch over queued items. It is gated on the
// workspace allowlist like a drain start, refuses 409 for an id that is not
// queued, has already settled, or belongs to another batch — naming it — and
// answers with the queue.
func (s *Server) handleQueueBatches(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	root, ok := s.batchRoot(w, r)
	if !ok {
		return
	}
	var req BatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if len(req.IDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ids must name at least one queued item"})
		return
	}
	if _, err := s.stores.Queue(root).CreateBatch(req.Name, req.IDs); writeBatchError(w, "create batch", err) {
		return
	}
	s.writeQueue(w, http.StatusOK, root)
}

// handleQueueBatch renames a batch, edits its membership, or dismisses it. A
// dismiss dissolves the grouping alone: no item's status moves and no running
// child is touched.
func (s *Server) handleQueueBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "PATCH, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	root, ok := s.batchRoot(w, r)
	if !ok {
		return
	}
	bid := strings.TrimSpace(r.PathValue("bid"))
	store := s.stores.Queue(root)
	if r.Method == http.MethodDelete {
		if err := store.DismissBatch(bid); writeBatchError(w, "dismiss batch", err) {
			return
		}
		s.writeQueue(w, http.StatusOK, root)
		return
	}
	var req BatchEditRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.Name != nil {
		if err := store.RenameBatch(bid, *req.Name); writeBatchError(w, "rename batch", err) {
			return
		}
	}
	if len(req.Add) > 0 {
		if err := store.AddToBatch(bid, req.Add); writeBatchError(w, "edit batch", err) {
			return
		}
	}
	if len(req.Remove) > 0 {
		if err := store.RemoveFromBatch(bid, req.Remove); writeBatchError(w, "edit batch", err) {
			return
		}
	}
	s.writeQueue(w, http.StatusOK, root)
}

// handleQueueBatchStart arms the drain scoped to one batch: it runs the batch's
// members in queue order, one child at a time, and disarms once none is runnable
// even though the rest of the queue is still pending. It refuses with 409
// whenever the repo already has work in flight — an armed drain, a running queue
// item, a live loop, a working tree another loop holds — with 409 when the batch
// holds nothing to run, and with 409 when a member is blocked by an unshipped
// ticket the batch does not carry, since only work inside the batch can be
// unblocked by draining it.
func (s *Server) handleQueueBatchStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	root, ok := s.batchRoot(w, r)
	if !ok {
		return
	}
	var req BatchStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	onFault, err := normalizeOnFault(req.OnFault)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	store := s.stores.Queue(root)
	items, meta, err := store.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read queue: " + err.Error()})
		return
	}
	if meta.Draining {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the queue is draining — stop it before starting a batch"})
		return
	}
	if running, ok := firstWithStatus(items, queue.StatusRunning); ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("%s is already running", running.ID)})
		return
	}
	if s.drain.repoLive(root) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a loop is already running in this repo — wait for it to finish"})
		return
	}
	if collision, blocked := s.folderCollision(root); blocked {
		writeJSON(w, http.StatusConflict, map[string]string{"error": collision.Error()})
		return
	}
	bid := strings.TrimSpace(r.PathValue("bid"))
	if _, err := store.Batch(bid); writeBatchError(w, "read batch", err) {
		return
	}
	blockers, err := s.batchBlockers(root, bid, items)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read blockers: " + err.Error()})
		return
	}
	if len(blockers) > 0 {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf(
				"batch %s is blocked by %s — ship them or take them into the batch",
				bid, strings.Join(blockers, ", "),
			),
		})
		return
	}
	if err := store.ArmBatch(bid, req.NoResume, onFault); errors.Is(err, queue.ErrNoRunnableItems) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	} else if writeBatchError(w, "arm batch", err) {
		return
	}
	s.drain.reconcileQueue(root)
	s.drain.ensure(s.drainCtx, root)
	s.writeQueue(w, http.StatusOK, root)
}

// batchRoot resolves the repo a batch gesture names, refusing an observe-only one
// the way every other queue write does.
func (s *Server) batchRoot(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("repo")
	root, ok := s.allowedRoot(name)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("repo %q is observe-only; only a Registered repo can batch queued work — register it first", name),
		})
		return "", false
	}
	return root, true
}

// batchBlockers names the still-open tickets outside the batch that hold one of
// its runnable members back, in queue order and each named once. A blocker the
// batch carries itself is left out — draining the batch is what resolves it — and
// so is one this queue already settled done, whatever the tracker mirror still
// says about it.
func (s *Server) batchBlockers(root, bid string, items []queue.Item) ([]string, error) {
	members := make([]string, 0, len(items))
	inside := map[string]bool{}
	shipped := map[string]bool{}
	for _, it := range items {
		if it.Batch == bid {
			inside[it.ID] = true
			if queue.Runnable(it.Status) {
				members = append(members, it.ID)
			}
		}
		if it.Status == queue.StatusDone {
			shipped[it.ID] = true
		}
	}
	unresolved, err := s.stores.Issues().UnresolvedBlockers(root, members)
	if err != nil {
		return nil, err
	}
	out := []string{}
	named := map[string]bool{}
	for _, member := range members {
		for _, blocker := range unresolved[member] {
			if inside[blocker] || shipped[blocker] || named[blocker] {
				continue
			}
			named[blocker] = true
			out = append(out, blocker)
		}
	}
	return out, nil
}

// writeBatchError answers a batch call's refusal and reports whether it wrote:
// 404 for a batch the repo does not hold, 409 for an id that cannot join one, and
// 500 for anything else. A nil error writes nothing.
func writeBatchError(w http.ResponseWriter, what string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, queue.ErrBatchNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, queue.ErrNotQueued),
		errors.Is(err, queue.ErrNotBatchable),
		errors.Is(err, queue.ErrAlreadyBatched):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": what + ": " + err.Error()})
	}
	return true
}

// emitQueueBatchFinished records the boundary a batch-scoped drain stopped at, so
// the activity view can tell a batch that ran dry from a loop a human halted while
// items are still queued. A whole-queue drain emits nothing.
func (s *Server) emitQueueBatchFinished(root, bid string, items []queue.Item) {
	if bid == "" {
		return
	}
	b, err := s.stores.Queue(root).Batch(bid)
	if err != nil {
		logger.Verbosef("read finished batch %s: %v", bid, err)
		b = hubstore.Batch{ID: bid}
	}
	s.emitQueueEvent(root, event.KindQueueBatchFinished,
		fmt.Sprintf("batch %s finished — no runnable members left", batchLabel(b)),
		map[string]any{"batch": b.ID, "name": b.Name, "outcomes": batchOutcomes(items, bid)})
}

// batchLabel is how a batch reads in a sentence: its name when it has one, its
// identifier otherwise.
func batchLabel(b hubstore.Batch) string {
	if b.Name != "" {
		return b.Name
	}
	return b.ID
}

// batchOutcomes tallies how a batch's members settled, the summary the finish
// event carries in place of listing them.
func batchOutcomes(items []queue.Item, bid string) map[string]int {
	tally := map[string]int{}
	for _, it := range items {
		if it.Batch == bid {
			tally[it.Status]++
		}
	}
	return tally
}

// batchViews maps the stored batches onto the Queue view's shape.
func batchViews(batches []hubstore.Batch) []BatchView {
	out := make([]BatchView, 0, len(batches))
	for _, b := range batches {
		out = append(out, BatchView{ID: b.ID, Name: b.Name, CreatedAt: b.CreatedAt.UTC().Format(time.RFC3339)})
	}
	return out
}
