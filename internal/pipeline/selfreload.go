package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/RomkaLTU/trau/internal/update"
)

// reloadHubOntoBase rebuilds the hub's binary from the base a unit of work just
// shipped to and asks the hub to restart onto it. The hub defers the restart to
// the first gap with nothing running anywhere, so the request is safe to make
// while this child still holds its queue item: the gap opens when the child
// exits, and the successor hub resumes the drain.
//
// The work is already merged by the time this runs, so no failure here may fault,
// pause or quarantine — every step logs its reason and ends the reload. The hub
// is never killed or force-restarted; the deferred request is the only channel.
func (p *Pipeline) reloadHubOntoBase(ctx context.Context) {
	if !p.HubSelfReload || p.RequestHubReload == nil || ctx.Err() != nil {
		return
	}
	buildCmd := strings.TrimSpace(p.HubReloadBuildCmd)
	if buildCmd == "" {
		p.logf("  ⚠ hub reload skipped (no build command configured: HUB_RELOAD_BUILD_CMD)")
		return
	}
	p.logf("  ↻ rebuilding the hub binary from %s", p.Base)
	version, err := p.rebuildAndRequestReload(ctx, buildCmd)
	if err != nil {
		if ctx.Err() == nil {
			p.logf("  ⚠ hub reload skipped (%v)", err)
		}
		return
	}
	p.logf("  ✓ hub reload pending: %s once nothing is running", version)
}

// rebuildAndRequestReload puts the repo back on the merged base, builds the hub's
// binary from it and hands the hub a reload request, reporting the version the
// hub will come back on.
func (p *Pipeline) rebuildAndRequestReload(ctx context.Context, buildCmd string) (string, error) {
	if err := p.Git.Checkout(ctx, p.Base, false); err != nil {
		return "", fmt.Errorf("checkout %s: %w", p.Base, err)
	}
	if !p.localDelivery(ctx) {
		if err := p.Git.Pull(ctx, p.Remote, p.Base); err != nil {
			return "", fmt.Errorf("pull %s: %w", p.Base, err)
		}
	}
	if err := p.buildForReload(ctx, buildCmd); err != nil {
		return "", err
	}
	version, err := p.hubBinaryVersion(ctx)
	if err != nil {
		return "", err
	}
	if _, err := p.RequestHubReload(ctx); err != nil {
		return "", fmt.Errorf("the hub declined: %w", err)
	}
	return version, nil
}

// buildForReload runs the repo's build command from the base tree, then reverts
// whatever tracked files it churned so the next run starts on a clean base.
func (p *Pipeline) buildForReload(ctx context.Context, buildCmd string) error {
	out, buildErr := p.runRepoCmd(ctx, "build", buildCmd)
	if err := p.Git.DiscardTracked(ctx); err != nil {
		p.logf("  ⚠ build-artifact sweep failed (continuing): %v", err)
	}
	if buildErr != nil {
		for _, line := range tailLines(string(out), 5) {
			p.logf("      %s", line)
		}
		return fmt.Errorf("build %q: %w", buildCmd, buildErr)
	}
	return nil
}

// hubBinaryVersion reads the version out of the binary the hub would re-exec. The
// hub only ever restarts onto a binary inside the repo that asked, so a child
// running from anywhere else — a PATH install started by hand — cannot vouch for
// what was just built and does not get to ask. A build that cannot even print its
// version must not be adopted either.
func (p *Pipeline) hubBinaryVersion(ctx context.Context) (string, error) {
	if p.probeBinary != nil {
		return p.probeBinary(ctx)
	}
	exe, err := update.ResolveBinary()
	if err != nil {
		return "", fmt.Errorf("resolve the hub binary: %w", err)
	}
	if !withinRepo(p.RepoRoot, exe) {
		return "", fmt.Errorf("this loop runs %s, not a binary the hub could reload from", exe)
	}
	version, err := update.ProbeVersion(ctx, exe)
	if err != nil {
		return "", fmt.Errorf("the built binary is unusable: %w", err)
	}
	return version, nil
}

// withinRepo reports whether path sits inside root, root itself included. It is
// the client-side twin of the hub's own guard, which refuses to reload onto a
// binary outside the repo that asked.
func withinRepo(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
