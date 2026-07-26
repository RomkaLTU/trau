package webserver

import (
	"context"
	"strings"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/tracker"
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
	lb, ok := s.queuedLabeler(root)
	if !ok {
		return
	}
	s.applyQueuedLabel(ctx, lb, ids, add)
}

// queuedLabeler is what one repo's queued-label writes need. It reports false for
// a repo with no queued label to write at all; a repo the hub can build no direct
// writer for resolves with a nil writer, leaving the internal and store-row
// writes to carry it.
type queuedLabeler struct {
	root   string
	label  string
	writer tracker.Writer
}

func (s *Server) queuedLabeler(root string) (queuedLabeler, bool) {
	repo, ok := s.findRepoByRoot(root)
	if !ok {
		return queuedLabeler{}, false
	}
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		logger.Verbosef("queued label %s: read config: %v", root, err)
		return queuedLabeler{}, false
	}
	label := strings.TrimSpace(cfg.QueuedLabel)
	if label == "" {
		return queuedLabeler{}, false
	}
	writer, err := s.newWriter(cfg)
	if err != nil {
		logger.Verbosef("queued label %s: no tracker writer: %v", root, err)
		writer = nil
	}
	return queuedLabeler{root: root, label: label, writer: writer}, true
}

func (s *Server) applyQueuedLabel(ctx context.Context, lb queuedLabeler, ids []string, add bool) {
	var addLabels, removeLabels []string
	if add {
		addLabels = []string{lb.label}
	} else {
		removeLabels = []string{lb.label}
	}

	for _, id := range ids {
		if _, internal, err := s.stores.Issues().Internal(lb.root, id); err == nil && internal {
			if _, err := s.stores.Issues().TransitionInternal(lb.root, id, hubstore.InternalTransition{
				AddLabels:    addLabels,
				RemoveLabels: removeLabels,
			}); err != nil {
				logger.Verbosef("queued label %s: transition internal issue: %v", id, err)
			}
			continue
		}
		if lb.writer != nil {
			if err := lb.writer.UpdateLabels(ctx, id, addLabels, removeLabels); err != nil {
				logger.Verbosef("queued label %s: update tracker labels: %v", id, err)
				continue
			}
		}
		if _, _, err := s.stores.Issues().UpdateSynced(lb.root, id, hubstore.SyncedPatch{
			AddLabels:    addLabels,
			RemoveLabels: removeLabels,
		}); err != nil {
			logger.Verbosef("queued label %s: mirror synced row: %v", id, err)
		}
	}
}

// reconcileQueuedLabels heals the repo's queued label against actual queue
// membership, running off the sync pull that just landed the tracker's labels.
// Only the disagreements are written, so a board already in step costs no tracker
// call. That covers a transition write the tracker refused, a label edited away by
// hand, and work queued before the label existed. Repos the hub can build no
// direct writer for are skipped: their rows would only be overwritten by the next
// pull.
func (s *Server) reconcileQueuedLabels(ctx context.Context, root string) {
	lb, ok := s.queuedLabeler(root)
	if !ok || lb.writer == nil {
		return
	}
	states, err := s.stores.Issues().LabelStates(root)
	if err != nil {
		logger.Verbosef("queued label %s: read stored labels: %v", root, err)
		return
	}
	items, _, err := s.stores.Queue(root).Snapshot()
	if err != nil {
		logger.Verbosef("queued label %s: read queue: %v", root, err)
		return
	}
	missing, stray := queuedLabelDrift(states, expectedQueuedLabel(items, states), lb.label)
	s.applyQueuedLabel(ctx, lb, missing, true)
	s.applyQueuedLabel(ctx, lb, stray, false)
}

// expectedQueuedLabel is the set of issues that should carry the queued label:
// planned work the hub has not started. A pending item — paused included, since a
// resume re-attempts it — covers its own issue and every sub-issue it captured. A
// running epic covers only the captured sub-issues still sitting in
// backlog/unstarted, so mid-epic the finished and in-flight slices drop out. Issues
// no sync has pulled are left out: the hub only labels what its store knows.
func expectedQueuedLabel(items []queue.Item, states []hubstore.LabelState) map[string]bool {
	groups := make(map[string]string, len(states))
	for _, st := range states {
		groups[st.Identifier] = st.StatusGroup
	}
	want := map[string]bool{}
	mark := func(id string) {
		if _, known := groups[id]; known {
			want[id] = true
		}
	}
	for _, it := range items {
		switch {
		case it.Status == queue.StatusPending, it.Status == queue.StatusPaused:
			for _, id := range queuedLabelTargets(it) {
				mark(id)
			}
		case it.Status == queue.StatusRunning && it.Kind == queue.KindEpic:
			for _, sub := range it.SubIssues {
				if plannedGroup(groups[sub.ID]) {
					mark(sub.ID)
				}
			}
		}
	}
	return want
}

func plannedGroup(group string) bool {
	return group == string(tracker.StatusGroupBacklog) || group == string(tracker.StatusGroupUnstarted)
}

// queuedLabelDrift splits the stored issues into the ones missing the queued label
// they should carry and the ones carrying one they should not, comparing labels
// the way the store merges them — case-insensitively.
func queuedLabelDrift(states []hubstore.LabelState, want map[string]bool, label string) (missing, stray []string) {
	for _, st := range states {
		switch has := hasLabel(st.Labels, label); {
		case want[st.Identifier] && !has:
			missing = append(missing, st.Identifier)
		case !want[st.Identifier] && has:
			stray = append(stray, st.Identifier)
		}
	}
	return missing, stray
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
