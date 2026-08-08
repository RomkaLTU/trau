package webserver

import (
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/state"
)

// releasingEpic names the Epic whose release holds a repo's queue, or "" when
// none does. A checkpoint state.ResumableRelease accepts is a release trau still
// owns — live, or crashed and pending a resume — so the repo's working tree is
// mid-merge on the epic branch and no new run may start beside it. The gate is
// deliberately independent of the liveness probes: those read a best-effort
// heartbeat, and a lost PUT or a hub restart must not open the repo to a second
// run. The hand-off marker leaves the merge to a human and a spent failure class
// leaves it to nobody; either opens the gate so the queue continues with its other
// items even though the epic PR sits unmerged. A checkpoint read the hub cannot
// make opens the gate rather than freezing every queue behind a store error.
func (s *Server) releasingEpic(root string) string {
	rows, err := s.stores.Checkpoints().All(root)
	if err != nil {
		logger.Verbosef("release gate %s: %v", root, err)
		return ""
	}
	for _, row := range rows {
		if liveRelease(row.CheckpointRow) {
			return row.Ticket
		}
	}
	return ""
}

// releaseGateEpic names the Epic whose release holds root's whole queue — what the
// board reads to say nothing else starts. Only a whole-repo hold answers: with
// worktrees the release holds the epic's own lane and the rest of the queue keeps
// starting beside it, so there is no gate to name.
func (s *Server) releaseGateEpic(root string) string {
	if worktreeRepo(root) {
		return ""
	}
	return s.releasingEpic(root)
}

// liveRelease reads one checkpoint row the way the gate does.
func liveRelease(row hubstore.CheckpointRow) bool {
	return state.ResumableRelease(
		row.Phase,
		checkpointField(row.Data, "RELEASE"),
		checkpointField(row.Data, "FAILURE_CLASS"),
	)
}

// heldByRelease reports whether a release holds root's queue against the item
// about to start, and names the Epic holding it. That Epic's own finalize is the
// one run the gate lets through, on the drain and the one-shot path alike. The hold
// is as wide as the tree the merge happens in: a shared checkout is held whole,
// because the release owns it mid-merge, while a repo with worktrees releases
// inside the epic's own tree and every other item keeps starting beside it.
func (s *Server) heldByRelease(root, id string) (string, bool) {
	epic := s.releasingEpic(root)
	return epic, epic != "" && epic != id && !worktreeRepo(root)
}
