package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/RomkaLTU/trau/internal/tracker"
)

func (p *Pipeline) epicBranchName(ctx context.Context) (string, error) {
	if p.epicBranch != "" {
		return p.epicBranch, nil
	}

	// Resolve deterministically by epic ID, never by the drift-prone title slug. Any
	// existing epic/<ID>-* branch IS the epic branch and is adopted as-is — local
	// first, then the remote (a fresh clone or a different machine). The title slug
	// only names a brand-new branch on the very first creation. Matching on the slug
	// instead would let a renamed Linear epic spawn a SECOND branch that orphans the
	// children's integration work.
	if branch, _ := p.Git.FindEpicBranch(ctx, p.EpicID); branch != "" {
		p.epicBranch = branch
		return branch, nil
	}

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
		p.epicBranch = remote
		return remote, nil
	}

	title, err := p.Tracker.Title(ctx, p.EpicID)
	if err != nil {
		p.logf("  epic title lookup error (using id-only branch): %v", err)
	}
	branch := epicBranch(p.EpicID, title)
	if err := p.Git.CreateBranch(ctx, branch, p.Base); err != nil {
		return "", &GiveUpError{ID: p.EpicID, Reason: "could not create epic branch for " + p.EpicID}
	}
	p.logf("  epic branch %s ← %s", branch, p.Base)
	if err := p.Git.Push(ctx, p.Remote, branch, false); err != nil {
		p.logf("  push epic branch error (continuing): %v", err)
	}
	p.epicBranch = branch
	return branch, nil
}

func epicBranch(id, title string) string {
	if slug := slugify(title); slug != "" {
		return "epic/" + id + "-" + slug
	}
	return "epic/" + id
}

func (p *Pipeline) ensureEpicPR(ctx context.Context, epicBranch string) (string, error) {
	prURL, _ := p.GitHub.PRURL(ctx, epicBranch)
	if prURL != "" {
		return prURL, nil
	}

	title, err := p.Tracker.Title(ctx, p.EpicID)
	if err != nil {
		title = p.EpicID
	}
	prURL, err = p.GitHub.CreatePR(ctx, p.Base, epicBranch, "Epic: "+title, epicPRBody(p.EpicID))
	if err != nil {
		return "", err
	}
	p.logf("  epic PR %s", prURL)
	return prURL, nil
}

func epicPRBody(id string) string {
	return fmt.Sprintf("## Summary\nEpic integration branch for %s.\n\nFeatures land on the epic branch first; this PR ships the epic to main once complete.\n\nLinear: %s", id, id)
}

// FinalizeEpic ships the epic only after every direct child is terminal. It is
// intentionally a loop-level finalizer, not part of a child merge: a child PR can
// land while siblings are still open, but the parent must not be shipped to main
// until the tracker confirms the whole child set is complete. Once it is, the epic
// branch is synced with the base (drift conflicts resolved by an agent), the epic
// PR is opened/adopted, its CI is gated with a bounded repair loop, and — when
// AUTO_MERGE is set — it is squash-merged to the base before the Linear epic closes.
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
	open, err := p.openSubIssues(ctx, statuser, subs)
	if err != nil {
		return err
	}
	if len(open) > 0 {
		p.logf("  epic %s still open — waiting on %s", p.EpicID, strings.Join(open, ", "))
		return nil
	}

	epic, err := p.epicBranchName(ctx)
	if err != nil {
		return fmt.Errorf("finalize epic %s: resolve branch: %w", p.EpicID, err)
	}
	synced, err := p.syncEpicForMerge(ctx, epic)
	if err != nil {
		return fmt.Errorf("finalize epic %s: sync with %s: %w", p.EpicID, p.Base, err)
	}
	prURL, err := p.ensureEpicPR(ctx, epic)
	if err != nil {
		return fmt.Errorf("finalize epic %s: create PR: %w", p.EpicID, err)
	}
	if !synced {
		p.logf("  ⚠ epic %s still conflicts with %s — PR left for manual resolution: %s", p.EpicID, p.Base, prURL)
		return nil
	}

	merged, err := p.epicCIAndMerge(ctx, prURL)
	if err != nil {
		return fmt.Errorf("finalize epic %s: ship: %w", p.EpicID, err)
	}

	extra := "All direct sub-issues are closed."
	if merged {
		extra += " Epic merged to " + p.Base + " via " + prURL + "."
	} else {
		extra += " Epic PR ready for review: " + prURL + "."
	}
	if err := p.Tracker.SetStatus(ctx, p.EpicID, "Done", extra); err != nil {
		return fmt.Errorf("finalize epic %s: close epic: %w", p.EpicID, err)
	}
	p.logf("  ✓ epic %s closed; PR %s", p.EpicID, prURL)
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
// resolving agent). Best-effort by design — any failure is logged, never blocking
// the child about to branch off.
func (p *Pipeline) syncEpicBest(ctx context.Context, epic string) {
	if err := p.Git.Checkout(ctx, epic, false); err != nil {
		p.logf("  epic sync skipped (checkout %s: %v)", epic, err)
		return
	}
	if err := p.Git.Pull(ctx, p.Remote, epic); err != nil {
		p.logf("  epic pull from %s skipped (%v)", p.Remote, err)
	}
	conflicted, err := p.Git.MergeRemote(ctx, p.Remote, p.Base)
	switch {
	case err != nil:
		p.logf("  epic sync skipped (merge %s: %v)", p.Base, err)
	case conflicted:
		_ = p.Git.MergeAbort(ctx)
		p.logf("  epic %s conflicts with %s — deferring resolution to epic finalize", epic, p.Base)
	default:
		if err := p.Git.Push(ctx, p.Remote, epic, false); err != nil {
			p.logf("  push synced epic branch error (continuing): %v", err)
		}
	}
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

// epicCIAndMerge gates the epic PR on CI and, when AUTO_MERGE is set, squash-merges
// it to the base. A red gate drives a bounded repair-agent loop on the epic branch
// before re-polling; an unrecoverable gate leaves the PR open for review. The bool
// reports whether the epic actually shipped to the base, so the caller closes the
// Linear epic with the right comment.
func (p *Pipeline) epicCIAndMerge(ctx context.Context, prURL string) (bool, error) {
	pr := prNumber(prURL)
	if st, _ := p.GitHub.PRState(ctx, pr); st == "MERGED" {
		return true, nil
	}

	for repair := 0; ; {
		if err := p.pollCI(ctx, pr); err == nil {
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
		if _, err := p.agentStep(ctx, p.EpicID, fmt.Sprintf("epic-repair%d", repair), epicRepairInstruction(p.EpicID, prURL, epic)); err != nil {
			return false, err
		}
		if err := p.Git.Push(ctx, p.Remote, epic, false); err != nil {
			p.logf("  push epic repair error (continuing): %v", err)
		}
	}

	if !p.AutoMerge {
		p.logf("  ✓ epic CI green — leaving merge to you (AUTO_MERGE=0): %s", prURL)
		return false, nil
	}
	if err := p.retryGH(ctx, "gh pr merge", func() error {
		if st, _ := p.GitHub.PRState(ctx, pr); st == "MERGED" {
			return nil
		}
		return p.GitHub.Merge(ctx, pr, p.MergeMethod, true)
	}); err != nil {
		return false, fmt.Errorf("merge epic PR %s: %w", prURL, err)
	}
	p.logf("  ✓ epic merged to %s via %s", p.Base, prURL)
	return true, nil
}

func resolveConflictsInstruction(id, base, branch string) string {
	return "The branch " + branch + " is mid-merge with " + base + " and has conflicts. Resolve EVERY conflicted file so the branch combines its own work with the latest " + base + ": run `git diff --name-only --diff-filter=U` to list them, edit each to keep BOTH sides' intent (never drop this branch's feature work, and never drop " + base + "'s newer changes; when both sides carry the SAME change — e.g. " + base + " already received it as a squash-merge — keep exactly one copy), then `git add` each resolved file. Run the relevant tests to confirm the combined result builds. Do NOT run `git commit`, `git merge --continue`, push, or open a PR — leave the resolved merge staged for the loop to finalize. Refs: " + id + "."
}

func epicRepairInstruction(epicID, prURL, branch string) string {
	return "The CI checks on the epic PR " + prURL + " (branch " + branch + ") are failing. You are on " + branch + " with the full epic integrated against the base. Investigate the failing checks, find the root cause, and fix it with minimal, targeted changes anywhere in the epic's code so the whole suite passes; run the relevant tests locally to confirm. Commit the fix with a Conventional Commit ('fix(scope): <subject>', imperative mood, no 'Co-authored-by'/AI-authorship trailers) but do NOT push or merge — the loop pushes and merges. Refs: " + epicID + "."
}

func (p *Pipeline) openSubIssues(ctx context.Context, statuser tracker.IssueStatuser, subs []tracker.SubIssue) ([]string, error) {
	var open []string
	for _, sub := range subs {
		st, err := statuser.IssueStatus(ctx, sub.ID)
		if err != nil {
			return nil, fmt.Errorf("finalize epic %s: status %s: %w", p.EpicID, sub.ID, err)
		}
		if st.Terminal() {
			continue
		}
		if st == tracker.StatusUnknown {
			open = append(open, sub.ID+" (unknown)")
			continue
		}
		open = append(open, sub.ID)
	}
	return open, nil
}
