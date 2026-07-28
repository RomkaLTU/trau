//go:build !windows

package webserver

// volumeRoots is empty off Windows: one filesystem root, reachable by walking up
// from the home directory the roots listing already carries.
func volumeRoots() []string { return nil }
