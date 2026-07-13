package pipeline

import (
	"context"

	"github.com/RomkaLTU/trau/internal/activity"
)

// cleanup strips AI-slop from the slice's diff before verify. It fails open:
// only a fatal agent error (pause/give-up) propagates.
func (p *Pipeline) cleanup(ctx context.Context, id string) error {
	if !p.Cleanup {
		return nil
	}
	p.setActivity(id, activity.Cleanup, "")
	p.logf("  ↳ cleanup: stripping unnecessary comments and slop from the diff")
	notesRef, _ := p.activeBuildNotes(id)
	_, err := p.agentStep(ctx, id, "cleanup", cleanupInstruction(id, buildNotesNote(notesRef)))
	if err != nil && isFatalAgentErr(err) {
		return err
	}
	if err != nil {
		p.logf("  cleanup agent error (continuing to verify): %v", err)
	}
	return nil
}

const (
	smallSliceMaxFiles = 5
	smallSliceMaxLines = 150
)

// worktreeSizer measures the current working-tree change size against a base
// branch. ExecGit implements it; a Git that does not (test stubs) makes the size
// gate fail open. Kept as an optional capability so the core Git interface stays
// unchanged.
type worktreeSizer interface {
	WorktreeDiffStat(ctx context.Context, base string) (files, lines int, err error)
}

// smallSlice reports whether a slice is tiny enough to skip the standalone cleanup
// and handoff phases: few files and few changed lines. Verify grades behavior, not
// slop, so minor slop surviving on a diff this small is an accepted cosmetic
// tradeoff; and on a diff this small verify can derive its own checklist from the
// ticket and the diff rather than a separately-authored brief.
func smallSlice(files, lines int) bool {
	return files <= smallSliceMaxFiles && lines <= smallSliceMaxLines
}

// tinyDiff reports whether the current working-tree diff against the build base is
// within the small-slice gate. It fails open — a Git that cannot size the tree or a
// measurement error both return false so the full chain runs — and phase names the
// step in the fail-open log line.
func (p *Pipeline) tinyDiff(ctx context.Context, phase string) bool {
	sizer, ok := p.Git.(worktreeSizer)
	if !ok {
		return false
	}
	base, err := p.buildBase(ctx)
	if err != nil {
		return false
	}
	files, lines, err := sizer.WorktreeDiffStat(ctx, base)
	if err != nil {
		p.logf("  size gate: could not measure diff (running %s): %v", phase, err)
		return false
	}
	return smallSlice(files, lines)
}

// skipCleanup decides whether the pipeline can drop the standalone cleanup phase for
// a tiny working-tree diff. Fails open (see tinyDiff).
func (p *Pipeline) skipCleanup(ctx context.Context, id string) bool {
	return p.tinyDiff(ctx, "cleanup")
}

// skipHandoff decides whether the pipeline can drop the standalone handoff agent for
// a tiny working-tree diff — verify then derives its checklist from the ticket and
// the diff. Fails open (see tinyDiff), so a diff that can't be sized runs the full
// handoff + verify chain.
func (p *Pipeline) skipHandoff(ctx context.Context, id string) bool {
	return p.tinyDiff(ctx, "handoff")
}

func cleanupInstruction(id, notesNote string) string {
	return "Before the QA verify step for " + id + ", clean up the code this slice added or changed (uncommitted on the current branch) so it reads as if a senior engineer on this project wrote it. " +
		"Review only the diff for this slice against the base branch. Remove: explanatory or narrating comments (anything that restates what the code does), section-banner comments, ticket IDs left in comments, commented-out code, and dead or unreachable code the slice introduced. Simplify AI tells: over-defensive guards for cases that cannot occur, redundant nil/error checks the surrounding codebase does not itself use, and belt-and-suspenders boilerplate a human wouldn't bother to write. Keep a comment only where a genuinely non-obvious decision needs one, matching the file's existing comment density. " +
		"This is behavior-preserving housekeeping: do NOT change program logic, rename public APIs, or touch code outside this slice's diff. Leave load-bearing code alone. Make the edits directly: do NOT list, count, or justify what you left unchanged, and do NOT emit a JSON or prose report. Leave the result uncommitted on disk — do NOT commit, push, open a PR, or touch the issue tracker. End with exactly one line: `trimmed N comments/lines across M files` or `no changes needed`." + notesNote
}
