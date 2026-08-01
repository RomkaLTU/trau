package pipeline

import (
	"context"
	"errors"
	"fmt"
)

// assertPRBaseCurrent makes the remote copy of base carry pin — the commit a PR
// opened against that base is diffed from — before any PR exists. GitHub diffs a PR
// against the base AS THE REMOTE HAS IT, so a base branch that moved on locally and
// was never pushed (an epic merged into the local base, say) turns the base's own
// drift into the PR's diff: a one-commit slice opens carrying every file those
// unpushed commits touched. A remote a plain push can repair is repaired — never a
// force push — and one that still misses the pin fails the phase with no PR opened,
// leaving the work on its branch for a resume. g and base name the repo the PR is
// opened in, so a Folder repo's child gates against its own remote.
func (p *Pipeline) assertPRBaseCurrent(ctx context.Context, g Git, base, pin string) error {
	carries, err := p.remoteCarries(ctx, g, base, pin)
	if err != nil {
		return err
	}
	if carries {
		return nil
	}
	p.logf("  ⚠ %s/%s is behind %s — pushing the base branch before opening the PR", p.Remote, base, pin)
	if err := g.Push(ctx, p.Remote, base, false); err != nil {
		return fmt.Errorf("%s: the repair push was rejected: %w", staleBaseNote(p.Remote, base, pin), err)
	}
	carries, err = p.remoteCarries(ctx, g, base, pin)
	if err != nil {
		return err
	}
	if !carries {
		return errors.New(staleBaseNote(p.Remote, base, pin))
	}
	p.logf("  ↻ %s/%s repaired — the PR base now carries %s", p.Remote, base, pin)
	return nil
}

// staleBaseNote says what the remote base is missing and the two ways an operator
// unblocks the run, since neither is something trau may do on its own: a force push
// would rewrite the base's history, and a protected branch is a deliberate policy.
func staleBaseNote(remote, base, pin string) string {
	return fmt.Sprintf(
		"pr base %s/%s does not carry %s, so a PR opened against it would diff against a stale base — push %s to %s yourself, or resolve the divergence, then retry",
		remote, base, pin, base, remote,
	)
}

// prBasePin names the commit the remote base must carry for a slice PR: the fork
// point the run recorded, falling back to the local base tip for a checkpoint that
// predates the pin.
func (p *Pipeline) prBasePin(id string) string {
	if sha := p.State.Get(id, "BASE_SHA"); sha != "" {
		return sha
	}
	return p.baseRef()
}

// healBaseDrift fast-forwards the remote base onto the local one at run start, the
// mirror of the pull that precedes it and the earliest point the origin==local
// invariant can be restored: a branch cut now from a base carrying commits the
// remote never saw would open a PR diffed against those commits. Best-effort by
// design — assertPRBaseCurrent is the hard guarantee, so a base this cannot push
// (protected, diverged) is logged and the run continues.
func (p *Pipeline) healBaseDrift(ctx context.Context) {
	tip, err := p.remoteTip(ctx, p.Git, p.Base)
	if err != nil {
		p.logf("  base drift check skipped (%v)", err)
		return
	}
	if tip == "" {
		return
	}
	carries, err := p.Git.IsAncestor(ctx, p.Base, tip)
	if err != nil {
		p.logf("  base drift check skipped (%v)", err)
		return
	}
	if carries {
		return
	}
	p.logf("  ⚠ %s/%s is behind the local %s — pushing the fast-forward", p.Remote, p.Base, p.Base)
	if err := p.Git.Push(ctx, p.Remote, p.Base, false); err != nil {
		p.logf("  ⚠ couldn't push %s to %s (%v) — continuing", p.Base, p.Remote, err)
	}
}

// remoteCarries reports whether the remote copy of branch contains commit.
func (p *Pipeline) remoteCarries(ctx context.Context, g Git, branch, commit string) (bool, error) {
	sha, err := p.remoteTip(ctx, g, branch)
	if err != nil || sha == "" {
		return false, err
	}
	return g.IsAncestor(ctx, commit, sha)
}

// remoteTip returns the commit remote/branch points at as the REMOTE reports it,
// fetched into the local object store so its history can be judged locally. It is
// read with ls-remote, never a remote-tracking ref: a push that failed leaves the
// tracking ref exactly as convincing as a push that worked, and a tip that moved on
// — a sibling squash-merged into the epic since — is missing from it entirely. A
// branch the remote does not have is an expected ("", nil).
func (p *Pipeline) remoteTip(ctx context.Context, g Git, branch string) (string, error) {
	sha, err := g.RemoteSHA(ctx, p.Remote, branch)
	if err != nil {
		return "", fmt.Errorf("read %s/%s: %w", p.Remote, branch, err)
	}
	if sha == "" {
		return "", nil
	}
	if known, _ := g.ResolvesToCommit(ctx, sha); !known {
		if err := g.Fetch(ctx, p.Remote, branch); err != nil {
			return "", fmt.Errorf("fetch %s/%s: %w", p.Remote, branch, err)
		}
	}
	return sha, nil
}
