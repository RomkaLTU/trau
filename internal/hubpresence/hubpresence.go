// Package hubpresence is the loop's presence heartbeat to the trau serve hub: it
// registers the running loop over HTTP on start, refreshes its reported session
// state on every change and on a timer, and deregisters on clean exit (ADR 0005,
// ADR 0008 §7). The hub holds presence and reaps a dead PID via signal 0.
// Presence is best-effort by design — a hub that never answers only leaves the
// loop unlisted; it must never block or fail the loop, so every network write
// happens off the caller's goroutine and its error is ignored.
package hubpresence

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/registry"
)

const (
	heartbeatInterval = 30 * time.Second
	writeTimeout      = 10 * time.Second
)

// client is the slice of hubclient the heartbeat needs, narrowed so a test can
// substitute a double.
type client interface {
	PutInstance(ctx context.Context, pid int, hb hubclient.InstanceHeartbeat) error
	DeleteInstance(ctx context.Context, pid int) error
}

// Handle is a live registration. Its methods are safe on a nil Handle, so a
// caller can register unconditionally and defer the cleanup without checking
// whether a hub was reachable.
type Handle struct {
	client client
	pid    int

	mu    sync.Mutex
	entry hubclient.InstanceHeartbeat

	dirty chan struct{}
	stop  chan struct{}
	once  sync.Once
}

// Register reports the calling loop's presence to the hub through c and heartbeats
// it until Deregister. A relative runsDir is resolved against repoRoot so the hub,
// running elsewhere, resolves the same absolute path. The initial write and every
// later one run on a background goroutine, so Register never blocks the loop.
func Register(c client, repoRoot, runsDir string) *Handle {
	if c == nil {
		return &Handle{}
	}
	if runsDir != "" && !filepath.IsAbs(runsDir) && repoRoot != "" {
		runsDir = filepath.Join(repoRoot, runsDir)
	}
	now := time.Now()
	h := &Handle{
		client: c,
		pid:    os.Getpid(),
		entry: hubclient.InstanceHeartbeat{
			RepoRoot:     repoRoot,
			RunsDir:      runsDir,
			StartedAt:    now,
			SessionState: registry.StateIdle,
			StateSince:   now,
		},
		dirty: make(chan struct{}, 1),
		stop:  make(chan struct{}),
	}
	go h.beat()
	return h
}

// SetState records what the session is doing and flushes it to the hub without
// waiting for the next heartbeat. StateSince advances whenever the reported
// activity changes — the state, ticket, or phase — so it reads as "in this state
// since". Best-effort and safe on a nil or never-registered Handle.
func (h *Handle) SetState(state, ticket, phase string) {
	if h == nil || h.client == nil {
		return
	}
	h.mu.Lock()
	if h.entry.SessionState != state || h.entry.Ticket != ticket || h.entry.Phase != phase {
		h.entry.StateSince = time.Now()
	}
	h.entry.SessionState = state
	h.entry.Ticket = ticket
	h.entry.Phase = phase
	h.mu.Unlock()
	h.nudge()
}

// Deregister stops the heartbeat and drops the loop's presence. Best-effort and
// idempotent; if the drop never lands, the exiting process's dead PID is the
// backstop the hub reaps via signal 0.
func (h *Handle) Deregister() {
	if h == nil || h.client == nil {
		return
	}
	h.once.Do(func() {
		close(h.stop)
		ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		defer cancel()
		_ = h.client.DeleteInstance(ctx, h.pid)
	})
}

func (h *Handle) beat() {
	h.flush()
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-h.dirty:
			h.flush()
		case <-t.C:
			h.flush()
		}
	}
}

func (h *Handle) flush() {
	h.mu.Lock()
	entry := h.entry
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	_ = h.client.PutInstance(ctx, h.pid, entry)
}

func (h *Handle) nudge() {
	select {
	case h.dirty <- struct{}{}:
	default:
	}
}
