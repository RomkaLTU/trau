package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/folderrepo"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/pipeline"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// QueueRequest is the body of POST /repos/{repo}/queue: the tracker identifier
// to register, an optional title, and an optional kind. Kind may be "ticket" or
// "epic"; left empty or "auto" the hub resolves it by looking the id up in the
// tracker, so the Loop card can add a bare id without knowing what it is.
// Provider is an ephemeral per-run override of the configured routing — it
// applies only to this item's child and never persists to config. Front lands
// the item in the first pending position instead of the back, never displacing
// a running item.
type QueueRequest struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Provider string `json:"provider"`
	Front    bool   `json:"front"`
}

// QueueItemView is one queued item as the Queue view reads it: its 1-based
// position, kind, identifier, title, issue source, per-run provider override,
// pending status, and — for an epic — the sub-issues captured when it was
// queued. ProviderPin is the Provider pinned on the underlying issue, which the
// run uses whenever the item carries no override of its own. Blockers are the
// item's still-unresolved blocked-by edges and Blocked reports whether it has
// any, so the queue can refuse to run the row on its own and say why. Removing
// reports a stop-then-remove in flight, so a running row that is on its way out
// reads as leaving rather than as work still under way. Batch names the batch
// holding the item, empty when none does.
type QueueItemView struct {
	Position    int              `json:"position"`
	Kind        string           `json:"kind"`
	ID          string           `json:"id"`
	Title       string           `json:"title,omitempty"`
	Source      string           `json:"source,omitempty"`
	Provider    string           `json:"provider,omitempty"`
	ProviderPin string           `json:"provider_pin,omitempty"`
	Status      string           `json:"status"`
	Reason      string           `json:"reason,omitempty"`
	Blockers    []string         `json:"blockers,omitempty"`
	Blocked     bool             `json:"blocked"`
	Removing    bool             `json:"removing,omitempty"`
	Batch       string           `json:"batch,omitempty"`
	SubIssues   []queue.SubIssue `json:"sub_issues,omitempty"`
	QueuedAt    string           `json:"queued_at,omitempty"`
}

// QueueResponse is the /repos/{repo}/queue resource: the repo's queue in
// registration order, whether the hub is currently draining it and since when,
// and whether a stop is ending the child that was running. DrainingSince is
// absent unless the queue is draining. ReleasingEpic names the Epic whose release
// holds the queue, so a drain that starts nothing reads as waiting on that release
// rather than as idle. Batches lists the repo's batches and DrainingBatch names the
// one the drain in flight is scoped to — empty when it is draining the whole queue
// — so a client can label the run.
type QueueResponse struct {
	Repo          string          `json:"repo"`
	Draining      bool            `json:"draining"`
	DrainingSince string          `json:"draining_since,omitempty"`
	DrainingBatch string          `json:"draining_batch"`
	Stopping      bool            `json:"stopping"`
	ReleasingEpic string          `json:"releasing_epic,omitempty"`
	Batches       []BatchView     `json:"batches"`
	Items         []QueueItemView `json:"items"`
}

// DrainRequest is the body of POST /repos/{repo}/queue/drain: whether the hub
// should be draining the repo's queue. Setting it true starts sequential
// execution; false pauses it, taking effect after the current child exits. On a
// start it also carries the run-level knobs — whether to ignore stored
// checkpoints, and what a fault does to the rest of the queue.
type DrainRequest struct {
	Draining bool   `json:"draining"`
	NoResume bool   `json:"no_resume,omitempty"`
	OnFault  string `json:"on_fault,omitempty"`
}

// MoveRequest is the body of POST /repos/{repo}/queue/{id}/move: either the
// direction to shift the item — -1 toward the front or 1 toward the back — or
// the absolute position to send it to, which only ever means "front". Exactly
// one of the two is accepted.
type MoveRequest struct {
	Dir int    `json:"dir,omitempty"`
	To  string `json:"to,omitempty"`
}

const moveToFront = "front"

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.viewQueue(w, r)
	case http.MethodPost:
		s.enqueue(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleQueueItem serves one queue row. Its wildcard also catches paths that name
// no row at all — a stale client's POST to the deleted /queue/shutdown lands here —
// so an id the queue does not hold is 404 whatever the method, and 405 stays
// reserved for a row that really is there.
func (s *Server) handleQueueItem(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		s.dequeue(w, r)
		return
	}
	root, ok := s.queueRoot(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if _, queued := s.queuedItem(root, id); !queued {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("%s is not in the queue", id)})
		return
	}
	w.Header().Set("Allow", http.MethodDelete)
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
}

// handleQueueDrain starts or pauses draining a repo's queue. Starting flips the
// persisted draining flag, settles any parked item whose checkpoint already
// proves it shipped, and launches the drain loop; pausing clears the flag and the
// loop stops after the current child exits — there is no mid-run kill, which is
// what the stop route adds on top. A start against a queue with nothing pending
// or paused is refused 409 and changes nothing. It is gated on the workspace
// allowlist like registration: only a Registered repo can be drained.
func (s *Server) handleQueueDrain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	name := r.PathValue("repo")
	root, ok := s.allowedRoot(name)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("repo %q is observe-only; only a Registered repo can be drained — register it first", name),
		})
		return
	}
	var req DrainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	onFault := ""
	if req.Draining {
		var err error
		if onFault, err = normalizeOnFault(req.OnFault); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if err := s.setDraining(root, req.Draining, req.NoResume, onFault); queueStartConflict(err) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.writeQueue(w, http.StatusOK, root)
}

// queueStartConflict reports the refusals a queue start answers 409 to rather
// than 500: a queue with nothing runnable in it, and a repo whose working tree
// another loop already holds.
func queueStartConflict(err error) bool {
	var collision folderCollisionError
	return errors.Is(err, queue.ErrNoRunnableItems) || errors.As(err, &collision)
}

func normalizeOnFault(raw string) (string, error) {
	onFault := strings.TrimSpace(raw)
	if onFault == "" {
		return queue.OnFaultHalt, nil
	}
	if onFault != queue.OnFaultHalt && onFault != queue.OnFaultSkip {
		return "", fmt.Errorf("on_fault %q must be %q or %q", raw, queue.OnFaultHalt, queue.OnFaultSkip)
	}
	return onFault, nil
}

// setDraining arms or pauses a repo's drain, the write behind both the REST drain
// route and the MCP start_queue/pause_queue tools. A start is refused with
// queue.ErrNoRunnableItems unless the queue holds a pending or paused item, so an
// empty or fully settled queue is never left armed waiting for work a later add
// would silently pick up, and with a folderCollisionError when a loop already
// holds a working tree this repo shares. Pausing only clears the flag, so the loop
// stops after the current child exits. onFault must already be normalized.
func (s *Server) setDraining(root string, draining, noResume bool, onFault string) error {
	store := s.stores.Queue(root)
	if !draining {
		if err := store.SetDraining(false); err != nil {
			return fmt.Errorf("set draining: %w", err)
		}
		return nil
	}
	if collision, blocked := s.folderCollision(root); blocked {
		return collision
	}
	if err := store.Arm(noResume, onFault); errors.Is(err, queue.ErrNoRunnableItems) {
		return err
	} else if err != nil {
		return fmt.Errorf("arm queue: %w", err)
	}
	s.drain.reconcileQueue(root)
	s.drain.ensure(s.drainCtx, root)
	return nil
}

// handleQueueItemAction dispatches the actions one queue row carries. They share
// a wildcard segment because literal .../queue/{id}/move and .../queue/{id}/run
// patterns conflict with .../queue/batches/{bid} in net/http's mux — both match
// .../queue/batches/move and neither is more specific — so the action is matched
// here instead.
func (s *Server) handleQueueItemAction(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("action") {
	case "move":
		s.handleQueueMove(w, r)
	case "run":
		s.handleQueueRun(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown queue action"})
	}
}

// handleQueueMove reorders a queued item: one slot up or down with dir, or to
// the front of the run order with to "front", which the running view's Run next
// and Resume both use to name what the drain launches next mid-drain. It is
// gated like a dequeue on any repo whose queue the hub can see, reports 404 for
// an unknown item and 409 for one that has started or settled, and answers with
// the reordered queue.
func (s *Server) handleQueueMove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	root, ok := s.queueRoot(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	var req MoveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	store := s.stores.Queue(root)

	var err error
	switch {
	case req.To != "" && req.Dir != 0:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send dir or to, not both"})
		return
	case req.To != "":
		if req.To != moveToFront {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("to %q must be %q", req.To, moveToFront)})
			return
		}
		_, err = store.MoveToFront(id)
	case req.Dir == -1 || req.Dir == 1:
		_, err = store.Move(id, req.Dir)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("dir must be -1 (up) or 1 (down), or to %q", moveToFront)})
		return
	}

	switch {
	case errors.Is(err, queue.ErrNotQueued):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("%s is not in the queue", id)})
	case errors.Is(err, queue.ErrRunning):
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("%s is running and cannot be reordered", id)})
	case errors.Is(err, queue.ErrNotPending):
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("%s has already settled — only a pending or paused item can be promoted", id),
		})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reorder: " + err.Error()})
	default:
		s.writeQueue(w, http.StatusOK, root)
	}
}

// handleQueueRun runs one queued item and nothing after it. It spawns the item's
// child exactly as a drain would, then starts the loop that waits for it —
// without arming draining, so the tick that settles the item finds the drain off
// and stops instead of picking up the next row. It refuses with 409 whenever the
// repo already has work in flight: an armed drain, a running queue item, a live
// loop, or an Epic whose release still holds the repo — only that Epic's own
// finalize is let through — and, so the one-shot cannot bypass the drain's dedup,
// whenever an unsettled queued epic already covers the item.
func (s *Server) handleQueueRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	name := r.PathValue("repo")
	root, ok := s.allowedRoot(name)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("repo %q is observe-only; only a Registered repo can run queued work — register it first", name),
		})
		return
	}
	store := s.stores.Queue(root)
	items, meta, err := store.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read queue: " + err.Error()})
		return
	}
	if meta.Draining {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the queue is draining — pause it before running a single item"})
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
	id := strings.TrimSpace(r.PathValue("id"))
	item, queued := itemByID(items, id)
	if !queued {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("%s is not in the queue", id)})
		return
	}
	if !queue.Runnable(item.Status) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("%s has already settled %s and cannot be run", item.ID, item.Status)})
		return
	}
	if epic, held := s.heldByRelease(root, item.ID); held {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("%s is releasing — nothing else runs in this repo until its release lands or is handed off", epic),
		})
		return
	}
	if epic, covered := coveringEpic(items, item); covered {
		writeJSON(w, http.StatusConflict, map[string]string{"error": coveredByEpic(item.ID, epic.ID)})
		return
	}
	blockers, err := s.stores.Issues().UnresolvedBlockers(root, []string{item.ID})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read blockers: " + err.Error()})
		return
	}
	if unresolved := blockers[item.ID]; len(unresolved) > 0 {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("%s is blocked by %s", item.ID, strings.Join(unresolved, ", ")),
		})
		return
	}
	_ = s.stores.DrainOutcomes().Remove(root, item.ID)
	pid, err := s.sup.Spawn(s.drain.spec(root, item, false))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "spawn: " + err.Error()})
		return
	}
	if err := store.MarkRunning(item.ID, pid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mark running: " + err.Error()})
		return
	}
	s.clearQueuedIssue(r.Context(), root, item.ID)
	s.drain.ensure(s.drainCtx, root)
	s.writeQueue(w, http.StatusOK, root)
}

// validateQueueTarget confirms a to-be-queued id exists in the repo's tracker and
// falls inside the slice of it this repo owns, returning its title and the answering
// tracker's source binding — the same provider name the sync records on the stored
// issue. It is best-effort: a repo without direct tracker credentials cannot be
// checked, so it passes and the id is queued unvalidated; a definite not-found or
// out-of-scope answer is refused with a clear status and ok=false.
func (s *Server) validateQueueTarget(w http.ResponseWriter, r *http.Request, name, id string) (title, source string, ok bool) {
	repo, found := s.findRepo(name)
	if !found {
		return "", "", true
	}
	source, reader, err := s.readerFor(repo)
	if err != nil {
		return "", source, true
	}
	item, err := reader.Issue(r.Context(), id)
	if errors.Is(err, tracker.ErrIssueNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": id + " not found in this repo's tracker"})
		return "", "", false
	}
	if err != nil {
		return "", source, true
	}
	if !item.InProject {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("%s (project %q) is outside the slice of the tracker this repo owns — refusing to queue a ticket it does not mirror", id, item.Project),
		})
		return "", "", false
	}
	return item.Title, source, true
}

// viewQueue lists a repo's queue scoped to the Active repo, in registration
// order with each item's position. It reads whatever queue file exists, so an
// observe-only repo the hub has seen run answers an empty queue rather than an
// error.
func (s *Server) viewQueue(w http.ResponseWriter, r *http.Request) {
	root, ok := s.queueRoot(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	s.writeQueue(w, http.StatusOK, root)
}

// writeQueue answers with the repo's current queue: its items in registration
// order and whether the hub is draining it. Every handler that mutates the queue
// ends here, so the response always reflects the persisted draining flag rather
// than the caller's local view of it.
func (s *Server) writeQueue(w http.ResponseWriter, status int, root string) {
	view, err := s.queueView(root)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, status, view)
}

func (s *Server) queueView(root string) (QueueResponse, error) {
	items, meta, err := s.stores.Queue(root).Snapshot()
	if err != nil {
		return QueueResponse{}, fmt.Errorf("read queue: %w", err)
	}
	pins, err := s.stores.Issues().Providers(root)
	if err != nil {
		return QueueResponse{}, fmt.Errorf("read provider pins: %w", err)
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	blockers, err := s.stores.Issues().UnresolvedBlockers(root, ids)
	if err != nil {
		return QueueResponse{}, fmt.Errorf("read blockers: %w", err)
	}
	batches, err := s.stores.Queue(root).Batches()
	if err != nil {
		return QueueResponse{}, fmt.Errorf("read batches: %w", err)
	}
	drainingSince := ""
	if !meta.DrainingSince.IsZero() {
		drainingSince = meta.DrainingSince.UTC().Format(time.RFC3339)
	}
	return QueueResponse{
		Repo:          filepath.Base(root),
		Draining:      meta.Draining,
		DrainingSince: drainingSince,
		DrainingBatch: meta.Batch,
		Stopping:      s.isStopping(root),
		ReleasingEpic: s.releasingEpic(root),
		Batches:       batchViews(batches),
		Items:         queueItemViews(items, pins, blockers, s.removingItems(root)),
	}, nil
}

// handleQueueStop stops a repo's loop where it stands: it disarms the drain
// synchronously so no tick spawns a new child, then — in the background — ends
// the child that was running (runningChild) with the same escalation a per-run
// Stop uses. It clears nothing: the stopped item parks at its checkpoint and
// every row stays queued, so Start picks the queue back up from there. It
// answers with the queue — 202 while the stop is in flight, 200 when there was
// no child to end — and a second POST during a stop is a no-op that answers the
// same way. It is gated on the workspace allowlist like a drain start: only a
// Registered repo can be stopped.
func (s *Server) handleQueueStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	name := r.PathValue("repo")
	root, ok := s.allowedRoot(name)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("repo %q is observe-only; only a Registered repo can be stopped — register it first", name),
		})
		return
	}
	store := s.stores.Queue(root)
	if err := store.SetDraining(false); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "disarm drain: " + err.Error()})
		return
	}
	items, _, err := store.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read queue: " + err.Error()})
		return
	}
	ticket, pid, running := s.runningChild(root, items)
	if !running {
		s.writeQueue(w, http.StatusOK, root)
		return
	}
	if !s.beginStopping(root) {
		s.writeQueue(w, http.StatusAccepted, root)
		return
	}
	// The drain loop is what parks the item once the child is gone, and a
	// disarmed queue may have none left running.
	s.drain.ensure(s.drainCtx, root)
	// Answered before the stop starts, so a child that dies on its graceful stop cannot
	// clear the in-flight flag out from under the ack that reports it.
	s.writeQueue(w, http.StatusAccepted, root)
	go s.stopRunningChild(root, ticket, pid)
}

// enqueue registers a ticket or epic for execution. It is gated on the workspace
// allowlist: only a Registered repo accepts Queue registration, so an
// observe-only repo is refused. Queuing an epic carries its sub-issues, captured
// through the hub's existing epic preview, so the queue records what an epic run
// will cover. Re-queuing something already present is refused with a clear
// message — except a pending item re-queued with front, which moves to the
// front instead. Registration is also refused when it would duplicate an
// unsettled row's work in the other direction of the hierarchy: a ticket a
// queued epic covers, or an epic whose children are queued on their own. A
// ticket that leaves its epic fully queued child by child is collapsed into that
// epic, so the queue never reads as covering an epic it does not hold.
func (s *Server) enqueue(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("repo")
	root, ok := s.allowedRoot(name)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("repo %q is observe-only; only a Registered repo can have work queued — register it first", name),
		})
		return
	}
	var req QueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	id := strings.TrimSpace(req.ID)
	if !reTicketID.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("id %q is not a valid ticket identifier", req.ID)})
		return
	}
	hint := queue.Kind(strings.TrimSpace(req.Kind))
	if hint != "" && hint != "auto" && hint != queue.KindTicket && hint != queue.KindEpic {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("kind %q must be %q, %q, or empty to auto-detect", req.Kind, queue.KindTicket, queue.KindEpic)})
		return
	}

	item := queue.Item{ID: id, Title: strings.TrimSpace(req.Title), Kind: hint, Provider: strings.TrimSpace(req.Provider)}

	iss, internal, err := s.stores.Issues().Internal(root, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read issue: " + err.Error()})
		return
	}
	if internal {
		// An internal issue is authoritative in the store and never in the tracker,
		// so resolve its title and epic children locally and skip the tracker
		// validation the synced path runs.
		item.Source = iss.Source
		if item.Title == "" {
			item.Title = iss.Title
		}
		if hint != queue.KindTicket {
			children, err := s.stores.Issues().InternalChildren(root, id)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resolve item: " + err.Error()})
				return
			}
			if len(children) > 0 {
				item.Kind = queue.KindEpic
				item.SubIssues = storedSubIssues(children)
			} else {
				item.Kind = queue.KindTicket
			}
		}
	} else {
		title, source, ok := s.validateQueueTarget(w, r, name, id)
		if !ok {
			return
		}
		item.Source = source
		if item.Title == "" {
			item.Title = title
		}
		// Resolve kind: an explicit ticket stays a ticket; otherwise (epic or
		// auto) list the children — any child makes it an epic carrying them.
		if hint != queue.KindTicket {
			subs, err := s.listEpicSubIssues(r.Context(), root, id)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "resolve item: " + err.Error()})
				return
			}
			if len(subs) > 0 {
				item.Kind = queue.KindEpic
				item.SubIssues = toQueueSubIssues(subs)
			} else {
				item.Kind = queue.KindTicket
			}
		}
	}

	if item.Kind == queue.KindEpic && folderrepo.Is(root) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": pipeline.ErrFolderRepoEpic.Error()})
		return
	}

	queued, err := s.stores.Queue(root).Load()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read queue: " + err.Error()})
		return
	}
	if reason, dup := duplicateEnqueueReason(queued, item); dup {
		writeJSON(w, http.StatusConflict, map[string]string{"error": reason})
		return
	}

	// A front enqueue answers 201 like a plain one; re-queuing a pending item
	// with front is a move-to-front answered 200 with the reordered queue rather
	// than the 409 a plain re-queue gets. Any other already-queued status —
	// running, paused, or settled — still conflicts.
	if req.Front {
		_, movedToFront, err := s.stores.Queue(root).AddFront(item)
		if errors.Is(err, queue.ErrAlreadyQueued) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("%s is already in the queue", id)})
			return
		} else if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enqueue: " + err.Error()})
			return
		}
		status := http.StatusCreated
		if movedToFront {
			status = http.StatusOK
		}
		s.afterEnqueue(r.Context(), root, item)
		s.writeQueue(w, status, root)
		return
	}

	if _, err := s.stores.Queue(root).Add(item); errors.Is(err, queue.ErrAlreadyQueued) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("%s is already in the queue", id)})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enqueue: " + err.Error()})
		return
	}
	s.afterEnqueue(r.Context(), root, item)
	s.writeQueue(w, http.StatusCreated, root)
}

// afterEnqueue settles what a fresh queue entry implies elsewhere: the tracker
// move, the epic's promotion, and the project's memory of where its tickets run.
func (s *Server) afterEnqueue(ctx context.Context, root string, item queue.Item) {
	s.markQueued(ctx, root, item)
	s.promoteQueuedEpic(ctx, root, item)
	s.rememberStartRepo(root, item.ID)
}

// dequeue removes an item from the queue by identifier, returning the resulting
// queue. It ejects the work with the row: the run's saved progress is wiped and
// the ticket goes back to Ready on the tracker, so a later pickup starts a fresh
// run rather than resuming this one. It reports 404 when the item is not queued.
// A running item is refused 409 as before unless the request opts in with stop=1,
// which stops the item's child first and answers 202 — the row goes once the
// process is confirmed gone, and an armed drain moves on to the next runnable
// item. Every other row is wiped behind the response, since a reset spawns a
// child of its own.
func (s *Server) dequeue(w http.ResponseWriter, r *http.Request) {
	root, ok := s.queueRoot(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	item, queued := s.queuedItem(root, id)
	if queued && item.Status == queue.StatusRunning && r.URL.Query().Get("stop") == "1" {
		if s.beginRemoving(root, id) {
			go s.removeRunningItem(root, item)
		}
		s.writeQueue(w, http.StatusAccepted, root)
		return
	}
	if _, err := s.stores.Queue(root).Remove(id); errors.Is(err, queue.ErrNotQueued) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("%s is not in the queue", id)})
		return
	} else if errors.Is(err, queue.ErrRunning) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("%s is running — remove it with stop=1 to stop the run first", id),
		})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "dequeue: " + err.Error()})
		return
	}
	if queued {
		s.clearQueued(r.Context(), root, item)
		go s.wipeRemovedRun(root, item)
	}
	s.writeQueue(w, http.StatusOK, root)
}

// queueRoot resolves a repo identifier to its root for queue operations: a
// Registered or SERVE_WORKSPACE-seeded repo first, falling back to any repo the
// hub has seen run so an observe-only repo's queue stays readable and removable.
func (s *Server) queueRoot(name string) (string, bool) {
	if root, ok := s.allowedRoot(name); ok {
		return root, true
	}
	if repo, ok := s.findRepo(name); ok {
		return repo.Root, true
	}
	return "", false
}

func itemByID(items []queue.Item, id string) (queue.Item, bool) {
	for _, it := range items {
		if it.ID == id {
			return it, true
		}
	}
	return queue.Item{}, false
}

// queueSettled reports whether an item is queue history — done, failed, skipped,
// or an epic whose PR is a human's to land. A settled row covers nothing, so the
// hierarchy guards ignore it.
func queueSettled(it queue.Item) bool {
	switch it.Status {
	case queue.StatusDone, queue.StatusFailed, queue.StatusSkipped, queue.StatusAwaitingMerge:
		return true
	default:
		return false
	}
}

// coveringEpic returns the unsettled queued epic whose sub-issues already cover
// target. Unlike the drain's duplicateReason it is position-independent: wherever
// the epic sits, its remaining-work run picks the child up. Only a standalone
// ticket can be covered; epics dedup their shared leaves through tracker state as
// they run.
func coveringEpic(items []queue.Item, target queue.Item) (queue.Item, bool) {
	if target.Kind == queue.KindEpic {
		return queue.Item{}, false
	}
	for _, it := range items {
		if it.Kind != queue.KindEpic || queueSettled(it) {
			continue
		}
		for _, sub := range it.SubIssues {
			if sub.ID == target.ID {
				return it, true
			}
		}
	}
	return queue.Item{}, false
}

// coveredByEpic is the refusal both the add-time and the one-shot-run guard
// answer with, so a caller reads the same sentence whichever gesture it made.
func coveredByEpic(id, epicID string) string {
	return fmt.Sprintf("%s is already covered by queued epic %s", id, epicID)
}

// duplicateEnqueueReason reports why registering item would queue work an
// existing row already covers: a ticket an unsettled queued epic carries, or an
// epic whose children sit in the queue as tickets of their own. An id the queue
// already holds is left to the store's exact-id refusal, which owns the
// re-queue and move-to-front answers.
func duplicateEnqueueReason(items []queue.Item, item queue.Item) (string, bool) {
	if _, exists := itemByID(items, item.ID); exists {
		return "", false
	}
	if epic, covered := coveringEpic(items, item); covered {
		return coveredByEpic(item.ID, epic.ID), true
	}
	if children := queuedIndividually(items, item.SubIssues); len(children) > 0 {
		return fmt.Sprintf(
			"%s covers %s, already queued individually — remove from the queue before queueing the epic",
			item.ID,
			strings.Join(children, ", "),
		), true
	}
	return "", false
}

// queuedIndividually lists the sub-issues an incoming epic carries that are
// already queued as unsettled tickets of their own.
func queuedIndividually(items []queue.Item, subs []queue.SubIssue) []string {
	out := make([]string, 0, len(subs))
	for _, sub := range subs {
		if it, exists := itemByID(items, sub.ID); exists && it.Kind == queue.KindTicket && !queueSettled(it) {
			out = append(out, sub.ID)
		}
	}
	return out
}

func queueItemViews(items []queue.Item, pins map[string]string, blockers map[string][]string, removing map[string]bool) []QueueItemView {
	out := make([]QueueItemView, 0, len(items))
	for i, it := range items {
		view := QueueItemView{
			Position:    i + 1,
			Kind:        string(it.Kind),
			ID:          it.ID,
			Title:       it.Title,
			Source:      it.Source,
			Provider:    it.Provider,
			ProviderPin: pins[it.ID],
			Status:      it.Status,
			Reason:      it.Reason,
			Blockers:    blockers[it.ID],
			Blocked:     len(blockers[it.ID]) > 0,
			Removing:    removing[it.ID],
			Batch:       it.Batch,
			SubIssues:   it.SubIssues,
		}
		if !it.QueuedAt.IsZero() {
			view.QueuedAt = it.QueuedAt.Format(time.RFC3339)
		}
		out = append(out, view)
	}
	return out
}

func toQueueSubIssues(subs []EpicSubIssue) []queue.SubIssue {
	out := make([]queue.SubIssue, 0, len(subs))
	for _, sub := range subs {
		out = append(out, queue.SubIssue{ID: sub.ID, Title: sub.Title, State: sub.State})
	}
	return out
}

// storedSubIssues maps an epic's stored children onto queue sub-issues, so a
// queued internal epic records the same shape a synced one does.
func storedSubIssues(children []hubstore.Issue) []queue.SubIssue {
	out := make([]queue.SubIssue, 0, len(children))
	for _, c := range children {
		out = append(out, storedSubIssue(c))
	}
	return out
}

// storedSubIssue maps one stored child onto a queue sub-issue, marking it done
// when its state group is terminal. A row reaches a terminal group two ways: a
// tracker sync writes "done", a transition trau mirrored itself writes
// "completed".
func storedSubIssue(c hubstore.Issue) queue.SubIssue {
	state := "todo"
	switch c.StatusGroup {
	case "done", "completed", "canceled":
		state = subIssueDone
	}
	return queue.SubIssue{ID: c.Identifier, Title: c.Title, State: state}
}
