package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/worktree"
)

// worktreeRemoveTimeout bounds a `git worktree remove` + prune pair. Removal is
// local disk work; anything slower than this is a stuck filesystem, and the caller
// gets an answer rather than a hung request.
const worktreeRemoveTimeout = 60 * time.Second

// WorktreeRequest is what a child reports about the tree it created, adopted, or
// finished with. The path is computed identically on both sides, so it is carried
// for the record rather than trusted as news; state lets a CLI settle path close the
// row in the same call it removed the tree.
type WorktreeRequest struct {
	Ticket string `json:"ticket"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
	State  string `json:"state,omitempty"`
}

// WorktreeView is a worktree row as every reader sees it — the run detail panel and
// a reconcile sweep alike.
type WorktreeView struct {
	ID        int64  `json:"id"`
	Repo      string `json:"repo"`
	Ticket    string `json:"ticket"`
	Path      string `json:"path"`
	Branch    string `json:"branch"`
	State     string `json:"state"`
	CreatedAt string `json:"created_at,omitempty"`
	// The app the hub serves out of this tree: the port it holds, the URL browser
	// verify drives, and its standing. AppState is stopped for every tree in a repo
	// with no APP_START_CMD, which is why Serving is carried beside it — a stopped
	// app in a repo that serves one is a Start away, while a repo with no
	// APP_START_CMD has nothing to start and shows no app control at all.
	Port         int    `json:"port,omitempty"`
	AppURL       string `json:"app_url,omitempty"`
	AppState     string `json:"app_state,omitempty"`
	AppPID       int    `json:"app_pid,omitempty"`
	AppStartedAt string `json:"app_started_at,omitempty"`
	Serving      bool   `json:"serving"`
}

// handleWorktrees lists (GET) or records (POST) the repo's per-ticket worktrees.
func (s *Server) handleWorktrees(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.listWorktrees(w, repo)
	case http.MethodPost:
		s.recordWorktree(w, r, repo)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) listWorktrees(w http.ResponseWriter, repo registry.Repo) {
	rows, err := s.stores.Worktrees().List(repo.Root)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list worktrees: " + err.Error()})
		return
	}
	serving := repoServesApp(repo.Root)
	views := make([]WorktreeView, 0, len(rows))
	for _, row := range rows {
		views = append(views, worktreeView(row, serving))
	}
	writeJSON(w, http.StatusOK, views)
}

// recordWorktree files what a run reports. A state of settled also removes the
// tree, so the CLI reset/requeue/purge paths and the hub's own settle converge on
// one code path instead of two that can drift.
func (s *Server) recordWorktree(w http.ResponseWriter, r *http.Request, repo registry.Repo) {
	var req WorktreeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	req.Ticket = strings.TrimSpace(req.Ticket)
	req.Path = strings.TrimSpace(req.Path)
	if req.Ticket == "" || req.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": hubstore.ErrWorktreeRequired.Error()})
		return
	}
	// A settle the child already performed locally still passes through here, so a
	// tree its own removal missed is finished off; the row lands either way.
	if strings.TrimSpace(req.State) == hubstore.WorktreeSettled {
		s.stopWorktreeAppFor(repo.Root, req.Ticket)
		if err := s.removeWorktreeTree(repo, req.Path); err != nil {
			logger.Verbosef("settle worktree %s: %v", req.Ticket, err)
		}
	}
	row, err := s.stores.Worktrees().Record(repo.Root, hubstore.WorktreeInput{
		Ticket: req.Ticket, Path: req.Path, Branch: req.Branch, State: req.State,
	})
	if errors.Is(err, hubstore.ErrWorktreeRequired) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "record worktree: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, worktreeView(row, repoServesApp(repo.Root)))
}

// handleWorktree reads (GET) or removes (DELETE) a single worktree row. DELETE is
// the manual Remove behind the run detail's button: it takes the tree off disk and
// settles the row, and it refuses while a run still holds the repo.
func (s *Server) handleWorktree(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "worktree id must be a number"})
		return
	}
	row, exists, err := s.stores.Worktrees().Get(repo.Root, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read worktree: " + err.Error()})
		return
	}
	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown worktree"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, worktreeView(row, repoServesApp(repo.Root)))
	case http.MethodDelete:
		s.deleteWorktree(w, repo, row)
	default:
		w.Header().Set("Allow", "GET, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) deleteWorktree(w http.ResponseWriter, repo registry.Repo, row hubstore.Worktree) {
	if s.repoIsLive(repo.Root) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "a run is live in " + repo.Name + " — stop it before removing " + row.Ticket + "'s worktree",
		})
		return
	}
	s.stopWorktreeApp(repo.Root, row)
	if err := s.removeWorktreeTree(repo, row.Path); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	settled, err := s.stores.Worktrees().SetState(repo.Root, row.ID, hubstore.WorktreeSettled)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "settle worktree: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, worktreeView(settled, repoServesApp(repo.Root)))
}

// settleWorktree is what every hub-side settle calls: the ticket finished, so its
// tree comes off disk and its row is marked settled. A ticket that never had a tree
// — the whole WORKTREES=0 world — costs one absent row lookup and nothing else.
func (s *Server) settleWorktree(repo registry.Repo, ticket string) {
	row, exists, err := s.stores.Worktrees().ByTicket(repo.Root, ticket)
	if err != nil {
		logger.Verbosef("settle worktree %s: %v", ticket, err)
		return
	}
	if !exists || row.State != hubstore.WorktreeActive {
		return
	}
	s.stopWorktreeApp(repo.Root, row)
	if err := s.removeWorktreeTree(repo, row.Path); err != nil {
		logger.Verbosef("settle worktree %s: %v", ticket, err)
		return
	}
	if _, err := s.stores.Worktrees().SettleTicket(repo.Root, ticket); err != nil {
		logger.Verbosef("settle worktree %s: mark settled: %v", ticket, err)
	}
}

// settleWorktreeAt is settleWorktree for the callers that hold a repo root rather
// than the registered Repo — the drain works in roots.
func (s *Server) settleWorktreeAt(root, ticket string) {
	if repo, ok := s.findRepoByRoot(root); ok {
		s.settleWorktree(repo, ticket)
	}
}

func (s *Server) removeWorktreeTree(repo registry.Repo, path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(s.removeCtx(), worktreeRemoveTimeout)
	defer cancel()
	if err := worktree.Remove(ctx, repo.Root, path); err != nil {
		return errors.New("remove worktree " + path + ": " + err.Error())
	}
	return nil
}

// reconcileWorktrees squares the worktrees table with git's registry, the disk and
// the apps it recorded at boot. A settled row whose directory survived — a hub
// killed between the removal and the row update — is pruned now; an active row whose
// directory is gone is marked orphaned, because there is nothing left to remove and a
// stale active row would make the run detail promise a tree that is not there.
func (s *Server) reconcileWorktrees() {
	for _, repo := range s.knownRepos(s.liveInstances()) {
		rows, err := s.stores.Worktrees().AllForRepo(repo.Root)
		if err != nil {
			logger.Verbosef("reconcile worktrees %s: %v", repo.Name, err)
			continue
		}
		if len(rows) == 0 {
			continue
		}
		for _, row := range rows {
			present := row.Path != "" && dirExists(row.Path)
			switch {
			case row.State == hubstore.WorktreeActive && !present:
				s.stopWorktreeApp(repo.Root, row)
				if _, err := s.stores.Worktrees().SetState(repo.Root, row.ID, hubstore.WorktreeOrphaned); err != nil {
					logger.Verbosef("reconcile worktrees %s: orphan %s: %v", repo.Name, row.Ticket, err)
				}
			case row.State != hubstore.WorktreeActive && present:
				s.stopWorktreeApp(repo.Root, row)
				if err := s.removeWorktreeTree(repo, row.Path); err != nil {
					logger.Verbosef("reconcile worktrees %s: %v", repo.Name, err)
				}
			default:
				// The tree stands: an app its pid says is still up is this hub's to
				// adopt, and one whose process is gone leaves a row promising a URL
				// nothing answers.
				s.reconcileWorktreeApp(repo.Root, row)
			}
		}
		// Whatever the rows said, git's own registry must not keep a tree the disk
		// has lost: a stale record makes the next `worktree add` refuse the path.
		ctx, cancel := context.WithTimeout(s.removeCtx(), worktreeRemoveTimeout)
		if err := worktree.Prune(ctx, repo.Root); err != nil {
			logger.Verbosef("reconcile worktrees %s: %v", repo.Name, err)
		}
		cancel()
	}
}

// removeCtx is the parent for a removal's own deadline: the drain context once
// Start has one, the background otherwise, so a boot-time reconcile and a request
// handler both work.
func (s *Server) removeCtx() context.Context {
	if s.drainCtx != nil {
		return s.drainCtx
	}
	return context.Background()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func worktreeView(row hubstore.Worktree, serving bool) WorktreeView {
	return WorktreeView{
		ID:           row.ID,
		Repo:         filepath.Base(row.Repo),
		Ticket:       row.Ticket,
		Path:         row.Path,
		Branch:       row.Branch,
		State:        row.State,
		CreatedAt:    row.CreatedAt,
		Port:         row.App.Port,
		AppURL:       appURLForPort(row.App),
		AppState:     appStateOr(row.App.State),
		AppPID:       row.App.PID,
		AppStartedAt: row.App.StartedAt,
		Serving:      serving,
	}
}

// repoServesApp reports whether the repo has an APP_START_CMD for its worktrees to
// serve. A config that will not read answers no: a reader that cannot know reads
// the same as a repo that serves nothing, and offers no app control rather than a
// control that can do nothing.
func repoServesApp(root string) bool {
	cfg, err := repoConfig(root)
	if err != nil {
		logger.Verbosef("read the app start command for %s: %v", root, err)
		return false
	}
	return strings.TrimSpace(cfg.AppStartCmd) != ""
}
