package webserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
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
// way a per-run Stop does, wipe what the run saved, and drop the queue row once
// the process is confirmed gone. The wipe runs before the row goes, while the
// in-flight flag still holds the drain off the repo, so nothing is spawned into
// the working tree the reset checks out. A child whose death is never confirmed
// leaves the row in place rather than orphaning it, and the in-flight flag still
// clears so a later DELETE retries the whole sequence.
func (s *Server) removeRunningItem(root string, it queue.Item) {
	defer s.endRemoving(root, it.ID)
	if err := s.stopAndWait(it.PID, stopKillGrace); err != nil {
		logger.Verbosef("remove %s from %s queue: stop pid %d: %v", it.ID, root, it.PID, err)
	}
	if registry.Alive(it.PID) {
		logger.Verbosef("remove %s from %s queue: pid %d still alive, leaving it queued", it.ID, root, it.PID)
		return
	}
	s.wipeRemovedRun(root, it)
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
		fmt.Sprintf("%s was stopped and removed from the queue — its saved progress is gone and the ticket is back to Ready", it.ID),
		map[string]any{"ticket": it.ID, "kind": string(it.Kind)})
}

// wipeTimeout bounds one removed ticket's reset child: long enough for a tracker
// write and a remote branch delete, short enough never to pin the hub on a dead one.
const wipeTimeout = 2 * time.Minute

// wipeRemovedRun drops what a removed row's run saved, so a later pickup starts a
// brand-new run instead of resuming this one. A repo whose own loop is still
// running owns its working tree — a reset checks the base branch out — so there
// only the checkpoint goes and the branch is left to the next removal.
func (s *Server) wipeRemovedRun(root string, it queue.Item) {
	targets := s.wipeTargets(root, it)
	reset := !s.drain.repoLive(root)
	if !reset && len(targets) > 0 {
		logger.Verbosef("remove %s from %s queue: a run holds this repo — %s keeps its branch and tracker row",
			it.ID, root, strings.Join(targets, ", "))
	}
	for _, id := range targets {
		if reset {
			if err := s.resetTicket(root, id); err != nil {
				s.emitQueueResetFailed(root, id, err)
			}
		}
		if err := s.stores.Checkpoints().Remove(root, id); err != nil {
			logger.Verbosef("remove %s from %s queue: drop %s checkpoint: %v", it.ID, root, id, err)
		}
	}
}

// wipeTargets is every ticket a removed row's wipe covers. The row's own ticket
// goes unless it already merged: that work shipped and its checkpoint is the only
// record of it, which is why removing a settled-but-unmerged row still wipes. An
// epic adds the sub-issue whose slice the removal interrupted, and only that one —
// a child with no checkpoint never started, and a terminal child is one the epic
// settled before this removal rather than something it was interrupted in.
func (s *Server) wipeTargets(root string, it queue.Item) []string {
	ids := make([]string, 0, len(it.SubIssues)+1)
	if s.stores.Checkpoints().Phase(root, it.ID) != state.Merged {
		ids = append(ids, it.ID)
	}
	for _, sub := range it.SubIssues {
		phase := s.stores.Checkpoints().Phase(root, sub.ID)
		if phase == "" || state.Terminal(phase) {
			continue
		}
		ids = append(ids, sub.ID)
	}
	return ids
}

// resetTicket takes the ticket's branch and saved state down and hands it back to
// the tracker Ready. Spawning `trau --reset` rather than doing it in-process keeps
// the single-writer discipline the other reset callers follow.
func (s *Server) resetTicket(root, id string) error {
	ctx, cancel := context.WithTimeout(s.drainCtx, wipeTimeout)
	defer cancel()
	if _, err := s.sup.Capture(ctx, SpawnSpec{
		Dir:  root,
		Args: []string{"--repo", root, "--reset", id, "--no-tui"},
		Env:  childEnv(s.home),
	}); err != nil {
		return fmt.Errorf("reset %s: %w", id, err)
	}
	return nil
}

// emitQueueResetFailed records a removal whose reset never landed, so the row
// vanishing is not the only account the human gets of a ticket left mid-run.
func (s *Server) emitQueueResetFailed(root, id string, err error) {
	s.emitQueueEvent(root, event.KindQueueResetFailed,
		fmt.Sprintf("%s was removed but its reset failed — the branch may still stand and the ticket is not back to Ready", id),
		map[string]any{"ticket": id, "error": err.Error()})
}
