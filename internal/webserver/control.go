package webserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/RomkaLTU/trau/internal/registry"
)

// StartRequest is the body of POST /api/v1/instances: the repo to start a loop
// in, named either by its allowlisted root or by its base name.
type StartRequest struct {
	Repo string `json:"repo"`
}

// StartResult is returned when the hub spawns a loop, carrying the child's PID so
// the caller can correlate it with the instance that self-registers moments later.
type StartResult struct {
	PID      int    `json:"pid"`
	Repo     string `json:"repo"`
	RepoRoot string `json:"repo_root"`
}

// startInstance spawns a headless loop in an allowlisted repo. Repos outside the
// workspace allowlist are observe-only and refused with a clear error, so the
// write path can never launch a loop somewhere the operator hasn't sanctioned.
func (s *Server) startInstance(w http.ResponseWriter, r *http.Request) {
	var req StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(req.Repo) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo is required"})
		return
	}
	root, ok := s.allowedRoot(req.Repo)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("repo %q is not on the serve workspace allowlist and is observe-only; add its root to SERVE_WORKSPACE to start loops there", req.Repo),
		})
		return
	}

	pid, err := s.sup.Spawn(SpawnSpec{
		Dir:  root,
		Args: []string{"--repo", root, "--no-tui"},
		Env:  childEnv(s.home),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to start loop: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, StartResult{PID: pid, Repo: filepath.Base(root), RepoRoot: root})
}

// handleStopInstance sends SIGTERM to a registered loop, hub-started or not, so a
// web stop flows through the same graceful shutdown as Ctrl-C and in-flight work
// checkpoints identically. Only a currently-registered PID can be stopped, which
// keeps the endpoint from being a general process killer.
func (s *Server) handleStopInstance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil || pid <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid pid"})
		return
	}
	if !s.registered(pid) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no live instance with that pid"})
		return
	}
	if err := s.sup.Signal(pid, syscall.SIGTERM); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to signal loop: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "stopping", "signal": "SIGTERM"})
}

func (s *Server) registered(pid int) bool {
	for _, e := range registry.Live(s.home) {
		if e.PID == pid {
			return true
		}
	}
	return false
}

// allows reports whether the hub may start a loop in root.
func (s *Server) allows(root string) bool {
	target := filepath.Clean(root)
	for _, r := range s.workspace {
		if r == target {
			return true
		}
	}
	return false
}

// allowedRoot resolves a start request's repo identifier to an allowlisted root.
// It matches an allowlisted root path exactly, or an unambiguous base name, so
// the UI can start a loop by either the path it shows or the short repo name.
func (s *Server) allowedRoot(ident string) (string, bool) {
	ident = strings.TrimSpace(ident)
	cleaned := filepath.Clean(ident)
	for _, r := range s.workspace {
		if r == cleaned {
			return r, true
		}
	}
	var match string
	for _, r := range s.workspace {
		if filepath.Base(r) == ident {
			if match != "" {
				return "", false
			}
			match = r
		}
	}
	return match, match != ""
}

// normalizeRoots cleans and de-duplicates the configured workspace roots while
// preserving order, so allowlist comparisons are path-stable.
func normalizeRoots(roots []string) []string {
	if len(roots) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(roots))
	out := make([]string, 0, len(roots))
	for _, raw := range roots {
		root := strings.TrimSpace(raw)
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, root)
	}
	return out
}

// workspaceRepo synthesizes a RepoView entry for an allowlisted repo the hub has
// never seen run, so it is startable before its first loop registers.
func workspaceRepo(root string) registry.Repo {
	return registry.Repo{
		Name:    filepath.Base(root),
		Root:    root,
		RunsDir: filepath.Join(root, ".trau", "runs"),
	}
}

// childEnv is the environment a spawned loop inherits, pinned to the hub's trau
// home so the child registers into the same registry the hub reads.
func childEnv(home string) []string {
	env := os.Environ()
	if home == "" {
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, "TRAU_HOME=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "TRAU_HOME="+home)
}
