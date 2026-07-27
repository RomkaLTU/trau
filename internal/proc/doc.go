// Package proc holds the process-control primitives that have no portable
// spelling: starting a child detached from its parent's signal group, and
// killing that child together with everything it spawned. Keeping them behind
// one build-tagged seam is what lets the rest of the tree compile for Windows
// unchanged (ADR 0023).
package proc
