package webserver

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/state"
)

// autoResumeBackoff is the base wait before an automatic re-attempt; the nth
// try waits n times it, so a wall that has not moved is not hammered.
const autoResumeBackoff = 2 * time.Minute

// autoResume is the pending re-attempt of one blamelessly parked item. It lives
// in memory only: a hub that dies with a plan outstanding comes back to the item
// parked, which is the same place a human would find it without the opt-in.
type autoResume struct {
	id       string
	attempts int
	due      time.Time
}

// releaseResumeTries bounds the automatic re-attempts a finalize that died
// mid-release gets. Unlike the repo's opt-in budget it is not optional: that
// item's own checkpoint is what holds every other item in the repo behind the
// release gate, so nobody but the hub can reopen it. It stays bounded, so a
// finalize that dies every time ends parked for a human instead of looping.
const releaseResumeTries = 2

// planAutoResume schedules the next automatic re-attempt of an item the drain
// just parked, when the pause is one a re-attempt can clear: a blameless provider
// rate/auth wall or an unreachable hub the repo opted into re-trying, or a
// finalize that died mid-release, which strands its repo's queue until something
// re-enters it. A fault, an unknown outcome, a deliberate stop and a spend ceiling
// otherwise stay parked — the last two clear only on a human or the cap's own
// reset, so re-entering a release over either would burn its re-attempts against
// an unchanged condition and stamp a blameless park faulted. Each re-attempt waits
// longer than the last and the tries are bounded, so an item whose condition never
// clears ends up parked exactly as it does today — a release that spends them
// stamped faulted, which opens the gate it was holding.
func (d *drainer) planAutoResume(root string, it queue.Item, class string) {
	release := class != state.FailStopped && class != state.FailBudget && d.crashedRelease(root, it.ID)
	tries := d.autoTries(root)
	switch {
	case release && tries < releaseResumeTries:
		tries = releaseResumeTries
	case !release && class != state.FailPaused:
		tries = 0
	}
	if tries > 0 && d.spendAutoResume(root, it.ID, tries) {
		return
	}
	d.forgetAutoResume(root, it.ID)
	if release {
		d.stampReleaseSpent(root, it.ID, tries)
	}
}

// spendAutoResume books one re-attempt of id against a budget of tries, reporting
// whether there was one left to spend. A plan held for another item starts over,
// so each item gets the whole budget.
func (d *drainer) spendAutoResume(root, id string, tries int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	plan, ok := d.resumes[root]
	if !ok || plan.id != id {
		plan = autoResume{id: id}
	}
	if plan.attempts >= tries {
		return false
	}
	plan.attempts++
	plan.due = d.now().Add(time.Duration(plan.attempts) * d.backoff)
	d.resumes[root] = plan
	return true
}

// crashedRelease reports whether the item the drain just parked left a release
// behind that trau still owns — the checkpoint the release gate reads.
func (d *drainer) crashedRelease(root, id string) bool {
	row, found, err := d.srv.stores.Checkpoints().One(root, id)
	if err != nil || !found {
		return false
	}
	return liveRelease(row)
}

// stampReleaseSpent ends a release the hub re-entered as often as its budget
// allows: the epic's checkpoint takes the same faulted class a dead ticket lands
// in, which parks it for a human and — since nothing re-attempts a faulted
// release — opens the gate its releasing phase held, so the rest of the queue
// drains on. A release that settled in the meantime is left alone.
func (d *drainer) stampReleaseSpent(root, id string, tries int) {
	row, found, err := d.srv.stores.Checkpoints().One(root, id)
	if err != nil || !found || !liveRelease(row) {
		return
	}
	data := map[string]string{}
	if row.Data != "" {
		_ = json.Unmarshal([]byte(row.Data), &data)
	}
	data["FAILURE_CLASS"] = state.FailFaulted
	data["FAILURE_REASON"] = fmt.Sprintf("the release died in %d automatic re-attempts — finish it by hand", tries)
	data["UPDATED"] = d.now().UTC().Format("2006-01-02 15:04:05")
	if err := d.srv.stores.Checkpoints().Upsert(root, id, data); err != nil {
		logger.Verbosef("stamp spent release %s/%s: %v", root, id, err)
	}
}

// holdForAutoResume keeps a disarmed repo's drain loop alive while a re-attempt
// is planned for it, re-arming the queue once the plan comes due, and reports
// whether the loop should wait rather than stop. The hub evaluating the plan is
// itself the proof that a hub-unreachable pause has cleared; a provider wall gets
// the backoff to clear in, and the re-attempt's own outcome is the test of
// whether it did.
func (d *drainer) holdForAutoResume(root string) bool {
	d.mu.Lock()
	plan, ok := d.resumes[root]
	d.mu.Unlock()
	if !ok {
		return false
	}
	if d.now().Before(plan.due) {
		return true
	}
	if err := d.srv.stores.Queue(root).Rearm(); err != nil {
		logger.Verbosef("auto-resume %s: %v", plan.id, err)
		d.forgetAutoResume(root, plan.id)
		return false
	}
	d.srv.emitQueueAutoResumed(root, plan)
	return true
}

// forgetAutoResume drops the plan held for id, so an item that settles — by a
// re-attempt that worked or by the reconcile sweep — starts any later pause with
// a fresh budget.
func (d *drainer) forgetAutoResume(root, id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if plan, ok := d.resumes[root]; ok && plan.id == id {
		delete(d.resumes, root)
	}
}

// configuredAutoResumeTries reads the repo's own opt-in, so the decision to spend
// tokens without a human click lives with the repo rather than with the hub. Zero
// — the default — keeps every blameless pause manual.
func (d *drainer) configuredAutoResumeTries(root string) int {
	cfg, err := repoConfig(root)
	if err != nil || !cfg.QueueAutoResume {
		return 0
	}
	return cfg.QueueAutoResumeTries
}

// emitQueueAutoResumed records a re-attempt the hub armed on its own, so the
// activity view can explain a run nobody clicked Start for.
func (s *Server) emitQueueAutoResumed(root string, plan autoResume) {
	s.emitQueueEvent(root, event.KindQueueAutoResumed,
		fmt.Sprintf("%s re-attempted automatically after a blameless pause (try %d)", plan.id, plan.attempts),
		map[string]any{"ticket": plan.id, "attempt": plan.attempts})
}
