package webserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/state"
)

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
