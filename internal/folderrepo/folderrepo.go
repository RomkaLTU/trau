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

// Is reports whether root is a Folder repo — not a git repository itself, but
// holding at least one directly inside it.
func Is(root string) bool {
	if root == "" || IsRepo(root) {
		return false
	}
	children, _, err := Children(root)
	return err == nil && len(children) > 0
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
