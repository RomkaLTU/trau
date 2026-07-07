package webserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/queue"
)

// QueueRequest is the body of POST /repos/{repo}/queue: the kind of work to
// register — a run-once ticket or an epic — and its tracker identifier, with an
// optional title carried from the board so the queued row reads without a
// second tracker call.
type QueueRequest struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Title string `json:"title"`
}

// QueueItemView is one queued item as the Queue view reads it: its 1-based
// position, kind, identifier, title, pending status, and — for an epic — the
// sub-issues captured when it was queued.
type QueueItemView struct {
	Position  int              `json:"position"`
	Kind      string           `json:"kind"`
	ID        string           `json:"id"`
	Title     string           `json:"title,omitempty"`
	Status    string           `json:"status"`
	Reason    string           `json:"reason,omitempty"`
	SubIssues []queue.SubIssue `json:"sub_issues,omitempty"`
	QueuedAt  string           `json:"queued_at,omitempty"`
}

// QueueResponse is the /repos/{repo}/queue resource: the repo's queue in
// registration order and whether the hub is currently draining it.
type QueueResponse struct {
	Repo     string          `json:"repo"`
	Draining bool            `json:"draining"`
	Items    []QueueItemView `json:"items"`
}

// DrainRequest is the body of POST /repos/{repo}/queue/drain: whether the hub
// should be draining the repo's queue. Setting it true starts sequential
// execution; false pauses it, taking effect after the current child exits.
type DrainRequest struct {
	Draining bool `json:"draining"`
}

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

func (s *Server) handleQueueItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	s.dequeue(w, r)
}

// handleQueueDrain starts or pauses draining a repo's queue. Starting flips the
// persisted draining flag and launches the drain loop; pausing clears the flag
// and the loop stops after the current child exits — there is no mid-run kill
// (Stop remains the per-run action). It is gated on the workspace allowlist like
// registration: only a Registered repo can be drained.
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
	if err := queue.NewStore(root).SetDraining(req.Draining); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "set draining: " + err.Error()})
		return
	}
	if req.Draining {
		s.drain.ensure(s.drainCtx, root)
	}
	s.writeQueue(w, http.StatusOK, root)
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
	items, draining, err := queue.NewStore(root).Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read queue: " + err.Error()})
		return
	}
	writeJSON(w, status, QueueResponse{Repo: filepath.Base(root), Draining: draining, Items: queueItemViews(items)})
}

// enqueue registers a ticket or epic for execution. It is gated on the workspace
// allowlist: only a Registered repo accepts Queue registration, so an
// observe-only repo is refused. Queuing an epic carries its sub-issues, captured
// through the hub's existing epic preview, so the queue records what an epic run
// will cover. Re-queuing something already present is refused with a clear
// message.
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
	kind := queue.Kind(strings.TrimSpace(req.Kind))
	if kind != queue.KindTicket && kind != queue.KindEpic {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("kind %q must be %q or %q", req.Kind, queue.KindTicket, queue.KindEpic)})
		return
	}
	item := queue.Item{Kind: kind, ID: id, Title: strings.TrimSpace(req.Title)}
	if kind == queue.KindEpic {
		subs, err := s.listEpicSubIssues(r.Context(), root, id)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "epic preview failed: " + err.Error()})
			return
		}
		item.SubIssues = toQueueSubIssues(subs)
	}
	if _, err := queue.NewStore(root).Add(item); errors.Is(err, queue.ErrAlreadyQueued) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("%s is already in the queue", id)})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enqueue: " + err.Error()})
		return
	}
	s.writeQueue(w, http.StatusCreated, root)
}

// dequeue removes a pending or terminal item from the queue by identifier,
// returning the resulting queue. It reports 404 when the item is not queued and
// 409 when it is running, so a running child is never orphaned by a dequeue.
func (s *Server) dequeue(w http.ResponseWriter, r *http.Request) {
	root, ok := s.queueRoot(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if _, err := queue.NewStore(root).Remove(id); errors.Is(err, queue.ErrNotQueued) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("%s is not in the queue", id)})
		return
	} else if errors.Is(err, queue.ErrRunning) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("%s is running and cannot be removed", id)})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "dequeue: " + err.Error()})
		return
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

func queueItemViews(items []queue.Item) []QueueItemView {
	out := make([]QueueItemView, 0, len(items))
	for i, it := range items {
		view := QueueItemView{
			Position:  i + 1,
			Kind:      string(it.Kind),
			ID:        it.ID,
			Title:     it.Title,
			Status:    it.Status,
			Reason:    it.Reason,
			SubIssues: it.SubIssues,
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
