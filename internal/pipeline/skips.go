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
// there, so a resume honors the same skips with no argv behind it.
func (p *Pipeline) loadSkips(id string) {
	p.runSkips = p.Skips
	if len(p.Skips) == 0 {
		p.runSkips = state.DecodeSkips(p.State.Get(id, state.SkipsKey))
	} else if err := p.State.Set(id, state.SkipsKey, state.EncodeSkips(p.Skips)); err != nil {
		p.logf("  ⚠ could not record the skipped work on %s's checkpoint (a resume will not honor it): %v", id, err)
	}
	if len(p.runSkips) == 0 {
		return
	}
	msg := "pipeline work skipped by the operator for this run: " + state.EncodeSkips(p.runSkips)
	p.logf("  ⏭ %s", msg)
	if p.Events != nil {
		p.Events.Emit(event.KindRunSkips, "", msg, map[string]any{"ticket": id, "skips": p.runSkips})
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
