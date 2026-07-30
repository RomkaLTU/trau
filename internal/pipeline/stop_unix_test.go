//go:build !windows

package pipeline

import (
	"context"
	"os"
	"syscall"
	"testing"
)

// deliverStop raises sig at this process, so signalledContext drives the loop's
// root context through the real signal.NotifyContext delivery main installs.
func deliverStop(t *testing.T, sig syscall.Signal, _ context.CancelFunc) {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Fatalf("raise %v: %v", sig, err)
	}
}
