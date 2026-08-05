//go:build !windows

package registry

import "path/filepath"

// canonicalRoot is Clean and nothing more: a unix filename may legally contain a
// backslash and the filesystem is case-sensitive, so any further folding would
// merge two directories that really are distinct.
func canonicalRoot(root string) string {
	return filepath.Clean(root)
}
