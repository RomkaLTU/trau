package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
)

// ReloadRequest names the repo whose binary the hub should restart onto.
type ReloadRequest struct {
	RepoRoot string `json:"repo_root"`
}

// ReloadAck is the answer to an accepted self-reload: it is pending, not done.
// The restart lands at the first hub-wide idle gap, which may be immediately or
// several runs away.
type ReloadAck struct {
	Pending  bool   `json:"pending"`
	RepoRoot string `json:"repo_root"`
	Version  string `json:"version"`
}

// handleHubReload marks a deferred restart onto the binary a repo builds. It
// trusts nothing the caller says: the repo's own config has to opt in
// (HUB_SELF_RELOAD), and the hub has to already be running from inside that
// repo, so a Homebrew hub is never restarted onto a working copy's build.
func (s *Server) handleHubReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.restart == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this hub cannot restart itself"})
		return
	}
	var req ReloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	root := strings.TrimSpace(req.RepoRoot)
	if root == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo_root is required"})
		return
	}
	repo, ok := s.findRepo(root)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}

	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		logger.Verbosef("self-reload %s: load config: %v", repo.Name, err)
		s.refuseReload(w, repo.Root, http.StatusInternalServerError,
			fmt.Sprintf("%s's own config could not be read", repo.Name))
		return
	}
	if !cfg.HubSelfReload {
		s.refuseReload(w, repo.Root, http.StatusForbidden,
			fmt.Sprintf("self-reload is off for %s; set HUB_SELF_RELOAD=1 in its .trau.ini to allow it", repo.Name))
		return
	}
	exe, err := s.executable()
	if err != nil {
		logger.Verbosef("self-reload %s: resolve hub executable: %v", repo.Name, err)
		s.refuseReload(w, repo.Root, http.StatusInternalServerError, "the hub could not resolve its own binary")
		return
	}
	if !withinRoot(repo.Root, exe) {
		logger.Verbosef("self-reload %s: hub runs from %s", repo.Name, exe)
		s.refuseReload(w, repo.Root, http.StatusConflict,
			fmt.Sprintf("hub is not running from %s's own binary", repo.Name))
		return
	}

	s.requestSelfReload(repo.Root)
	s.recordReload(repo.Root, "accepted — pending the next hub-wide idle gap")
	writeJSON(w, http.StatusAccepted, ReloadAck{Pending: true, RepoRoot: repo.Root, Version: s.version})
}

// refuseReload answers a self-reload the hub will not perform and records why
// against the repo that asked.
func (s *Server) refuseReload(w http.ResponseWriter, root string, status int, reason string) {
	s.recordReload(root, "refused — "+reason)
	writeJSON(w, status, map[string]string{"error": reason})
}

// recordReload logs and persists what became of a self-reload request, so
// "was a reload pending?" stays answerable long after the child that asked has
// exited — the drain holds its next spawn on a pending one, and a request that
// never landed leaves the same silence a hang does.
func (s *Server) recordReload(root, outcome string) {
	logger.Verbosef("self-reload %s: %s", filepath.Base(root), outcome)
	s.emitQueueEvent(root, event.KindHubReload, "self-reload "+outcome,
		map[string]any{"outcome": outcome})
}

// requestSelfReload marks a reload pending for root and starts the watcher that
// applies it. Repeat requests coalesce onto the first, so a repo asking twice
// still restarts the hub once.
func (s *Server) requestSelfReload(root string) {
	s.selfReloadMu.Lock()
	defer s.selfReloadMu.Unlock()
	if s.selfReload != "" {
		return
	}
	s.selfReload = root
	go s.awaitReloadGap(s.drainCtx)
}

// selfReloadPending returns the repo a self-reload is waiting on, empty when
// none is pending.
func (s *Server) selfReloadPending() string {
	s.selfReloadMu.Lock()
	defer s.selfReloadMu.Unlock()
	return s.selfReload
}

// awaitReloadGap restarts the hub at the first moment nothing is running
// anywhere. The drain holds its next spawn while a reload is pending, so the
// gap arrives as soon as the child in flight finishes rather than never.
func (s *Server) awaitReloadGap(ctx context.Context) {
	t := time.NewTicker(s.reloadPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.hubIdle() {
				s.triggerRestart()
				return
			}
		}
	}
}

// hubIdle reports whether the whole hub is between runs: no queue item running
// in any repo, and no live instance past idle. Idle instances are open
// dashboards rather than runs (ADR 0018), so they never hold a reload back. A
// store that cannot be read counts as busy — a reload waits rather than
// restarts on an unknown state.
func (s *Server) hubIdle() bool {
	if s.hasBusyInstance("") {
		return false
	}
	for _, root := range s.effectiveRoots() {
		items, _, err := s.stores.Queue(root).Snapshot()
		if err != nil {
			return false
		}
		if _, running := firstWithStatus(items, queue.StatusRunning); running {
			return false
		}
	}
	return true
}
