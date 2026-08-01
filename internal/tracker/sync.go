package tracker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

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
	// Type and Level are the tracker's own work-item type name and the normalized
	// backlog level the project places it on. Azure DevOps only: every other
	// provider leaves both empty.
	Type     string
	Level    string
	Comments []SyncedComment
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

// azurePulls coalesces the sync work of repos that mirror the same Azure DevOps
// board. Two repos bound to one organization, team project and team scope produce
// byte-identical reads, so the first one in flight runs the WIQL, the batch read and
// the comment sweep and the rest of that tick read its answer; each still stores its
// own rows (ADR 0028 §7). A reader is built fresh per sync, so the group is
// package-level — process-wide is exactly the scope the sharing needs. Sharers also
// share the winner's context, which is safe because the syncer gives every repo on a
// tick the same budget.
var azurePulls singleflight.Group

// commentWorkers bounds the discussion sweep one pull fans out. Azure DevOps serves
// comments one work item at a time, so a full pull owes a round-trip per ticket that
// has any: serially that outlasts the sync budget, unbounded it spends the
// organization's whole request allowance in one burst.
const commentWorkers = 8

func (r *azureReader) SyncPull(ctx context.Context, binding ProjectBinding, since string) ([]SyncedIssue, error) {
	project := r.projectOf(binding)
	pulled, err, _ := azurePulls.Do(r.scopeKey("pull", project, since), func() (any, error) {
		return r.syncPull(ctx, project, since)
	})
	if err != nil {
		return nil, err
	}
	return pulled.([]SyncedIssue), nil
}

func (r *azureReader) syncPull(ctx context.Context, project, since string) ([]SyncedIssue, error) {
	items, err := r.workItems(ctx, project, since)
	if err != nil {
		return nil, err
	}
	blockers, err := r.client.BlockerStates(ctx, project, items)
	if err != nil {
		return nil, fmt.Errorf("read blockers: %w", err)
	}
	levels := readBacklogLevels(ctx, r.client, project, r.teams)
	out := make([]SyncedIssue, len(items))
	for i := range items {
		out[i] = r.synced(project, &items[i], blockers, levels)
	}
	r.sweepComments(ctx, project, items, out)
	return out, nil
}

func (r *azureReader) ProjectIdentifiers(ctx context.Context, binding ProjectBinding) ([]string, error) {
	project := r.projectOf(binding)
	pulled, err, _ := azurePulls.Do(r.scopeKey("identifiers", project, ""), func() (any, error) {
		return r.boardIDs(ctx, project, "")
	})
	if err != nil {
		return nil, err
	}
	ids := pulled.([]int)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, azureIdentifier(id))
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

// scopeKey names the read a repo's board scope produces, so two repos asking the
// same question of the same organization recognise each other's work.
func (r *azureReader) scopeKey(stage, project, since string) string {
	return strings.Join(append([]string{stage, r.org, project, r.areaPath, since}, r.teams...), "\x00")
}

// synced maps one work item onto a SyncedIssue, discussion excluded — that costs a
// round-trip per item and is swept separately. Work-item file attachments are not
// mirrored: their bytes sit behind the same PAT the pull holds, which the hub's
// attachment surface cannot present.
func (r *azureReader) synced(project string, item *azureapi.WorkItem, blockers map[int]bool, levels azureapi.Levels) SyncedIssue {
	out := SyncedIssue{
		ID:           azureIdentifier(item.ID),
		ExternalID:   strconv.Itoa(item.ID),
		Title:        item.Title,
		Description:  item.Description,
		Status:       item.State,
		Group:        mapAzureGroup(item.Category(), item.Reason),
		Priority:     item.Priority,
		Labels:       item.Tags,
		Parent:       azureParentIdentifier(item.Parent),
		HasChildren:  item.HasChildren(),
		URL:          r.client.WorkItemURL(project, item.ID),
		CreatedAt:    item.CreatedAt,
		UpdatedAt:    item.UpdatedAt,
		AssigneeID:   item.AssignedToID,
		AssigneeName: item.AssignedToName,
		Type:         item.Type,
		Level:        string(levels.Of(item.Type)),
	}
	for _, id := range item.BlockedBy {
		out.BlockedBy = append(out.BlockedBy, SyncedBlocker{
			ID:       azureIdentifier(id),
			Resolved: blockers[id],
		})
	}
	return out
}

// sweepComments fills in the discussions of the work items that report one, fanned
// out across commentWorkers because Azure DevOps serves them one item at a time.
// Losing a discussion costs the pull that discussion rather than the ticket, so a
// failed read is counted and named rather than returned — the group carries no
// context of its own, so one failure never cancels the reads still in flight.
func (r *azureReader) sweepComments(ctx context.Context, project string, items []azureapi.WorkItem, out []SyncedIssue) {
	var g errgroup.Group
	g.SetLimit(commentWorkers)
	var lost atomic.Int64
	for i := range items {
		if items[i].CommentCount == 0 {
			continue
		}
		g.Go(func() error {
			comments, err := r.client.Comments(ctx, project, items[i].ID)
			if err != nil {
				lost.Add(1)
				return fmt.Errorf("read comments for work item %d: %w", items[i].ID, err)
			}
			for _, c := range comments {
				out[i].Comments = append(out[i].Comments, SyncedComment{
					ExternalID: strconv.Itoa(c.ID),
					Author:     c.Author,
					Body:       c.Body,
					CreatedAt:  c.CreatedAt,
					UpdatedAt:  c.UpdatedAt,
				})
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		logger.Printf("azure: %d of %d work items pulled without their discussion: %v", lost.Load(), len(items), err)
	}
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
