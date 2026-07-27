package webserver

import (
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

// planAutoResume schedules the next automatic re-attempt of an item the drain
// just parked, when the repo opted in and the pause was blameless — a provider
// rate/auth wall or an unreachable hub, never a fault, an unknown outcome, or a
// deliberate stop. Each re-attempt waits longer than the last and the budget is
// bounded, so an item whose condition never clears ends up parked for a human
// exactly as it does today.
func (d *drainer) planAutoResume(root string, it queue.Item, class string) {
	if class != state.FailPaused {
		d.forgetAutoResume(root, it.ID)
		return
	}
	tries := d.autoTries(root)
	if tries <= 0 {
		d.forgetAutoResume(root, it.ID)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	plan, ok := d.resumes[root]
	if !ok || plan.id != it.ID {
		plan = autoResume{id: it.ID}
	}
	if plan.attempts >= tries {
		delete(d.resumes, root)
		return
	}
	plan.attempts++
	plan.due = d.now().Add(time.Duration(plan.attempts) * d.backoff)
	d.resumes[root] = plan
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
