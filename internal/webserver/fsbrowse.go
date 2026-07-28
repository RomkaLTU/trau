package webserver

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// FSEntry is one directory in a browse listing. IsRepo is the same `.git` proof
// registration accepts, so a marked row is one the wizard will take.
type FSEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	IsRepo bool   `json:"is_repo"`
}

// FSBrowseResponse is one directory's immediate subdirectories plus where the
// picker can go from here. Path and Parent are empty on the roots listing, and
// Parent is empty again at a volume root, where up means back to that listing.
// Separator lets the browser build breadcrumbs without guessing at one.
type FSBrowseResponse struct {
	Path      string    `json:"path"`
	Parent    string    `json:"parent"`
	Separator string    `json:"separator"`
	IsRepo    bool      `json:"is_repo"`
	Entries   []FSEntry `json:"entries"`
}

// handleFSBrowse lists a directory for the onboarding folder picker, which the
// browser cannot open natively onto the hub machine. Reading the host's tree is
// at least as sensitive as registering a path out of it, so it takes the same
// exposure gate as registration.
func (s *Server) handleFSBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.denyRegistrationIfExposed(w, "browsing the filesystem") {
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		resp, err := browseRoots()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp, err := browseDir(path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// browseRoots is the picker's opening listing: the home directory plus every
// mounted volume root. Entries carry their full path as the display name, having
// no parent listing to read a bare base name against.
func browseRoots() (FSBrowseResponse, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return FSBrowseResponse{}, fmt.Errorf("cannot locate the home directory: %v", err)
	}
	home = filepath.Clean(home)
	entries := []FSEntry{{Name: home, Path: home, IsRepo: isGitToplevel(home)}}
	for _, root := range volumeRoots() {
		entries = append(entries, FSEntry{Name: root, Path: root, IsRepo: isGitToplevel(root)})
	}
	return FSBrowseResponse{Separator: string(filepath.Separator), Entries: entries}, nil
}

// browseDir lists the directories directly under path.
func browseDir(path string) (FSBrowseResponse, error) {
	root, err := absDir(path)
	if err != nil {
		return FSBrowseResponse{}, err
	}
	dirents, err := os.ReadDir(root)
	if err != nil {
		return FSBrowseResponse{}, fmt.Errorf("cannot read %q: %v", root, err)
	}
	entries := make([]FSEntry, 0, len(dirents))
	for _, dirent := range dirents {
		child := filepath.Join(root, dirent.Name())
		if !isBrowsableDir(dirent, child) {
			continue
		}
		entries = append(entries, FSEntry{Name: dirent.Name(), Path: child, IsRepo: isGitToplevel(child)})
	}
	return FSBrowseResponse{
		Path:      root,
		Parent:    parentDir(root),
		Separator: string(filepath.Separator),
		IsRepo:    isGitToplevel(root),
		Entries:   entries,
	}, nil
}

// isBrowsableDir resolves symlinks so a linked projects folder stays navigable.
func isBrowsableDir(dirent os.DirEntry, path string) bool {
	if dirent.Type()&os.ModeSymlink == 0 {
		return dirent.IsDir()
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// absDir cleans path and proves it names a directory the hub can open, the check
// every filesystem endpoint runs before it reads or writes anything.
func absDir(path string) (string, error) {
	root := filepath.Clean(path)
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("path %q must be absolute", path)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("cannot open %q: %v", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path %q is not a directory", root)
	}
	return root, nil
}

func parentDir(root string) string {
	parent := filepath.Dir(root)
	if parent == root {
		return ""
	}
	return parent
}

// isGitToplevel reports whether dir holds a `.git` entry — a directory for a
// normal clone, a file for a worktree.
func isGitToplevel(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
