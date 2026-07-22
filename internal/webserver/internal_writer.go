package webserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// internalWriter is the tracker.Writer for repos on the internal provider: it
// files grilled outcomes straight into the hub's issue store (ADR 0007). It runs
// inside the hub, which owns the database, so unlike tracker.Internal there is
// no hubclient hop — writes call the store directly.
type internalWriter struct {
	server *Server
	root   string
	prefix string
}

func (s *Server) internalWriterFor(repo registry.Repo, cfg config.Config) tracker.Writer {
	return &internalWriter{
		server: s,
		root:   repo.Root,
		prefix: config.InternalPrefix(cfg.IssuePrefixConfigured, repo.Name),
	}
}

// CreateIssue returns the hub's own issue link as the URL — an internal issue
// has no external home.
func (w *internalWriter) CreateIssue(_ context.Context, draft tracker.IssueDraft) (tracker.NewIssue, error) {
	iss, err := w.server.stores.Issues().CreateInternal(w.root, w.prefix, hubstore.InternalDraft{
		Title:       draft.Title,
		Description: draft.Description,
		Labels:      draft.Labels,
		Parent:      draft.Parent,
	})
	if err != nil {
		return tracker.NewIssue{}, err
	}
	w.server.registerAttachments(w.root, iss.Identifier, scanIssueImages(iss.Description))
	w.server.bindUploadedAttachments(w.root, iss.Identifier, iss.Description)
	return tracker.NewIssue{Identifier: iss.Identifier, URL: "/backlog?issue=" + iss.Identifier}, nil
}

func (w *internalWriter) AddComment(_ context.Context, id, body string) error {
	_, err := w.server.stores.Issues().TransitionInternal(w.root, id, hubstore.InternalTransition{Comment: body})
	return err
}

func (w *internalWriter) UpdateDescription(_ context.Context, id, body string) error {
	issues := w.server.stores.Issues()
	iss, found, err := issues.Internal(w.root, id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s: %w", id, hubstore.ErrInternalIssueNotFound)
	}
	if _, err := issues.UpdateInternal(w.root, id, hubstore.InternalDraft{
		Title:       iss.Title,
		Description: body,
		State:       iss.StatusGroup,
		Labels:      iss.Labels,
		Parent:      iss.Parent,
	}); err != nil {
		return err
	}
	w.server.registerAttachments(w.root, id, scanIssueImages(body))
	w.server.bindUploadedAttachments(w.root, id, body)
	return nil
}

func (w *internalWriter) UpdateLabels(_ context.Context, id string, add, remove []string) error {
	_, err := w.server.stores.Issues().TransitionInternal(w.root, id, hubstore.InternalTransition{
		AddLabels:    add,
		RemoveLabels: remove,
	})
	return err
}

// LinkBlocks persists the blocker→blocked edge in the issue store, so an internal
// epic keeps the execution ordering wireGrillBlocks wires between its slices.
// AddRelation is idempotent, so a re-apply wires nothing new.
func (w *internalWriter) LinkBlocks(_ context.Context, blocker, blocked string) error {
	return w.server.stores.Issues().AddRelation(w.root, blocker, blocked)
}

func (w *internalWriter) PublishDocument(context.Context, tracker.DocumentDraft) (tracker.PublishedDocument, error) {
	return tracker.PublishedDocument{}, errors.New("internal issues have no document store; publishing a PRD needs an external tracker")
}

func (w *internalWriter) AssignIssue(context.Context, string, string) error {
	return fmt.Errorf("internal issues are always Unassigned: %w", tracker.ErrUnsupported)
}

func (w *internalWriter) AssignableUsers(context.Context, string) ([]tracker.AssignableUser, error) {
	return nil, fmt.Errorf("internal issues have no tracker users to assign: %w", tracker.ErrUnsupported)
}
