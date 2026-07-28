package webserver

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// volumeRoots is every mounted drive letter, as `C:\`. The bit mask names the
// drives that are actually mounted, so nothing probes an empty A: and stalls.
func volumeRoots() []string {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	roots := []string{}
	for i := range 26 {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		roots = append(roots, string(rune('A'+i))+":"+string(filepath.Separator))
	}
	return roots
}
