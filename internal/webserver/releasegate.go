package webserver

import (
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/state"
)

// releasingEpic names the Epic whose release holds a repo's queue, or "" when
// none does. A checkpoint at the releasing phase carrying no hand-off marker is a
// release trau still owns — live, or crashed and pending a resume — so the repo's
// working tree is mid-merge on the epic branch and no new run may start beside it.
// The gate is deliberately independent of the liveness probes: those read a
// best-effort heartbeat, and a lost PUT or a hub restart must not open the repo to
// a second run. The hand-off marker leaves the merge to a human, which opens the
// gate so the queue continues with its other items even though the epic PR sits
// unmerged. A checkpoint read the hub cannot make opens the gate rather than
// freezing every queue behind a store error.
func (s *Server) releasingEpic(root string) string {
	rows, err := s.stores.Checkpoints().All(root)
	if err != nil {
		logger.Verbosef("release gate %s: %v", root, err)
		return ""
	}
	for _, row := range rows {
		if row.Phase != state.Releasing {
			continue
		}
		if checkpointField(row.Data, "RELEASE") == state.ReleaseAwaitingHuman {
			continue
		}
		return row.Ticket
	}
	return ""
}

// heldByRelease reports whether a release holds root's queue against the item
// about to start, and names the Epic holding it. That Epic's own finalize is the
// one run the gate lets through, on the drain and the one-shot path alike.
func (s *Server) heldByRelease(root, id string) (string, bool) {
	epic := s.releasingEpic(root)
	return epic, epic != "" && epic != id
}
