package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// Requeue makes a quarantined ticket eligible again in one command: it restores
// the tracker labels and status, drops the saved checkpoint, closes the attempt PR
// and deletes the attempt branch, local and remote. A folder run's attempt is every
// child it shipped to, so all of its PRs close and all of its branches go. Every
// step is skipped when it has nothing left to do, so a second call changes nothing
// and says so. A ticket whose attempt PR already merged is refused unless force
// overrides — the same guard --reset applies to a merged checkpoint.
func (p *Pipeline) Requeue(ctx context.Context, id string, force bool) error {
	attempts := p.attempts(id)
	states := make([]string, len(attempts))
	for i, a := range attempts {
		if a.pr == "" {
			continue
		}
		states[i], _ = a.github.PRState(ctx, a.pr)
		if states[i] == "MERGED" && !force {
			return console.Actionable(
				fmt.Errorf("%s is already shipped (PR %s is merged%s)", id, a.pr, a.inRepo()),
				"requeue "+id,
				"its code is already merged — pass --force to requeue it anyway")
		}
	}
	branch := p.featureBranch(ctx, id)

	var changed []string
	var errs []error

	switch line, err := p.requeueTracker(ctx, id); {
	case err != nil:
		errs = append(errs, fmt.Errorf("restore the tracker labels and status: %w", err))
	case line != "":
		changed = append(changed, line)
	}

	for i, a := range attempts {
		if a.pr != "" && states[i] != "CLOSED" && states[i] != "MERGED" {
			if err := a.github.ClosePR(ctx, a.pr); err != nil {
				errs = append(errs, fmt.Errorf("close PR %s%s: %w", a.pr, a.inRepo(), err))
			} else {
				changed = append(changed, "closed the attempt PR "+a.pr+a.inRepo())
			}
		}
		dropped, dropErrs := p.dropAttemptBranch(ctx, a, branch)
		changed = append(changed, dropped...)
		errs = append(errs, dropErrs...)
	}

	if phase := p.State.Get(id, "PHASE"); phase != "" {
		p.clearLocalState(id)
		changed = append(changed, "cleared the saved checkpoint (was "+phase+")")
	}

	for _, line := range changed {
		p.logf("  ✓ %s", line)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("requeue %s: %w", id, err)
	}
	if len(changed) == 0 {
		p.logf("  %s is already queued — nothing to do", id)
		return nil
	}
	p.logf("  %s is eligible again", id)
	return nil
}

// dropAttemptBranch deletes the attempt branch on both sides, returning what it
// dropped and what it could not. A branch that is already gone — locally, on the
// remote, or both — contributes neither. The base is never a candidate, and a
// remote-less repo skips the remote half rather than reporting an unreachable one.
func (p *Pipeline) dropAttemptBranch(ctx context.Context, a attempt, branch string) (changed []string, errs []error) {
	if branch == "" || branch == p.Base {
		return nil, nil
	}
	if local, _ := a.git.BranchExists(ctx, branch); local {
		if err := p.baseCheckout(ctx, a, true); err != nil {
			errs = append(errs, fmt.Errorf("check out %s%s: %w", p.baseFor(a.repo), a.inRepo(), err))
		} else if err := a.git.DeleteBranch(ctx, branch); err != nil {
			errs = append(errs, fmt.Errorf("delete branch %s%s: %w", branch, a.inRepo(), err))
		} else {
			changed = append(changed, "deleted branch "+branch+a.inRepo())
		}
	}
	if p.localDelivery(ctx) {
		return changed, errs
	}
	switch pushed, err := a.git.RemoteBranchExists(ctx, p.Remote, branch); {
	case err != nil:
		errs = append(errs, fmt.Errorf("look up %s/%s%s: %w", p.Remote, branch, a.inRepo(), err))
	case pushed:
		if err := a.git.DeletePushedBranch(ctx, p.Remote, branch); err != nil {
			errs = append(errs, fmt.Errorf("delete %s/%s%s: %w", p.Remote, branch, a.inRepo(), err))
		} else {
			changed = append(changed, fmt.Sprintf("deleted %s/%s%s", p.Remote, branch, a.inRepo()))
		}
	}
	return changed, errs
}

// requeueTracker restores the ticket's tracker half — quarantine label off, ready
// label on, status back to unstarted — and reports what that changed, or "" when
// the ticket already sat that way.
func (p *Pipeline) requeueTracker(ctx context.Context, id string) (string, error) {
	drift, known := p.quarantineDrift(ctx, id)
	if known && len(drift) == 0 {
		return "", nil
	}
	if err := p.Tracker.Reset(ctx, id); err != nil {
		return "", err
	}
	if !known {
		return "restored the tracker labels and status", nil
	}
	return "tracker: " + strings.Join(drift, ", "), nil
}

// quarantineDrift lists how the ticket still differs from one the picker would
// take — quarantine label present, ready label missing, status past unstarted —
// and whether the tracker could answer at all. A tracker that cannot report labels
// or status, or whose read fails, answers unknown and the caller restores blind:
// the write is idempotent, so only the reporting gets coarser.
func (p *Pipeline) quarantineDrift(ctx context.Context, id string) ([]string, bool) {
	detailer, ok := p.Tracker.(tracker.IssueDetailer)
	if !ok {
		return nil, false
	}
	statuser, ok := p.Tracker.(tracker.IssueStatuser)
	if !ok {
		return nil, false
	}
	detail, err := detailer.IssueDetail(ctx, id)
	if err != nil {
		return nil, false
	}
	status, err := statuser.IssueStatus(ctx, id)
	if err != nil {
		return nil, false
	}

	var drift []string
	if p.QuarantineLabel != "" && slices.Contains(detail.Labels, p.QuarantineLabel) {
		drift = append(drift, "dropped "+p.QuarantineLabel)
	}
	if p.ReadyLabel != "" && !slices.Contains(detail.Labels, p.ReadyLabel) {
		drift = append(drift, "restored "+p.ReadyLabel)
	}
	if status != tracker.StatusOpen {
		drift = append(drift, "moved back to To Do")
	}
	return drift, true
}
