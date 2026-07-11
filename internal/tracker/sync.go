package tracker

import (
	"context"
	"strings"

	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
)

// ProjectBinding is a repo's resolved tracker target — the stable ids a sync pull
// filters on. The hub caches it so later syncs skip the team/project lookup. For
// Linear both are node ids; for Jira the project is a key and TeamID is empty.
type ProjectBinding struct {
	TeamID    string
	ProjectID string
	Project   string
}

// Resolved reports whether the binding carries a target to filter on, so a caller
// can tell a cached binding from an empty one that still needs resolving.
func (b ProjectBinding) Resolved() bool {
	return strings.TrimSpace(b.TeamID) != "" || strings.TrimSpace(b.ProjectID) != ""
}

// SyncedComment is one comment pulled with an issue for the local store.
type SyncedComment struct {
	ExternalID string
	Author     string
	Body       string
	CreatedAt  string
	UpdatedAt  string
}

// SyncedIssue is one issue pulled for the hub's local store, carrying the full
// content trau's working copy keeps: description, comments, and the metadata the
// backlog and prompt-building read.
type SyncedIssue struct {
	ID          string
	ExternalID  string
	Title       string
	Description string
	Status      string
	Group       StatusGroup
	Priority    int
	Labels      []string
	Parent      string
	HasChildren bool
	DueDate     string
	URL         string
	CreatedAt   string
	UpdatedAt   string
	Comments    []SyncedComment
}

func (r *linearReader) ResolveBinding(ctx context.Context) (ProjectBinding, error) {
	team, err := r.client.TeamByKey(ctx, r.team)
	if err != nil {
		return ProjectBinding{}, err
	}
	b := ProjectBinding{TeamID: team.ID}
	if project := strings.TrimSpace(r.project); project != "" {
		proj, err := r.client.ProjectByName(ctx, project)
		if err != nil {
			return ProjectBinding{}, err
		}
		b.ProjectID = proj.ID
		b.Project = proj.Name
	}
	return b, nil
}

func (r *linearReader) SyncPull(ctx context.Context, binding ProjectBinding, since string) ([]SyncedIssue, error) {
	issues, err := r.client.ProjectIssues(ctx, binding.TeamID, binding.ProjectID, since)
	if err != nil {
		return nil, err
	}
	out := make([]SyncedIssue, 0, len(issues))
	for i := range issues {
		out = append(out, linearSynced(&issues[i]))
	}
	return out, nil
}

func linearSynced(iss *linearapi.SyncIssue) SyncedIssue {
	out := SyncedIssue{
		ID:          iss.Identifier,
		ExternalID:  iss.ID,
		Title:       iss.Title,
		Description: iss.Description,
		Status:      iss.State.Name,
		Group:       mapLinearGroup(iss.State.Type),
		Priority:    iss.Priority,
		Labels:      labelNames(iss.Labels),
		Parent:      iss.Parent,
		HasChildren: iss.HasChildren,
		DueDate:     iss.DueDate,
		URL:         iss.URL,
		CreatedAt:   iss.CreatedAt,
		UpdatedAt:   iss.UpdatedAt,
	}
	for _, c := range iss.Comments {
		out.Comments = append(out.Comments, SyncedComment{
			ExternalID: c.ID,
			Author:     c.Author,
			Body:       c.Body,
			CreatedAt:  c.CreatedAt,
			UpdatedAt:  c.UpdatedAt,
		})
	}
	return out
}

func (r *jiraReader) ResolveBinding(ctx context.Context) (ProjectBinding, error) {
	key := strings.TrimSpace(r.project)
	if key == "" {
		return ProjectBinding{}, ErrReaderUnavailable
	}
	return ProjectBinding{ProjectID: key, Project: key}, nil
}

func (r *jiraReader) SyncPull(ctx context.Context, binding ProjectBinding, since string) ([]SyncedIssue, error) {
	key := strings.TrimSpace(binding.ProjectID)
	if key == "" {
		key = strings.TrimSpace(r.project)
	}
	issues, err := r.client.SyncIssues(ctx, key, since)
	if err != nil {
		return nil, err
	}
	out := make([]SyncedIssue, 0, len(issues))
	for i := range issues {
		out = append(out, jiraSynced(&issues[i]))
	}
	return out, nil
}

func jiraSynced(iss *jiraapi.SyncIssue) SyncedIssue {
	out := SyncedIssue{
		ID:          iss.Key,
		ExternalID:  iss.Key,
		Title:       iss.Summary,
		Description: iss.Description,
		Status:      iss.Status.Name,
		Group:       mapJiraGroup(iss.Status.Category, iss.Resolution),
		Priority:    iss.Priority,
		Labels:      iss.Labels,
		Parent:      iss.Parent,
		HasChildren: iss.IsEpic,
		DueDate:     iss.DueDate,
		CreatedAt:   iss.Created,
		UpdatedAt:   iss.Updated,
	}
	for _, c := range iss.Comments {
		out.Comments = append(out.Comments, SyncedComment{
			ExternalID: c.ID,
			Author:     c.Author,
			Body:       c.Body,
			CreatedAt:  c.Created,
			UpdatedAt:  c.Updated,
		})
	}
	return out
}
