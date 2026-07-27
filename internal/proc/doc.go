// Package proc holds the process-control primitives that have no portable
// spelling: starting a child detached from its parent's signal group, asking one
// to shut down gracefully, probing whether one is still alive, and killing that
// child together with everything it spawned. Keeping them behind one build-tagged
// seam is what lets the rest of the tree compile for Windows unchanged (ADR 0023).
package proc
