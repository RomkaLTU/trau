package pipeline

import "context"

// cleanup strips AI-slop from the slice's diff before verify. It fails open:
// only a fatal agent error (pause/give-up) propagates.
func (p *Pipeline) cleanup(ctx context.Context, id string) error {
	if !p.Cleanup {
		return nil
	}
	p.logf("  ↳ cleanup: stripping unnecessary comments and slop from the diff")
	_, err := p.agentStep(ctx, id, "cleanup", cleanupInstruction(id))
	if err != nil && isFatalAgentErr(err) {
		return err
	}
	if err != nil {
		p.logf("  cleanup agent error (continuing to verify): %v", err)
	}
	return nil
}

func cleanupInstruction(id string) string {
	return "Before the QA verify step for " + id + ", clean up the code this slice added or changed (uncommitted on the current branch) so it reads as if a senior engineer on this project wrote it. " +
		"Review only the diff for this slice against the base branch. Remove: explanatory or narrating comments (anything that restates what the code does), section-banner comments, ticket IDs left in comments, commented-out code, and dead or unreachable code the slice introduced. Simplify AI tells: over-defensive guards for cases that cannot occur, redundant nil/error checks the surrounding codebase does not itself use, and belt-and-suspenders boilerplate a human wouldn't bother to write. Keep a comment only where a genuinely non-obvious decision needs one, matching the file's existing comment density. " +
		"This is behavior-preserving housekeeping: do NOT change program logic, rename public APIs, or touch code outside this slice's diff. When unsure whether something is load-bearing, leave it. After editing, run only the tests relevant to this slice to confirm they still pass; if a change breaks them, revert that change. Leave the result uncommitted on disk — do NOT commit, push, open a PR, or touch the issue tracker."
}
