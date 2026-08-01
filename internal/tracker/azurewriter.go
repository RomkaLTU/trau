package tracker

import (
	"context"
	"fmt"
	"strings"

	"github.com/RomkaLTU/trau/internal/tracker/azureapi"
)

// azureWriter creates and edits Azure DevOps work items directly through the Work
// Item Tracking REST API, as the single identity behind the repo's personal access
// token. It files at the levels the project's own backlog configuration declares:
// a create is a requirement (a User Story, or a Bug for a defect) and a slice of
// it is a Task. trau never files an Epic or a Feature — the Feature above a
// created story is one the board already has, and re-parenting anywhere else is
// left to Azure DevOps (ADR 0031).
//
// Assignment is the one gap: Azure identity search lives on a separate Graph host
// behind a PAT scope ADR 0024 does not require, so it reports ErrUnsupported and
// the hub hides the affordance.
type azureWriter struct {
	client  *azureapi.Client
	project string
	// team is the board whose backlog configuration decides the level model. The
	// hub builds a writer per request, so levels is resolved at most once per apply.
	team   string
	levels *azureapi.Levels
}

func (w *azureWriter) CreateIssue(ctx context.Context, draft IssueDraft) (NewIssue, error) {
	itemType, err := w.itemType(ctx, draft)
	if err != nil {
		return NewIssue{}, err
	}
	var parent int
	if id := strings.TrimSpace(draft.Parent); id != "" {
		if parent, err = workItemID(id); err != nil {
			return NewIssue{}, fmt.Errorf("resolve parent %s: %w", id, err)
		}
	}
	id, err := w.client.CreateWorkItem(ctx, w.project, azureapi.NewWorkItem{
		Type:        itemType,
		Title:       draft.Title,
		Description: draft.Description,
		Tags:        draft.Labels,
		Parent:      parent,
	})
	if err != nil {
		return NewIssue{}, err
	}
	return NewIssue{Identifier: azureIdentifier(id), URL: w.client.WorkItemURL(w.project, id)}, nil
}

// itemType resolves the work-item type a draft files as: a slice of an issue this
// apply is building out is a Task, everything else a requirement. A pinned type is
// honoured only when the project places it on that same level, which is what keeps
// a create from filing an Epic or a Feature.
func (w *azureWriter) itemType(ctx context.Context, draft IssueDraft) (string, error) {
	levels, err := w.backlogLevels(ctx)
	if err != nil {
		return "", err
	}
	level := azureapi.LevelRequirement
	if draft.Slice {
		level = azureapi.LevelTask
	}
	if pinned := strings.TrimSpace(draft.Type); pinned != "" {
		if levels.Of(pinned) != level {
			return "", fmt.Errorf("azure: %q is not a %s-level work-item type on %s", pinned, level, w.project)
		}
		return pinned, nil
	}
	name := levels.Default(level)
	if name == "" {
		return "", fmt.Errorf("azure: %s declares no %s-level work-item type to file", w.project, level)
	}
	return name, nil
}

// CreatableTypes lists the requirement-level work-item types a create may file
// as — the project's own default first, and the Bug type beside it when the team
// files bugs as requirements rather than as tasks.
func (w *azureWriter) CreatableTypes(ctx context.Context) ([]string, error) {
	levels, err := w.backlogLevels(ctx)
	if err != nil {
		return nil, err
	}
	return levels.At(azureapi.LevelRequirement), nil
}

func (w *azureWriter) backlogLevels(ctx context.Context) (azureapi.Levels, error) {
	if w.levels == nil {
		levels, err := w.client.BacklogLevels(ctx, w.project, w.team)
		if err != nil {
			return azureapi.Levels{}, err
		}
		w.levels = &levels
	}
	return *w.levels, nil
}

func (w *azureWriter) AddComment(ctx context.Context, id, body string) error {
	n, err := workItemID(id)
	if err != nil {
		return err
	}
	return w.client.AddComment(ctx, w.project, n, body)
}

func (w *azureWriter) UpdateDescription(ctx context.Context, id, body string) error {
	n, err := workItemID(id)
	if err != nil {
		return err
	}
	return w.client.SetDescription(ctx, w.project, n, body)
}

func (w *azureWriter) UpdateLabels(ctx context.Context, id string, add, remove []string) error {
	n, err := workItemID(id)
	if err != nil {
		return err
	}
	return w.client.UpdateTags(ctx, w.project, n, add, remove)
}

func (w *azureWriter) LinkBlocks(ctx context.Context, blocker, blocked string) error {
	from, err := workItemID(blocker)
	if err != nil {
		return err
	}
	to, err := workItemID(blocked)
	if err != nil {
		return err
	}
	return w.client.LinkPredecessor(ctx, w.project, to, from)
}

// PublishDocument files the PRD as a work item and links to it, the same fallback
// Jira takes: Azure DevOps keeps its documents in a wiki, which is a separate host
// and a PAT scope the tracker credentials do not carry.
func (w *azureWriter) PublishDocument(ctx context.Context, draft DocumentDraft) (PublishedDocument, error) {
	created, err := w.CreateIssue(ctx, IssueDraft{Title: draft.Title, Description: draft.Markdown})
	if err != nil {
		return PublishedDocument{}, err
	}
	return PublishedDocument{URL: created.URL, Identifier: created.Identifier, Kind: DocumentKindIssue}, nil
}

func (w *azureWriter) AssignIssue(ctx context.Context, id, assigneeID string) error {
	return fmt.Errorf("tracker: azure assignment over the direct API is not supported: %w", ErrUnsupported)
}

func (w *azureWriter) AssignableUsers(ctx context.Context, query string) ([]AssignableUser, error) {
	return nil, fmt.Errorf("tracker: azure assignee lookup over the direct API is not supported: %w", ErrUnsupported)
}
