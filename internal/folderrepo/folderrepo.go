// Package folderrepo discovers the Child repos of a Folder repo: a registered
// folder that is not itself a git repository, whose direct git children are the
// repositories a run may ship to. Children are never registered — they are found
// by this scan at run time and used only as ship targets.
package folderrepo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MaxChildren bounds the scan. The folders this exists for hold around fifty
// repositories, so the cap guards against a picked volume root rather than
// standing in the way of a real Folder repo.
const MaxChildren = 256

// Child is a git repository directly inside a Folder repo.
type Child struct {
	Name string
	Path string
}

// IsRepo reports whether dir is a git toplevel: a `.git` directory for a normal
// clone, a `.git` file for a worktree.
func IsRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// Census is what a scan of a candidate Folder repo found: its Child repos, whether
// the scan stopped at MaxChildren, and whether root is a Folder repo at all.
type Census struct {
	Children  []Child
	Truncated bool
	IsFolder  bool
}

// Scan takes the census of root in a single directory read. A root that is itself
// a git repository, that cannot be read, or that holds no git repositories is not
// a Folder repo, and a caller reads that off IsFolder rather than an error.
func Scan(root string) Census {
	if root == "" || IsRepo(root) {
		return Census{}
	}
	children, truncated, err := Children(root)
	if err != nil || len(children) == 0 {
		return Census{}
	}
	return Census{Children: children, Truncated: truncated, IsFolder: true}
}

// Is reports whether root is a Folder repo — not a git repository itself, but
// holding at least one directly inside it.
func Is(root string) bool { return Scan(root).IsFolder }

// Nearest walks up from dir to the first Folder repo, or "" when no ancestor
// holds git repositories. It is how a working directory outside any git
// repository still resolves to the folder it is standing in.
func Nearest(dir string) string {
	for dir != "" {
		if Is(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// Collides reports whether runs in the two roots would share a working tree: one
// of them is a Folder repo and the other a repository sitting inside it. Paths are
// compared cleaned — git answers with forward slashes on Windows while --repo is
// taken verbatim — and one root against itself is the same repo, not a collision.
func Collides(root, other string) bool {
	if root == "" || other == "" {
		return false
	}
	root, other = filepath.Clean(root), filepath.Clean(other)
	if root == other {
		return false
	}
	return (contains(root, other) && Is(root)) || (contains(other, root) && Is(other))
}

// CollisionReason states what a colliding run is waiting on: the ticket already
// running in the repo it would share a working tree with, and why sharing one is a
// refusal. The CLI and the hub both refuse with it, so an operator meets the same
// sentence wherever the collision surfaces.
func CollisionReason(ticket, root string) string {
	running := "a loop"
	if ticket != "" {
		running = ticket
	}
	return running + " is already running in " + root + " — a folder repo and a repo inside it share a working tree, so one of them has to finish first"
}

// contains reports whether path sits inside root.
func contains(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Children lists the git repositories directly inside root, in name order. Only
// the top level is read: a Folder repo's children are its immediate
// subdirectories, and a child is never descended into. truncated reports that
// root holds more than MaxChildren of them, so a caller says so rather than
// quietly working on a prefix.
func Children(root string) (children []Child, truncated bool, err error) {
	dirents, err := os.ReadDir(root)
	if err != nil {
		return nil, false, fmt.Errorf("scan folder repo %s: %w", root, err)
	}
	children = []Child{}
	for _, dirent := range dirents {
		if !dirent.IsDir() || strings.HasPrefix(dirent.Name(), ".") {
			continue
		}
		path := filepath.Join(root, dirent.Name())
		if !IsRepo(path) {
			continue
		}
		if len(children) == MaxChildren {
			return children, true, nil
		}
		children = append(children, Child{Name: dirent.Name(), Path: path})
	}
	return children, false, nil
}
