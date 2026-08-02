package webserver

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// requeueTimeout bounds an issue requeue: it drives a fresh trau to restore the
// tracker labels and status, close the attempt PR, and delete the attempt
// branches, so it pays for several tracker and forge round-trips where a
// preview pays for one.
const requeueTimeout = 3 * time.Minute

// handleIssueRequeue makes a quarantined ticket eligible again from the web: it
// drives a fresh trau with --requeue <id> in the repo root — the same recovery
// the CLI offers — then repairs the queue snapshot the drain left behind, so
// neither the ticket's own row nor an epic carrying it keeps reporting the
// quarantine. It is gated on the workspace allowlist like a dry-run, and refused
// while the repo is draining or holds a live loop: a requeue rewrites tracker
// state, checks out the base, and deletes branches a running child could be
// standing on. It answers with the repaired queue, so the caller lands on the
// same snapshot the queue endpoint would serve.
func (s *Server) handleIssueRequeue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !reTicketID.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("%q is not a valid ticket identifier", id)})
		return
	}
	name := r.PathValue("repo")
	root, ok := s.allowedRoot(name)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("repo %q is not on the serve workspace allowlist and is observe-only; add its root to SERVE_WORKSPACE to requeue its tickets", name),
		})
		return
	}
	store := s.stores.Queue(root)
	_, meta, err := store.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read queue: " + err.Error()})
		return
	}
	if meta.Draining || s.repoIsLive(root) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("%s has a loop running — stop it before requeuing %s", filepath.Base(root), id),
		})
		return
	}
	if collision, ok := s.folderCollision(root); ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": collision.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requeueTimeout)
	defer cancel()
	if _, err := s.sup.Capture(ctx, SpawnSpec{
		Dir:  root,
		Args: []string{"--repo", root, "--requeue", id, "--no-tui"},
		Env:  childEnv(s.home),
	}); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("requeue %s failed: %v", id, err)})
		return
	}
	if err := store.Requeue(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "restore queue row: " + err.Error()})
		return
	}
	s.writeQueue(w, http.StatusOK, root)
}
