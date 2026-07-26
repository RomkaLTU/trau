// Package forkpoint resolves the commit a run's own work starts at, so the
// pipeline and the diff endpoint anchor a run's changes at the same place.
package forkpoint

import "context"

// Git is the git surface a fork point is resolved with. Revisions that share no
// history are an expected ("", nil) from MergeBase, a commit on another line of
// history an expected (false, nil) from IsAncestor, and a rev that names nothing
// an expected (false, nil) from ResolvesToCommit; only a git failure errors.
type Git interface {
	MergeBase(ctx context.Context, a, b string) (string, error)
	IsAncestor(ctx context.Context, ancestor, rev string) (bool, error)
	ResolvesToCommit(ctx context.Context, rev string) (bool, error)
}

// Anchor is what a run's work is measured from: the branch it was cut from and,
// when the run pinned one as it cut its branch, the commit it forked at.
type Anchor struct {
	Base string
	Pin  string
}

// Resolve returns the commit head's own work starts at — whichever of the anchor's
// pin and its merge-base with the base branch sits further along head.
//
// The pin wins once the base branch has been rebuilt on another line of history,
// where the merge-base collapses to an ancient common ancestor and reports every
// sibling's work as this run's. The merge-base wins the other way round: a build
// that rebased the branch onto a base which advanced mid-run replays the run's
// commits on top of a sibling's, leaving the pin behind as a boundary that no
// longer separates the two. Without either, the result is "" — nothing anchors the
// run's work, and the caller has no diff to show.
func Resolve(ctx context.Context, git Git, anchor Anchor, head string) (string, error) {
	pin, err := usablePin(ctx, git, anchor.Pin, head)
	if err != nil {
		return "", err
	}
	if anchor.Base == "" {
		return pin, nil
	}
	mergeBase, err := git.MergeBase(ctx, anchor.Base, head)
	if err != nil {
		return "", err
	}
	if pin == "" {
		return mergeBase, nil
	}
	if mergeBase == "" {
		return pin, nil
	}
	behindMergeBase, err := git.IsAncestor(ctx, pin, mergeBase)
	if err != nil {
		return "", err
	}
	if behindMergeBase {
		return mergeBase, nil
	}
	return pin, nil
}

// usablePin drops a pin that cannot bound the run's own work: one head no longer
// contains, and one whose commit is gone from the repo altogether — a discarded
// rebase or a fresh clone leaves a run pinned to a commit git cannot resolve.
func usablePin(ctx context.Context, git Git, pin, head string) (string, error) {
	if pin == "" {
		return "", nil
	}
	resolves, err := git.ResolvesToCommit(ctx, pin)
	if err != nil {
		return "", err
	}
	if !resolves {
		return "", nil
	}
	onHead, err := git.IsAncestor(ctx, pin, head)
	if err != nil {
		return "", err
	}
	if !onHead {
		return "", nil
	}
	return pin, nil
}
