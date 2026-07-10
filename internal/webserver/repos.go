package webserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// RegisterRepoRequest is the body of POST /api/v1/repos: the absolute path to a
// git repository the hub should be allowed to start loops in.
type RegisterRepoRequest struct {
	Path string `json:"path"`
}

// denyRegistrationIfExposed enforces the exposure gate shared by register and
// unregister: loopback binds are always open, but on a non-loopback bind the
// bearer token alone is not enough — SERVE_ALLOW_REGISTER must be set to change
// the startable-repo set. It reports whether it wrote a 403 refusal that names
// the key, so callers return early on true.
func (s *Server) denyRegistrationIfExposed(w http.ResponseWriter, action string) bool {
	if Loopback(s.bind) || s.allowRegister {
		return false
	}
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error": fmt.Sprintf("%s on an exposed bind requires SERVE_ALLOW_REGISTER=1 in addition to SERVE_TOKEN; set it to open registration deliberately, or register from a loopback trau serve on the host", action),
	})
	return true
}

// registerRepo makes a repo startable from the hub by persisting its root to the
// hub-owned registration store. It is fail-closed on exposure: on a non-loopback
// bind registration is refused unless SERVE_ALLOW_REGISTER is set, so a leaked
// bearer token can never widen the set of directories trau will run agents in by
// default. On loopback the caller already owns the machine, so registration is
// open.
func (s *Server) registerRepo(w http.ResponseWriter, r *http.Request) {
	if s.denyRegistrationIfExposed(w, "registering a repo") {
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
	if err := s.repos.Register(root); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to register repo: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, RepoView{Repo: workspaceRepo(root), Allowed: true, Registered: true})
}

func (s *Server) handleRepo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	s.unregisterRepo(w, r)
}

// unregisterRepo reverses a web registration, dropping the repo back to
// observe-only. Only the hub-owned registered set is touched: the repo's runs,
// events, and transcripts stay browsable exactly as they do after any loop
// exits, and nothing on disk in the repo is removed. A repo granted by the
// static SERVE_WORKSPACE seed is config-owned, not registry-owned, so the
// attempt is refused rather than silently doing nothing. It follows the same
// exposure gate as registration: refused on a non-loopback bind unless
// SERVE_ALLOW_REGISTER is set.
func (s *Server) unregisterRepo(w http.ResponseWriter, r *http.Request) {
	if s.denyRegistrationIfExposed(w, "unregistering a repo") {
		return
	}
	name := r.PathValue("repo")
	if _, ok := matchRoot(s.workspace, name); ok {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("repo %q is granted by the SERVE_WORKSPACE config and cannot be unregistered over the API; remove its root from SERVE_WORKSPACE instead", name),
		})
		return
	}
	registered, _ := s.repos.Registered()
	root, ok := matchRoot(registered, name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("repo %q is not registered", name)})
		return
	}
	removed, err := s.repos.Unregister(root)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to unregister repo: " + err.Error()})
		return
	}
	if !removed {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("repo %q is not registered", name)})
		return
	}
	writeJSON(w, http.StatusOK, RepoView{Repo: workspaceRepo(root), Allowed: false})
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
	registered, _ := s.repos.Registered()
	if len(registered) == 0 {
		return s.workspace
	}
	merged := make([]string, 0, len(s.workspace)+len(registered))
	merged = append(merged, s.workspace...)
	merged = append(merged, registered...)
	return normalizeRoots(merged)
}
