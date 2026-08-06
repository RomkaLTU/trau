package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/RomkaLTU/trau/internal/activity"
	"github.com/RomkaLTU/trau/internal/forge"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// The stacked epic shape (EPIC_STACKED_PRS, opt-in and off by default). Instead of
// fanning every slice into one epic integration branch, child N is cut from child
// N−1's branch, the bottom PR targets the base, and the layers are linked into a
// native GitHub stack. There is no epic branch and no epic PR, and NOTHING merges
// until every layer is green — the epic's finalize then merges the whole stack once,
// from the top.
//
// Merge-once-at-the-end is the load-bearing invariant, not a simplification. A
// partial stack merge makes GitHub retarget the layers above it AND non-fast-forward
// rewrite their head branches (docs/research/stacked-prs-spike.md, Q6) — both things
// trau refuses to do to its own branches. By holding every merge to the end, the base
// under every open PR is a branch trau itself owns and never moves, so there is no
// mid-flight rewrite for a run to absorb. See docs/pr-base-hygiene.md.

// stackDecision memoizes the shape one epic runs in. The decision is made once, at
// the epic's start, and every later call in the run reads this rather than
// re-probing: a flaky probe mid-epic must not strand half a chain by switching the
// epic to the classic flow between two children.
type stackDecision struct {
	epic string
	on   bool
}

// stackLinkMemo holds the chain this run last linked on GitHub, and the epic that
// chain belongs to. The key is what makes the memo self-checking: a Pipeline is
// reused across epics, and a chain linked for the previous one must never answer
// for this one's — so the memo is asked about an epic, not merely read.
type stackLinkMemo struct {
	epic     string
	branches []string
}

// holds reports whether epic's chain is already linked, exactly as branches.
func (m stackLinkMemo) holds(epic string, branches []string) bool {
	return m.epic == epic && slices.Equal(m.branches, branches)
}

// stackLayerMemo holds the branch the ticket in flight is stacked on, so every
// consumer of buildBase during that run answers with the layer the slice was
// actually cut from — never with a chain tip that has since grown to include the
// slice's own branch.
type stackLayerMemo struct {
	base string
}

// stackLayer is one slice of a stacked epic: the ticket, the branch carrying it,
// the branch below it, and the PR chaining the two.
type stackLayer struct {
	ID     string
	Branch string
	Base   string
	PR     string
	PRURL  string
}

// primeStackLayer restores, at the start of a ticket run, the branch that ticket's
// slice was cut from. A resume must keep stacking on the layer it originally chose:
// re-deriving it from the chain's tip would answer with the run's own branch, since
// by then the chain already contains it.
func (p *Pipeline) primeStackLayer(id string) {
	if p.EpicID == "" {
		return
	}
	p.stackLayer = stackLayerMemo{base: p.State.Get(id, "BASE")}
}

// stackedEpic reports whether THIS epic runs as a native GitHub stack. The gates are
// checked once per epic and the verdict is memoized: the shape is settled at epic
// start and never changes mid-epic, because a chain half-built one way cannot be
// finished the other.
func (p *Pipeline) stackedEpic(ctx context.Context) bool {
	if p.EpicID == "" {
		return false
	}
	if p.stackedShape.epic == p.EpicID {
		return p.stackedShape.on
	}
	on := p.resolveStackedShape(ctx)
	p.stackedShape = stackDecision{epic: p.EpicID, on: on}
	return on
}

// resolveStackedShape makes the shape decision for one epic. Every gate that fails
// says so in one line and hands the WHOLE epic to the classic flow — a mode that
// engages for some children and not others is worse than either shape.
func (p *Pipeline) resolveStackedShape(ctx context.Context) bool {
	if why := p.stackedShapeRefusal(ctx); why != "" {
		if p.EpicStackedPRs {
			p.logf("  ⓘ epic %s runs the classic epic flow — %s", p.EpicID, why)
		}
		return false
	}
	p.logf("  ⓘ epic %s runs as a GitHub stack (EPIC_STACKED_PRS=1) — slices chain onto each other and merge once, from the top", p.EpicID)
	return true
}

// stackedShapeRefusal names why this epic cannot run stacked, or "" when it can:
// the flag is off, the repo delivers with no remote at all, its remote is not
// GitHub, an epic branch from a classic run already owns the epic's children, or
// GitHub's stacked-PRs preview does not answer for the repo.
func (p *Pipeline) stackedShapeRefusal(ctx context.Context) string {
	if !p.EpicStackedPRs {
		return "EPIC_STACKED_PRS is off"
	}
	if p.FolderRepo {
		return "a folder repo has no single repository to stack in"
	}
	if p.localDelivery(ctx) {
		return "the repo has no remote, so there are no PRs to stack"
	}
	if f := p.forgeOf(ctx, p.Git, p.Forge); f != forge.GitHub {
		return "stacked PRs are a GitHub feature and this repo is on " + f.Label()
	}
	// An epic branch — here or on the remote, where a run on another machine may
	// have left it — means this epic already fanned out the classic way, and half a
	// classic epic cannot be finished as a stack.
	if epic, _ := p.Git.FindEpicBranch(ctx, p.EpicID); epic != "" {
		return "epic branch " + epic + " already carries this epic's work"
	}
	if epic, err := p.Git.FindRemoteEpicBranch(ctx, p.Remote, p.EpicID); err == nil && epic != "" {
		return "epic branch " + epic + " on " + p.Remote + " already carries this epic's work"
	}
	s, ok := p.stacker()
	if !ok {
		return "this repo's forge has no stacked pull requests"
	}
	enabled, err := s.StacksEnabled(ctx)
	if err != nil {
		return fmt.Sprintf("GitHub's stacked-PRs preview did not answer for this repo (%v)", err)
	}
	if !enabled {
		return "GitHub's stacked-PRs preview is not enabled for this repo"
	}
	return ""
}

// stacker is the run's Delivery seen as a Stacker, false when it delivers to a
// forge with no stacked-pull-request concept. Every caller reads false exactly as
// it reads a failed probe — run the classic shape and merge each PR on its own.
func (p *Pipeline) stacker() (Stacker, bool) {
	s, ok := p.Delivery.(Stacker)
	return s, ok
}

// stackedBase names the branch the slice in flight is layered on: the memo its
// branch cut left behind, else the current tip of the epic's chain — which is the
// configured base while the chain is still empty, so the bottom layer targets the
// base like any ordinary slice.
func (p *Pipeline) stackedBase(ctx context.Context) (string, error) {
	if p.stackLayer.base != "" {
		return p.stackLayer.base, nil
	}
	chain, err := p.stackChain(ctx)
	if err != nil {
		return "", err
	}
	if len(chain) == 0 {
		return p.Base, nil
	}
	return chain[len(chain)-1].Branch, nil
}

// stackedCutRef resolves the ref a new layer's branch is cut from, and records that
// layer's base for the rest of the run. The bottom layer is cut from the base as the
// REMOTE has it (the epic base semantics: a local base that drifted behind would
// bury the remote's commits in the slice's own PR); every layer above is cut from
// the branch below it, fetched from the remote when this machine has never seen it —
// the resume path on a second machine.
func (p *Pipeline) stackedCutRef(ctx context.Context, id string) (string, error) {
	base := p.stackLayer.base
	if base == "" {
		// Rediscover the chain so a later run adopts a partial stack and keeps
		// building on its tip instead of starting a second chain from the base.
		chain, err := p.stackChain(ctx)
		if err != nil {
			return "", fmt.Errorf("build %s: resolve the layer below: %w", id, err)
		}
		chain = p.resolveStackPRs(ctx, chain)
		p.logStackChain(chain)
		base = p.Base
		if len(chain) > 0 {
			base = chain[len(chain)-1].Branch
		}
	}
	p.stackLayer = stackLayerMemo{base: base}
	if base == p.Base {
		return p.epicBaseRef(ctx), nil
	}
	if exists, _ := p.Git.BranchExists(ctx, base); exists {
		return base, nil
	}
	if remote, _ := p.Git.RemoteBranchExists(ctx, p.Remote, base); remote {
		if err := p.Git.CheckoutRemoteBranch(ctx, p.Remote, base); err != nil {
			return "", fmt.Errorf("build %s: adopt the layer below (%s) from %s: %w", id, base, p.Remote, err)
		}
		p.logf("  ↳ layer %s adopted from %s to stack on", base, p.Remote)
		return base, nil
	}
	return "", &GiveUpError{ID: id, Reason: "the layer below (" + base + ") is on neither this machine nor " + p.Remote + ", so " + id + " has nothing to stack on"}
}

// stackChain walks the epic's layers bottom-first, from the base upward: each layer
// is the child whose recorded base is the branch of the layer below it. It reads the
// run's own checkpoints, which is what makes a partial chain adoptable by a later
// run — the walk stops at the first missing link rather than guessing past it.
func (p *Pipeline) stackChain(ctx context.Context) ([]stackLayer, error) {
	subs, err := p.Tracker.SubIssues(ctx, p.EpicID)
	if err != nil {
		return nil, fmt.Errorf("read the children of %s: %w", p.EpicID, err)
	}
	above := make(map[string]stackLayer, len(subs))
	for _, sub := range subs {
		layer := stackLayer{
			ID:     sub.ID,
			Branch: p.State.Get(sub.ID, "BRANCH"),
			Base:   p.State.Get(sub.ID, "BASE"),
			PR:     p.State.Get(sub.ID, "PR"),
			PRURL:  p.State.Get(sub.ID, "PR_URL"),
		}
		if layer.Branch == "" || layer.Base == "" || layer.Branch == layer.Base {
			continue
		}
		if _, taken := above[layer.Base]; taken {
			// Two children claiming the same base is not a chain; the first one
			// found keeps the slot rather than the walk forking arbitrarily.
			continue
		}
		above[layer.Base] = layer
	}
	chain := make([]stackLayer, 0, len(above))
	// The walk places every layer at most once: it stops the second time it stands
	// on a base it has already stepped off, which is what checkpoints describing a
	// cycle look like from here. Re-appending a layer would leave the wrong branch
	// as the top PR the whole stack merges from — silently, since a cycle reads as
	// a longer chain, not as an error.
	//
	// The length bound is the same statement counted rather than remembered — a
	// chain can be no longer than the layers it is built from — and is strict for
	// the same reason. Counting alone would not do: a child stacked on a branch
	// this walk never reaches raises len(above) without lengthening any chain, and
	// hands a cycle exactly the extra step the strict bound took away.
	placed := make(map[string]bool, len(above))
	for cur := p.Base; len(chain) < len(above) && !placed[cur]; {
		next, ok := above[cur]
		if !ok {
			break
		}
		placed[cur] = true
		chain = append(chain, next)
		cur = next.Branch
	}
	return chain, nil
}

// resolveStackPRs fills in the PR each layer carries where the checkpoint has none —
// a run that died between opening a PR and recording it — by asking GitHub for the
// open PR on that layer's branch.
func (p *Pipeline) resolveStackPRs(ctx context.Context, chain []stackLayer) []stackLayer {
	out := make([]stackLayer, 0, len(chain))
	for _, layer := range chain {
		if layer.PR == "" {
			if url, _ := p.Delivery.PRURL(ctx, layer.Branch); url != "" {
				layer.PR, layer.PRURL = prNumber(url), url
			}
		}
		out = append(out, layer)
	}
	return out
}

// logStackChain records the chain a walk read back, so the topology does not have
// to be reconstructed from branch names. It names what the chain is rather than
// what was adopted: the same walk runs on a straight-through epic, where every
// layer it names is one this run just built.
func (p *Pipeline) logStackChain(chain []stackLayer) {
	if len(chain) == 0 {
		return
	}
	parts := make([]string, 0, len(chain))
	for _, layer := range chain {
		part := layer.ID + " (" + layer.Branch
		if layer.PR != "" {
			part += ", PR #" + layer.PR
		} else {
			part += ", no PR yet"
		}
		parts = append(parts, part+")")
	}
	p.logf("  ↳ epic %s stack: %d layer(s) — %s", p.EpicID, len(chain), strings.Join(parts, " → "))
}

// assertStackedBaseCurrent is the freshness gate in the stacked shape. A layer above
// the bottom targets a branch trau owns and which cannot move before finalize, so the
// sibling-squash drift the gate exists for cannot happen there; what is left to prove
// — that the remote carries the base at the commit this layer was cut from — is the
// ordinary assertion the bottom layer needs anyway.
func (p *Pipeline) assertStackedBaseCurrent(ctx context.Context, id, base string) error {
	return p.assertPRBaseCurrent(ctx, p.Git, base, p.prBasePin(id))
}

// linkStackLayer chains a freshly opened slice PR into the epic's stack. Linking is
// additive, so the whole chain is re-linked each time it grows by a layer, and a
// failure only warns: the branches are already chained, and the finalize re-links
// before it merges.
func (p *Pipeline) linkStackLayer(ctx context.Context, id, branch string) {
	if branch == "" || !p.stackedEpic(ctx) {
		return
	}
	chain, err := p.stackChain(ctx)
	if err != nil {
		p.logf("  ⚠ couldn't read the stack to link %s into it (continuing): %v", id, err)
		return
	}
	branches := stackBranches(chain, branch, p.stackLayer.base)
	if len(branches) < 2 {
		return
	}
	if err := p.linkStack(ctx, branches); err != nil {
		p.logf("  ⚠ couldn't link %s into the stack (continuing — the finalize links it again): %v", id, err)
	}
}

// linkStack links a chain bottom-first, with the transient-retry guard every other
// gh call goes through, and announces the chains it really sent. A layer passes
// here twice — once when its PR opens, once when it completes — so a chain
// identical to the last one linked for this epic is skipped, log included. A
// failed link leaves the memo alone, which keeps the finalize's re-link the
// repair it is meant to be.
func (p *Pipeline) linkStack(ctx context.Context, branches []string) error {
	if p.stackLinked.holds(p.EpicID, branches) {
		return nil
	}
	s, ok := p.stacker()
	if !ok {
		return nil
	}
	err := p.retryGH(ctx, "gh stack link", func() error {
		return s.LinkStack(ctx, branches)
	})
	if err != nil {
		return err
	}
	p.stackLinked = stackLinkMemo{epic: p.EpicID, branches: slices.Clone(branches)}
	p.logf("  ⛁ stack linked: %s", strings.Join(branches, " → "))
	return nil
}

// stackBranches lists the chain's branches bottom-first, appending branch when the
// walk has not caught up with it yet — the PR is opened before the layer's own
// checkpoint is complete, so the run's current slice may not be in the chain it
// reads back.
func stackBranches(chain []stackLayer, branch, base string) []string {
	branches := make([]string, 0, len(chain)+1)
	for _, layer := range chain {
		branches = append(branches, layer.Branch)
	}
	if len(branches) > 0 && branches[len(branches)-1] == branch {
		return branches
	}
	if base != "" && len(branches) > 0 && branches[len(branches)-1] != base {
		// The walk ended somewhere other than the branch this slice was cut from,
		// so the chain it read is not this slice's chain — link nothing rather than
		// declaring a stack that does not exist.
		return nil
	}
	return append(branches, branch)
}

// completeStackedLayer settles a child of a stacked epic. The layer is done when its
// PR is open, linked into the stack and green — layer N's branch contains layers
// 1..N, so its CI is cumulative by construction — and it does NOT merge: the epic's
// finalize merges every layer at once. The ticket still closes so the epic's own
// gate can see the child as delivered, and its PR reads as awaiting the merge it is
// genuinely still waiting for.
func (p *Pipeline) completeStackedLayer(ctx context.Context, id, pr string) error {
	p.linkStackLayer(ctx, id, p.State.Get(id, "BRANCH"))
	if err := p.markDone(ctx, id, "  ✓ %s is green and stacked — it merges with the stack"); err != nil {
		return err
	}
	p.setPRStatus(id, prStatusAwaitingMerge)
	return nil
}

// finalizeStackedEpic ships a stacked epic: it replaces the classic epic PR's gate
// and merge. Every layer must be open and green — an upper layer's checks say
// nothing about the layers below it, so each one is polled — and only then does the
// whole stack merge, once, from the top PR. Under AUTO_MERGE=0 the stack is left
// standing and the finalize waits for the operator to merge it, mirroring the
// ticket-level manual-merge wait.
func (p *Pipeline) finalizeStackedEpic(ctx context.Context) error {
	chain, err := p.stackChain(ctx)
	if err != nil {
		return fmt.Errorf("finalize epic %s: read the stack: %w", p.EpicID, err)
	}
	chain = p.resolveStackPRs(ctx, chain)
	p.logStackChain(chain)
	if len(chain) == 0 {
		return p.handOffEpic("no chained slice PR was ever recorded for it, so there is no stack to merge", "")
	}
	for _, layer := range chain {
		if err := p.assertLayerShippable(ctx, layer); err != nil {
			return err
		}
	}
	top := chain[len(chain)-1]
	// Re-link before merging: a chain a child run failed to link is repaired here.
	// A chain this run already linked is skipped by linkStack's memo — no call goes
	// out, so a stack someone unstacked on GitHub after that link is NOT re-linked;
	// the merge then fails with gh's exit 2, which says exactly that.
	if err := p.linkStack(ctx, stackBranches(chain, top.Branch, top.Base)); err != nil {
		p.logf("  ⚠ couldn't re-link the stack before merging (continuing): %v", err)
	}
	if !p.autoMerge() {
		return p.awaitStackMerge(ctx, chain, top)
	}
	p.setActivity(p.EpicID, activity.Merge, "")
	if err := p.mergeStack(ctx, top); err != nil {
		return err
	}
	p.markLayersMerged(chain)
	p.checkpointEpicMerged(ctx, top.PRURL)
	p.logf("  ✓ epic %s merged to %s — %d layer(s) via the stack on %s", p.EpicID, p.Base, len(chain), top.PRURL)
	extra := fmt.Sprintf("All direct sub-issues are delivered. The stack of %d slice PR(s) merged to %s via %s.", len(chain), p.Base, top.PRURL)
	if err := p.Tracker.SetStatus(ctx, p.EpicID, tracker.StageDone, extra); err != nil {
		return fmt.Errorf("finalize epic %s: close epic: %w", p.EpicID, err)
	}
	p.logf("  ✓ epic %s closed", p.EpicID)
	p.reloadHubOntoBase(ctx)
	return nil
}

// assertLayerShippable refuses to merge a stack a layer is not ready for: a layer
// with no PR at all, one closed without merging, or one whose CI is not green. Each
// ends the finalize as a hand-off, leaving the whole stack standing for a human —
// merging around a bad layer is exactly the partial merge this shape exists to
// avoid.
func (p *Pipeline) assertLayerShippable(ctx context.Context, layer stackLayer) error {
	if layer.PR == "" {
		return p.handOffEpic("layer "+layer.ID+" ("+layer.Branch+") has no PR, so the stack is incomplete", "")
	}
	switch st, _ := p.Delivery.PRState(ctx, layer.PR); st {
	case "MERGED":
		// A human merged part of the stack by hand. Nothing left to gate on this
		// layer, and the top merge lands whatever is still open.
		p.logf("  ⓘ stack layer #%s (%s) already merged", layer.PR, layer.ID)
		return nil
	case "CLOSED":
		return p.handOffEpic("layer PR #"+layer.PR+" ("+layer.ID+") was closed without merging, so the stack cannot ship as it stands", layer.PRURL)
	}
	p.setActivity(p.EpicID, activity.CIWait, "")
	if err := p.pollCI(ctx, layer.PR, layer.Base); err != nil {
		p.logf("  ✗ stack layer #%s (%s): %v", layer.PR, layer.ID, err)
		return p.handOffEpic("layer PR #"+layer.PR+" ("+layer.ID+") is not green, and no layer merges until every layer is", layer.PRURL)
	}
	return nil
}

// mergeStack merges the whole stack from its top PR. It is attempted once, not
// through the transient-retry guard: a stack merge is all-or-nothing, so a failure
// leaves the stack exactly as it was, and every documented failure is deterministic.
// A top PR another actor already merged short-circuits, so re-running the finalize
// adopts that merge rather than repeating it.
func (p *Pipeline) mergeStack(ctx context.Context, top stackLayer) error {
	if st, _ := p.Delivery.PRState(ctx, top.PR); st == "MERGED" {
		return nil
	}
	s, ok := p.stacker()
	if !ok {
		return nil
	}
	err := s.MergeStack(ctx, top.PR, p.MergeMethod)
	if err == nil {
		return nil
	}
	p.markEpicAwaitingHuman()
	return &GiveUpError{ID: p.EpicID, Reason: stackMergeReason(top.PR, err)}
}

// stackMergeReason names what a failed stack merge means, from gh's exit status:
// 2 is the top PR no longer being in a stack, 3 a rebase conflict, 4 an API
// failure, 5 a stack or merge method GitHub will not take, 9 the stacked-PRs
// feature no longer answering. The codes are gh-stack's own (v0.1.0
// cmd/utils.go). Anything else keeps gh's own message rather than inventing a
// cause.
func stackMergeReason(pr string, err error) string {
	prefix := "stack merge of PR #" + pr + " failed: "
	var merr *StackMergeError
	if !errors.As(err, &merr) {
		return prefix + err.Error()
	}
	switch merr.Code {
	case 2:
		return prefix + "GitHub no longer reads PR #" + pr + " as part of a stack — someone unstacked it, so the slice PRs have to be merged by hand, bottom first"
	case 3:
		return prefix + "GitHub could not rebase the stack onto the base — resolve the conflict and merge the stack yourself (trau runs no automated rebase repair)"
	case 4:
		return prefix + "GitHub's API rejected it — " + merr.Error()
	case 5:
		return prefix + "GitHub refused the merge as asked for — a layer is unmergeable, or the repo does not allow this MERGE_METHOD — " + merr.Error()
	case 9:
		return prefix + "GitHub's stacked-PRs preview is no longer available for this repo, so the stack has to be merged by hand"
	}
	return prefix + merr.Error()
}

// awaitStackMerge is the AUTO_MERGE=0 path: the stack is green and left standing
// for the operator to merge. It polls every layer until each reads merged — a stack
// merge lands one commit per layer and every layer reports its own merged state —
// mirroring the ticket-level manual-merge wait, including its notification.
func (p *Pipeline) awaitStackMerge(ctx context.Context, chain []stackLayer, top stackLayer) error {
	p.markEpicAwaitingHuman()
	p.setPRStatus(p.EpicID, prStatusAwaitingMerge)
	_ = p.State.Set(p.EpicID, "PR", top.PR)
	_ = p.State.Set(p.EpicID, "PR_URL", top.PRURL)
	p.setActivity(p.EpicID, activity.MergeWait, "")
	p.logf("  ⏳ epic %s: every layer is green — merge the stack from PR #%s yourself (%s)", p.EpicID, top.PR, p.manualMergeReason())
	p.emitEpicAwaitingMerge(p.manualMergeReason()+" — the whole stack is yours to merge from its top PR", top.PRURL)
	for {
		if p.stackMerged(ctx, chain) {
			p.markLayersMerged(chain)
			p.checkpointEpicMerged(ctx, top.PRURL)
			extra := "All direct sub-issues are delivered. The stack merged to " + p.Base + " via " + top.PRURL + "."
			if err := p.Tracker.SetStatus(ctx, p.EpicID, tracker.StageDone, extra); err != nil {
				return fmt.Errorf("finalize epic %s: close epic: %w", p.EpicID, err)
			}
			p.logf("  ✓ epic %s closed; the stack merged to %s", p.EpicID, p.Base)
			p.reloadHubOntoBase(ctx)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p.sleep(p.CIPoll)
	}
}

// markLayersMerged settles the layers' PR status once the stack has landed. Each
// child completed with its PR still awaiting this merge, which was the truth then
// and is stale now — leaving it would show every delivered slice as still waiting.
func (p *Pipeline) markLayersMerged(chain []stackLayer) {
	for _, layer := range chain {
		p.setPRStatus(layer.ID, prStatusMerged)
	}
}

// stackMerged reports whether every layer of the stack now reads merged. Anything
// short of that — a layer still open, one closed without merging, a state gh could
// not read — keeps the wait going, rather than declaring a delivery the base never
// received.
func (p *Pipeline) stackMerged(ctx context.Context, chain []stackLayer) bool {
	for _, layer := range chain {
		if st, _ := p.Delivery.PRState(ctx, layer.PR); st != "MERGED" {
			return false
		}
	}
	return true
}
