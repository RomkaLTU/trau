package webserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
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
	drainStop      drainAction = "stop"      // draining is off or the queue ran dry; the loop exits
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
// fault or provider pause — waits, never spawning a second child while one is in
// flight, or finishes the drain once the queue has run dry so a completed queue
// reads stopped instead of idling armed. It is the whole drain policy, pure
// enough to table-test.
func (d *drainer) tick(root string) (drainAction, error) {
	store := d.srv.stores.Queue(root)
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
	next, ok := firstRunnable(items)
	if !ok {
		finished, err := store.FinishDraining()
		if err != nil {
			return drainWait, err
		}
		if finished {
			return drainStop, nil
		}
		return drainWait, nil
	}
	if d.repoLive(root) {
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
	_ = d.srv.stores.DrainOutcomes().Remove(root, next.ID)
	pid, err := d.srv.sup.Spawn(d.spec(root, next, meta.NoResume))
	if err != nil {
		return drainWait, err
	}
	if err := store.MarkRunning(next.ID, pid); err != nil {
		return drainWait, err
	}
	return drainSpawn, nil
}

// classUnknown marks a child that exited without leaving a drain report and
// whose checkpoint carries no clean-finish evidence: the outcome is unknown, so
// the drain parks the item for a human rather than guessing done. No child or
// checkpoint ever records it — only reconcileOutcome synthesizes it.
const classUnknown = "unknown"

// reconcileOutcome settles a finished child, returning the failure class and
// reason. Every hub-spawned child posts a drain outcome on every exit — a clean
// finish posts an empty class — so a recorded outcome's presence is itself
// evidence:
//
//   - outcome present, non-empty class → the child's own outcome is authoritative
//     (it catches an epic whose fault lives on a sub-issue's checkpoint);
//   - outcome present, empty class → a clean finish; the checkpoint-derived
//     outcome (a give-up, or "" for done) stands;
//   - outcome absent → the child died without recording an outcome (SIGKILL,
//     crash, or a failed post). A checkpoint failure class still stands, and a
//     ticket the checkpoint proves merged still settles done, but otherwise the
//     outcome is unknown (classUnknown) and must not settle done — an epic never
//     has a checkpoint of its own, so a dead epic child lands here.
//
// A hub store read error reads as absent, keeping the safe path: never settle
// done on missing evidence.
func (d *drainer) reconcileOutcome(root string, it queue.Item) (class, reason string) {
	class, reason = d.outcome(root, it)
	if rep, found, err := d.srv.stores.DrainOutcomes().One(root, it.ID); err == nil && found {
		_ = d.srv.stores.DrainOutcomes().Remove(root, it.ID)
		if rep.Class != "" {
			class, reason = rep.Class, rep.Reason
		}
		return class, reason
	}
	if class == "" && !d.cleanFinish(root, it) {
		return classUnknown, "child exited without a drain report — outcome unknown"
	}
	return class, reason
}

// cleanFinish reports whether a report-absent child nonetheless left durable
// proof on its checkpoint that it finished cleanly: a ticket item whose phase
// reached merged in the authoritative checkpoints table. An epic has no
// checkpoint of its own, so its only clean-finish proof is a present, empty
// report; a report-absent epic is never a clean finish.
func (d *drainer) cleanFinish(root string, it queue.Item) bool {
	if it.Kind == queue.KindEpic {
		return false
	}
	return d.srv.stores.Checkpoints().Phase(root, it.ID) == state.Merged
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

// repoRunsDir resolves the runs dir a loop child launched in root writes to,
// mirroring how the child itself resolves config: the repo-root .trau.ini and
// ~/.trau.ini layers plus the repo-root-local trau.ini the child reads because it
// runs with root as its working directory. A relative RUNS_DIR resolves against
// the repo root; an unset value or a config error falls back to the default. It
// is what keeps the hub folding a repo's file-era run data in from where the
// child actually put it, not a hardcoded .trau/runs.
func repoRunsDir(root string) string {
	projectPath := config.ProjectConfigPath(root)
	var userPath string
	if home, err := os.UserHomeDir(); err == nil {
		userPath = config.ProjectConfigPath(home)
	}
	localPath := config.LocalConfigPath()
	if !filepath.IsAbs(localPath) {
		localPath = filepath.Join(root, localPath)
	}
	runsDir := config.Defaults().RunsDir
	if cfg, err := config.LoadLayered(projectPath, userPath, localPath, ""); err == nil && cfg.RunsDir != "" {
		runsDir = cfg.RunsDir
	}
	if !filepath.IsAbs(runsDir) {
		runsDir = filepath.Join(root, runsDir)
	}
	return runsDir
}

// classifyDrainOutcome maps a finished child's failure class — as the loop
// recorded it (state.FailureClass) — to what the queue does with the item. An
// unknown outcome (classUnknown: a child that exited without a drain report and
// left no clean-finish evidence) always parks the item and stops the drain, so a
// missing outcome never settles done. A provider pause parks the same way for a
// resume. A fault halts by default, or — when the queue was started
// on-fault=skip — settles the item failed and lets the drain move on. A give-up
// is a settled dead end the queue moves past; a clean finish settles done.
func classifyDrainOutcome(class, onFault string) (status string, pause bool) {
	switch class {
	case classUnknown:
		return queue.StatusPaused, true
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
// stored checkpoints. --drain-report carries the ticket the child reports its
// exit outcome under, so the child posts to the hub keyed by it.
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
	args = append(args, "--drain-report", it.ID)
	return SpawnSpec{Dir: root, Args: args, Env: childEnv(d.srv.home)}
}

// checkpointOutcome reads the finished child's recorded checkpoint from the
// authoritative table — its phase and the loop's own failure marker/reason, never
// agent output — and returns the loop's failure class plus the reason to surface.
// A merged or healthy run, or a ticket with no checkpoint, classifies as no
// failure ("").
func (d *drainer) checkpointOutcome(root string, it queue.Item) (class, reason string) {
	row, found, err := d.srv.stores.Checkpoints().One(root, it.ID)
	if err != nil || !found {
		return "", ""
	}
	class = state.FailureClass(row.Phase, checkpointField(row.Data, "FAILURE_CLASS"), row.FailureReason)
	if class == "" {
		return "", ""
	}
	return class, row.FailureReason
}

// repoHasLiveInstance reports whether a loop — a manual loop or a Run once — is
// already running in root, so the drainer waits for it instead of spawning a
// second child in the same repo. An idle instance is an open dashboard, not a
// run, and does not block; every other state (or a legacy entry with no state)
// means a run is in flight or holding WIP.
func (d *drainer) repoHasLiveInstance(root string) bool {
	for _, e := range d.srv.liveInstances() {
		if e.RepoRoot == root && e.SessionState != registry.StateIdle {
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
