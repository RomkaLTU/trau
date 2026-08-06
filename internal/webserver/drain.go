package webserver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/forge/bitbucketapi"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
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
	srv       *Server
	poll      time.Duration
	backoff   time.Duration
	now       func() time.Time
	alive     func(pid int) bool
	repoLive  func(root string) bool
	outcome   func(root string, it queue.Item) (class, reason string)
	prState   func(root, pr string) string
	autoTries func(root string) int

	mu      sync.Mutex
	active  map[string]context.CancelFunc
	resumes map[string]autoResume
	watch   map[string]drainWatch
}

func newDrainer(s *Server) *drainer {
	d := &drainer{
		srv:     s,
		poll:    drainPoll,
		backoff: autoResumeBackoff,
		now:     time.Now,
		alive:   registry.Alive,
		prState: forgePRState,
		active:  map[string]context.CancelFunc{},
		resumes: map[string]autoResume{},
		watch:   map[string]drainWatch{},
	}
	d.repoLive = d.repoHasLiveInstance
	d.outcome = d.checkpointOutcome
	d.autoTries = d.configuredAutoResumeTries
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
	d.stampTick(root)
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

// tick takes one drain decision and files what it held on, so a repo that starts
// nothing while runnable work waits can be asked why — on the queue API and, once
// per hold episode, in the feed.
func (d *drainer) tick(root string) (drainAction, error) {
	act, hold, err := d.decide(root)
	d.recordHold(root, hold)
	return act, err
}

// decide advances a repo's queue by one decision: it launches the next runnable
// item no open blocker holds back, settles a finished one per the failure
// taxonomy — pausing the drain on a fault or provider pause, but leaving a row
// whose removal is in flight to the removal, which drops it rather than parking
// it — waits, never spawning a second child while one is in flight, while a
// pending self-reload is waiting for its idle gap, or while an Epic's release
// holds the repo — only that Epic's own finalize starts then — or finishes the
// drain once the queue has nothing left to run so a completed — or armed but
// empty — queue reads stopped instead of idling armed. A tick that finds a
// running epic first advances its sub-issue rows onto what their checkpoints
// already say, so the queue's count moves as children settle. Every wait it
// returns names the gate it stopped at. It is the whole drain policy, pure enough
// to table-test.
func (d *drainer) decide(root string) (drainAction, drainHold, error) {
	store := d.srv.stores.Queue(root)
	items, meta, err := store.Snapshot()
	if err != nil {
		return heldByErr(holdQueueError, "the repo's queue could not be read", fmt.Errorf("read queue: %w", err))
	}
	if running, ok := firstWithStatus(items, queue.StatusRunning); ok {
		d.advanceSubIssues(root, running)
		if d.alive(running.PID) || d.srv.isRemoving(root, running.ID) {
			return drainWait, drainHold{}, nil
		}
		class, reason := d.reconcileOutcome(root, running)
		if status, pause := classifyDrainOutcome(class, meta.OnFault); pause {
			if err := store.Pause(running.ID, reason); err != nil {
				return heldByErr(holdQueueError, fmt.Sprintf("%s could not be parked", running.ID),
					fmt.Errorf("park %s: %w", running.ID, err))
			}
			d.planAutoResume(root, running, class)
		} else {
			if err := store.Finish(running.ID, status, reason); err != nil {
				return heldByErr(holdQueueError, fmt.Sprintf("%s could not be settled", running.ID),
					fmt.Errorf("settle %s: %w", running.ID, err))
			}
			d.srv.clearQueued(d.srv.drainCtx, root, running)
			d.forgetAutoResume(root, running.ID)
		}
		return drainReconcile, drainHold{}, nil
	}
	if !meta.Draining {
		if d.holdForAutoResume(root) {
			return drainWait, drainHold{}, nil
		}
		return drainStop, drainHold{}, nil
	}
	next, ok, err := d.firstUnblocked(root, items, meta.Batch)
	if err != nil {
		return heldByErr(holdQueueError, "the queue's blockers could not be read", fmt.Errorf("read blockers: %w", err))
	}
	if !ok {
		finished, err := store.FinishDraining()
		if err != nil {
			return heldByErr(holdQueueError, "the drain could not be disarmed", fmt.Errorf("disarm drain: %w", err))
		}
		if finished {
			d.srv.emitQueueBatchFinished(root, meta.Batch, items)
			return drainStop, drainHold{}, nil
		}
		return heldBy(holdBlocked, blockedReason(items, meta.Batch))
	}
	if repo := d.srv.selfReloadPending(); repo != "" {
		return heldBy(holdSelfReload, fmt.Sprintf(
			"a self-reload onto %s is waiting for the hub to fall idle", filepath.Base(repo)))
	}
	if d.repoLive(root) {
		return heldBy(holdRepoBusy, "a loop is already running in this repo")
	}
	if epic, held := d.srv.heldByRelease(root, next.ID); held {
		finalize, queued := runnableItem(items, epic)
		if !queued {
			return heldBy(holdRelease, fmt.Sprintf(
				"%s is releasing and its own finalize is not queued to finish it", epic))
		}
		next = finalize
	}
	// Global dedup: a standalone ticket an earlier queued epic already covers
	// is skipped, not run twice. First occurrence wins.
	if reason, dup := duplicateReason(items, next); dup {
		if err := store.MarkSkipped(next.ID, reason); err != nil {
			return heldByErr(holdQueueError, fmt.Sprintf("%s could not be marked skipped", next.ID),
				fmt.Errorf("skip %s: %w", next.ID, err))
		}
		return drainReconcile, drainHold{}, nil
	}
	_ = d.srv.stores.DrainOutcomes().Remove(root, next.ID)
	pid, err := d.srv.sup.Spawn(d.spec(root, next, meta.NoResume))
	if err != nil {
		return heldByErr(holdLaunchFailed, fmt.Sprintf("%s could not be launched", next.ID),
			fmt.Errorf("spawn %s: %w", next.ID, err))
	}
	if err := store.MarkRunning(next.ID, pid); err != nil {
		return heldByErr(holdQueueError, fmt.Sprintf("%s could not be marked running", next.ID),
			fmt.Errorf("mark %s running: %w", next.ID, err))
	}
	d.srv.clearQueuedIssue(d.srv.drainCtx, root, next.ID)
	return drainSpawn, drainHold{}, nil
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
//   - outcome absent → the child died without recording an outcome (a kill,
//     crash, or a failed post). A checkpoint failure class still stands, and an
//     item the checkpoint proves merged still settles done, but otherwise the
//     outcome is unknown (classUnknown) and must not settle done — a dead child
//     that never reached merged lands here.
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
// proof on its checkpoint that it finished cleanly: an item whose phase reached
// merged in the authoritative checkpoints table. An epic is judged on the same
// evidence a ticket is — a shipped epic writes a checkpoint of its own under the
// epic id (checkpointEpicMerged).
func (d *drainer) cleanFinish(root string, it queue.Item) bool {
	return d.srv.stores.Checkpoints().Phase(root, it.ID) == state.Merged
}

// advanceSubIssues moves a running epic's sub-issue rows onto the states their
// own checkpoints have already reached, so the queue's count follows the drain
// instead of jumping when the epic settles. A row already recorded done stays
// done — an enqueue-time snapshot of a child the tracker files closed is fresher
// than whatever checkpoint an older run left behind — and the epic's settle
// remains the authoritative closer.
func (d *drainer) advanceSubIssues(root string, it queue.Item) {
	if it.Kind != queue.KindEpic {
		return
	}
	states := map[string]string{}
	for _, sub := range it.SubIssues {
		if sub.State == subIssueDone {
			continue
		}
		next := subIssueState(d.srv.stores.Checkpoints().Phase(root, sub.ID))
		if next != "" && next != sub.State {
			states[sub.ID] = next
		}
	}
	if len(states) == 0 {
		return
	}
	if err := d.srv.stores.Queue(root).SetSubIssueStates(it.ID, states); err != nil {
		logger.Verbosef("advance sub-issues of %s: %v", it.ID, err)
	}
}

// subIssueState maps a child's checkpoint phase onto the sub-issue state the
// queue records, or "" while the child has reached no terminal phase. A merged
// child is done; a quarantined one is settled but not shipped, and says so
// rather than passing for either done or untouched work.
func subIssueState(phase string) string {
	switch phase {
	case state.Merged:
		return subIssueDone
	case state.Quarantined:
		return subIssueQuarantined
	}
	return ""
}

// The three independent proofs a swept item's work already shipped, in the order
// the sweep asks for them: the hub's own checkpoint table, the hub's mirror of the
// tracker, and — last, because it costs a subprocess — the forge.
const (
	evidenceCheckpoint = "checkpoint merged"
	evidenceTracker    = "tracker Done"
	evidencePR         = "PR merged"
)

// prStateTimeout bounds the forge lookup one swept item costs, so a boot sweep
// over a long queue cannot hang on an unreachable network. prStateGrace is the
// share of that budget held back for the wait after the deadline kills gh, since
// WaitDelay runs on top of the deadline rather than inside it.
const (
	prStateTimeout = 5 * time.Second
	prStateGrace   = time.Second
)

// reconciledReason replaces the stale fault a swept item was parked with: the
// evidence that settled it instead.
func reconciledReason(evidence string) string {
	return evidence + " — the work had already shipped"
}

// settleEvidence names the ground truth that proves an item's work is complete,
// or "" when nothing does. The three sources are independent on purpose: a child
// killed between its merge and the checkpoint write leaves only the merged PR
// behind, and work finished out of band while the hub was down shows up as a
// tracker status and nothing else.
func (d *drainer) settleEvidence(root string, it queue.Item) string {
	row, found, err := d.srv.stores.Checkpoints().One(root, it.ID)
	checkpointed := found && err == nil
	switch {
	case checkpointed && row.Phase == state.Merged:
		return evidenceCheckpoint
	case d.trackerDone(root, it.ID):
		return evidenceTracker
	case checkpointed && d.prMerged(root, row):
		return evidencePR
	default:
		return ""
	}
}

// trackerDone reports whether the hub's issue store — its mirror of the tracker,
// refreshed on the sync tick — already files the item under the done group.
func (d *drainer) trackerDone(root, id string) bool {
	iss, found, err := d.srv.stores.Issues().Get(root, id)
	if err != nil || !found {
		return false
	}
	return iss.StatusGroup == string(tracker.StatusGroupDone)
}

// prMerged reports whether the PR the item's checkpoint recorded has since been
// merged. It is the evidence that outlives a child killed after its merge landed
// but before the phase reached merged, and the one an epic whose finalize never
// ran leaves behind. A Folder repo run has one PR per changed Child repo, each
// asked about in that child — a folder root is not a git repository, so gh has
// nothing to answer from there — and it settles only when every one of them
// merged: a single merged sibling would close the item over PRs still open.
func (d *drainer) prMerged(root string, row hubstore.CheckpointRow) bool {
	if ships := shipsFromCheckpoint(row.Data); len(ships) > 0 {
		for _, s := range ships {
			if s.PR == "" || d.prState(filepath.Join(root, s.Repo), s.PR) != "MERGED" {
				return false
			}
		}
		return true
	}
	pr := checkpointField(row.Data, "PR")
	if pr == "" {
		return false
	}
	return d.prState(root, pr) == "MERGED"
}

// forgePRState asks the forge a repository is hosted on for a PR's state,
// through the same path the pipeline merges with: gh on GitHub, the REST API on
// Bitbucket Cloud. Anything that is not a clean answer reads as unknown, so a
// missing credential or an offline machine leaves the item parked rather than
// settling it — the reconcile sweep settles only on proof.
func forgePRState(root, pr string) string {
	ctx, cancel := context.WithTimeout(context.Background(), prStateTimeout-prStateGrace)
	defer cancel()
	cfg, cfgErr := repoConfig(root)
	client, ok := bitbucketapi.ClientFor(ctx, root, config.Declared(root, "FORGE"), "", cfg.BitbucketEmail, cfg.BitbucketAPIToken)
	if !ok {
		// Every other forge is gh's to answer, whether or not this repo's own
		// config could be read — that answer needs no credential from it.
		return ghPRState(root, pr)
	}
	if cfgErr != nil {
		return ""
	}
	found, err := client.PullRequest(ctx, pr)
	if err != nil || found == nil {
		return ""
	}
	return found.State
}

// ghPRState reads a PR's state with gh. The grace bounds the read: a credential
// helper gh leaves holding the pipe would otherwise outlive the cancelled
// command.
func ghPRState(root, pr string) string {
	ctx, cancel := context.WithTimeout(context.Background(), prStateTimeout-prStateGrace)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", pr, "--json", "state", "-q", ".state")
	cmd.Dir = root
	cmd.WaitDelay = prStateGrace
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// reconcileQueue settles the unsettled items an outage left behind. A hub that
// comes back to an item whose work verifiably finished — an out-of-band resume, a
// child that died after merging, a ticket a human closed while the hub was down,
// an epic PR the operator finally merged — should not make a Start re-discover the
// fact. Every unresolved item is compared against ground truth rather than against
// the class it settled with, and settles through the same path a finished child
// does, so its sub-issues and the web queue follow. Anything short of proof stays
// exactly as it is, and the sweep spawns nothing.
func (d *drainer) reconcileQueue(root string) {
	store := d.srv.stores.Queue(root)
	items, _, err := store.Snapshot()
	if err != nil {
		logger.Verbosef("reconcile queue %s: %v", root, err)
		return
	}
	for _, it := range items {
		if !awaitsResolution(it.Status) {
			continue
		}
		evidence := d.settleEvidence(root, it)
		if evidence == "" {
			continue
		}
		if err := store.Finish(it.ID, queue.StatusDone, reconciledReason(evidence)); err != nil {
			logger.Verbosef("reconcile %s: %v", it.ID, err)
			continue
		}
		if err := d.srv.stores.DrainOutcomes().Remove(root, it.ID); err != nil {
			logger.Verbosef("reconcile %s: drop drain outcome: %v", it.ID, err)
		}
		d.forgetAutoResume(root, it.ID)
		d.srv.clearQueued(d.srv.drainCtx, root, it)
		d.srv.emitQueueReconciled(root, it, evidence)
	}
}

// awaitsResolution reports whether an item's status leaves work something outside
// the drain may since have finished: a park, a fault, or an epic PR handed to a
// human. Those are the rows the sweep re-checks against ground truth.
func awaitsResolution(status string) bool {
	switch status {
	case queue.StatusPaused, queue.StatusFailed, queue.StatusAwaitingMerge:
		return true
	}
	return false
}

// emitQueueReconciled records a settle the sweep made on stored evidence alone,
// so the activity view can explain a queue item that reached done with no run of
// its own behind it.
func (s *Server) emitQueueReconciled(root string, it queue.Item, evidence string) {
	s.emitQueueEvent(root, event.KindQueueReconciled,
		fmt.Sprintf("%s settled done without a run — %s", it.ID, evidence),
		map[string]any{"ticket": it.ID, "kind": string(it.Kind), "evidence": evidence})
}

// emitQueueEvent appends a hub-authored event and pushes it to the live feed,
// the shared tail of every move the hub makes on a queue with no run behind it.
func (s *Server) emitQueueEvent(root, kind, msg string, fields map[string]any) {
	rows, err := s.stores.Events().Append(root, []hubstore.NewEvent{{
		TS:     time.Now().UTC().Format(time.RFC3339),
		Kind:   kind,
		Msg:    msg,
		Fields: marshalFields(fields),
	}})
	if err != nil {
		logger.Verbosef("queue event %s: %v", kind, err)
		return
	}
	name := filepath.Base(root)
	for _, row := range rows {
		s.publishEvent(root, name, row)
	}
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
// missing outcome never settles done. A provider pause and a deliberate stop park
// the same way for a resume. A fault halts by default, or — when the queue was
// started on-fault=skip — settles the item failed and lets the drain move on. A
// give-up is a settled dead end the queue moves past; a clean finish settles done.
// An epic release handed to a human settles awaiting-merge: visibly not done, but
// settled, so the rest of the queue drains on and nothing re-attempts a PR only a
// person can land.
func classifyDrainOutcome(class, onFault string) (status string, pause bool) {
	switch class {
	case classUnknown:
		return queue.StatusPaused, true
	case state.FailAwaitingMerge:
		return queue.StatusAwaitingMerge, false
	case state.FailPaused, state.FailStopped:
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
// stored checkpoints; an item's Provider override rides along as --provider.
// --drain-report carries the ticket the child reports its exit outcome under,
// so the child posts to the hub keyed by it.
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
	if it.Provider != "" {
		args = append(args, "--provider", it.Provider)
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
// second child in the same repo. The wait keeps the drain armed, so a takeover
// block is temporary: the queue retries once the lock's process dies.
func (d *drainer) repoHasLiveInstance(root string) bool {
	return d.srv.hasBusyInstance(root)
}

// runnableItem returns id's queue row when the drain could launch it. A
// release-gated tick reaches past the head of run order with it: every row ahead
// of the releasing Epic is waiting on the release that Epic has to finish.
func runnableItem(items []queue.Item, id string) (queue.Item, bool) {
	for _, it := range items {
		if it.ID == id && queue.Runnable(it.Status) {
			return it, true
		}
	}
	return queue.Item{}, false
}

func firstWithStatus(items []queue.Item, status string) (queue.Item, bool) {
	for _, it := range items {
		if it.Status == status {
			return it, true
		}
	}
	return queue.Item{}, false
}

// firstUnblocked returns the next item the drain should launch: the first that is
// pending or paused and whose blockers have all resolved. A paused item sits ahead
// of any pending one behind it — the drain stopped when it paused — so a resume
// re-attempts it before moving on. An item an open blocker still holds back is
// passed over rather than started, so the blocker runs first even when the queue
// holds it further down. A batch scope narrows the choice to that batch's members;
// everything queued outside it is passed over however runnable it is. Reporting
// none leaves the caller to FinishDraining, which refuses to disarm a scope whose
// runnable items are merely blocked.
func (d *drainer) firstUnblocked(root string, items []queue.Item, batch string) (queue.Item, bool, error) {
	runnable := make([]string, 0, len(items))
	shipped := map[string]bool{}
	for _, it := range items {
		switch {
		case queue.Runnable(it.Status) && inBatch(it, batch):
			runnable = append(runnable, it.ID)
		case it.Status == queue.StatusDone:
			shipped[it.ID] = true
		}
	}
	if len(runnable) == 0 {
		return queue.Item{}, false, nil
	}
	blockers, err := d.srv.stores.Issues().UnresolvedBlockers(root, runnable)
	if err != nil {
		return queue.Item{}, false, err
	}
	for _, it := range items {
		if queue.Runnable(it.Status) && inBatch(it, batch) && !heldBack(blockers[it.ID], shipped) {
			return it, true, nil
		}
	}
	return queue.Item{}, false, nil
}

// inBatch reports whether the armed scope covers an item: everything queued when
// the drain runs the whole queue, the batch's members otherwise.
func inBatch(it queue.Item, batch string) bool {
	return batch == "" || it.Batch == batch
}

// heldBack reports whether any blocker still holds an item back. A blocker this
// queue already settled done has shipped, whatever the tracker mirror — refreshed
// only on the sync tick — still says about it.
func heldBack(blockers []string, shipped map[string]bool) bool {
	for _, id := range blockers {
		if !shipped[id] {
			return true
		}
	}
	return false
}
