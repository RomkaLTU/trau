package webserver

import (
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
)

// stopKillGrace bounds how long a queue Stop waits for a child to exit on its
// own graceful stop before stopAndWait escalates to a group kill. A stopped run
// spends that window preserving its WIP on the feature branch and cleaning back
// to base, so the grace sits above the pipeline's cleanup budget rather than
// cutting it short.
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

// childRun is one loop process a queue Stop has to end, named by the ticket it is
// working so the log says which lane failed to die rather than only its PID.
type childRun struct {
	ticket string
	pid    int
}

// runningChildren names the loop processes a stop has to end: every one of the
// queue's own running rows — a repo draining WORKTREE_PARALLEL lanes has one per
// lane, and a Stop that ended only the first would leave the rest running against a
// disarmed queue — and, when the queue holds none, the repo's live instance. That
// second case is the run the queue never launched — a CLI start, which the Loop view
// shows as running just the same — and Stop is the gesture that ends it either way.
func (s *Server) runningChildren(root string, items []queue.Item) []childRun {
	kids := make([]childRun, 0, len(items))
	for _, it := range items {
		if it.Status == queue.StatusRunning {
			kids = append(kids, childRun{ticket: it.ID, pid: it.PID})
		}
	}
	if len(kids) > 0 {
		return kids
	}
	if e, live := s.liveInstance(root); live {
		return []childRun{{ticket: e.Ticket, pid: e.PID}}
	}
	return nil
}

// stopRunningChildren ends every child a stop found in flight, exactly as a per-run
// Stop does: a graceful stop so each loop checkpoints and preserves its WIP,
// escalating to a group kill only if it outlasts the grace. The lanes are stopped
// concurrently so the grace is spent once rather than once per lane. It settles
// nothing itself — the drain tick that finds a process gone reads its stopped
// checkpoint and parks that item, so every ticket stays resumable and every other
// row stays queued. A child whose death is never confirmed keeps its row as it is;
// the in-flight flag still clears, so a later POST retries.
func (s *Server) stopRunningChildren(root string, kids []childRun) {
	defer s.endStopping(root)
	var wg sync.WaitGroup
	for _, kid := range kids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.stopAndWait(kid.pid, stopKillGrace); err != nil {
				logger.Verbosef("stop %s queue: stop %s (pid %d): %v", root, kid.ticket, kid.pid, err)
			}
		}()
	}
	wg.Wait()
}
