package tracker

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/tracker/azureapi"
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
	ID           string
	ExternalID   string
	Title        string
	Description  string
	Status       string
	Group        StatusGroup
	Priority     int
	Labels       []string
	Parent       string
	HasChildren  bool
	DueDate      string
	URL          string
	CreatedAt    string
	UpdatedAt    string
	AssigneeID   string
	AssigneeName string
	Comments     []SyncedComment
	// Attachments are the files the issue references — the tracker's own file
	// list plus the images its markdown embeds. Metadata only: a pull never
	// downloads bytes.
	Attachments []Attachment
	// BlockedBy are the issue's inbound "blocked by" links as the tracker
	// reports them, so the hub reflects the relation graph for synced issues.
	BlockedBy []SyncedBlocker
}

// SyncedBlocker is one blocking issue on a pulled issue's blocked-by links: its
// identifier and whether the tracker already shows it resolved.
type SyncedBlocker struct {
	ID       string
	Resolved bool
}

func (r *linearReader) ResolveBinding(ctx context.Context) (ProjectBinding, error) {
	if strings.TrimSpace(r.team) == "" {
		return ProjectBinding{}, ErrNoTeamKey
	}
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
	scanner := AttachmentScanner{}
	for i := range issues {
		out = append(out, linearSynced(&issues[i], scanner))
	}
	return out, nil
}

func (r *linearReader) ProjectIdentifiers(ctx context.Context, binding ProjectBinding) ([]string, error) {
	return r.client.ProjectIssueIDs(ctx, binding.TeamID, binding.ProjectID)
}

func (r *linearReader) Identity(ctx context.Context) (id, name string, err error) {
	return r.client.Viewer(ctx)
}

func linearSynced(iss *linearapi.SyncIssue, scanner AttachmentScanner) SyncedIssue {
	out := SyncedIssue{
		ID:           iss.Identifier,
		ExternalID:   iss.ID,
		Title:        iss.Title,
		Description:  iss.Description,
		Status:       iss.State.Name,
		Group:        mapLinearGroup(iss.State.Type),
		Priority:     iss.Priority,
		Labels:       labelNames(iss.Labels),
		Parent:       iss.Parent,
		HasChildren:  iss.HasChildren,
		DueDate:      iss.DueDate,
		URL:          iss.URL,
		CreatedAt:    iss.CreatedAt,
		UpdatedAt:    iss.UpdatedAt,
		AssigneeID:   iss.AssigneeID,
		AssigneeName: iss.AssigneeName,
	}
	bodies := []string{iss.Description}
	for _, c := range iss.Comments {
		out.Comments = append(out.Comments, SyncedComment{
			ExternalID: c.ID,
			Author:     c.Author,
			Body:       c.Body,
			CreatedAt:  c.CreatedAt,
			UpdatedAt:  c.UpdatedAt,
		})
		bodies = append(bodies, c.Body)
	}
	for _, b := range iss.BlockedBy {
		out.BlockedBy = append(out.BlockedBy, SyncedBlocker{ID: b.Identifier, Resolved: b.State.IsTerminal()})
	}
	listed := make([]Attachment, 0, len(iss.Attachments))
	for _, at := range iss.Attachments {
		listed = append(listed, Attachment{URL: at.URL, Filename: at.Filename, Source: AttachmentLinear})
	}
	out.Attachments = mergeAttachments(listed, scanner.Scan(bodies...))
	return out
}

func (r *jiraReader) ResolveBinding(ctx context.Context) (ProjectBinding, error) {
	key := strings.TrimSpace(r.project)
	if key == "" {
		return ProjectBinding{}, ErrNoProjectKey
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
	scanner := NewAttachmentScanner(r.baseURL)
	for i := range issues {
		out = append(out, jiraSynced(&issues[i], scanner))
	}
	return out, nil
}

func (r *jiraReader) ProjectIdentifiers(ctx context.Context, binding ProjectBinding) ([]string, error) {
	key := strings.TrimSpace(binding.ProjectID)
	if key == "" {
		key = strings.TrimSpace(r.project)
	}
	return r.client.ProjectKeys(ctx, key)
}

func (r *jiraReader) Identity(ctx context.Context) (id, name string, err error) {
	return r.client.Myself(ctx)
}

func (r *azureReader) ResolveBinding(ctx context.Context) (ProjectBinding, error) {
	project := strings.TrimSpace(r.project)
	if project == "" {
		return ProjectBinding{}, ErrNoTeamProject
	}
	return ProjectBinding{ProjectID: project, Project: project}, nil
}

func (r *azureReader) SyncPull(ctx context.Context, binding ProjectBinding, since string) ([]SyncedIssue, error) {
	project := r.projectOf(binding)
	items, err := r.workItems(ctx, project, since)
	if err != nil {
		return nil, err
	}
	blockers, err := r.client.BlockerStates(ctx, project, items)
	if err != nil {
		return nil, err
	}
	out := make([]SyncedIssue, 0, len(items))
	var lost []error
	for i := range items {
		iss, err := r.synced(ctx, project, &items[i], blockers)
		if err != nil {
			lost = append(lost, err)
		}
		out = append(out, iss)
	}
	if len(lost) > 0 {
		logger.Printf("azure: %d of %d work items pulled without their discussion: %v", len(lost), len(items), lost[0])
	}
	return out, nil
}

func (r *azureReader) ProjectIdentifiers(ctx context.Context, binding ProjectBinding) ([]string, error) {
	ids, err := r.client.SyncIDs(ctx, r.projectOf(binding), r.areaPath, "")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, azureIdentifier(r.prefix, id))
	}
	return out, nil
}

func (r *azureReader) Identity(ctx context.Context) (id, name string, err error) {
	return r.client.ConnectionData(ctx)
}

// projectOf prefers the team project cached on the binding, falling back to the
// reader's own configured one.
func (r *azureReader) projectOf(b ProjectBinding) string {
	if p := strings.TrimSpace(b.ProjectID); p != "" {
		return p
	}
	return strings.TrimSpace(r.project)
}

// synced maps one work item onto a SyncedIssue. Azure DevOps serves a discussion
// one work item at a time, so the extra round-trip is spent only on items that
// report a comment; losing it costs the pull that discussion rather than the
// ticket, so the issue is returned alongside the read failure. Work-item file
// attachments are not mirrored: their bytes sit behind the same PAT the pull
// holds, which the hub's attachment surface cannot present.
func (r *azureReader) synced(ctx context.Context, project string, item *azureapi.WorkItem, blockers map[int]bool) (SyncedIssue, error) {
	out := SyncedIssue{
		ID:           azureIdentifier(r.prefix, item.ID),
		ExternalID:   strconv.Itoa(item.ID),
		Title:        item.Title,
		Description:  item.Description,
		Status:       item.State,
		Group:        mapAzureGroup(item.Category(), item.Reason),
		Priority:     item.Priority,
		Labels:       item.Tags,
		Parent:       azureParentIdentifier(r.prefix, item.Parent),
		HasChildren:  item.HasChildren(),
		URL:          r.client.WorkItemURL(project, item.ID),
		CreatedAt:    item.CreatedAt,
		UpdatedAt:    item.UpdatedAt,
		AssigneeID:   item.AssignedToID,
		AssigneeName: item.AssignedToName,
	}
	for _, id := range item.BlockedBy {
		out.BlockedBy = append(out.BlockedBy, SyncedBlocker{
			ID:       azureIdentifier(r.prefix, id),
			Resolved: blockers[id],
		})
	}
	if item.CommentCount == 0 {
		return out, nil
	}
	comments, err := r.client.Comments(ctx, project, item.ID)
	if err != nil {
		return out, fmt.Errorf("read comments for work item %d: %w", item.ID, err)
	}
	for _, c := range comments {
		out.Comments = append(out.Comments, SyncedComment{
			ExternalID: strconv.Itoa(c.ID),
			Author:     c.Author,
			Body:       c.Body,
			CreatedAt:  c.CreatedAt,
			UpdatedAt:  c.UpdatedAt,
		})
	}
	return out, nil
}

func jiraSynced(iss *jiraapi.SyncIssue, scanner AttachmentScanner) SyncedIssue {
	out := SyncedIssue{
		ID:           iss.Key,
		ExternalID:   iss.Key,
		Title:        iss.Summary,
		Description:  iss.Description,
		Status:       iss.Status.Name,
		Group:        mapJiraGroup(iss.Status.Category, iss.Resolution),
		Priority:     iss.Priority,
		Labels:       iss.Labels,
		Parent:       iss.Parent,
		HasChildren:  iss.HasChildren,
		DueDate:      iss.DueDate,
		CreatedAt:    iss.Created,
		UpdatedAt:    iss.Updated,
		AssigneeID:   iss.AssigneeID,
		AssigneeName: iss.AssigneeName,
	}
	bodies := []string{iss.Description}
	for _, c := range iss.Comments {
		out.Comments = append(out.Comments, SyncedComment{
			ExternalID: c.ID,
			Author:     c.Author,
			Body:       c.Body,
			CreatedAt:  c.Created,
			UpdatedAt:  c.Updated,
		})
		bodies = append(bodies, c.Body)
	}
	for _, b := range iss.BlockedBy {
		out.BlockedBy = append(out.BlockedBy, SyncedBlocker{ID: b.Key, Resolved: b.Resolved})
	}
	listed := make([]Attachment, 0, len(iss.Attachments))
	for _, at := range iss.Attachments {
		listed = append(listed, Attachment{
			URL:      at.Content,
			Filename: at.Filename,
			MimeType: at.MimeType,
			Size:     at.Size,
			Source:   AttachmentJira,
		})
	}
	out.Attachments = mergeAttachments(listed, scanner.Scan(bodies...))
	return out
}
