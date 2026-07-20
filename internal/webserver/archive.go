package webserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
)

// handleIssueAction dispatches the sub-actions of a single issue. The route is a
// wildcard segment rather than a literal /archive because net/http's mux would
// otherwise flag it as conflicting with the issues/internal/{id} pattern; an
// unrecognized action answers 404.
func (s *Server) handleIssueAction(w http.ResponseWriter, r *http.Request) {
	switch action := r.PathValue("action"); action {
	case "archive":
		s.handleIssueArchive(w, r)
	case "attachments":
		s.handleIssueAttachments(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("unknown issue action %q", action)})
	}
}

// ArchiveRequest is the body of PUT /repos/{repo}/issues/{id}/archive: whether the
// issue is archived. The call is idempotent — archiving an archived issue, or
// clearing a live one, is a no-op that still answers with the current state.
type ArchiveRequest struct {
	Archived bool `json:"archived"`
}

// ArchiveResponse is the archive endpoint's answer: the updated issue with its new
// archived state, and how many pending queue entries the archive removed so the UI
// can toast it. QueueRemoved is zero on an unarchive.
type ArchiveResponse struct {
	IssueResponse
	QueueRemoved int `json:"queue_removed"`
}

// handleIssueArchive flips a stored issue's archive state (any source) and, on an
// archive, prunes the issue's — and its children's — pending queue entries so
// nothing archived is left waiting to run. Running and paused items are left
// alone. Sync never revives the archive bit (ADR 0007), so the board stays clear
// of it across pulls. An unknown repo or identifier answers 404.
func (s *Server) handleIssueArchive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var req ArchiveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	iss, found, err := s.stores.Issues().SetArchived(repo.Root, id, req.Archived)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "archive issue: " + err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": id + " not found in this repo"})
		return
	}
	removed := 0
	if req.Archived {
		removed = s.dropPendingFromQueue(repo.Root, s.archiveQueueTargets(repo.Root, iss))
	}
	writeJSON(w, http.StatusOK, ArchiveResponse{
		IssueResponse: s.storeIssueResponse(repo, iss),
		QueueRemoved:  removed,
	})
}

// archiveQueueTargets is the set of identifiers an archive prunes from the queue:
// the issue itself plus, when it heads an epic, its children across sources — a
// listing failure degrades to the issue alone rather than failing the archive.
func (s *Server) archiveQueueTargets(root string, iss hubstore.Issue) []string {
	ids := []string{iss.Identifier}
	if !iss.HasChildren {
		return ids
	}
	children, err := s.stores.Issues().Children(root, iss.Identifier)
	if err != nil {
		logger.Verbosef("archive %s: list children of %s: %v", root, iss.Identifier, err)
		return ids
	}
	for _, c := range children {
		ids = append(ids, c.Identifier)
	}
	return ids
}

// dropPendingFromQueue removes the pending queue entries whose id is in ids,
// through the same Queue.Remove path a manual dequeue takes, and reports how many
// it removed. Only pending items are touched: a running or paused entry is left in
// place so an archive never yanks work executing or parked mid-run.
func (s *Server) dropPendingFromQueue(root string, ids []string) int {
	if len(ids) == 0 {
		return 0
	}
	q := s.stores.Queue(root)
	items, err := q.Load()
	if err != nil {
		logger.Verbosef("archive %s: read queue: %v", root, err)
		return 0
	}
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	removed := 0
	for _, it := range items {
		if it.Status != queue.StatusPending {
			continue
		}
		if _, ok := want[it.ID]; !ok {
			continue
		}
		if _, err := q.Remove(it.ID); err != nil {
			logger.Verbosef("archive %s: drop %s from queue: %v", root, it.ID, err)
			continue
		}
		removed++
	}
	return removed
}
