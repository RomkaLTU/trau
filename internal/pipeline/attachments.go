package pipeline

import (
	"context"

	"github.com/RomkaLTU/trau/internal/attachfile"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// materializeAttachments writes the ticket's images and files under the ticket's
// /tmp attachment directory so the agent can read them, leaving the target repo's
// working tree untouched. A tracker without the capability materializes nothing;
// a file that will not fetch comes back carrying its error for the prompt to note,
// so a screenshot the hub cannot reach never faults the run.
func (p *Pipeline) materializeAttachments(ctx context.Context, id string, refs []tracker.AttachmentRef) []attachfile.File {
	fetcher, ok := p.Tracker.(tracker.AttachmentFetcher)
	if !ok || len(refs) == 0 {
		return nil
	}
	return attachfile.Materialize(ctx, id, refs, fetcher.AttachmentBytes)
}
