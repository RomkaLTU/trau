package pipeline

import (
	"context"
	"strings"

	"github.com/RomkaLTU/trau/internal/state"
)

// exitState is everything a run has to undo when it ends.
type exitState struct {
	// startBranch is where HEAD was before the run first moved it. Empty means nothing
	// has moved yet, or the run started on a detached HEAD, which is no checkout to
	// return to.
	startBranch string

	// stashedBranch is the branch the user's WIP was on when EnsureCleanBase auto-stashed
	// it, so ExitCleanup can check that branch back out and pop the stash at session end.
	// Empty means nothing was stashed this run.
	stashedBranch string

	// epicBranch is the epic branch this run resolved — the branch it must settle, and
	// the memo epicBranchName resolves only once.
	epicBranch string

	// epicPushed records that this run put the epic branch on the remote, so exit hygiene
	// may delete that copy too when the branch turns out to carry nothing.
	epicPushed bool

	// epicStacked records that dependent work hangs off the epic branch — a slice branch
	// cut from it, or a child PR opened against it — so exit hygiene must not drop it.
	epicStacked bool

	// settledEpic names the epic whose branch exit hygiene has already resolved, so a
	// second ExitCleanup neither pushes nor drops it again. It outlives the rest, which
	// each cleanup consumes; arming a fresh run clears it.
	settledEpic string
}

// armExitCleanup readies the run's exit hygiene, once per run: it records where HEAD
// was before the run first moves it, and re-arms epic settling so a run that shares a
// long-lived pipeline with an earlier one still owns whatever it leaves behind. Only a
// detached HEAD goes unrecorded — a ticket branch trau itself cut is still where the
// user parked the working copy, and a run resuming from it moves HEAD off it just as
// readily. A run entered on an already-cancelled context never moves HEAD itself, so it
// records nothing rather than spending a git call git would abort.
func (p *Pipeline) armExitCleanup(ctx context.Context) {
	if p.exit.startBranch != "" {
		return
	}
	p.exit.settledEpic = ""
	if ctx.Err() != nil {
		return
	}
	branch, err := p.Git.CurrentBranch(ctx)
	if err != nil || branch == detachedHead {
		return
	}
	p.exit.startBranch = branch
}

// ExitCleanup is the run's exit hygiene, deferred by every entry point so it runs
// on success, refusal, error and signal alike: it aborts a merge the run died inside,
// commits what the run left uncommitted on its own branch, puts HEAD back on the
// branch the run started on, settles whatever the run left on the epic branch, and
// pops the WIP EnsureCleanBase auto-stashed. It consumes what it restores, so a
// second deferred call does nothing, and every step is best-effort — on failure the
// WIP stays safe in `git stash` and the log tells the user how to finish by hand.
func (p *Pipeline) ExitCleanup(ctx context.Context) {
	home, stashed := p.exit.startBranch, p.exit.stashedBranch
	p.exit.startBranch, p.exit.stashedBranch = "", ""
	if stashed != "" {
		home = stashed
	}
	// Detach from the loop's context and give the cleanup its own deadline so a
	// Ctrl-C (which cancels ctx) still hands the user's checkout and WIP back rather
	// than leaving them stranded.
	ctx, cancel := detachedCleanup(ctx)
	defer cancel()

	p.abortHalfMerge(ctx)
	p.preserveRunLeftovers(ctx, home)
	restored := p.restoreCheckout(ctx, home)
	p.settleEpicBranch(ctx)
	if stashed == "" {
		return
	}
	if !restored {
		p.logf("  ⚠ your WIP is safe — run `git stash pop` on %s to restore it", stashed)
		return
	}
	if err := p.Git.StashPop(ctx); err != nil {
		p.logf("  ⚠ back on %s but couldn't pop your WIP (%v) — it's in `git stash list`; run `git stash pop` to restore it", stashed, err)
		return
	}
	p.logf("  ↪ restored your WIP on %s", stashed)
}

// abortHalfMerge undoes a merge the run died inside, on whatever branch it died on.
// A signal landing while the conflict-resolution agent works leaves MERGE_HEAD and
// conflict-marked files in the tree, and preserving those the way ordinary leftovers
// are preserved would stage the markers and commit them — on the epic branch, in the
// middle of a release. Aborting puts the branch back on the tip the merge started
// from, which is where the finalize that resumes re-enters.
func (p *Pipeline) abortHalfMerge(ctx context.Context) {
	if mid, err := p.Git.MergeInProgress(ctx); err != nil || !mid {
		return
	}
	if err := p.Git.MergeAbort(ctx); err != nil {
		p.logf("  ⚠ couldn't abort the merge left in progress (%v) — run `git merge --abort` before the next run", err)
		return
	}
	p.logf("  ↩ aborted the merge the run left in progress")
}

// preserveRunLeftovers commits whatever the run left uncommitted back to the branch it
// left it on, before the restore below moves HEAD away: git carries uncommitted changes
// across a checkout, so leftovers would otherwise ride onto the branch the user is handed
// back, collide with the stash pop that restores their WIP, and strand a dead run's work
// where the next run's reconcile no longer recognizes it. The base and the branch HEAD is
// returning to are the user's own — neither is written to. With no branch to return to —
// a second cleanup, or a run interrupted before it recorded one — nothing moves HEAD and
// the dirty tree is the user's, so there is nothing to preserve.
func (p *Pipeline) preserveRunLeftovers(ctx context.Context, home string) {
	if home == "" {
		return
	}
	branch, err := p.Git.CurrentBranch(ctx)
	if err != nil || branch == home || p.onBase(branch) {
		return
	}
	if dirty, err := p.Git.StatusPorcelain(ctx); err != nil || strings.TrimSpace(dirty) == "" {
		return
	}
	_ = p.Git.AddAll(ctx)
	if err := p.Git.Commit(ctx, "wip: preserved when the run ended", true); err != nil {
		p.logf("  ⚠ couldn't commit the work left on %s (%v) — it follows your checkout", branch, err)
		return
	}
	p.logf("  saved the work the run left on %s", branch)
}

// restoreCheckout puts HEAD back on branch, reporting whether the working copy
// ended up there. An unrecorded branch, and a run the user started on a detached
// HEAD, leave the checkout where the run left it.
func (p *Pipeline) restoreCheckout(ctx context.Context, branch string) bool {
	if branch == "" || branch == detachedHead {
		return false
	}
	if cur, err := p.Git.CurrentBranch(ctx); err == nil && cur == branch {
		return true
	}
	if err := p.Git.Checkout(ctx, branch, false); err != nil {
		p.logf("  ⚠ couldn't switch back to %s (%v) — check it out by hand", branch, err)
		return false
	}
	p.logf("  ↩ back on %s", branch)
	return true
}

// settleEpicBranch resolves the epic branch the run leaves behind so no exit
// strands an untracked integration branch: one carrying commits is pushed and given
// a draft PR (or adopts the PR it already has), one carrying none is deleted unless
// this run stacked work on it. An epic this run shipped has nothing left to settle.
// Settling is consume-once per epic: a second exit finds it resolved and touches
// nothing.
func (p *Pipeline) settleEpicBranch(ctx context.Context) {
	epic, pushed, stacked := p.exit.epicBranch, p.exit.epicPushed, p.exit.epicStacked
	p.exit.epicBranch, p.exit.epicPushed, p.exit.epicStacked = "", false, false
	if p.EpicID == "" || p.exit.settledEpic == p.EpicID || p.State.Get(p.EpicID, "PHASE") == state.Merged {
		return
	}
	p.exit.settledEpic = p.EpicID
	if epic == "" {
		// A run that never resolved the branch itself still owns whatever an earlier
		// one left behind for this epic.
		epic, _ = p.Git.FindEpicBranch(ctx, p.EpicID)
		if epic == "" {
			return
		}
	}
	settleRef, remoteAhead := epic, false
	if tip := p.epicRemoteTip(ctx, epic); tip != "" {
		settleRef, remoteAhead = tip, true
	}
	commits, err := p.Git.Commits(ctx, p.epicWorkBase(ctx), settleRef)
	if err != nil {
		p.logf("  ⚠ couldn't tell whether epic branch %s carries work (%v) — leaving it in place", epic, err)
		return
	}
	if len(commits) == 0 {
		if stacked {
			p.logf("  ⏳ epic %s left unfinished — nothing landed on %s yet, but this run stacked work on it", p.EpicID, epic)
			return
		}
		p.dropEmptyEpicBranch(ctx, epic, pushed)
		return
	}
	if p.localDelivery(ctx) {
		p.logf("  ⏳ epic %s left unfinished — %d commit(s) on %s to merge into %s yourself", p.EpicID, len(commits), epic, p.Base)
		return
	}
	if !remoteAhead {
		if err := p.Git.Push(ctx, p.Remote, epic, true); err != nil {
			p.logf("  ⚠ couldn't push epic branch %s (%v) — its %d commit(s) are on this machine only", epic, err, len(commits))
			return
		}
	}
	prURL, err := p.ensureEpicPR(ctx, epic, true)
	if err != nil {
		p.logf("  ⚠ couldn't open a PR for epic branch %s (%v) — its %d commit(s) are pushed to %s", epic, err, len(commits), p.Remote)
		return
	}
	p.logf("  ⏳ epic %s left unfinished — %d commit(s) on %s, tracked by %s", p.EpicID, len(commits), epic, prURL)
}

// epicRemoteTip returns the epic branch as the remote has it, when that copy already
// contains everything the local ref does. A child's PR squash-merges into the epic
// branch on the remote and nothing pulls it back until the NEXT child syncs, so a
// drain cut short after a child landed leaves a local ref that reads empty while the
// remote carries the work: settling from it would drop a branch other children are
// stacked on, or push it non-fast-forward and never track it with a PR. Empty means
// the local ref is the one to settle — local delivery, no remote copy, an unreachable
// remote, or local commits the remote is missing.
func (p *Pipeline) epicRemoteTip(ctx context.Context, epic string) string {
	if p.localDelivery(ctx) {
		return ""
	}
	sha, err := p.remoteTip(ctx, p.Git, epic)
	if err != nil || sha == "" {
		return ""
	}
	if ahead, _ := p.Git.IsAncestor(ctx, epic, sha); !ahead {
		return ""
	}
	return sha
}

// markEpicBranchStacked records dependent work on the epic branch — a slice branch
// cut from it, or a child PR opened against it — so exit hygiene stops treating it as
// disposable: dropping it would orphan that slice or close its PR.
func (p *Pipeline) markEpicBranchStacked() {
	p.exit.epicStacked = true
}

// dropEmptyEpicBranch deletes an epic branch no child ever landed on. The remote copy
// goes only when this run pushed it there — a branch adopted from the remote belongs
// to whoever created it, and another machine may still be stacking on it. git refuses
// to delete the branch HEAD is on, so a run with no checkout to restore steps back to
// the base first rather than leaving the branch dangling.
func (p *Pipeline) dropEmptyEpicBranch(ctx context.Context, epic string, pushed bool) {
	if cur, err := p.Git.CurrentBranch(ctx); err == nil && cur == epic {
		if _, err := p.checkoutBase(ctx, false); err != nil {
			p.logf("  ⚠ couldn't step off the empty epic branch %s (%v) — delete it by hand", epic, err)
			return
		}
	}
	if err := p.Git.DeleteBranch(ctx, epic); err != nil {
		p.logf("  ⚠ couldn't delete the empty epic branch %s (%v) — delete it by hand", epic, err)
		return
	}
	if pushed {
		_ = p.Git.DeletePushedBranch(ctx, p.Remote, epic)
	}
	p.logf("  ✕ dropped epic branch %s — no work landed on it", epic)
}
