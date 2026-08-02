package webserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
)

// drainStallAfter is how long a draining repo may go without a drain tick before
// the queue reads as held on nothing at all. The loop ticks every drainPoll, so
// anything past this is a loop that stopped rather than one pacing itself: it
// exited while the queue stayed armed, or it is parked inside a tick with no
// return left to report a gate of its own.
const drainStallAfter = 2 * time.Minute

// drainStallPoll is how often the hub looks for a drain loop that stopped
// deciding. Every other gate is filed by the tick that stopped at it; a stall has
// no tick left to file anything, so only a sweep from outside the loop can turn
// it into a feed entry instead of silence.
const drainStallPoll = 30 * time.Second

// holdGate names one reason a queue reads armed while nothing starts, so the
// wait is attributable instead of silent. It is what a drain tick stopped at with
// runnable work in hand, and it reaches the operator verbatim.
type holdGate string

const (
	holdBlocked      holdGate = "blocked"
	holdSelfReload   holdGate = "self-reload"
	holdRepoBusy     holdGate = "repo-busy"
	holdRelease      holdGate = "release"
	holdQueueError   holdGate = "queue-error"
	holdLaunchFailed holdGate = "launch-failed"
	holdStalled      holdGate = "stalled"
)

// drainHold is why a draining repo started nothing, and since when. A zero gate
// means nothing held it — the tick made progress.
type drainHold struct {
	gate   holdGate
	reason string
	since  time.Time
}

// drainWatch is what the hub knows about a repo's drain loop between ticks: when
// it last reached a decision, and the hold that decision ended on.
type drainWatch struct {
	tick time.Time
	hold drainHold
}

// heldBy is the tick result of a gate that stopped a spawn the queue was
// otherwise ready to make.
func heldBy(gate holdGate, reason string) (drainAction, drainHold, error) {
	return drainWait, drainHold{gate: gate, reason: reason}, nil
}

// heldByErr is heldBy for a gate that is a failed call. The reason it hands the
// operator is written for them, never the raw error — a store or exec failure
// carries hub-internal paths, and both the queue API and the feed publish what
// lands here — while the error itself goes to the verbose log and back up the
// tick's own return.
func heldByErr(gate holdGate, reason string, err error) (drainAction, drainHold, error) {
	logger.Verbosef("drain held by %s: %v", gate, err)
	return drainWait, drainHold{gate: gate, reason: reason}, err
}

// recordHold files the hold a tick ended on and stamps the tick, announcing one
// event per hold episode: a gate that holds for hours is a single hold, so the
// feed carries the start of the wait rather than a line every drainPoll. A gate
// that gives way to a different one starts a fresh episode of its own. An episode
// that continues keeps the moment it began and takes this tick's reason, so a
// wait still on the same gate never recites an item list the queue has moved past.
func (d *drainer) recordHold(root string, hold drainHold) {
	d.mu.Lock()
	w := d.watch[root]
	episode := hold.gate != "" && hold.gate != w.hold.gate
	switch {
	case episode:
		hold.since = d.now()
	case hold.gate != "":
		hold.since = w.hold.since
	}
	d.watch[root] = drainWatch{tick: d.now(), hold: hold}
	d.mu.Unlock()
	if episode {
		d.srv.emitSpawnHeld(root, hold)
	}
}

// stampTick marks root's loop as being at a decision point, so a queue armed a
// moment ago never reads as one whose loop stopped before its first tick. Callers
// hold mu.
func (d *drainer) stampTick(root string) {
	w := d.watch[root]
	w.tick = d.now()
	d.watch[root] = w
}

// spawnHold reports what holds a draining repo's next spawn, zero when nothing
// does. A gate the loop stopped at names itself; a loop that has reached no
// decision within drainStallAfter reads as stalled whatever it was waiting for,
// timed from its last decision — or, for a queue whose loop never reached a first
// one, from the moment the queue was armed.
func (d *drainer) spawnHold(root string, armedAt time.Time) drainHold {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, watched := d.watch[root]
	if watched && d.now().Sub(w.tick) <= drainStallAfter {
		return w.hold
	}
	since := w.tick
	if since.IsZero() {
		since = armedAt
	}
	return drainHold{
		gate:   holdStalled,
		reason: "the drain loop has reached no decision — the queue is armed with nothing draining it",
		since:  since,
	}
}

// watchStalls announces the hold no tick is left to report: an armed queue whose
// drain loop has stopped reaching decisions, which on the feed is otherwise
// indistinguishable from a queue with nothing to do.
func (d *drainer) watchStalls(ctx context.Context) {
	t := time.NewTicker(drainStallPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.announceStalls()
		}
	}
}

func (d *drainer) announceStalls() {
	for _, root := range d.srv.effectiveRoots() {
		_, meta, err := d.srv.stores.Queue(root).Snapshot()
		if err != nil || !meta.Draining {
			continue
		}
		hold := d.spawnHold(root, meta.DrainingSince)
		if hold.gate == holdStalled && d.noteStall(root, hold) {
			d.srv.emitSpawnHeld(root, hold)
		}
	}
}

// noteStall files a stall as a hold episode of its own, reporting whether this
// sweep is the one that found it. It leaves the tick stamp alone: nothing has
// decided anything, so the stall stands until something does.
func (d *drainer) noteStall(root string, hold drainHold) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.watch[root]
	fresh := w.hold.gate != holdStalled
	w.hold = hold
	d.watch[root] = w
	return fresh
}

// blockedReason names the runnable items the drain passed over because a blocker
// still holds every one of them back — an armed queue with work it may not start
// yet.
func blockedReason(items []queue.Item, batch string) string {
	held := make([]string, 0, len(items))
	for _, it := range items {
		if queue.Runnable(it.Status) && inBatch(it, batch) {
			held = append(held, it.ID)
		}
	}
	return fmt.Sprintf("an unresolved blocker holds back every runnable item (%s)", strings.Join(held, ", "))
}

// emitSpawnHeld records a spawn the drain held with runnable work queued, so a
// queue that reads armed and idle explains itself in the feed instead of being
// indistinguishable from a hang.
func (s *Server) emitSpawnHeld(root string, hold drainHold) {
	s.emitQueueEvent(root, event.KindQueueSpawnHeld,
		fmt.Sprintf("spawn held: %s — %s", hold.gate, hold.reason),
		map[string]any{"gate": string(hold.gate), "reason": hold.reason})
}
