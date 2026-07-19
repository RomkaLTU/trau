package update

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/logger"
)

const (
	applyIdle    = "idle"
	applyRunning = "running"
	applyFailed  = "failed"

	applyTimeout   = 5 * time.Minute
	applyTailLines = 20
)

var (
	// ErrNotBrew rejects an apply on an install Homebrew does not own. trau
	// never replaces its own binary: whoever installed it owns updating it.
	ErrNotBrew = errors.New("not a homebrew install")

	// ErrApplyInFlight rejects a second apply while one is still running.
	ErrApplyInFlight = errors.New("an update is already being applied")
)

// ApplyState is how the one-click update is going, carried on the /update
// payload so a client polls one endpoint for the whole flow. Message holds the
// tail of the brew output, and only when the upgrade failed.
type ApplyState struct {
	State   string `json:"state"`
	Message string `json:"message"`
}

// Apply upgrades a Homebrew-managed install and restarts the hub onto the
// result. It returns as soon as the upgrade is under way — brew takes minutes —
// and the outcome reads back from Status. restart is the same unconditional
// server-side restart POST /hub/restart triggers, reporting whether this hub can
// restart itself at all; warning about active runs is the client's job, done
// before this is ever reached.
func (c *Checker) Apply(restart func() bool) error {
	if _, method := c.local(); method != installBrew {
		return ErrNotBrew
	}

	c.mu.Lock()
	if c.applyState == applyRunning {
		c.mu.Unlock()
		return ErrApplyInFlight
	}
	c.applyState, c.applyMessage = applyRunning, ""
	c.mu.Unlock()

	go c.apply(restart)
	return nil
}

func (c *Checker) apply(restart func() bool) {
	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	out, err := c.upgrade(ctx)
	logger.Printf("update apply: brew upgrade --cask trau:\n%s", out)
	if err != nil {
		message := tail(string(out), applyTailLines)
		if message == "" {
			message = err.Error()
		}
		c.setApplyState(applyFailed, message)
		return
	}

	// A brew that reports nothing to do still restarts when drift was already
	// pending: the user asked to get current, not to run an upgrade. A hub that
	// cannot restart itself settles anyway and leaves the drift pending.
	if c.driftPending() && restart() {
		return
	}

	c.setApplyState(applyIdle, "")
	if err := c.CheckNow(ctx); err != nil {
		logger.Verbosef("update apply: %v", err)
	}
}

// driftPending re-probes the binary the upgrade may have replaced, dropping the
// cached answer the probe would otherwise still be serving.
func (c *Checker) driftPending() bool {
	c.mu.Lock()
	c.probedAt = time.Time{}
	c.mu.Unlock()
	return c.Status().RestartPending
}

func (c *Checker) setApplyState(state, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.applyState, c.applyMessage = state, message
}

// brewUpgrade resolves brew explicitly: the hub runs detached with whatever PATH
// its spawner had, so a missing brew is a plain error rather than a lost exec.
func brewUpgrade(ctx context.Context) ([]byte, error) {
	brew, err := exec.LookPath("brew")
	if err != nil {
		return nil, fmt.Errorf("brew not found on PATH: %w", err)
	}
	return exec.CommandContext(ctx, brew, "upgrade", "--cask", "trau").CombinedOutput()
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
