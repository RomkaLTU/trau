package webserver

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/tracker"
)

// syncBackoffCap bounds how long a repo with a failing tracker waits between
// retries, so it recovers within a bounded window once its config is fixed while
// never hot-looping the tracker API in the meantime.
const syncBackoffCap = 30 * time.Minute

// syncOnceTimeout bounds a single repo's pull so a hung tracker call cannot pin a
// sync goroutine for the life of the hub.
const syncOnceTimeout = 2 * time.Minute

// repoSync is one repo's background-sync state: whether a pull is in flight right
// now, how many times in a row it has failed, and the earliest time it is due for
// another attempt (pushed into the future while backing off).
type repoSync struct {
	syncing     bool
	failures    int
	nextAttempt time.Time
}

// syncer refreshes each allowlisted repo's issue store from its tracker on an
// interval, off every request path. Each repo syncs independently: a broken
// tracker config backs that repo off on its own without stalling the others, and
// the currently-syncing flag it exposes feeds the repos API's freshness.
type syncer struct {
	srv *Server

	mu    sync.Mutex
	state map[string]*repoSync
}

func newSyncer(s *Server) *syncer {
	return &syncer{srv: s, state: map[string]*repoSync{}}
}

// run refreshes every repo on interval for the life of ctx: an immediate pass
// seeds freshness on startup, then a tick fires the due repos. A non-positive
// interval disables the loop, leaving the store to on-demand pulls only.
func (sy *syncer) run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	sy.tick(ctx, interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sy.tick(ctx, interval)
		}
	}
}

// tick fires a background sync for every allowlisted repo that is due and not
// already syncing, each in its own goroutine so one slow tracker never blocks the
// rest.
func (sy *syncer) tick(ctx context.Context, interval time.Duration) {
	now := time.Now()
	for _, root := range sy.srv.effectiveRoots() {
		if !sy.claim(root, now) {
			continue
		}
		go sy.syncOne(ctx, interval, root)
	}
}

func (sy *syncer) syncOne(ctx context.Context, interval time.Duration, root string) {
	ctx, cancel := context.WithTimeout(ctx, syncOnceTimeout)
	defer cancel()
	_, err := sy.srv.syncRepo(ctx, workspaceRepo(root))
	sy.settle(root, interval, err)
}

// claim marks a repo as syncing if it is due and idle, reporting whether the
// caller now owns its in-flight sync. It is the guard that stops a slow pull from
// overlapping the next tick's pull of the same repo.
func (sy *syncer) claim(root string, now time.Time) bool {
	sy.mu.Lock()
	defer sy.mu.Unlock()
	st := sy.state[root]
	if st == nil {
		st = &repoSync{}
		sy.state[root] = st
	}
	if st.syncing || now.Before(st.nextAttempt) {
		return false
	}
	st.syncing = true
	return true
}

// settle records a finished sync: success clears the backoff and leaves the repo
// due next tick; a tracker failure backs it off exponentially. A repo with no
// direct credentials is not a failure — it simply has nothing to pull — so it
// backs off to the cap and checks in rarely rather than every interval.
func (sy *syncer) settle(root string, interval time.Duration, err error) {
	sy.mu.Lock()
	defer sy.mu.Unlock()
	st := sy.state[root]
	if st == nil {
		return
	}
	st.syncing = false
	switch {
	case err == nil:
		st.failures = 0
		st.nextAttempt = time.Time{}
	case errors.Is(err, tracker.ErrReaderUnavailable):
		st.failures = 0
		st.nextAttempt = time.Now().Add(syncBackoffCap)
	default:
		st.failures++
		st.nextAttempt = time.Now().Add(syncBackoff(interval, st.failures))
	}
}

// syncing reports whether a background pull is in flight for root right now, the
// currently-syncing signal the repos API surfaces.
func (sy *syncer) syncing(root string) bool {
	sy.mu.Lock()
	defer sy.mu.Unlock()
	st := sy.state[root]
	return st != nil && st.syncing
}

// syncBackoff spaces out a failing repo's retries: the interval doubled once per
// consecutive failure, capped so a persistently broken tracker settles at a slow
// heartbeat that still recovers on its own once fixed.
func syncBackoff(interval time.Duration, failures int) time.Duration {
	shift := failures
	if shift > 8 {
		shift = 8
	}
	d := interval << shift
	if d <= 0 || d > syncBackoffCap {
		return syncBackoffCap
	}
	return d
}
