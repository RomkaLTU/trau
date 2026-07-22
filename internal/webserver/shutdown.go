package webserver

import (
	"time"

	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
)

// shutdownKillGrace bounds how long a queue shutdown's teardown waits for a
// running child to exit on its own SIGTERM before stopAndWait escalates to a
// group SIGKILL. A stopped run spends that window preserving its WIP on the
// feature branch and cleaning back to base, so the grace sits above the
// pipeline's cleanup budget rather than cutting it short.
//
// shutdownRaceWindow bounds the separate watch for a child the drainer spawned
// into the repo as the shutdown landed — a registration gap measured in
// milliseconds, not a process that has to be waited out.
//
// Both are vars so tests can compress them instead of sleeping for real seconds.
var (
	shutdownKillGrace  = 90 * time.Second
	shutdownRaceWindow = 5 * time.Second
)

// beginShutdown / endShutdown / isShuttingDown flag which repos have a queue
// teardown in flight. beginShutdown reports false when one is already running,
// so a concurrent POST /queue/shutdown becomes a no-op instead of racing the
// first request's stop-then-clear sequence. The flag is in-memory only and does
// not survive a hub restart.
func (s *Server) beginShutdown(root string) bool {
	s.shutdownMu.Lock()
	defer s.shutdownMu.Unlock()
	if s.shuttingDown[root] {
		return false
	}
	s.shuttingDown[root] = true
	return true
}

func (s *Server) endShutdown(root string) {
	s.shutdownMu.Lock()
	delete(s.shuttingDown, root)
	s.shutdownMu.Unlock()
}

func (s *Server) isShuttingDown(root string) bool {
	s.shutdownMu.Lock()
	defer s.shutdownMu.Unlock()
	return s.shuttingDown[root]
}

// teardownQueue runs a shutdown's async half, once the handler has already
// disarmed the drain synchronously: stop the child that was running when the
// shutdown was requested, poll the registry for one the drainer raced into
// existence before the disarm took effect and stop that too, drop the
// checkpoints a live loop would otherwise make refuseWhenLive refuse to touch —
// the ticket that was running and every item left paused — and finally empty
// the queue. If a pid's death is never confirmed — stopAndWait exhausted a group
// SIGKILL and it is still alive — teardown stops there and leaves the queue
// untouched rather than clear a repo out from under a process it could not
// actually kill; the in-flight flag still clears, so a later POST retries the
// whole sequence.
func (s *Server) teardownQueue(root string, running queue.Item, hasRunning bool) {
	defer s.endShutdown(root)
	if hasRunning {
		if err := s.stopAndWait(running.PID, shutdownKillGrace); err != nil {
			logger.Verbosef("shutdown %s: stop %s (pid %d): %v", root, running.ID, running.PID, err)
		}
		if registry.Alive(running.PID) {
			logger.Verbosef("shutdown %s: %s (pid %d) still alive after teardown, leaving queue for a retry", root, running.ID, running.PID)
			return
		}
		s.dropCheckpoint(root, running.ID)
	}
	if !s.stopRaceSpawned(root) {
		logger.Verbosef("shutdown %s: a race-spawned child is still alive after teardown, leaving queue for a retry", root)
		return
	}
	items, _, err := s.stores.Queue(root).Snapshot()
	if err != nil {
		logger.Verbosef("shutdown %s: snapshot before clear: %v", root, err)
	} else {
		for _, it := range items {
			if it.Status == queue.StatusPaused {
				s.dropCheckpoint(root, it.ID)
			}
		}
	}
	if _, err := s.stores.Queue(root).Clear(); err != nil {
		logger.Verbosef("shutdown %s: clear queue: %v", root, err)
	}
}

// stopRaceSpawned polls the registry for shutdownRaceWindow, stopping a child
// the drainer spawned into root between the handler's queue snapshot and the
// disarm taking effect. A single immediate check misses this: a real child's
// own registration lands over the network once it starts up, measurably later
// than the near-instant gap teardownQueue otherwise runs in, so this has to
// wait it out rather than look once. It reports false only when it found a
// live race-spawned child and could not confirm it dead afterward — otherwise
// (nothing raced in, or it raced in and died) it reports true.
func (s *Server) stopRaceSpawned(root string) bool {
	deadline := time.Now().Add(shutdownRaceWindow)
	for {
		if e, ok := s.liveInstance(root); ok {
			if err := s.stopAndWait(e.PID, shutdownKillGrace); err != nil {
				logger.Verbosef("shutdown %s: stop race-spawned pid %d: %v", root, e.PID, err)
			}
			return !registry.Alive(e.PID)
		}
		if time.Now().After(deadline) {
			return true
		}
		time.Sleep(stopWaitPoll)
	}
}

// dropCheckpoint forgets ticket's checkpoint the same way handleClearRun does:
// checkpoint only, never git or the tracker. A missing checkpoint — an item
// that never got far enough to write one — is not an error.
func (s *Server) dropCheckpoint(root, ticket string) {
	if err := s.stores.Checkpoints().Remove(root, ticket); err != nil {
		logger.Verbosef("shutdown %s: clear checkpoint %s: %v", root, ticket, err)
	}
}
