package pipeline

import (
	"context"
	"fmt"
)

// A PR whose diff is this many times the run's own — and wider by enough files that
// a small slice cannot trip it — is carrying somebody else's history.
const (
	foreignDiffMultiple = 3
	foreignDiffMinExtra = 10
)

// sliceDiffStatter measures a run's own committed diff, the extra git surface the
// merge gate needs beyond the shared Git seam. ExecGit satisfies it; a Git that does
// not leaves the size half of the gate unmeasured.
type sliceDiffStatter interface {
	DiffStat(ctx context.Context, base, branch string) (files, additions, deletions int, err error)
}

// foreignWorkInPR names why a PR must not be auto-merged, or "" when it may. GitHub
// diffs a PR against its base as the REMOTE has it, so a base missing the commit the
// run was cut from turns every commit of base drift into part of the PR — and the
// auto-merge then buries that drift inside the base as duplicated content: 470 files
// where the slice touched 47, 13 commits where it pushed one. The run's own record is
// the reference: the commits it put on the branch above its fork point, and the files
// they touch. Anything the gate cannot measure — no fork point,
// a gh or git that will not answer — reads as fine, so no delivery is ever blocked on
// the gate's own instrumentation.
func (p *Pipeline) foreignWorkInPR(ctx context.Context, id, pr string) string {
	pin, branch := p.State.Get(id, "BASE_SHA"), p.State.Get(id, "BRANCH")
	if pin == "" || branch == "" {
		return ""
	}
	prCommits, prFiles, err := p.Delivery.PRSize(ctx, pr)
	if err != nil {
		p.logf("  merge gate: could not size PR #%s (merging anyway): %v", pr, err)
		return ""
	}
	own, err := p.Git.Commits(ctx, pin, branch)
	if err != nil {
		p.logf("  merge gate: could not list this run's commits (merging anyway): %v", err)
		return ""
	}
	if prCommits > 0 && len(own) > 0 && prCommits != len(own) {
		return fmt.Sprintf("PR #%s carries %d commit(s) but this run pushed %d — its base is not the commit the branch was cut from", pr, prCommits, len(own))
	}
	ownFiles := p.sliceFileCount(ctx, pin, branch)
	if ownFiles > 0 && prFiles >= ownFiles*foreignDiffMultiple && prFiles-ownFiles >= foreignDiffMinExtra {
		return fmt.Sprintf("PR #%s changes %d file(s) but this run touched %d — the diff is not this slice", pr, prFiles, ownFiles)
	}
	return ""
}

// stackedPRRefusal names why a PR must not be auto-merged because it is a layer of
// a GitHub stack, or "" when it may merge. The legacy merge endpoint `gh pr merge`
// calls refuses a stacked PR at every position of the stack, so the attempt can only
// end in an opaque failure and the ticket is parked for a human who can merge the
// whole stack. A human can stack Trau's PRs at any moment, so the question is asked
// here, immediately before the merge. Like the size half of the gate the check is
// fail-open: a gh that cannot answer reads as unstacked.
func (p *Pipeline) stackedPRRefusal(ctx context.Context, id, pr string) string {
	s, ok := p.stacker()
	if !ok {
		return ""
	}
	stacked, err := s.InStack(ctx, pr)
	if err != nil {
		p.logf("  merge gate: could not tell whether PR #%s is stacked (merging anyway): %v", pr, err)
		return ""
	}
	if !stacked {
		return ""
	}
	ref := p.State.Get(id, "PR_URL")
	if ref == "" {
		ref = "#" + pr
	}
	return fmt.Sprintf("PR %s is part of a GitHub stack; Trau cannot merge stacked PRs — merge it with 'gh stack merge' or the stack UI, or remove it from the stack and requeue", ref)
}

// sliceFileCount counts the files the run's own commits touch, 0 when that diff
// cannot be measured.
func (p *Pipeline) sliceFileCount(ctx context.Context, pin, branch string) int {
	differ, ok := p.Git.(sliceDiffStatter)
	if !ok {
		return 0
	}
	files, _, _, err := differ.DiffStat(ctx, pin, branch)
	if err != nil {
		p.logf("  merge gate: could not size this run's diff (skipping the size check): %v", err)
		return 0
	}
	return files
}
