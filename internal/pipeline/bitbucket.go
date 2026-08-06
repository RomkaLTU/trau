package pipeline

import (
	"context"
	"fmt"

	"github.com/RomkaLTU/trau/internal/forge/bitbucketapi"
)

// Bitbucket delivers pull requests to Bitbucket Cloud over its REST API. It is
// the second implementation of Delivery and deliberately implements nothing more:
// stacked pull requests have no Bitbucket analogue, so it is not a Stacker and an
// epic asking for the stacked shape is refused before it starts.
//
// It keeps the same swallow-and-default reads as the GitHub implementation — an
// unreachable API reads as "no PR" / "no checks" — so a transient failure
// re-polls instead of aborting a ticket that is otherwise fine.
type Bitbucket struct {
	Client *bitbucketapi.Client
}

// PRURL returns the open PR's URL for branch, or "" when none exists. Declined
// and superseded pull requests are never adopted: Bitbucket keeps every one a
// branch ever had, and adopting a dead one strands the run's commits.
func (b Bitbucket) PRURL(ctx context.Context, branch string) (string, error) {
	pr, err := b.Client.FindPullRequest(ctx, branch, bitbucketapi.StateOpen)
	if err != nil || pr == nil {
		return "", nil
	}
	return pr.URL, nil
}

// MergedPRURL returns the merged PR's URL for branch, or "" when the branch has
// none. A finalize re-attempt after the epic PR shipped adopts that merged PR
// instead of re-creating one Bitbucket refuses as having no commits.
func (b Bitbucket) MergedPRURL(ctx context.Context, branch string) (string, error) {
	pr, err := b.Client.FindPullRequest(ctx, branch, bitbucketapi.StateMerged)
	if err != nil || pr == nil {
		return "", nil
	}
	return pr.URL, nil
}

func (b Bitbucket) CreatePR(ctx context.Context, base, head, title, body string, draft bool) (string, error) {
	pr, err := b.Client.CreatePullRequest(ctx, base, head, title, body, draft)
	if err != nil {
		return "", fmt.Errorf("bitbucket create pull request: %w", err)
	}
	return pr.URL, nil
}

func (b Bitbucket) MarkPRReady(ctx context.Context, pr string) error {
	if err := b.Client.UpdatePullRequest(ctx, pr, "", true); err != nil {
		return fmt.Errorf("bitbucket mark pull request ready: %w", err)
	}
	return nil
}

func (b Bitbucket) UpdatePRBody(ctx context.Context, pr, body string) error {
	if err := b.Client.UpdatePullRequest(ctx, pr, body, false); err != nil {
		return fmt.Errorf("bitbucket update pull request: %w", err)
	}
	return nil
}

// PRState returns the PR's state, or "" when it cannot be read. Bitbucket spells
// OPEN and MERGED exactly as GitHub does, but it has no CLOSED: a pull request a
// human rejected is DECLINED, and one its source branch outgrew is SUPERSEDED.
// Both are the end GitHub calls CLOSED, and both are reported as it, so the
// phases branching on a closed PR — the manual-merge wait, the requeue sweep —
// read one vocabulary whichever forge answered.
func (b Bitbucket) PRState(ctx context.Context, pr string) (string, error) {
	found, err := b.Client.PullRequest(ctx, pr)
	if err != nil || found == nil {
		return "", nil
	}
	switch found.State {
	case bitbucketapi.StateDeclined, bitbucketapi.StateSuperseded:
		return "CLOSED", nil
	}
	return found.State, nil
}

// Checks returns the build statuses posted against the PR's head commit, rolled
// up into the same buckets the merge gate reads from `gh pr checks`. An API
// error reads as no checks, so the gate re-polls rather than failing the ticket.
func (b Bitbucket) Checks(ctx context.Context, pr string) ([]Check, error) {
	statuses, err := b.Client.BuildStatuses(ctx, pr)
	if err != nil {
		return nil, nil
	}
	checks := make([]Check, 0, len(statuses))
	for _, s := range statuses {
		checks = append(checks, Check{Name: s.Name, Bucket: s.Bucket()})
	}
	return checks, nil
}

// PRSize reports the PR's commit and changed-file counts. An API failure reads as
// (0, 0, nil) — the merge gate then has nothing to compare and lets the merge
// through, as it does for every other blind spot.
func (b Bitbucket) PRSize(ctx context.Context, pr string) (int, int, error) {
	commits, files, err := b.Client.Size(ctx, pr)
	if err != nil {
		return 0, 0, nil
	}
	return commits, files, nil
}

func (b Bitbucket) ClosePR(ctx context.Context, pr string) error {
	if err := b.Client.DeclinePullRequest(ctx, pr); err != nil {
		return fmt.Errorf("bitbucket decline pull request: %w", err)
	}
	return nil
}

func (b Bitbucket) Merge(ctx context.Context, pr, method string, deleteBranch bool) error {
	if err := b.Client.Merge(ctx, pr, bitbucketapi.MergeStrategy(method), deleteBranch); err != nil {
		return fmt.Errorf("bitbucket merge pull request: %w", err)
	}
	return nil
}
