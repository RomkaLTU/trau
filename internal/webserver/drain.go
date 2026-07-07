package webserver

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// drainPoll is how often a draining repo re-evaluates: long enough not to spin,
// short enough that the next queued item starts promptly after the current
// child exits.
const drainPoll = 2 * time.Second

// drainAction is what one tick of a repo's drain loop decided to do, reported so
// the loop can pace itself and tests can assert progress.
type drainAction string

const (
	drainSpawn     drainAction = "spawn"     // a pending item's child was launched
	drainReconcile drainAction = "reconcile" // a finished child settled its item
	drainWait      drainAction = "wait"      // a child or another run is in flight
	drainStop      drainAction = "stop"      // draining is off; the loop exits
)

// drainer executes each Repo's queue one child run at a time. It owns no queue
// state of its own — every decision reads the persisted queue file — so a hub
// restart resumes exactly where the last one left off. One goroutine per repo
// keeps draining strictly sequential: a repo never has two hub-spawned children
// at once.
type drainer struct {
	srv      *Server
	poll     time.Duration
	alive    func(pid int) bool
	repoLive func(root string) bool
	outcome  func(root string, it queue.Item) string

	mu     sync.Mutex
	active map[string]context.CancelFunc
}

func newDrainer(s *Server) *drainer {
	d := &drainer{
		srv:    s,
		poll:   drainPoll,
		alive:  registry.Alive,
		active: map[string]context.CancelFunc{},
	}
	d.repoLive = d.repoHasLiveInstance
	d.outcome = d.checkpointOutcome
	return d
}

// ensure starts a drain loop for root unless one is already running, so a repeat
// start or a resume never spawns a second loop for the same repo.
func (d *drainer) ensure(ctx context.Context, root string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.active[root]; ok {
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	d.active[root] = cancel
	go d.run(loopCtx, root)
}

func (d *drainer) run(ctx context.Context, root string) {
	defer func() {
		d.mu.Lock()
		delete(d.active, root)
		d.mu.Unlock()
	}()
	for {
		act, _ := d.tick(root)
		if act == drainStop {
			return
		}
		if act == drainReconcile {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.poll):
		}
	}
}

// tick advances a repo's queue by one decision: it launches the next pending
// item, settles a finished one, or waits, never spawning a second child while
// one is in flight. It is the whole drain policy, pure enough to table-test.
func (d *drainer) tick(root string) (drainAction, error) {
	store := queue.NewStore(root)
	items, draining, err := store.Snapshot()
	if err != nil {
		return drainWait, err
	}
	if running, ok := firstWithStatus(items, queue.StatusRunning); ok {
		if d.alive(running.PID) {
			return drainWait, nil
		}
		if err := store.Finish(running.ID, d.outcome(root, running)); err != nil {
			return drainWait, err
		}
		return drainReconcile, nil
	}
	if !draining {
		return drainStop, nil
	}
	if d.repoLive(root) {
		return drainWait, nil
	}
	pending, ok := firstWithStatus(items, queue.StatusPending)
	if !ok {
		return drainWait, nil
	}
	pid, err := d.srv.sup.Spawn(d.spec(root, pending))
	if err != nil {
		return drainWait, err
	}
	if err := store.MarkRunning(pending.ID, pid); err != nil {
		return drainWait, err
	}
	return drainSpawn, nil
}

// spec is the launch a queued item spawns: a ticket runs as the existing
// run-once, an epic as the existing epic flow, matching the /instances start
// paths byte for byte so a queued run is indistinguishable from a manual one.
func (d *drainer) spec(root string, it queue.Item) SpawnSpec {
	args := []string{"--repo", root, "--no-tui"}
	if it.Kind == queue.KindEpic {
		args = append(args, "--parent", it.ID)
	} else {
		args = append(args, "--parent", it.ID, "--once")
	}
	return SpawnSpec{Dir: root, Args: args, Env: childEnv(d.srv.home)}
}

// checkpointOutcome reads the finished child's checkpoint to settle its item.
// The nuanced fault/pause taxonomy is a later slice; here a quarantined ticket
// is the only failure, and anything else counts as done so the queue moves on.
func (d *drainer) checkpointOutcome(root string, it queue.Item) string {
	store := state.NewStore(filepath.Join(root, ".trau", "runs"))
	if store.Get(it.ID, "PHASE") == state.Quarantined {
		return queue.StatusFailed
	}
	return queue.StatusDone
}

// repoHasLiveInstance reports whether any loop — a manual loop or a Run once —
// is already live in root, so the drainer waits for it instead of spawning a
// second child in the same repo.
func (d *drainer) repoHasLiveInstance(root string) bool {
	for _, e := range registry.Live(d.srv.home) {
		if e.RepoRoot == root {
			return true
		}
	}
	return false
}

func firstWithStatus(items []queue.Item, status string) (queue.Item, bool) {
	for _, it := range items {
		if it.Status == status {
			return it, true
		}
	}
	return queue.Item{}, false
}
