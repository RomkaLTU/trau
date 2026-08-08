package webserver

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/pipeline"
	"github.com/RomkaLTU/trau/internal/queue"
)

// requeueTimeout bounds an issue requeue: it drives a fresh trau to restore the
// tracker labels and status, close the attempt PR, and delete the attempt
// branches, so it pays for several tracker and forge round-trips where a
// preview pays for one.
const requeueTimeout = 3 * time.Minute

// requeueTarget resolves what both requeue endpoints need before they can act: a
// POST, a plausible ticket id under idKey, a repo the workspace allowlist admits,
// and a ticket that repo's tracker can still resolve — a row queued before a switch
// to internal names an id no reader answers for. It writes the refusal itself and
// answers false once it has.
func (s *Server) requeueTarget(w http.ResponseWriter, r *http.Request, idKey string) (root, id string, ok bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return "", "", false
	}
	id = strings.TrimSpace(r.PathValue(idKey))
	if !reTicketID.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("%q is not a valid ticket identifier", id)})
		return "", "", false
	}
	name := r.PathValue("repo")
	root, ok = s.allowedRoot(name)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("repo %q is not on the serve workspace allowlist and is observe-only; add its root to SERVE_WORKSPACE to requeue its tickets", name),
		})
		return "", "", false
	}
	if s.refuseUnresolvableTarget(w, name, id) {
		return "", "", false
	}
	return root, id, true
}

// spawnRequeue drives a fresh trau through the same --requeue recovery the CLI
// offers, in the repo root and never with --force.
func (s *Server) spawnRequeue(ctx context.Context, root, id string) ([]byte, error) {
	out, err := s.sup.Capture(ctx, SpawnSpec{
		Dir:  root,
		Args: []string{"--repo", root, "--requeue", id, "--no-tui"},
		Env:  childEnv(s.home),
	})
	// The child removes the tree as part of its own requeue; the hub owns the row,
	// so it settles from its own record whether or not the child got that far.
	s.settleWorktreeAt(root, id)
	return out, err
}

// restoreQueueRow repairs the queue snapshot the drain left behind. The recovery
// rewrites the tracker and drops the checkpoint but never touches the hub's queue,
// so until the settled row goes back to pending the ticket reads eligible
// everywhere except where the drain looks. It writes the refusal itself.
func (s *Server) restoreQueueRow(w http.ResponseWriter, root, id string) bool {
	if err := s.stores.Queue(root).Requeue(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "restore queue row: " + err.Error()})
		return false
	}
	return true
}

// handleIssueRequeue makes a quarantined ticket eligible again from the web: it
// drives a fresh trau with --requeue <id> in the repo root — the same recovery
// the CLI offers — then repairs the queue snapshot the drain left behind, so
// neither the ticket's own row nor an epic carrying it keeps reporting the
// quarantine. It is gated on the workspace allowlist like a dry-run, and refused
// while the repo is draining or has no free run lane: a requeue rewrites tracker
// state, checks out the base, and deletes branches a running child could be
// standing on. It answers with the repaired queue, so the caller lands on the
// same snapshot the queue endpoint would serve.
func (s *Server) handleIssueRequeue(w http.ResponseWriter, r *http.Request) {
	root, id, ok := s.requeueTarget(w, r, "id")
	if !ok {
		return
	}
	_, meta, err := s.stores.Queue(root).Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read queue: " + err.Error()})
		return
	}
	if meta.Draining || s.drain.lanesFull(root) {
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
	if _, err := s.spawnRequeue(ctx, root, id); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("requeue %s failed: %v", id, err)})
		return
	}
	if !s.restoreQueueRow(w, root, id) {
		return
	}
	s.writeQueue(w, http.StatusOK, root)
}

// RunRequeueResult is what a run requeue changed, in the recovery's own words —
// labels restored, attempt PR closed, branches dropped, checkpoint cleared. An
// empty list is the idempotent answer: the ticket already sat eligible.
type RunRequeueResult struct {
	Ticket  string   `json:"ticket"`
	Changed []string `json:"changed"`
}

// handleRunRequeue is the way back from an applied fix session (ADR 0034): it
// drives the same trau --requeue the CLI offers — never with --force — and
// answers with the steps that changed, so the receipt can list them. Where the
// quarantine banner's requeue refuses on the whole repo, this one refuses on the
// ticket alone: a rewritten ticket goes back in front of a loop already draining
// others, and the drainer picks it up naturally. A run that is no longer failed —
// requeued from the CLI already, or never quarantined — has nothing to drive, but
// its queue row is restored all the same: the give-up outlives the checkpoint the
// recovery cleared, and until the row goes back "eligible again" is a promise the
// drain does not keep.
func (s *Server) handleRunRequeue(w http.ResponseWriter, r *http.Request) {
	root, ticket, ok := s.requeueTarget(w, r, "ticket")
	if !ok {
		return
	}
	items, err := s.stores.Queue(root).Load()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read queue: " + err.Error()})
		return
	}
	if busy := s.requeueBlocker(root, ticket, items); busy != "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": busy})
		return
	}

	changed := []string{}
	if _, failed := s.grillFailedRunFor(root, ticket); failed {
		ctx, cancel := context.WithTimeout(r.Context(), requeueTimeout)
		defer cancel()
		out, err := s.spawnRequeue(ctx, root, ticket)
		if err != nil {
			status := http.StatusBadGateway
			if strings.Contains(err.Error(), pipeline.AlreadyShipped) {
				status = http.StatusConflict
			}
			writeJSON(w, status, map[string]string{"error": requeueRefusal(ticket, err)})
			return
		}
		changed = parseRequeueChanges(out)
	}
	if !s.restoreQueueRow(w, root, ticket) {
		return
	}
	writeJSON(w, http.StatusOK, RunRequeueResult{Ticket: ticket, Changed: changed})
}

// requeueBlocker names why ticket cannot be requeued right now, or "" when it
// can: a live loop working it, or a queue row still waiting for a drain or held
// by one. A settled row is no obstacle — that give-up is what the recovery undoes.
func (s *Server) requeueBlocker(root, ticket string, items []queue.Item) string {
	cleaned := filepath.Clean(root)
	for _, e := range s.liveInstances() {
		if e.Ticket == ticket && filepath.Clean(e.RepoRoot) == cleaned {
			return fmt.Sprintf("%s is running (pid %d) — stop it before requeuing", ticket, e.PID)
		}
	}
	for _, it := range items {
		if it.ID != ticket {
			continue
		}
		if it.Status == queue.StatusRunning {
			return fmt.Sprintf("%s is running — stop it before requeuing", ticket)
		}
		if queue.Runnable(it.Status) {
			return fmt.Sprintf("%s is already queued in %s — remove it from the queue before requeuing", ticket, filepath.Base(root))
		}
	}
	return ""
}

// requeueRefusal renders what the child refused with for a browser. Capture
// decorates the exit status with the child's first stderr line, which carries
// the refusal behind the console's own prefix and the "requeue <ticket>" context
// the CLI adds — neither belongs on a receipt.
func requeueRefusal(ticket string, err error) string {
	msg := err.Error()
	if _, rest, found := strings.Cut(msg, console.ErrorPrefix); found {
		msg = rest
	}
	return strings.TrimPrefix(msg, "requeue "+ticket+": ")
}

// parseRequeueChanges reads the steps a --requeue reported from its stdout. Each
// one is a plain-mode console line "HH:MM:SS   ✓ <what changed>"; the closing
// summary carries no glyph and is not a step.
func parseRequeueChanges(stdout []byte) []string {
	changed := []string{}
	for _, line := range strings.Split(string(stdout), "\n") {
		if _, step, found := strings.Cut(line, "✓ "); found {
			changed = append(changed, strings.TrimSpace(step))
		}
	}
	return changed
}
