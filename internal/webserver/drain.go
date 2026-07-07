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
	outcome  func(root string, it queue.Item) (class, reason string)

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

// tick advances a repo's queue by one decision: it launches the next runnable
// item, settles a finished one per the failure taxonomy — pausing the drain on a
// fault or provider pause — or waits, never spawning a second child while one is
// in flight. It is the whole drain policy, pure enough to table-test.
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
		class, reason := d.outcome(root, running)
		if status, pause := classifyDrainOutcome(class); pause {
			if err := store.Pause(running.ID, reason); err != nil {
				return drainWait, err
			}
		} else if err := store.Finish(running.ID, status, reason); err != nil {
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
	next, ok := firstRunnable(items)
	if !ok {
		return drainWait, nil
	}
	pid, err := d.srv.sup.Spawn(d.spec(root, next))
	if err != nil {
		return drainWait, err
	}
	if err := store.MarkRunning(next.ID, pid); err != nil {
		return drainWait, err
	}
	return drainSpawn, nil
}

// classifyDrainOutcome maps a finished child's failure class — as the loop
// recorded it (state.FailureClass) — to what the queue does with the item: a
// give-up is a settled dead end the queue moves past, while a fault or a provider
// pause parks the item and stops the drain for the operator to resume. A clean
// finish (no failure class) settles done.
func classifyDrainOutcome(class string) (status string, pause bool) {
	switch class {
	case state.FailFaulted, state.FailPaused:
		return queue.StatusPaused, true
	case state.FailGaveUp:
		return queue.StatusFailed, false
	default:
		return queue.StatusDone, false
	}
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

// checkpointOutcome reads the finished child's recorded checkpoint — its phase
// and the loop's own failure marker/reason, never agent output — and returns the
// loop's failure class plus the reason to surface. A merged or healthy run
// classifies as no failure ("").
func (d *drainer) checkpointOutcome(root string, it queue.Item) (class, reason string) {
	store := state.NewStore(filepath.Join(root, ".trau", "runs"))
	phase := store.Get(it.ID, "PHASE")
	reason = store.Get(it.ID, "FAILURE_REASON")
	class = state.FailureClass(phase, store.Get(it.ID, "FAILURE_CLASS"), reason)
	if class == "" {
		reason = ""
	}
	return class, reason
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

// firstRunnable returns the next item the drain should launch: the first that is
// pending or paused. A paused item sits ahead of any pending one behind it — the
// drain stopped when it paused — so a resume re-attempts it before moving on.
func firstRunnable(items []queue.Item) (queue.Item, bool) {
	for _, it := range items {
		if it.Status == queue.StatusPending || it.Status == queue.StatusPaused {
			return it, true
		}
	}
	return queue.Item{}, false
}
