package webserver

import (
	"fmt"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
)

// queueItemKey names one repo's queue row, the unit a removal is in flight for.
type queueItemKey struct {
	root string
	id   string
}

// beginRemoving / endRemoving / isRemoving flag which running items have a
// stop-then-remove in flight. beginRemoving reports false when one is already
// running, so a repeat DELETE is a no-op instead of a second stop, and the flag
// holds the drain off the row until the removal finishes — without it the tick
// that finds the stopped child would park the item and disarm the queue. The
// flag is in-memory only and does not survive a hub restart.
func (s *Server) beginRemoving(root, id string) bool {
	s.removeMu.Lock()
	defer s.removeMu.Unlock()
	key := queueItemKey{root: root, id: id}
	if s.removing[key] {
		return false
	}
	s.removing[key] = true
	return true
}

func (s *Server) endRemoving(root, id string) {
	s.removeMu.Lock()
	delete(s.removing, queueItemKey{root: root, id: id})
	s.removeMu.Unlock()
}

func (s *Server) isRemoving(root, id string) bool {
	s.removeMu.Lock()
	defer s.removeMu.Unlock()
	return s.removing[queueItemKey{root: root, id: id}]
}

// removingItems lists the rows of root a removal is in flight for, so the queue
// view can show the wait rather than a running row that answers nothing.
func (s *Server) removingItems(root string) map[string]bool {
	s.removeMu.Lock()
	defer s.removeMu.Unlock()
	out := map[string]bool{}
	for key := range s.removing {
		if key.root == root {
			out[key.id] = true
		}
	}
	return out
}

// removeRunningItem takes a running item out of the queue: stop its child the
// way a per-run Stop does — WIP preserved on the feature branch — and drop the
// queue row once the process is confirmed gone. The ticket, its checkpoint and
// its run history are left exactly as the stop left them, so the item stays
// re-queueable; only the queue row and the queued label go. A child whose death
// is never confirmed leaves the row in place rather than orphaning it, and the
// in-flight flag still clears so a later DELETE retries the whole sequence.
func (s *Server) removeRunningItem(root string, it queue.Item) {
	defer s.endRemoving(root, it.ID)
	if err := s.stopAndWait(it.PID, stopKillGrace); err != nil {
		logger.Verbosef("remove %s from %s queue: stop pid %d: %v", it.ID, root, it.PID, err)
	}
	if registry.Alive(it.PID) {
		logger.Verbosef("remove %s from %s queue: pid %d still alive, leaving it queued", it.ID, root, it.PID)
		return
	}
	if _, err := s.stores.Queue(root).ForceRemove(it.ID); err != nil {
		logger.Verbosef("remove %s from %s queue: %v", it.ID, root, err)
		return
	}
	if err := s.stores.DrainOutcomes().Remove(root, it.ID); err != nil {
		logger.Verbosef("remove %s from %s queue: drop drain outcome: %v", it.ID, root, err)
	}
	s.drain.forgetAutoResume(root, it.ID)
	s.clearQueued(s.drainCtx, root, it)
	s.emitQueueItemRemoved(root, it)
}

// emitQueueItemRemoved records a running item a human took out of the queue, so
// the activity view can explain a run that ended with no outcome behind it.
func (s *Server) emitQueueItemRemoved(root string, it queue.Item) {
	s.emitQueueEvent(root, event.KindQueueItemRemoved,
		fmt.Sprintf("%s was stopped and removed from the queue — the ticket is kept", it.ID),
		map[string]any{"ticket": it.ID, "kind": string(it.Kind)})
}
