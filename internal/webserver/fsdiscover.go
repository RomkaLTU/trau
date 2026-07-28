package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Bounds on the child-repo scan, so a picked volume root costs a listing rather
// than a full-disk walk.
const (
	discoverMaxDepth = 2
	discoverMaxDirs  = 2000
	discoverMaxRepos = 64
)

// FSDiscoverResponse is what the picker can offer for one picked folder: the
// folder itself when it is a git toplevel, otherwise the repositories found
// beneath it. Neither — not a repo, nothing below — is the git-init case.
type FSDiscoverResponse struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	IsRepo   bool      `json:"is_repo"`
	Children []FSEntry `json:"children"`
}

// GitInitRequest is the body of POST /api/v1/fs/init: the folder to turn into a
// repository.
type GitInitRequest struct {
	Path string `json:"path"`
}

// GitInitResponse is the 201 body of POST /api/v1/fs/init: the initialized root
// and the branch its first commit created, which is the repo's base branch.
type GitInitResponse struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

// handleFSDiscover reports whether a picked folder is a repository and, when it
// is not, which repositories sit beneath it — the two answers that let the picker
// take a parent folder instead of refusing it. It reads the host's tree exactly
// as the browse listing does, so it takes the same exposure gate.
func (s *Server) handleFSDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.denyRegistrationIfExposed(w, "scanning a folder for repositories") {
		return
	}
	resp, err := discoverRepos(strings.TrimSpace(r.URL.Query().Get("path")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleFSInit turns a folder with no git anywhere into a repository so
// onboarding can continue with it instead of dead-ending. A folder that already
// holds repositories is refused: a repo nested above others is never what the
// picker meant to offer. It writes to the host, so it takes the registration
// exposure gate.
func (s *Server) handleFSInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.denyRegistrationIfExposed(w, "initializing a git repository") {
		return
	}
	var req GitInitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	found, err := discoverRepos(req.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if found.IsRepo {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("path %q is already a git repository", found.Path),
		})
		return
	}
	if len(found.Children) > 0 {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("path %q holds %d git repositories; add those instead of nesting a repository above them", found.Path, len(found.Children)),
		})
		return
	}
	branch, err := initRepo(r.Context(), found.Path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, GitInitResponse{Path: found.Path, Branch: branch})
}

// discoverRepos classifies a picked folder: a git toplevel is offered as itself,
// and anything else is scanned for the repositories beneath it.
func discoverRepos(path string) (FSDiscoverResponse, error) {
	root, err := absDir(path)
	if err != nil {
		return FSDiscoverResponse{}, err
	}
	resp := FSDiscoverResponse{
		Path:     root,
		Name:     filepath.Base(root),
		IsRepo:   isGitToplevel(root),
		Children: []FSEntry{},
	}
	if !resp.IsRepo {
		resp.Children = childRepos(root)
	}
	return resp, nil
}

// childRepos finds the repositories nested under root, breadth-first and bounded.
// A repository is never descended into — repos join a project as siblings, never
// nested — and symlinked children are left alone, which keeps the walk inside the
// picked tree and free of cycles.
func childRepos(root string) []FSEntry {
	found := []FSEntry{}
	frontier := []string{root}
	budget := discoverMaxDirs
	for depth := 0; depth < discoverMaxDepth && len(frontier) > 0; depth++ {
		next := []string{}
		for _, dir := range frontier {
			if budget == 0 {
				return found
			}
			budget--
			dirents, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, dirent := range dirents {
				if !dirent.IsDir() || strings.HasPrefix(dirent.Name(), ".") {
					continue
				}
				child := filepath.Join(dir, dirent.Name())
				if !isGitToplevel(child) {
					next = append(next, child)
					continue
				}
				if len(found) == discoverMaxRepos {
					return found
				}
				found = append(found, FSEntry{
					Name:   strings.TrimPrefix(child, root+string(filepath.Separator)),
					Path:   child,
					IsRepo: true,
				})
			}
		}
		frontier = next
	}
	return found
}

// initRepo turns a git-free folder into a repository the loop can work in: `git
// init` plus an empty first commit, without which there is no base branch to cut
// work from. A failed commit — an unset git identity is the usual cause — takes
// the fresh `.git` back out, so a refused init leaves the folder as it was.
func initRepo(ctx context.Context, root string) (string, error) {
	if err := runGit(ctx, root, "init"); err != nil {
		return "", err
	}
	if err := runGit(ctx, root, "commit", "--allow-empty", "-m", "Initial commit"); err != nil {
		_ = os.RemoveAll(filepath.Join(root, ".git"))
		return "", err
	}
	return gitOutput(ctx, root, "rev-parse", "--abbrev-ref", "HEAD"), nil
}

func runGit(ctx context.Context, root string, args ...string) error {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err == nil {
		return nil
	}
	if detail := strings.TrimSpace(string(out)); detail != "" {
		return fmt.Errorf("git %s: %s", args[0], detail)
	}
	return fmt.Errorf("git %s: %v", args[0], err)
}
