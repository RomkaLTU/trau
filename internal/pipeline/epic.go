package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/RomkaLTU/trau/internal/activity"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/prompts"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

func (p *Pipeline) epicBranchName(ctx context.Context) (string, error) {
	if p.exit.epicBranch != "" {
		return p.exit.epicBranch, nil
	}

	// Resolve deterministically by epic ID, never by the drift-prone title slug. Any
	// existing epic/<ID>-* branch IS the epic branch and is adopted as-is — local
	// first, then the remote (a fresh clone or a different machine). The title slug
	// only names a brand-new branch on the very first creation. Matching on the slug
	// instead would let a renamed Linear epic spawn a SECOND branch that orphans the
	// children's integration work.
	if branch, _ := p.Git.FindEpicBranch(ctx, p.EpicID); branch != "" {
		p.exit.epicBranch = branch
		return branch, nil
	}

	if !p.localDelivery(ctx) {
		remote, rerr := p.Git.FindRemoteEpicBranch(ctx, p.Remote, p.EpicID)
		if rerr != nil {
			// An indeterminate remote must NOT fall through to creating a duplicate.
			return "", fmt.Errorf("resolve epic branch for %s: check remote: %w", p.EpicID, rerr)
		}
		if remote != "" {
			if err := p.Git.CheckoutRemoteBranch(ctx, p.Remote, remote); err != nil {
				return "", fmt.Errorf("resolve epic branch %s: adopt from %s: %w", remote, p.Remote, err)
			}
			p.logf("  epic branch %s adopted from %s", remote, p.Remote)
			p.exit.epicBranch = remote
			return remote, nil
		}
	}

	title, err := p.Tracker.Title(ctx, p.EpicID)
	if err != nil {
		p.logf("  epic title lookup error (using id-only branch): %v", err)
	}
	branch := epicBranch(p.EpicID, title)
	base := p.epicBaseRef(ctx)
	if err := p.Git.CreateBranch(ctx, branch, base); err != nil {
		return "", &GiveUpError{ID: p.EpicID, Reason: "could not create epic branch for " + p.EpicID}
	}
	p.logf("  epic branch %s ← %s", branch, base)
	if !p.localDelivery(ctx) {
		err := p.Git.Push(ctx, p.Remote, branch, false)
		if err != nil {
			p.logf("  push epic branch error (continuing): %v", err)
		}
		p.exit.epicPushed = err == nil
	}
	p.exit.epicBranch = branch
	return branch, nil
}

func epicBranch(id, title string) string {
	if slug := slugify(title); slug != "" {
		return "epic/" + id + "-" + slug
	}
	return "epic/" + id
}

// epicBaseRef names the ref a brand-new epic branch is cut from: the base branch as
// the remote has it, fetched first. An epic is a long-lived integration branch every
// child stacks on, so cutting it from a local base that drifted behind the remote
// starts it behind those commits — each child PR then diffs against them as if they
// were the child's own work. Only a remote tip that cannot be resolved at all falls
// back to the local ref.
func (p *Pipeline) epicBaseRef(ctx context.Context) string {
	if p.localDelivery(ctx) {
		return p.baseRef()
	}
	// A fetch exits 0 without creating the tracking ref when the clone's refspec
	// does not cover the base, so the tip is proven resolvable on both paths.
	fetched := p.fetchBaseTip(ctx)
	tip := p.remoteBaseTip(ctx)
	if tip == "" {
		p.logf("  ⚠ %s/%s could not be resolved — cutting the epic from the local %s", p.Remote, p.Base, p.Base)
		return p.baseRef()
	}
	if !fetched {
		p.logf("  fetch %s failed — cutting the epic from the last known remote tip", tip)
	}
	return tip
}

// epicWorkBase names the ref the epic branch's own commits are counted against: the
// remote tip epicBaseRef cuts a new epic branch from, so the commits the branch was
// merely cut on top of never read as work the epic carries. It reads the tracking ref
// the cut already used rather than fetching again, and falls back to the local base
// when that ref resolves to nothing.
func (p *Pipeline) epicWorkBase(ctx context.Context) string {
	if tip := p.remoteBaseTip(ctx); tip != "" {
		return tip
	}
	return p.baseRef()
}

// remoteBaseTip names the base branch as the remote-tracking ref has it, or "" when
// there is no such ref to read: local delivery, or a clone whose refspec never covered
// the base.
func (p *Pipeline) remoteBaseTip(ctx context.Context) string {
	if p.localDelivery(ctx) {
		return ""
	}
	tip := p.Remote + "/" + p.Base
	if known, _ := p.Git.ResolvesToCommit(ctx, tip); !known {
		return ""
	}
	return tip
}

// ensureEpicPR returns the epic branch's open PR, opening one when it has none.
// draft opens it as a draft: exit hygiene tracks an unfinished epic that way, and
// the finalize that later ships the epic adopts that same PR and marks it ready.
func (p *Pipeline) ensureEpicPR(ctx context.Context, epicBranch string, draft bool) (string, error) {
	prURL, _ := p.Delivery.PRURL(ctx, epicBranch)
	if prURL != "" {
		return prURL, nil
	}

	if err := p.assertPRBaseCurrent(ctx, p.Git, p.Base, p.baseRef()); err != nil {
		return "", err
	}
	title, err := p.Tracker.Title(ctx, p.EpicID)
	if err != nil {
		title = p.EpicID
	}
	prURL, err = p.Delivery.CreatePR(ctx, p.Base, epicBranch, epicPRTitle(p.EpicID, title), p.epicPRBody(p.EpicID), draft)
	if err != nil {
		if strings.Contains(err.Error(), "No commits between") {
			if merged, _ := p.Delivery.MergedPRURL(ctx, epicBranch); merged != "" {
				p.logf("  epic PR already merged %s", merged)
				return merged, nil
			}
		}
		return "", err
	}
	p.logf("  epic PR %s", prURL)
	return prURL, nil
}

// epicPRTitle builds the epic PR's Conventional-Commit-style header —
// 'epic(<id>): <subject>' — so the squash of epic→main lands as a conventional
// subject. Tracker titles already carrying an "Epic:" prefix are stripped first
// so the header never stacks two markers, and the subject is case-conformed and
// truncated like a deterministic commit subject.
func epicPRTitle(id, title string) string {
	t := strings.TrimSpace(title)
	for {
		rest, ok := strings.CutPrefix(t, "Epic:")
		if !ok {
			rest, ok = strings.CutPrefix(t, "epic:")
		}
		if !ok {
			break
		}
		t = strings.TrimSpace(rest)
	}
	subject := strings.TrimRight(conformSubjectCase(commitSubject(t)), ".")
	if subject == "" {
		subject = id
	}
	return "epic(" + id + "): " + subject
}

// FinalizeEpic ships the epic only after every direct child is terminal. It is
// intentionally a loop-level finalizer, not part of a child merge: a child PR can
// land while siblings are still open, but the parent must not be shipped to main
// until the tracker confirms the whole child set is complete. Once it is, the epic
// branch is synced with the base (drift conflicts resolved by an agent), the epic
// PR is opened/adopted, its CI is gated with a bounded repair loop, and — when the
// run may merge — it is squash-merged to the base before the Linear epic closes.
// While any child still reads open it declines with an *EpicUnfinalizedError, and
// a release it ran out of moves on ends in an *EpicHandOffError, so the caller
// parks the epic either way instead of mistaking the outcome for a delivery.
func (p *Pipeline) FinalizeEpic(ctx context.Context) error {
	if p.EpicID == "" {
		return nil
	}
	statuser, ok := p.Tracker.(tracker.IssueStatuser)
	if !ok {
		p.logf("  epic close skipped — tracker cannot report child issue status")
		return nil
	}
	subs, err := p.Tracker.SubIssues(ctx, p.EpicID)
	if err != nil {
		return fmt.Errorf("finalize epic %s: list sub-issues: %w", p.EpicID, err)
	}
	if len(subs) == 0 {
		return nil
	}
	open, regressed, err := p.openSubIssues(ctx, statuser, subs)
	if err != nil {
		return err
	}
	for _, id := range regressed {
		p.reassertDone(ctx, id)
	}
	if len(open) > 0 {
		p.logf("  epic %s still open — waiting on %s", p.EpicID, strings.Join(open, ", "))
		return &EpicUnfinalizedError{EpicID: p.EpicID, Open: open}
	}
	p.checkpointEpicReleasing(ctx)
	// The release is its own unit of work, so it re-resolves the skip set against
	// the epic rather than inheriting whatever the last sub-issue resolved — a skip
	// a child read back from its own checkpoint must never disarm the epic's CI or
	// merge gate. Resolved here rather than at entry so a finalize that declines
	// (children still open) leaves the epic's row exactly as it found it.
	p.loadSkips(p.EpicID)
	p.abortHalfMerge(ctx)

	// A stacked epic has no epic branch and no epic PR to resolve: its layers are
	// already open PRs chained onto each other, and shipping it is one merge of the
	// whole stack. Asked before epicBranchName, which would create the very branch
	// this shape exists to do without.
	if p.stackedEpic(ctx) {
		return p.finalizeStackedEpic(ctx)
	}

	epic, err := p.epicBranchName(ctx)
	if err != nil {
		return fmt.Errorf("finalize epic %s: resolve branch: %w", p.EpicID, err)
	}
	if p.localDelivery(ctx) {
		return p.finalizeEpicLocally(ctx, epic)
	}
	synced, err := p.syncEpicForMerge(ctx, epic)
	if err != nil {
		return fmt.Errorf("finalize epic %s: sync with %s: %w", p.EpicID, p.Base, err)
	}
	prURL, err := p.ensureEpicPR(ctx, epic, false)
	if err != nil {
		return fmt.Errorf("finalize epic %s: create PR: %w", p.EpicID, err)
	}
	if !synced {
		p.logf("  ⚠ epic %s still conflicts with %s — PR left for manual resolution: %s", p.EpicID, p.Base, prURL)
		return p.handOffEpic("conflicts with "+p.Base+" that no repair attempt could resolve", prURL)
	}

	merged, err := p.epicCIAndMerge(ctx, prURL)
	if err != nil {
		return fmt.Errorf("finalize epic %s: ship: %w", p.EpicID, err)
	}

	// An epic PR nobody merged has shipped nothing, so the epic ticket goes to
	// review beside it rather than closing on work still sitting in a branch.
	if !merged {
		extra := "All direct sub-issues are delivered. Epic PR ready for review: " + prURL + "."
		if err := p.Tracker.SetStatus(ctx, p.EpicID, tracker.StageInReview, extra); err != nil {
			return fmt.Errorf("finalize epic %s: mark epic in review: %w", p.EpicID, err)
		}
		p.logf("  ⏳ epic %s left open — PR awaiting review: %s", p.EpicID, prURL)
		return p.handOffEpic("CI never went green", prURL)
	}

	extra := "All direct sub-issues are delivered. Epic merged to " + p.Base + " via " + prURL + "."
	if err := p.Tracker.SetStatus(ctx, p.EpicID, tracker.StageDone, extra); err != nil {
		return fmt.Errorf("finalize epic %s: close epic: %w", p.EpicID, err)
	}
	p.logf("  ✓ epic %s closed; PR %s", p.EpicID, prURL)
	p.reloadHubOntoBase(ctx)
	return nil
}

// finalizeEpicLocally ships a complete epic on a remote-less repo: with no PR to
// open and no CI to gate, the epic branch is squash-merged into the base right here
// and the tracker epic closes on that. A run that may not merge still leaves the
// epic to the operator, so it stays open on its branch until they land it — there
// is no PR to poll, so this cannot block on the merge the way the remote path
// does. A merge git refuses leaves the epic branch untouched for a human, exactly
// as an unresolvable drift conflict does on the remote path.
func (p *Pipeline) finalizeEpicLocally(ctx context.Context, epic string) error {
	if !p.autoMerge() {
		p.logf("  ⏳ epic %s is complete on %s — merge it into %s yourself (%s)", p.EpicID, epic, p.Base, p.manualMergeReason())
		return p.handOffEpic(p.manualMergeReason()+" — "+epic+" is yours to merge into "+p.Base, "")
	}
	if err := p.Git.Checkout(ctx, p.Base, false); err != nil {
		return fmt.Errorf("finalize epic %s: checkout %s: %w", p.EpicID, p.Base, err)
	}
	title, err := p.Tracker.Title(ctx, p.EpicID)
	if err != nil {
		p.logf("  epic title lookup error (using id-only subject): %v", err)
	}
	if err := p.Git.SquashMerge(ctx, epic, epicPRTitle(p.EpicID, title)); err != nil {
		p.logf("  ⚠ epic %s could not be merged into %s (%v) — branch left for manual resolution", p.EpicID, p.Base, err)
		return p.handOffEpic(epic+" could not be merged into "+p.Base, "")
	}
	p.checkpointEpicMerged(ctx, "")
	_ = p.State.Set(p.EpicID, "DELIVERY", deliveryLocal)
	extra := "All direct sub-issues are delivered. Epic squash-merged into " + p.Base + " — " + localDeliveryNote + "."
	if err := p.Tracker.SetStatus(ctx, p.EpicID, tracker.StageDone, extra); err != nil {
		return fmt.Errorf("finalize epic %s: close epic: %w", p.EpicID, err)
	}
	p.logf("  ✓ epic %s closed; merged into %s locally", p.EpicID, p.Base)
	p.reloadHubOntoBase(ctx)
	return nil
}

// syncEpicBest keeps the epic branch current between children: the local epic is
// first fast-forwarded from the REMOTE epic — siblings squash-merge into the
// remote, so a stale local epic would hand the next child a base missing that
// (squashed) work, tempting its build agent to merge the sibling's raw feature
// branch and poisoning the child's PR with commits the epic only ever contains in
// squashed form (a guaranteed merge conflict). Then a clean merge of the remote
// base is pushed so the next child branches off an up-to-date epic. A conflicting
// merge is aborted and deferred to the authoritative finalize sync (which runs a
// resolving agent).
//
// The sync is transactional: the merge of the base is a local commit, and a push
// that does not land leaves the local epic ahead of the remote one — a divergence
// no later run can reconcile, since pull --ff-only refuses and every child is then
// pinned to a fork point the remote never gets. So the pre-merge tip is recorded, a
// failed push rolls the local epic back to it, and the pull→merge→push runs once
// more for the race where a sibling landed between the pull and the push.
//
// Best-effort by design with one exception: an epic that has already diverged with
// real local-only work is fatal here (see reconcileEpicWithRemote), because cutting
// a slice from it wedges that slice.
func (p *Pipeline) syncEpicBest(ctx context.Context, epic string) error {
	if p.localDelivery(ctx) {
		return nil
	}
	if err := p.Git.Checkout(ctx, epic, false); err != nil {
		p.logf("  epic sync skipped (checkout %s: %v)", epic, err)
		return nil
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := p.Git.Pull(ctx, p.Remote, epic); err != nil {
			p.logf("  epic pull from %s skipped (%v)", p.Remote, err)
			if err := p.reconcileEpicWithRemote(ctx, epic); err != nil {
				return err
			}
		}
		tip, err := p.Git.HeadSHA(ctx)
		if err != nil || tip == "" {
			p.logf("  epic sync skipped (read %s tip: %v) — an unpushed merge could not be rolled back", epic, err)
			return nil
		}
		if pushed, done := p.mergeBaseIntoEpic(ctx, epic); pushed || done {
			return nil
		}
		if err := p.Git.ResetHard(ctx, tip); err != nil {
			p.logf("  ⚠ %s could not be rolled back onto %s (%v) — the local epic may be ahead of %s", epic, tip, err, p.Remote)
			return nil
		}
		p.logf("  ↺ unpushed sync merge rolled back — %s is level with %s/%s again", epic, p.Remote, epic)
	}
	return nil
}

// mergeBaseIntoEpic merges the remote base into the checked-out epic and pushes the
// result. pushed reports whether the sync landed on the remote; done reports that
// the merge never committed anything, so the caller has nothing to roll back and no
// reason to retry — a merge that failed outright, or a conflicted merge, which is
// aborted here and left to the finalize sync (it runs a resolving agent).
func (p *Pipeline) mergeBaseIntoEpic(ctx context.Context, epic string) (pushed, done bool) {
	conflicted, err := p.Git.MergeRemote(ctx, p.Remote, p.Base)
	switch {
	case err != nil:
		p.logf("  epic sync skipped (merge %s: %v)", p.Base, err)
		return false, true
	case conflicted:
		_ = p.Git.MergeAbort(ctx)
		p.logf("  epic %s conflicts with %s — deferring resolution to epic finalize", epic, p.Base)
		return false, true
	}
	if err := p.Git.Push(ctx, p.Remote, epic, false); err != nil {
		p.logf("  push synced epic branch error (continuing): %v", err)
		return false, false
	}
	return true, false
}

// reconcileEpicWithRemote answers what a refused pull --ff-only means, at the one
// moment the answer is still cheap: before the next slice is cut from the epic.
//
//   - Behind, level or ahead: best-effort as before — the sync push that follows
//     repairs an ahead branch.
//   - Diverged with merge commits only: everything the local copy alone carries is a
//     sync merge trau made itself, or the base work that merge brought over, so the
//     local epic is reset onto the remote tip — where the siblings' squash merges
//     live — and the sync continues, merging the base again right after.
//   - Diverged with real local-only work: fatal. Nothing may drop that work and trau
//     never force-pushes a shared branch, so the run stops here as a resumable fault
//     instead of building a whole slice that the commit/PR gate must then refuse.
//
// A remote that cannot be read is indeterminate, never fatal — the commit/PR gate
// stays the hard guarantee. Established divergence that cannot be classified or
// healed is unsafe: proceeding would only move the same failure to the end of the
// build.
func (p *Pipeline) reconcileEpicWithRemote(ctx context.Context, epic string) error {
	tip, err := p.remoteTip(ctx, p.Git, epic)
	if err != nil {
		p.logf("  epic divergence check skipped (%v)", err)
		return nil
	}
	if tip == "" {
		return nil
	}
	behind, err := p.Git.IsAncestor(ctx, epic, tip)
	if err != nil {
		p.logf("  epic divergence check skipped (%v)", err)
		return nil
	}
	if behind {
		return nil
	}
	ahead, err := p.Git.IsAncestor(ctx, tip, epic)
	if err != nil {
		p.logf("  epic divergence check skipped (%v)", err)
		return nil
	}
	if ahead {
		return nil
	}
	// The base's own commits are reachable through a sync merge's second parent, so
	// they sit in tip..epic whenever the swallowed push was a merge that brought
	// something in — which is every merge this sync makes, since a merge that changed
	// nothing is a fast-forward and commits nothing. Excluding the base is therefore
	// what makes "local-only" mean work rather than the base the merge pulled over.
	local, err := p.Git.RevListNoMerges(ctx, tip, epic, p.remoteBaseTip(ctx))
	if err != nil {
		return errors.New(divergedEpicNote(p.Remote, epic, tip,
			fmt.Sprintf("Trau could not list the local-only commits: %v.", err)))
	}
	if len(local) > 0 {
		return errors.New(divergedEpicNote(p.Remote, epic, tip,
			"The local branch carries work the remote does not have:\n  "+strings.Join(local, "\n  ")))
	}
	if err := p.Git.ResetHard(ctx, tip); err != nil {
		return errors.New(divergedEpicNote(p.Remote, epic, tip,
			fmt.Sprintf("Every local-only commit is a disposable sync merge, but the reset onto the remote tip failed: %v.", err)))
	}
	p.logf("  ↺ %s had diverged from %s/%s with sync merges only — reset onto %s", epic, p.Remote, epic, tip)
	return nil
}

// divergedEpicNote tells the operator why the run stopped before it cut a slice and
// how to repair the branch by hand. blocker is one or more lines naming what is in
// the way of the automatic heal.
func divergedEpicNote(remote, epic, tip, blocker string) string {
	return fmt.Sprintf(
		"epic branch %s has diverged from %s/%s (remote tip %s).\n%s\nA slice cut from this branch would be pinned to a commit %s never carries, so the run stops before the branch is cut.\n%s",
		epic, remote, epic, tip, blocker, remote, baseRecoverySteps(remote, epic),
	)
}

// assertEpicBaseCurrent makes the remote epic branch carry the commit the run was
// cut from, before a child PR is opened against it. syncEpicBest pushes the synced
// epic best-effort, which is right mid-run but not here: a push that never landed
// leaves the remote epic behind the recorded fork point, and GitHub then diffs the
// child against a base older than the branch point — burying the base's own drift in
// the child's PR as thousands of foreign lines. A remote a push can repair is
// repaired; one that still misses the fork point fails the phase.
//
// Classic epics only. A stacked epic has no epic branch to gate — its children
// target the slice branch below them — and answers with assertStackedBaseCurrent.
func (p *Pipeline) assertEpicBaseCurrent(ctx context.Context, id, epic string) error {
	pin := p.State.Get(id, "BASE_SHA")
	if pin == "" {
		return nil
	}
	return p.assertPRBaseCurrent(ctx, p.Git, epic, pin)
}

// syncEpicForMerge brings the base into the epic branch before the epic ships to
// main so the epic PR is mergeable. The local epic is first fast-forwarded from
// the remote epic (children squash-merged into the remote; pushing a stale local
// epic would be rejected as non-fast-forward). A clean merge is pushed; a drift
// conflict is resolved by a bounded repair-agent loop, then the merge is completed
// and pushed. Returns false (with the merge aborted) when the conflicts could not
// be resolved, so the caller leaves the PR open for a human instead of shipping a
// broken merge.
func (p *Pipeline) syncEpicForMerge(ctx context.Context, epic string) (bool, error) {
	if err := p.Git.Checkout(ctx, epic, false); err != nil {
		return false, fmt.Errorf("checkout %s: %w", epic, err)
	}
	if err := p.Git.Pull(ctx, p.Remote, epic); err != nil {
		p.logf("  epic pull from %s skipped (%v)", p.Remote, err)
	}
	return p.syncBranchWithBase(ctx, p.EpicID, epic, p.Base, "epic-sync")
}

// epicCIAndMerge gates the epic PR on CI and ships it to the base: with AUTO_MERGE
// set it squash-merges once green; without it, it waits for the operator to merge the
// green PR by hand (a close without merge is a rejection → give-up, leaving the epic
// branch intact and unshipped). A red gate drives a bounded repair-agent loop on the
// epic branch before re-polling; an unrecoverable gate leaves the PR open for review.
// The bool reports whether the epic actually shipped to the base, so the caller closes
// the Linear epic with the right comment.
func (p *Pipeline) epicCIAndMerge(ctx context.Context, prURL string) (bool, error) {
	pr := prNumber(prURL)
	if st, _ := p.Delivery.PRState(ctx, pr); st == "MERGED" {
		p.checkpointEpicMerged(ctx, prURL)
		return true, nil
	}
	// The PR may be the draft an earlier run's exit hygiene opened; neither a merge
	// nor a reviewer can take a draft, and one already open for review is not news.
	if err := p.Delivery.MarkPRReady(ctx, pr); err != nil {
		logger.Verbosef("mark epic PR %s ready: %v", pr, err)
	}

	for repair := 0; ; {
		p.setActivity(p.EpicID, activity.CIWait, "")
		if err := p.pollCI(ctx, pr, p.Base); err == nil {
			break
		} else {
			p.logf("  ✗ epic CI: %v", err)
		}
		if repair >= p.MaxRepairs {
			p.logf("  ⚠ epic CI not green after %d repair attempt(s) — leaving PR for review: %s", repair, prURL)
			return false, nil
		}
		repair++
		epic, err := p.epicBranchName(ctx)
		if err != nil {
			return false, err
		}
		if err := p.Git.Checkout(ctx, epic, false); err != nil {
			return false, fmt.Errorf("epic repair %d: checkout %s: %w", repair, epic, err)
		}
		p.logf("  ⚠ epic CI red — repair attempt %d/%d", repair, p.MaxRepairs)
		p.setActivity(p.EpicID, activity.Merge, fmt.Sprintf("epic-repair%d/%d", repair, p.MaxRepairs))
		if _, err := p.agentStep(ctx, p.EpicID, fmt.Sprintf("epic-repair%d", repair), epicRepairInstruction(p.prompts, p.EpicID, prURL, epic)); err != nil {
			return false, err
		}
		if err := p.Git.Push(ctx, p.Remote, epic, false); err != nil {
			p.logf("  push epic repair error (continuing): %v", err)
		}
	}

	if !p.autoMerge() {
		p.markEpicPRAwaitingHuman(prURL)
		merged, err := p.waitForManualMerge(ctx, p.EpicID, pr, prURL)
		if err != nil {
			return false, err
		}
		if !merged {
			p.setPRStatus(p.EpicID, prStatusClosed)
			return false, p.giveUp(ctx, p.EpicID, fmt.Sprintf("epic PR #%s closed without merge", pr))
		}
		p.checkpointEpicMerged(ctx, prURL)
		p.logf("  ✓ epic merged to %s via %s", p.Base, prURL)
		return true, nil
	}
	p.setActivity(p.EpicID, activity.Merge, "")
	if err := p.retryGH(ctx, "merge pull request", func() error {
		if st, _ := p.Delivery.PRState(ctx, pr); st == "MERGED" {
			return nil
		}
		return p.Delivery.Merge(ctx, pr, p.MergeMethod, true)
	}); err != nil {
		return false, fmt.Errorf("merge epic PR %s: %w", prURL, err)
	}
	p.checkpointEpicMerged(ctx, prURL)
	p.logf("  ✓ epic merged to %s via %s", p.Base, prURL)
	return true, nil
}

// checkpointEpicReleasing opens the epic's own run row the moment shipping starts.
// Title and phase land together — a phase-less row reads as a run still in flight.
// Releasing ranks above the resume scan's in-flight window, so the epic is never
// picked up as ticket work; a release the run dies inside is re-entered through
// ResumableRelease instead. A shipped epic keeps its terminal checkpoint:
// reopening the release would un-ship it, and the re-run that follows usually
// fails on the branch it has already merged away, stranding the epic non-terminal
// for good.
func (p *Pipeline) checkpointEpicReleasing(ctx context.Context) {
	if p.State.Get(p.EpicID, "PHASE") == state.Merged {
		return
	}
	if err := p.State.Set(p.EpicID, "PHASE", state.Releasing); err != nil {
		p.logf("  epic checkpoint releasing error (continuing): %v", err)
		return
	}
	p.stampEpicTitle(ctx)
	_ = p.State.Set(p.EpicID, "RELEASE", state.ReleaseActive)
}

// ResumableRelease reports whether this run's epic sits at a release a dead
// finalize left behind — a releasing checkpoint no hand-off parked and no failure
// class ended. The loop re-enters FinalizeEpic for it rather than grazing for a
// fresh ticket, which would reset a working tree the release still owns.
func (p *Pipeline) ResumableRelease() bool {
	if p.EpicID == "" {
		return false
	}
	return state.ResumableRelease(
		p.State.Get(p.EpicID, "PHASE"),
		p.State.Get(p.EpicID, "RELEASE"),
		p.State.Get(p.EpicID, "FAILURE_CLASS"),
	)
}

// markEpicAwaitingHuman marks the release parked for a human — an unresolvable
// drift conflict, a gate that never went green, or a merge only the operator can
// make. The releasing phase stays: the epic is still mid-release.
func (p *Pipeline) markEpicAwaitingHuman() {
	if err := p.State.Set(p.EpicID, "RELEASE", state.ReleaseAwaitingHuman); err != nil {
		p.logf("  epic hand-off marker error (continuing): %v", err)
	}
}

// markEpicPRAwaitingHuman parks the release on a human and records the PR they
// have to land: the card links it, and the queue's reconcile sweep later reads
// that same PR to settle the item once the merge happens. Every way the epic ends
// up waiting on a person goes through here, so a run parked mid-wait leaves the
// same readable end state a killed one does.
func (p *Pipeline) markEpicPRAwaitingHuman(prURL string) {
	p.markEpicAwaitingHuman()
	if prURL == "" {
		return
	}
	_ = p.State.Set(p.EpicID, "PR", prNumber(prURL))
	_ = p.State.Set(p.EpicID, "PR_URL", prURL)
	p.setPRStatus(p.EpicID, prStatusAwaitingMerge)
}

// handOffEpic ends the finalize on a release only a human can land: it parks the
// epic on that human with the PR recorded, notifies the operator, and returns the
// typed hand-off the caller settles awaiting-merge on instead of recording a
// delivery.
func (p *Pipeline) handOffEpic(why, prURL string) error {
	p.markEpicPRAwaitingHuman(prURL)
	p.emitEpicAwaitingMerge(why, prURL)
	return &EpicHandOffError{EpicID: p.EpicID, PRURL: prURL, Reason: why}
}

// emitEpicAwaitingMerge records the state_change that hands the release over,
// riding the same pathway the ticket-level AUTO_MERGE=0 wait uses so a release
// trau could not finish reaches the operator as a notification rather than as
// silence.
func (p *Pipeline) emitEpicAwaitingMerge(why, prURL string) {
	if p.Events == nil {
		return
	}
	fields := map[string]any{"ticket": p.EpicID, "state": "awaiting_merge"}
	if prURL != "" {
		fields["pr"] = prNumber(prURL)
		fields["url"] = prURL
	}
	p.Events.Emit("state_change", state.Releasing, "epic "+p.EpicID+" awaits your merge — "+why, fields)
}

// emitEpicDelivered records the state_change fired the moment the epic actually
// lands on the base. A release can drain for hours in the background, so the
// delivery owes the operator a push of its own rather than only the absence of a
// problem.
func (p *Pipeline) emitEpicDelivered(prURL string) {
	if p.Events == nil {
		return
	}
	fields := map[string]any{"ticket": p.EpicID, "state": "epic_delivered"}
	msg := "epic " + p.EpicID + " merged to " + p.Base
	if prURL != "" {
		fields["pr"] = prNumber(prURL)
		fields["url"] = prURL
		msg += " via " + prURL
	}
	p.Events.Emit("state_change", state.Merged, msg, fields)
}

// checkpointEpicMerged records the shipped epic as a merged run — title, PR and
// terminal phase beside its PR status, with the hand-off marker dropped now that
// nothing is left to hand off — and announces the delivery, once: a re-finalize
// that merely re-adopts the merged PR must not push the news a second time. The
// phase stays terminal on purpose: an in-flight one would make the epic id a
// resume target.
func (p *Pipeline) checkpointEpicMerged(ctx context.Context, prURL string) {
	delivered := p.State.Get(p.EpicID, "PHASE") == state.Merged
	if err := p.State.Set(p.EpicID, "PHASE", state.Merged); err != nil {
		p.logf("  epic checkpoint merged error (continuing): %v", err)
		return
	}
	p.stampEpicTitle(ctx)
	if prURL != "" {
		_ = p.State.Set(p.EpicID, "PR", prNumber(prURL))
		_ = p.State.Set(p.EpicID, "PR_URL", prURL)
	}
	_ = p.State.Unset(p.EpicID, "RELEASE")
	p.setPRStatus(p.EpicID, prStatusMerged)
	if !delivered {
		p.emitEpicDelivered(prURL)
	}
}

// stampEpicTitle names the epic's run row from the tracker; a failed lookup leaves
// the row on its id rather than holding up the checkpoint it rides with.
func (p *Pipeline) stampEpicTitle(ctx context.Context) {
	title, err := p.Tracker.Title(ctx, p.EpicID)
	if err != nil {
		p.logf("  epic title lookup error (leaving the row id-only): %v", err)
		return
	}
	if title != "" {
		_ = p.State.Set(p.EpicID, "TITLE", title)
	}
}

func resolveConflictsInstruction(r prompts.Renderer, id, base, branch string) string {
	return r.Render("resolve_conflicts", prompts.ResolveConflictsData{ID: id, Base: base, Branch: branch})
}

func epicRepairInstruction(r prompts.Renderer, epicID, prURL, branch string) string {
	return r.Render("epic_repair", prompts.EpicRepairData{EpicID: epicID, PRURL: prURL, Branch: branch})
}

// openSubIssues splits the children into the ones that still block the epic and
// the ones trau delivered whose tracker status regressed afterwards. Beyond the
// tracker's own verdict, only trau's full delivery record outranks it — a merged
// checkpoint AND the TRACKER_DONE marker written once the tracker confirmed the
// close, which is exactly what an external automation can undo behind trau's back.
// A merged checkpoint alone means trau never saw the ticket close, and a status it
// could not read at all says nothing, so both keep blocking. A delivered child
// still sitting in the delivered state is a delivery, not a regression: a QA gate
// is non-terminal by design and belongs to the team, not to the loop.
func (p *Pipeline) openSubIssues(ctx context.Context, statuser tracker.IssueStatuser, subs []tracker.SubIssue) ([]string, []string, error) {
	var open, regressed []string
	for _, sub := range subs {
		st, err := statuser.IssueStatus(ctx, sub.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("finalize epic %s: status %s: %w", p.EpicID, sub.ID, err)
		}
		if st.Terminal() {
			continue
		}
		switch {
		case st == tracker.StatusUnknown:
			open = append(open, sub.ID+" (unknown)")
		case !p.deliveredByTrau(sub.ID):
			open = append(open, sub.ID)
		case p.behindDeliveredState(st):
			p.logf("  %s fell behind %s after trau merged it — restoring it", sub.ID, p.deliveredStateName())
			regressed = append(regressed, sub.ID)
		default:
			p.logf("  %s is not closed in the tracker but trau merged it — counting it delivered", sub.ID)
		}
	}
	return open, regressed, nil
}

// deliveredByTrau reports whether trau's own record proves it shipped id: the
// checkpoint reached the merged phase and the tracker confirmed the Done write.
func (p *Pipeline) deliveredByTrau(id string) bool {
	return p.State.Get(id, "PHASE") == state.Merged && p.State.Get(id, "TRACKER_DONE") == "1"
}

// reassertDone restores a delivered child that fell behind the delivered state
// after trau itself set it. The merged checkpoint already settles terminality, so
// the write is best-effort and never blocks the finalize.
func (p *Pipeline) reassertDone(ctx context.Context, id string) {
	delivered := p.deliveredStateName()
	note := "Delivered by trau"
	if pr := p.State.Get(id, "PR"); pr != "" {
		note += " in PR #" + pr
	}
	note += " and moved out of " + delivered + " afterwards — restoring it."
	if err := p.Tracker.SetStatus(ctx, id, tracker.StageDone, note); err != nil {
		p.logf("  re-assert %s for %s error (continuing): %v", delivered, id, err)
		return
	}
	p.logf("  ↻ %s restored to %s after a tracker status regression", id, delivered)
}
