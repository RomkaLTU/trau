package webserver

import (
	"context"
	"fmt"
	"os"
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
	meta, err := store.Meta()
	if err != nil {
		return drainWait, err
	}
	if running, ok := firstWithStatus(items, queue.StatusRunning); ok {
		if d.alive(running.PID) {
			return drainWait, nil
		}
		class, reason := d.reconcileOutcome(root, running)
		if status, pause := classifyDrainOutcome(class, meta.OnFault); pause {
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
	// Global dedup: a standalone ticket an earlier queued epic already covers
	// is skipped, not run twice. First occurrence wins.
	if reason, dup := duplicateReason(items, next); dup {
		if err := store.MarkSkipped(next.ID, reason); err != nil {
			return drainWait, err
		}
		return drainReconcile, nil
	}
	_ = os.Remove(reportPath(root))
	pid, err := d.srv.sup.Spawn(d.spec(root, next, meta.NoResume))
	if err != nil {
		return drainWait, err
	}
	if err := store.MarkRunning(next.ID, pid); err != nil {
		return drainWait, err
	}
	return drainSpawn, nil
}

// reconcileOutcome settles a finished child, returning the failure class and
// reason. The child's own report is authoritative for a pause or fault (it
// catches an epic whose fault lives on a sub-issue), while a give-up and a clean
// finish fall through to the checkpoint-derived outcome.
func (d *drainer) reconcileOutcome(root string, it queue.Item) (class, reason string) {
	class, reason = d.outcome(root, it)
	if rep, ok := queue.ReadReport(reportPath(root)); ok {
		_ = os.Remove(reportPath(root))
		if rep.Class != "" {
			class, reason = rep.Class, rep.Reason
		}
	}
	return class, reason
}

// duplicateReason reports whether a next-to-run ticket is already covered by an
// earlier queued epic, so the drain skips it instead of running it twice. Only a
// standalone ticket can be a duplicate; epics dedup their shared leaves through
// tracker state as they run.
func duplicateReason(items []queue.Item, next queue.Item) (string, bool) {
	if next.Kind != queue.KindTicket {
		return "", false
	}
	for _, it := range items {
		if it.ID == next.ID {
			break
		}
		if it.Kind != queue.KindEpic {
			continue
		}
		for _, sub := range it.SubIssues {
			if sub.ID == next.ID {
				return fmt.Sprintf("duplicate of %s sub-issue", it.ID), true
			}
		}
	}
	return "", false
}

// reportPath is where a queued child writes its drain report; one path per repo
// is safe because the drain runs children strictly one at a time.
func reportPath(root string) string {
	return filepath.Join(root, ".trau", "runs", ".drain-report")
}

// classifyDrainOutcome maps a finished child's failure class — as the loop
// recorded it (state.FailureClass) — to what the queue does with the item. A
// provider pause always parks the item and stops the drain for a resume. A fault
// halts the same way by default, or — when the queue was started on-fault=skip —
// settles the item failed and lets the drain move on. A give-up is a settled
// dead end the queue moves past; a clean finish settles done.
func classifyDrainOutcome(class, onFault string) (status string, pause bool) {
	switch class {
	case state.FailPaused:
		return queue.StatusPaused, true
	case state.FailFaulted:
		if onFault == queue.OnFaultSkip {
			return queue.StatusFailed, false
		}
		return queue.StatusPaused, true
	case state.FailGaveUp:
		return queue.StatusFailed, false
	default:
		return queue.StatusDone, false
	}
}

// spec is the launch a queued item spawns: a ticket runs as the existing
// run-once, an epic as the existing epic flow, matching the /instances start
// paths so a queued run is indistinguishable from a manual one. noResume ignores
// stored checkpoints, and every child leaves a drain report for its outcome.
func (d *drainer) spec(root string, it queue.Item, noResume bool) SpawnSpec {
	args := []string{"--repo", root, "--no-tui"}
	if it.Kind == queue.KindEpic {
		args = append(args, "--parent", it.ID)
	} else {
		args = append(args, "--parent", it.ID, "--once")
	}
	if noResume {
		args = append(args, "--no-resume")
	}
	args = append(args, "--drain-report", reportPath(root))
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
