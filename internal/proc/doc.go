// Package proc holds the process-control primitives that have no portable
// spelling: starting a child detached from its parent's signal group, asking one
// to shut down gracefully, probing whether one is still alive, killing that
// child together with everything it spawned, and resolving the binary name a
// PTY spawn hands to the OS. Keeping them behind one build-tagged seam is what
// lets the rest of the tree compile for Windows unchanged (ADR 0023).
package proc
