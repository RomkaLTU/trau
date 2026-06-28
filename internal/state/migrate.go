package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// MigrateLegacyRunsDir does a one-shot, atomic move of a legacy runs dir to the new
// location so an upgrade preserves in-flight checkpoints without user action. It moves
// oldDir → newDir only when oldDir is an existing directory and newDir does not yet
// exist, so it never merges onto or clobbers live artifacts; the parent of newDir is
// created first. The move is os.Rename — atomic when both paths share a filesystem,
// which they do when one nests beside the other under the same base. Idempotent: once
// the legacy dir is gone it is a no-op, so it is safe to call on every startup.
// Returns whether it actually moved anything.
func MigrateLegacyRunsDir(oldDir, newDir string) (bool, error) {
	oldDir, newDir = strings.TrimSpace(oldDir), strings.TrimSpace(newDir)
	if oldDir == "" || newDir == "" || oldDir == newDir {
		return false, nil
	}
	if fi, err := os.Stat(oldDir); err != nil || !fi.IsDir() {
		return false, nil
	}
	if _, err := os.Stat(newDir); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if parent := filepath.Dir(newDir); parent != "." && parent != "" {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return false, err
		}
	}
	if err := os.Rename(oldDir, newDir); err != nil {
		return false, err
	}
	return true, nil
}
