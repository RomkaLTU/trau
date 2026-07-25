package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/RomkaLTU/trau/internal/activity"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/prompts"
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
	snapper, _ := p.Git.(worktreeSnapshotter)
	before := p.snapshotTree(ctx, snapper)
	out, err := p.agentStep(ctx, id, "cleanup", cleanupInstruction(p.prompts, id, buildNotesNote(notesRef)))
	p.recordCleanupOutcome(ctx, id, snapper, before, out)
	if err != nil && isFatalAgentErr(err) {
		return err
	}
	if err != nil {
		p.logf("  cleanup agent error (continuing to verify): %v", err)
	}
	return nil
}

// worktreeSnapshotter records the working tree as a git tree object and compares
// two such snapshots. ExecGit implements it; a Git that does not (test stubs)
// leaves cleanup unmeasured rather than reporting numbers nobody took.
type worktreeSnapshotter interface {
	SnapshotWorktree(ctx context.Context) (string, error)
	DiffTrees(ctx context.Context, from, to string) (files, insertions, deletions int, err error)
}

// snapshotTree returns "" when the tree cannot be captured — the signal that this
// run records no cleanup_outcome.
func (p *Pipeline) snapshotTree(ctx context.Context, snapper worktreeSnapshotter) string {
	if snapper == nil {
		return ""
	}
	tree, err := snapper.SnapshotWorktree(ctx)
	if err != nil {
		p.logf("  cleanup: could not snapshot the working tree (outcome unmeasured): %v", err)
		return ""
	}
	return tree
}

// recordCleanupOutcome files what cleanup did to the working tree, measured from
// the snapshots taken either side of the agent, alongside the agent's own claim.
func (p *Pipeline) recordCleanupOutcome(ctx context.Context, id string, snapper worktreeSnapshotter, before, agentOut string) {
	if before == "" || p.Events == nil {
		return
	}
	after := p.snapshotTree(ctx, snapper)
	if after == "" {
		return
	}
	files, insertions, deletions, err := snapper.DiffTrees(ctx, before, after)
	if err != nil {
		p.logf("  cleanup: could not measure the outcome: %v", err)
		return
	}
	p.Events.Emit(event.KindCleanupOutcome, "cleanup",
		fmt.Sprintf("cleanup changed %d file(s), +%d/-%d", files, insertions, deletions),
		map[string]any{
			"ticket":      id,
			"files":       files,
			"insertions":  insertions,
			"deletions":   deletions,
			"agent_claim": cleanupClaim(agentOut),
		})
}

// cleanupClaim is the agent's self-reported result: the last non-empty line of its
// output, which the cleanup prompt pins to "trimmed N comments/lines across M
// files" or "no changes needed".
func cleanupClaim(out string) string {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
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

func cleanupInstruction(r prompts.Renderer, id, notesNote string) string {
	return r.Render("cleanup", prompts.CleanupData{ID: id, NotesNote: notesNote})
}
