package pipeline

import (
	"context"
	"fmt"

	"github.com/RomkaLTU/trau/internal/forge"
)

// assertDeliverable refuses a run whose repo is hosted somewhere trau cannot open
// a pull request — or whose forge it can reach but holds no credential for —
// before a ticket is picked and an agent is paid for a change that would die at
// PR time. A repo with no remote is not refused: it delivers locally and never
// reaches a forge at all.
func (p *Pipeline) assertDeliverable(ctx context.Context) error {
	if p.localDelivery(ctx) {
		return nil
	}
	if reason := p.forgeOf(ctx, p.Git, p.Forge).Unsupported(); reason != "" {
		return fmt.Errorf("%s cannot be delivered to: %s", p.repoLabel(), reason)
	}
	if ready, ok := p.Delivery.(DeliveryReady); ok {
		if reason := ready.NotReady(); reason != "" {
			return fmt.Errorf("%s cannot be delivered to: %s", p.repoLabel(), reason)
		}
	}
	return nil
}

// forgeOf identifies where one repository is hosted, reading its remote through
// the run's own git seam. declared is what that repository states for itself and
// overrides the remote; nothing is taken from the tracker, whose service says
// nothing about where the code lives.
func (p *Pipeline) forgeOf(ctx context.Context, g Git, declared string) forge.Forge {
	return forge.Resolve(declared, g.RemoteURL(ctx, p.Remote))
}

// baseOf settles the branch one repository ships to, asking its remote rather
// than reading whatever it has checked out. The run's own base is the fallback
// for a remote with no answer.
func (p *Pipeline) baseOf(ctx context.Context, g Git, declared string) string {
	return forge.ResolveBase(declared, g.RemoteDefaultBranch(ctx, p.Remote), p.Base)
}
