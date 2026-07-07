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
	SubIssues []queue.SubIssue `json:"sub_issues,omitempty"`
	QueuedAt  string           `json:"queued_at,omitempty"`
}

// QueueResponse is the /repos/{repo}/queue resource: the repo's queue in
// registration order.
type QueueResponse struct {
	Repo  string          `json:"repo"`
	Items []QueueItemView `json:"items"`
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
	items, err := queue.NewStore(root).Load()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read queue: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, QueueResponse{Repo: filepath.Base(root), Items: queueItemViews(items)})
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
	items, err := queue.NewStore(root).Add(item)
	if errors.Is(err, queue.ErrAlreadyQueued) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("%s is already in the queue", id)})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enqueue: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, QueueResponse{Repo: filepath.Base(root), Items: queueItemViews(items)})
}

// dequeue removes a pending item from the queue by identifier, returning the
// resulting queue. It reports 404 when the item is not queued.
func (s *Server) dequeue(w http.ResponseWriter, r *http.Request) {
	root, ok := s.queueRoot(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	items, err := queue.NewStore(root).Remove(id)
	if errors.Is(err, queue.ErrNotQueued) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("%s is not in the queue", id)})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "dequeue: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, QueueResponse{Repo: filepath.Base(root), Items: queueItemViews(items)})
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
