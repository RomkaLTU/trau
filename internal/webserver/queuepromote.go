package webserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/RomkaLTU/trau/internal/folderrepo"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/queue"
)

// promoteQueuedEpic collapses an epic's children into the epic itself once the
// queue holds every child that still needs work: the pending child rows are
// swapped for one epic row in the first child's place, so a queue filled in
// ticket by ticket describes the same run queueing the epic would have. It runs
// after a ticket lands and is best-effort — an epic that cannot be resolved
// leaves the children queued as they are. A Folder repo refuses to run an epic at
// all, so its queue keeps the sub-issues as the individual tickets it accepted.
func (s *Server) promoteQueuedEpic(ctx context.Context, root string, item queue.Item) {
	if item.Kind != queue.KindTicket || folderrepo.Is(root) {
		return
	}
	epic, children, err := s.epicToPromote(ctx, root, item.ID)
	if err != nil {
		logger.Verbosef("promote epic over %s: %v", item.ID, err)
		return
	}
	if len(children) == 0 {
		return
	}
	if _, err := s.stores.Queue(root).Promote(epic, children); err != nil {
		logger.Verbosef("promote epic %s: %v", epic.ID, err)
		return
	}
	s.markQueued(ctx, root, epic)
}

// epicToPromote resolves the epic a just-queued ticket completes and the child
// rows a promotion swaps for it, answering no children whenever the queue does
// not yet cover the whole epic. The store's own view of the family gates the
// tracker read, so only the add that completes an epic pays for the preview.
func (s *Server) epicToPromote(ctx context.Context, root, id string) (queue.Item, []string, error) {
	iss, found, err := s.stores.Issues().Get(root, id)
	if err != nil {
		return queue.Item{}, nil, fmt.Errorf("read issue: %w", err)
	}
	if !found || iss.Parent == "" {
		return queue.Item{}, nil, nil
	}
	queued, err := s.stores.Queue(root).Load()
	if err != nil {
		return queue.Item{}, nil, fmt.Errorf("read queue: %w", err)
	}
	siblings, err := s.stores.Issues().Children(root, iss.Parent)
	if err != nil {
		return queue.Item{}, nil, fmt.Errorf("read children: %w", err)
	}
	hidden := hiddenChildren(siblings)
	if _, ok := promotableChildren(queued, storedSubIssues(siblings), hidden); !ok {
		return queue.Item{}, nil, nil
	}
	epic, err := s.epicQueueItem(ctx, root, iss.Parent)
	if err != nil {
		return queue.Item{}, nil, err
	}
	children, ok := promotableChildren(queued, epic.SubIssues, hidden)
	if !ok {
		return queue.Item{}, nil, nil
	}
	epic.Provider = sharedProvider(queued, children)
	return epic, children, nil
}

// epicQueueItem builds the item a direct registration would have queued for an
// epic: its stored title and source binding, and its sub-issues read the way
// enqueue reads them — from the store for an internal issue, through the tracker
// preview for a synced one.
func (s *Server) epicQueueItem(ctx context.Context, root, id string) (queue.Item, error) {
	iss, found, err := s.stores.Issues().Get(root, id)
	if err != nil {
		return queue.Item{}, fmt.Errorf("read epic: %w", err)
	}
	if !found {
		return queue.Item{}, errors.New("epic " + id + " is not in the issue store")
	}
	item := queue.Item{Kind: queue.KindEpic, ID: id, Title: iss.Title, Source: iss.Source}
	if iss.Source == hubstore.SourceInternal {
		children, err := s.stores.Issues().InternalChildren(root, id)
		if err != nil {
			return queue.Item{}, fmt.Errorf("read internal children: %w", err)
		}
		item.SubIssues = storedSubIssues(children)
		return item, nil
	}
	subs, err := s.listEpicSubIssues(ctx, root, id)
	if err != nil {
		return queue.Item{}, fmt.Errorf("epic preview: %w", err)
	}
	item.SubIssues = toQueueSubIssues(subs)
	return item, nil
}

// promotableChildren lists the rows a promotion swaps for the epic: one per
// sub-issue that still needs work, skipping the ones hidden reports as no longer
// part of the family. It reports false unless every one of them is queued as a
// pending ticket of its own, so a partly queued epic, a child already running, and
// a child whose own run settled are all left alone.
func promotableChildren(items []queue.Item, subs []queue.SubIssue, hidden map[string]bool) ([]string, bool) {
	children := make([]string, 0, len(subs))
	for _, sub := range subs {
		if sub.State == subIssueDone || hidden[sub.ID] {
			continue
		}
		it, queued := itemByID(items, sub.ID)
		if !queued || it.Kind != queue.KindTicket || it.Status != queue.StatusPending {
			return nil, false
		}
		children = append(children, sub.ID)
	}
	return children, len(children) > 0
}

// sharedProvider returns the per-run provider override the rows being promoted
// agree on, so an epic collapsed out of children that all pinned one keeps it.
// Any disagreement falls back to the configured routing.
func sharedProvider(items []queue.Item, ids []string) string {
	shared := ""
	for i, id := range ids {
		it, _ := itemByID(items, id)
		if i > 0 && it.Provider != shared {
			return ""
		}
		shared = it.Provider
	}
	return shared
}

// hiddenChildren collects the stored children the board leaves out of an epic's
// family — the deleted and archived rows — so the promotion asks after the family
// the operator sees whichever source listed the epic's sub-issues. A hidden child
// is not work the queue has to cover.
func hiddenChildren(children []hubstore.Issue) map[string]bool {
	hidden := map[string]bool{}
	for _, c := range children {
		if c.DeletedAt != "" || c.ArchivedAt != "" {
			hidden[c.Identifier] = true
		}
	}
	return hidden
}
