package webserver

import (
	"time"

	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
)

// stopKillGrace bounds how long a halt — a queue Stop, or one running item's
// removal — waits for a child to exit on its own graceful stop before
// stopAndWait escalates to a group kill. A stopped run spends that window
// preserving its WIP on the feature branch and cleaning back to base, so the
// grace sits above the pipeline's cleanup budget rather than cutting it short.
// It is a var so tests can compress it instead of sleeping for real seconds.
var stopKillGrace = 90 * time.Second

// beginStopping / endStopping / isStopping flag which repos have a queue stop in
// flight. beginStopping reports false when one is already running, so a repeat
// POST /queue/stop is a no-op instead of a second stop, and the flag keeps the
// queue reading stopping until the child is gone — the window in which the row
// is still marked running but the run is already on its way out. The flag is
// in-memory only and does not survive a hub restart.
func (s *Server) beginStopping(root string) bool {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	if s.stopping[root] {
		return false
	}
	s.stopping[root] = true
	return true
}

func (s *Server) endStopping(root string) {
	s.stopMu.Lock()
	delete(s.stopping, root)
	s.stopMu.Unlock()
}

func (s *Server) isStopping(root string) bool {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	return s.stopping[root]
}

// runningChild names the loop process a stop has to end: the queue's own running
// row when it has one, and otherwise the repo's live instance. The second case is
// the run the queue never launched — a CLI start, which the Loop view shows as
// running just the same — and Stop is the gesture that ends it either way.
func (s *Server) runningChild(root string, items []queue.Item) (ticket string, pid int, ok bool) {
	if it, found := firstWithStatus(items, queue.StatusRunning); found {
		return it.ID, it.PID, true
	}
	if e, live := s.liveInstance(root); live {
		return e.Ticket, e.PID, true
	}
	return "", 0, false
}

// stopRunningChild ends the child a stop found in flight, exactly as a per-run
// Stop does: a graceful stop so the loop checkpoints and preserves its WIP,
// escalating to a group kill only if it outlasts the grace. It settles nothing itself —
// the drain tick that finds the process gone reads the stopped checkpoint and
// parks the item, so the ticket stays resumable and every other row stays
// queued. A child whose death is never confirmed keeps the row as it is; the
// in-flight flag still clears, so a later POST retries.
func (s *Server) stopRunningChild(root, ticket string, pid int) {
	defer s.endStopping(root)
	if err := s.stopAndWait(pid, stopKillGrace); err != nil {
		logger.Verbosef("stop %s queue: stop %s (pid %d): %v", root, ticket, pid, err)
	}
}
