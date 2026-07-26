package webserver

import (
	"context"
	"strings"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
)

// markQueued labels the issues an item covers as waiting in the queue: the
// ticket, or an epic and every sub-issue it captured.
func (s *Server) markQueued(ctx context.Context, root string, it queue.Item) {
	s.writeQueuedLabel(ctx, root, queuedLabelTargets(it), true)
}

// clearQueued strips the queued label from an item that left the queue — a
// dequeue, a settle, or a teardown — including the sub-issues an epic captured.
// A sub-issue whose own slice already ran lost the label when it went In
// Progress, so covering all of them leaves exactly the never-started ones
// changed.
func (s *Server) clearQueued(ctx context.Context, root string, it queue.Item) {
	s.writeQueuedLabel(ctx, root, queuedLabelTargets(it), false)
}

// clearQueuedIssue strips the queued label from one issue: the item that just
// flipped to running. An epic's sub-issues keep theirs until their own slice
// starts.
func (s *Server) clearQueuedIssue(ctx context.Context, root, id string) {
	s.writeQueuedLabel(ctx, root, []string{id}, false)
}

// writeQueuedLabel applies the repo's QUEUED_LABEL to ids and mirrors it onto
// their store rows so the board follows without waiting for the next sync. Every
// write is best-effort — a failure never fails the queue operation itself. A repo
// the hub can build no direct writer for — internal-provider, GitHub, missing
// credentials — still gets the internal and store-row writes.
func (s *Server) writeQueuedLabel(ctx context.Context, root string, ids []string, add bool) {
	if len(ids) == 0 {
		return
	}
	repo, ok := s.findRepoByRoot(root)
	if !ok {
		return
	}
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		logger.Verbosef("queued label %s: read config: %v", root, err)
		return
	}
	label := strings.TrimSpace(cfg.QueuedLabel)
	if label == "" {
		return
	}
	var addLabels, removeLabels []string
	if add {
		addLabels = []string{label}
	} else {
		removeLabels = []string{label}
	}

	writer, err := s.newWriter(cfg)
	if err != nil {
		logger.Verbosef("queued label %s: no tracker writer: %v", root, err)
		writer = nil
	}

	for _, id := range ids {
		if _, internal, err := s.stores.Issues().Internal(root, id); err == nil && internal {
			if _, err := s.stores.Issues().TransitionInternal(root, id, hubstore.InternalTransition{
				AddLabels:    addLabels,
				RemoveLabels: removeLabels,
			}); err != nil {
				logger.Verbosef("queued label %s: transition internal issue: %v", id, err)
			}
			continue
		}
		if writer != nil {
			if err := writer.UpdateLabels(ctx, id, addLabels, removeLabels); err != nil {
				logger.Verbosef("queued label %s: update tracker labels: %v", id, err)
				continue
			}
		}
		if _, _, err := s.stores.Issues().UpdateSynced(root, id, hubstore.SyncedPatch{
			AddLabels:    addLabels,
			RemoveLabels: removeLabels,
		}); err != nil {
			logger.Verbosef("queued label %s: mirror synced row: %v", id, err)
		}
	}
}

// queuedLabelTargets is every issue an item's queued label covers: the ticket, or
// the epic and the sub-issues captured when it was queued.
func queuedLabelTargets(it queue.Item) []string {
	ids := make([]string, 0, len(it.SubIssues)+1)
	ids = append(ids, it.ID)
	for _, sub := range it.SubIssues {
		ids = append(ids, sub.ID)
	}
	return ids
}

// queuedItem reads the item id still names in the queue, so a removal about to
// drop it can cover the sub-issues it carries.
func (s *Server) queuedItem(root, id string) (queue.Item, bool) {
	items, _, err := s.stores.Queue(root).Snapshot()
	if err != nil {
		logger.Verbosef("queued label %s: read queue: %v", root, err)
		return queue.Item{}, false
	}
	return itemByID(items, id)
}
