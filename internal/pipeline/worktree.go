package pipeline

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/worktree"
)

// artifactWorktreeSetup keeps the output of a failed WORKTREE_SETUP_CMD, the only
// evidence of why a run never got past provisioning.
const artifactWorktreeSetup = "worktree-setup"

// worktreesOn reports whether this run isolates each ticket in its own git
// worktree. A Folder repo never does: a folder has no repository at its root for
// git to add a tree to, so WORKTREES=1 there is a logged notice and folder runs
// keep sharing their children's checkouts.
func (p *Pipeline) worktreesOn() bool {
	if !p.Worktrees {
		return false
	}
	if p.FolderRepo {
		if !p.worktreeNoticed {
			p.worktreeNoticed = true
			p.logf("  ℹ WORKTREES=1 ignored: %s is a folder of repositories, which has no repo at its root to add a worktree to", filepath.Base(p.RepoRoot))
		}
		return false
	}
	return true
}

// worktreeTicket names the tree a ticket's work happens in. An Epic is serial
// inside — every sub-issue builds on the epic branch in turn, in this one process —
// so it takes exactly one tree, keyed by the epic id and reused by its sub-issues
// and its finalize. Every other run keys its tree by its own ticket.
func (p *Pipeline) worktreeTicket(id string) string {
	if p.EpicID != "" {
		return p.EpicID
	}
	return id
}

// worktreePath is where this ticket's tree lives — computed from configuration
// alone, so the loop, the hub and the CLI all name the same directory without
// passing it to one another.
func (p *Pipeline) worktreePath(id string) string {
	return worktree.Path(p.WorktreesDir, filepath.Base(p.RepoRoot), p.worktreeTicket(id))
}

// worktreeBranch names the branch a fresh tree is created holding: the ticket's
// recorded branch, else the branch trau already cut for it — the epic branch when
// the tree is the epic's own, since that is what a finalize works on. Empty starts
// the tree detached at the base tip and leaves the branch to be cut inside it.
func (p *Pipeline) worktreeBranch(ctx context.Context, id string) string {
	if branch := p.State.Get(id, "BRANCH"); branch != "" {
		return branch
	}
	if id == p.EpicID {
		branch, _ := p.Git.FindEpicBranch(ctx, p.EpicID)
		return branch
	}
	branch, _ := p.Git.FindFeatureBranch(ctx, id)
	return branch
}

// prepareWorktree puts the ticket's worktree in place and re-points the run at it:
// git, delivery, the agent phases and every tracked-file lookup act in the tree
// from here on, while RepoRoot — the key every hub record is written under — stays
// the registered root.
//
// It replaces the shared-checkout clean-base flow. Nothing about the user's
// checkout is inspected, stashed or reset: with worktrees on, a dirty checkout is
// simply not this run's business, which is the point of the feature. A branch some
// other tree already holds parks the ticket with that tree named, and a failed
// setup command faults it with the command's output kept as an artifact and the
// tree deliberately left standing. An Epic run lands here once per sub-issue and
// once for the finalize, all naming the epic's own tree: provisioning is idempotent,
// so the first call creates it and every later one adopts it with the previous
// child's work in place.
func (p *Pipeline) prepareWorktree(ctx context.Context, id string) error {
	if !p.worktreesOn() {
		return nil
	}
	key := p.worktreeTicket(id)
	target := p.worktreePath(id)
	if target == "" {
		return fmt.Errorf("WORKTREES=1 for %s but no worktrees directory resolved — set WORKTREES_DIR", id)
	}
	if p.WorkTree != "" && p.WorkTree != target {
		p.logf("  ℹ --worktree %s stands aside: WORKTREES=1 provisions %s for %s", p.WorkTree, target, key)
	}

	branch := p.worktreeBranch(ctx, id)

	res, err := worktree.Provision(ctx, worktree.Options{
		RepoRoot: p.RepoRoot,
		Dir:      p.WorktreesDir,
		Repo:     filepath.Base(p.RepoRoot),
		Ticket:   key,
		Branch:   branch,
		Base:     p.Base,
		Remote:   p.Remote,
		Copy:     p.WorktreeCopy,
		RunsDir:  p.RunsDir,
		SetupCmd: p.WorktreeSetupCmd,
		Logf:     p.logf,
	})
	var setup *worktree.SetupError
	switch {
	case errors.As(err, &setup):
		// The tree is kept and reported: whoever reads the captured output needs
		// both it and the directory it failed in.
		p.adoptWorktree(ctx, key, res)
		return p.faultWorktreeSetup(id, setup)
	case err != nil:
		var held *worktree.HeldError
		if errors.As(err, &held) {
			return p.parkWorktreeHeld(id, held)
		}
		return fmt.Errorf("provision the worktree for %s: %w", key, err)
	}
	p.adoptWorktree(ctx, key, res)
	return nil
}

// adoptWorktree re-points the run at the provisioned tree and tells the hub about
// it, under the id the tree is keyed by — the epic's own id for an Epic's shared
// tree, so the settle that removes it is the epic's. Wiring the repo-pinned git
// identity into the tree is re-run because the include is measured from the tree's
// own config file, and reporting is best-effort: a hub that missed the record
// still gets it from the boot reconcile.
func (p *Pipeline) adoptWorktree(ctx context.Context, id string, res worktree.Result) {
	if res.Path == "" {
		return
	}
	p.WorkTree = res.Path
	if p.GitAt != nil {
		p.Git = p.GitAt(res.Path)
	}
	if p.DeliveryAt != nil {
		p.Delivery = p.DeliveryAt(res.Path)
	}
	if _, err := EnsureRepoConfigInclude(ctx, p.RepoRoot, res.Path); err != nil {
		p.logf("  ⚠ wire %s into the worktree's git config: %v", RepoConfigFile, err)
	}
	if p.ReportWorktree != nil {
		if err := p.ReportWorktree(ctx, id, res.Path, res.Branch); err != nil {
			p.logf("  ⚠ report the worktree for %s to the hub: %v", id, err)
		}
	}
}

// faultWorktreeSetup parks the ticket Faulted on a WORKTREE_SETUP_CMD that exited
// non-zero, keeping the command's output as an artifact. Nothing has been built
// yet, so the ticket resumes from the top once the command or the tree is fixed —
// and the tree stays put, because the output is only actionable next to it.
func (p *Pipeline) faultWorktreeSetup(id string, setup *worktree.SetupError) error {
	p.putArtifact(id, artifactWorktreeSetup, setup.Cmd+"\n\n"+setup.Output)
	reason := fmt.Sprintf("worktree setup command failed in %s: %v", setup.Tree, setup.Err)
	_ = p.State.Set(id, "FAILURE_CLASS", state.FailFaulted)
	_ = p.State.Set(id, "FAILURE_REASON", reason)
	p.logf("  ⚠ %s: WORKTREE_SETUP_CMD failed in %s — the tree is kept for inspection", id, setup.Tree)
	for _, line := range lastLines(setup.Output, 20) {
		p.logf("    %s", line)
	}
	p.emitState(id, p.State.Get(id, "PHASE"), "faulted", "worktree_setup")
	return &FaultError{ID: id, Phase: p.State.Get(id, "PHASE"), Err: setup}
}

// parkWorktreeHeld parks a ticket whose branch is checked out somewhere else. git
// allows a branch one working tree at a time, so this is a human's call — keep the
// branch where it is, or remove that tree — never something to retry around.
func (p *Pipeline) parkWorktreeHeld(id string, held *worktree.HeldError) error {
	p.markPaused(id, held.Error())
	p.logf("  ⏸ %s cannot get a worktree — %s", id, held.Error())
	p.emitState(id, p.State.Get(id, "PHASE"), "paused", "worktree_held")
	return &PausedError{ID: id, Phase: p.State.Get(id, "PHASE"), Reason: held.Error()}
}

// settleTicketWorktree removes the ticket's tree and closes its hub row. Every
// settle path lands here — a reset, a requeue, a purge — so the CLI leaves the same
// record the hub's own settle does and neither has to trust the other to have run.
// A sub-issue of a running Epic owns no tree of its own: it works in the epic's,
// which its siblings and the finalize still need, so it leaves the tree to the
// epic's own settle.
func (p *Pipeline) settleTicketWorktree(ctx context.Context, id string) {
	if !p.worktreesOn() {
		return
	}
	if key := p.worktreeTicket(id); key != id {
		p.logf("  ℹ %s keeps the worktree it shares with epic %s — the epic settles it", id, key)
		return
	}
	path := p.worktreePath(id)
	if path == "" {
		return
	}
	if err := worktree.Remove(ctx, p.RepoRoot, path); err != nil {
		p.logf("  ⚠ remove the worktree %s: %v", path, err)
	}
	// The run may still be pointed at the tree that just went away.
	if p.WorkTree == path {
		p.WorkTree = ""
		if p.GitAt != nil {
			p.Git = p.GitAt(p.RepoRoot)
		}
		if p.DeliveryAt != nil {
			p.Delivery = p.DeliveryAt(p.RepoRoot)
		}
	}
	if p.SettleWorktree != nil {
		if err := p.SettleWorktree(ctx, id, path); err != nil {
			p.logf("  ⚠ settle the worktree for %s with the hub: %v", id, err)
		}
	}
}

// lastLines returns at most n trailing non-blank lines of out, so a failed setup
// command's reason reaches the log without its whole build transcript.
func lastLines(out string, n int) []string {
	lines := []string{}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimRight(line, "\r"); strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
