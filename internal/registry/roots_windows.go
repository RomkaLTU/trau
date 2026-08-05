package registry

import (
	"path/filepath"
	"strings"
)

// canonicalRoot folds the two spellings Windows counts as one directory. Clean
// already turns the forward slashes `git rev-parse` hands back into separators,
// which leaves the drive letter: the filesystem matches it case-insensitively, so
// `c:\src\app` and `C:\src\app` are the same repo. A UNC volume is left alone —
// its server and share are part of the name a caller reads back.
func canonicalRoot(root string) string {
	cleaned := filepath.Clean(root)
	if vol := filepath.VolumeName(cleaned); len(vol) == 2 {
		return strings.ToUpper(vol) + cleaned[len(vol):]
	}
	return cleaned
}
