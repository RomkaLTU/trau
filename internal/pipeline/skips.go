package pipeline

import (
	"fmt"
	"slices"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/state"
)

// loadSkips settles which work the ticket in flight bypasses and makes the answer
// durable. A run started with --skip records its set on the ticket's own
// checkpoint; a run started without one adopts what an earlier attempt recorded
// there, so a resume honors the same skips with no argv behind it. An epic run
// declares the set once for the whole epic, so it is recorded on the epic's
// checkpoint too and every unit of work under it — each sub-issue run and the
// release — resolves against that.
func (p *Pipeline) loadSkips(id string) {
	p.rememberEpicSkips()
	p.runSkips = p.resolveSkips(id)
	p.recordSkips(id, p.runSkips)
	if len(p.runSkips) == 0 {
		return
	}
	msg := "pipeline work skipped by the operator for this run: " + state.EncodeSkips(p.runSkips)
	p.logf("  ⏭ %s", msg)
	if p.Events != nil {
		p.Events.Emit(event.KindRunSkips, "", msg, map[string]any{"ticket": id, "skips": p.runSkips})
	}
}

// resolveSkips settles one unit of work's effective set: this run's declared set
// first, then what an earlier attempt recorded on that unit's own checkpoint, and
// last — for a sub-issue of the epic — the set the epic run declared. That last
// fallback is what carries a skip to a child the loop reaches for the first time
// after a restart, since only children that already started have a checkpoint.
// It reads downward only: the release resolves against the epic's checkpoint
// alone, so a set one sub-issue stored can never disarm the epic's CI or merge
// gate.
func (p *Pipeline) resolveSkips(id string) []string {
	if len(p.Skips) > 0 {
		return p.Skips
	}
	if keys := state.DecodeSkips(p.State.Get(id, state.SkipsKey)); len(keys) > 0 {
		return keys
	}
	if p.EpicID == "" || id == p.EpicID {
		return nil
	}
	return state.DecodeSkips(p.State.Get(p.EpicID, state.SkipsKey))
}

// rememberEpicSkips makes an epic run's declared set durable before the release
// opens the epic's row, so a loop killed between children and resumed with no argv
// behind it can still read it for the next child. Only a run that drives the whole
// epic may speak for it: a single sub-issue run that merely builds on the epic
// branch declared its set for that sub-issue alone, and stamping the epic with it
// would lower the bar for every sibling a later run picks up.
func (p *Pipeline) rememberEpicSkips() {
	if p.EpicID == "" || !p.EpicRun {
		return
	}
	p.recordSkips(p.EpicID, p.Skips)
}

// recordSkips puts a resolved set on a unit of work's own checkpoint, so a later
// resume honors it with no argv behind it. An empty set writes nothing — a run
// that skips nothing must not open a checkpoint to say so.
func (p *Pipeline) recordSkips(id string, keys []string) {
	want := state.EncodeSkips(keys)
	if want == "" || p.State.Get(id, state.SkipsKey) == want {
		return
	}
	if err := p.State.Set(id, state.SkipsKey, want); err != nil {
		p.logf("  ⚠ could not record the skipped work on %s's checkpoint (a resume will not honor it): %v", id, err)
	}
}

// skipping reports whether this run bypasses the named work.
func (p *Pipeline) skipping(key string) bool { return slices.Contains(p.runSkips, key) }

// autoMerge reports whether the run may merge its own green PR. Skipping merge
// takes exactly the AUTO_MERGE=0 path: the PR is opened and left for a human.
func (p *Pipeline) autoMerge() bool { return p.AutoMerge && !p.skipping(config.SkipMerge) }

// manualMergeReason names why a run handed its green deliverable to a human, for
// the log line that does the handing over.
func (p *Pipeline) manualMergeReason() string {
	if p.skipping(config.SkipMerge) {
		return "merge skipped for this run"
	}
	return "AUTO_MERGE=0"
}

// skippedVerifyLine is what the PR body's Testing section and the ticket's QA note
// both state in place of verify facts when the operator skipped the Verify Step.
const skippedVerifyLine = "Verification was skipped by the operator for this run — this slice needs manual QA before it merges"

// skipVerify closes the Verify Step for a run that bypasses it. The checkpoint
// still advances to verified, so runPhases goes on to commit/PR and a later resume
// re-enters at the Ship Step rather than at the verify this run already declined.
func (p *Pipeline) skipVerify(id string) error {
	p.logf("  ⏭ verify skipped — no rubric, verdict or proofs are produced for this run")
	if err := p.setPhase(id, state.Verified); err != nil {
		return fmt.Errorf("verify %s: checkpoint verified: %w", id, err)
	}
	return nil
}
