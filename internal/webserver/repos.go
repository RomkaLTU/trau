package webserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/RomkaLTU/trau/internal/registry"
)

// RegisterRepoRequest is the body of POST /api/v1/repos: the absolute path to a
// git repository the hub should be allowed to start loops in.
type RegisterRepoRequest struct {
	Path string `json:"path"`
}

// registerRepo makes a repo startable from the hub by persisting its root to the
// hub-owned workspace.json. It is fail-closed on exposure: on a non-loopback bind
// registration is refused outright, so a leaked bearer token can never widen the
// set of directories trau will run agents in. On loopback the caller already owns
// the machine, so registration is open.
func (s *Server) registerRepo(w http.ResponseWriter, r *http.Request) {
	if !Loopback(s.bind) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "registering a repo is refused on an exposed bind; register from a loopback trau serve on the host instead",
		})
		return
	}
	var req RegisterRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	root, err := validateRepoPath(req.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := registry.RegisterRepo(s.home, root); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to register repo: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, RepoView{Repo: workspaceRepo(root), Allowed: true})
}

// validateRepoPath normalizes a registration path and rejects anything that is
// not an existing directory at a git toplevel. The path must be absolute so the
// hub records an unambiguous root; a `.git` entry — directory or file — proves a
// toplevel while covering worktrees whose `.git` is a file.
func validateRepoPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("path %q must be absolute", path)
	}
	root := filepath.Clean(trimmed)
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("path %q does not exist", root)
		}
		return "", fmt.Errorf("cannot access path %q: %v", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path %q is not a directory", root)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return "", fmt.Errorf("path %q is not a git repository (no .git found)", root)
	}
	return root, nil
}

// effectiveRoots is the per-request allowlist: the static SERVE_WORKSPACE seed
// merged with the repos registered from the web. Reading it fresh on every call
// is what lets a registration take effect without restarting serve.
func (s *Server) effectiveRoots() []string {
	registered := registry.RegisteredRepos(s.home)
	if len(registered) == 0 {
		return s.workspace
	}
	merged := make([]string, 0, len(s.workspace)+len(registered))
	merged = append(merged, s.workspace...)
	merged = append(merged, registered...)
	return normalizeRoots(merged)
}
