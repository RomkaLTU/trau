package pipeline

import (
	"context"
	"syscall"
	"testing"
)

// deliverStop ends the root context without raising a signal. Windows has no
// per-process raise, and its one delivery mechanism — the console Ctrl-Break
// proc.StopGracefully sends to the group Detached made a child the leader of —
// would land on the test binary's own group and take the runner down with it.
// stop is the very cancel NotifyContext's handler goroutine calls when a signal
// does arrive, so the pipeline still observes the cancelled root context these
// tests assert on.
func deliverStop(t *testing.T, _ syscall.Signal, stop context.CancelFunc) {
	t.Helper()
	stop()
}
