package tracker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/tracker/azureapi"
	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
)

// ErrReaderUnavailable means the repo carries no direct tracker credentials, so
// the hub cannot browse the backlog without falling back to an agent/MCP — which
// a Reader never does. It is also returned for GitHub, the one provider the hub
// cannot sync, so the hub shows a backlog-unavailable state instead of erroring;
// those repos keep reading their tickets straight from the tracker.
var ErrReaderUnavailable = errors.New("tracker: no direct API credentials configured")

// ErrNoProjectKey means a Jira repo carries valid REST credentials but no project
// key, so the reader can reach the tracker yet has no project to bind. Distinct
// from ErrReaderUnavailable (no credentials at all): the fix is the project key.
var ErrNoProjectKey = errors.New("jira: no project key configured — set LINEAR_TEAM in this repo's .trau.ini")

// ErrNoTeamKey is the Linear counterpart of ErrNoProjectKey: a usable API key but
// no team to bind. A key resolved from the user layer makes the provider look
// configured for every repo, so refusing locally keeps those repos off the shared
// key's budget.
var ErrNoTeamKey = errors.New("linear: no team key configured — set LINEAR_TEAM in this repo's .trau.ini")

// ErrNoTeamProject is the Azure DevOps counterpart of ErrNoProjectKey: a usable
// organization URL and token but no team project to bind. Every work-item route is
// project-scoped, so there is nothing to pull until one is named.
var ErrNoTeamProject = errors.New("azure: no team project configured — set LINEAR_TEAM in this repo's .trau.ini")

// ErrIssueNotFound means the tracker has no issue with the requested identifier,
// so a caller can tell a mistyped or absent ticket apart from a transport error.
var ErrIssueNotFound = errors.New("tracker: issue not found")

// StatusGroup buckets a tracker issue's workflow state into a small,
// provider-neutral set the backlog board groups its columns on.
type StatusGroup string

const (
	StatusGroupBacklog   StatusGroup = "backlog"
	StatusGroupUnstarted StatusGroup = "unstarted"
	StatusGroupStarted   StatusGroup = "started"
	StatusGroupDone      StatusGroup = "done"
	StatusGroupCanceled  StatusGroup = "canceled"
	StatusGroupUnknown   StatusGroup = "unknown"
)

// BacklogItem is one issue in a Project's full backlog: its identifier, title,
// display status and normalized group, label names, and epic relationship.
// Parent carries the epic a sub-issue belongs to ("" for a top-level issue);
// HasChildren marks an issue that is itself an epic/parent. Ready reports whether
// the issue carries the repo's ready label, so the board can distinguish it.
type BacklogItem struct {
	ID          string
	Title       string
	Status      string
	Group       StatusGroup
	Labels      []string
	Parent      string
	HasChildren bool
	Ready       bool
}

// IssueSummary is one issue read by identifier: the BacklogItem fields plus the
// issue's own project and whether it falls inside the slice of the tracker this repo
// owns. A ticket with InProject false exists but is out of that scope — another
// project, or a part of this one the repo does not mirror — so the hub can refuse to
// run it here instead of trusting the raw id. When the repo narrows nothing,
// InProject is always true — there is no guard.
type IssueSummary struct {
	BacklogItem
	Project   string
	InProject bool
}

// Reader lists a Project's full tracker backlog directly through a provider's
// REST/GraphQL API, with no agent process and no MCP. It is the read counterpart
// of Writer: the seam the serve hub uses to browse every ticket in the Active
// repo's Project — not just the eligible queue — using the repo's own credentials.
type Reader interface {
	Backlog(ctx context.Context) ([]BacklogItem, error)
	// Issue fetches one issue by its human identifier (e.g. "COD-712"), so the
	// hub can confirm a specific ticket before running it. It returns
	// ErrIssueNotFound when the tracker has no such issue. A ticket outside the
	// repo's scope is returned with InProject false rather than hidden, so the
	// caller can explain why it cannot be run here.
	Issue(ctx context.Context, id string) (IssueSummary, error)
	// ResolveBinding resolves the repo's configured team/project to the tracker's
	// stable ids, so the hub caches them and later syncs skip the lookup (ADR
	// 0007). Callers persist the result and pass it back to SyncPull.
	ResolveBinding(ctx context.Context) (ProjectBinding, error)
	// SyncPull returns the repo's Project issues — description and comments
	// included — through one server-side Project-filtered query, never the
	// whole-team walk. binding carries the ids resolved by ResolveBinding. When
	// since is a tracker timestamp the pull is incremental, returning only issues
	// updated after it; an empty since is a full Project pull.
	SyncPull(ctx context.Context, binding ProjectBinding, since string) ([]SyncedIssue, error)
	// ProjectIdentifiers returns just the human identifiers of every issue in the
	// repo's Project — the cheap full set (identifiers only) a reconciliation sweep
	// diffs against the store to catch issues deleted, archived, or moved out of the
	// Project, which an updated-since SyncPull never reports (ADR 0007). binding
	// carries the ids resolved by ResolveBinding.
	ProjectIdentifiers(ctx context.Context, binding ProjectBinding) ([]string, error)
	// Identity resolves who the repo binding's credentials belong to — the tracker
	// user behind them, returned as a stable id and display name. It is the hub's
	// Me for the repo: for Linear a personal API key is a user (its viewer), for
	// Jira the token's /myself. A sync resolves it best-effort — an identity failure
	// never blocks the issue pull.
	Identity(ctx context.Context) (id, name string, err error)
}

// RateLimited reports whether err is a tracker refusing the request because the
// API key's shared request budget is spent, and when it said the budget refills
// (zero when it did not say). A rate limit is transient and self-healing, so a
// caller waits it out rather than recording a failure the repo cannot fix.
func RateLimited(err error) (resetAt time.Time, ok bool) {
	var linear *linearapi.RateLimitError
	if errors.As(err, &linear) {
		return linear.ResetAt, true
	}
	var jira *jiraapi.RateLimitError
	if errors.As(err, &jira) {
		return jira.ResetAt, true
	}
	var azure *azureapi.RateLimitError
	if errors.As(err, &azure) {
		return azure.ResetAt, true
	}
	return time.Time{}, false
}

// Unauthorized reports whether err is a tracker rejecting the repo's credentials,
// whichever provider answered.
func Unauthorized(err error) bool {
	return errors.Is(err, linearapi.ErrUnauthorized) ||
		errors.Is(err, jiraapi.ErrUnauthorized) ||
		errors.Is(err, azureapi.ErrUnauthorized)
}

// ErrorKind labels a sync failure by what it takes to clear. A rate limit or a
// transport failure clears itself; a config failure needs the repo's settings or
// credentials fixed before anything will pull.
type ErrorKind string

const (
	ErrorConfig    ErrorKind = "config"
	ErrorRateLimit ErrorKind = "rate-limit"
	ErrorTransient ErrorKind = "transient"
)

// Classify labels err so the health surface can tell a repo that is briefly cut off
// from one that is misconfigured. Anything unrecognised reads as config, so a failure
// nobody can promise will clear still asks a human to look.
func Classify(err error) ErrorKind {
	if _, limited := RateLimited(err); limited || errors.Is(err, jiraapi.ErrRateLimited) {
		return ErrorRateLimit
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return ErrorTransient
	}
	return ErrorConfig
}

// NewReader builds a direct Reader for the provider from cfg, or
// ErrReaderUnavailable when cfg carries no usable direct credentials. A Reader
// only ever uses the direct API — it never falls back to an agent/MCP, so the
// hub's reads stay a single, explicit tracker identity.
func NewReader(provider string, cfg Config) (Reader, error) {
	switch provider {
	case "linear":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, ErrReaderUnavailable
		}
		return &linearReader{client: linearapi.New(cfg.APIKey), team: cfg.Team, project: cfg.Project, readyLabel: cfg.ReadyLabel}, nil
	case "jira":
		if cfg.BaseURL == "" || cfg.Email == "" || cfg.APIKey == "" {
			return nil, ErrReaderUnavailable
		}
		return &jiraReader{
			client:     jiraapi.New(cfg.BaseURL, cfg.Email, cfg.APIKey),
			baseURL:    cfg.BaseURL,
			project:    cfg.Team,
			readyLabel: cfg.ReadyLabel,
		}, nil
	case "azure":
		if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.APIKey) == "" {
			return nil, ErrReaderUnavailable
		}
		return &azureReader{
			client:      azureapi.New(cfg.BaseURL, cfg.APIKey),
			org:         cfg.BaseURL,
			project:     cfg.Team,
			areaPath:    cfg.AreaPath,
			teams:       cfg.BoardTeams,
			readyLabel:  cfg.ReadyLabel,
			statusTodo:  cfg.StatusOverrides[StageTodo],
			boardStates: parseAzureBoardStates(cfg.BoardStates),
		}, nil
	case "github":
		return nil, ErrReaderUnavailable
	default:
		return nil, fmt.Errorf("unknown tracker provider %q (expected: linear | jira | azure)", provider)
	}
}

type linearReader struct {
	client     *linearapi.Client
	team       string
	project    string
	readyLabel string
}

func (r *linearReader) Backlog(ctx context.Context) ([]BacklogItem, error) {
	team, err := r.client.TeamByKey(ctx, r.team)
	if err != nil {
		return nil, err
	}
	issues, err := r.client.TeamBacklog(ctx, team.ID)
	if err != nil {
		return nil, err
	}
	out := make([]BacklogItem, 0, len(issues))
	for _, iss := range issues {
		if !inProject(iss.ProjectName, r.project) {
			continue
		}
		labels := labelNames(iss.Labels)
		out = append(out, BacklogItem{
			ID:          iss.Identifier,
			Title:       iss.Title,
			Status:      iss.State.Name,
			Group:       mapLinearGroup(iss.State.Type),
			Labels:      labels,
			Parent:      iss.ParentID,
			HasChildren: iss.HasChildren,
			Ready:       containsLabel(labels, r.readyLabel),
		})
	}
	return out, nil
}

func (r *linearReader) Issue(ctx context.Context, id string) (IssueSummary, error) {
	iss, err := r.client.Issue(ctx, id)
	if err != nil {
		if errors.Is(err, linearapi.ErrNotFound) {
			return IssueSummary{}, ErrIssueNotFound
		}
		return IssueSummary{}, err
	}
	labels := labelNames(iss.Labels)
	return IssueSummary{
		BacklogItem: BacklogItem{
			ID:          iss.Identifier,
			Title:       iss.Title,
			Status:      iss.State.Name,
			Group:       mapLinearGroup(iss.State.Type),
			Labels:      labels,
			Parent:      iss.Parent.Identifier,
			HasChildren: len(iss.Children) > 0,
			Ready:       containsLabel(labels, r.readyLabel),
		},
		Project:   iss.Project.Name,
		InProject: inProject(iss.Project.Name, r.project),
	}, nil
}

type jiraReader struct {
	client     *jiraapi.Client
	baseURL    string
	project    string
	readyLabel string
}

func (r *jiraReader) Backlog(ctx context.Context) ([]BacklogItem, error) {
	issues, err := r.client.Backlog(ctx, r.project)
	if err != nil {
		return nil, err
	}
	out := make([]BacklogItem, 0, len(issues))
	for _, iss := range issues {
		out = append(out, BacklogItem{
			ID:          iss.Key,
			Title:       iss.Summary,
			Status:      iss.StatusName,
			Group:       mapJiraGroup(iss.StatusCategory, iss.Resolution),
			Labels:      iss.Labels,
			Parent:      iss.ParentKey,
			HasChildren: iss.HasChildren,
			Ready:       containsLabel(iss.Labels, r.readyLabel),
		})
	}
	return out, nil
}

func (r *jiraReader) Issue(ctx context.Context, id string) (IssueSummary, error) {
	iss, err := r.client.Issue(ctx, id)
	if err != nil {
		if errors.Is(err, jiraapi.ErrNotFound) {
			return IssueSummary{}, ErrIssueNotFound
		}
		return IssueSummary{}, err
	}
	return IssueSummary{
		BacklogItem: BacklogItem{
			ID:          iss.Key,
			Title:       iss.Summary,
			Status:      iss.Status.Name,
			Group:       mapJiraGroup(iss.Status.Category, iss.Resolution),
			Labels:      iss.Labels,
			Parent:      iss.Parent,
			HasChildren: iss.HasChildren,
			Ready:       containsLabel(iss.Labels, r.readyLabel),
		},
		Project:   iss.Project.Key,
		InProject: inProject(iss.Project.Key, r.project),
	}, nil
}

// azureReader reads an Azure DevOps team project's work items over the Work Item
// Tracking REST API. Work item numbers are unique organization-wide, so every
// identifier the reader hands back is the number itself (ADR 0024 §1). areaPath and
// teams narrow the board to a slice of the project; with neither set the whole team
// project is mirrored.
type azureReader struct {
	client     *azureapi.Client
	org        string
	project    string
	areaPath   string
	teams      []string
	readyLabel string
	statusTodo string // STATUS_TODO, which also decides which Proposed state groups as unstarted
	// boardStates maps the team's Kanban columns onto trau's status groups, empty
	// when the repo sets no AZURE_BOARD_STATES and grouping stays category-derived.
	boardStates azureBoardStates
}

func (r *azureReader) Backlog(ctx context.Context) ([]BacklogItem, error) {
	items, err := r.workItems(ctx, r.project, "")
	if err != nil {
		return nil, err
	}
	out := make([]BacklogItem, 0, len(items))
	for i := range items {
		out = append(out, r.backlogItem(ctx, &items[i]))
	}
	return out, nil
}

// backlogItem renders one work item as a board row.
func (r *azureReader) backlogItem(ctx context.Context, item *azureapi.WorkItem) BacklogItem {
	return BacklogItem{
		ID:          azureIdentifier(item.ID),
		Title:       item.Title,
		Status:      item.State,
		Group:       r.group(ctx, item),
		Labels:      item.Tags,
		Parent:      azureParentIdentifier(item.Parent),
		HasChildren: item.HasChildren(),
		Ready:       containsLabel(item.Tags, r.readyLabel),
	}
}

// group resolves the status group a work item files under: the repo's board-column
// mapping when it answers, and the categories the work item's own type declares
// when it does not (ADR 0036).
func (r *azureReader) group(ctx context.Context, item *azureapi.WorkItem) StatusGroup {
	if group, ok := r.boardStates.group(item.BoardColumn, item.State, item.Reason); ok {
		return group
	}
	return mapAzureGroup(r.states(ctx, item), r.statusTodo, item.State, item.Reason)
}

// states reads the states the work item's own type declares, so grouping follows
// the process the project actually runs rather than the state names this build
// happens to know. Metadata the token cannot read returns nothing, which leaves
// the name table in charge instead of failing the pull.
func (r *azureReader) states(ctx context.Context, item *azureapi.WorkItem) []azureapi.State {
	project := strings.TrimSpace(item.Project)
	if project == "" {
		project = strings.TrimSpace(r.project)
	}
	states, err := r.client.StateCategories(ctx, project, item.Type)
	if err != nil {
		logger.Debugf("azure: read %s states in %s: %v", item.Type, project, err)
		return nil
	}
	return states
}

// workItems resolves the reader's scope to full work items: the ids WIQL matches
// server-side, then the batched detail read those ids feed. Each step names itself
// in its error so a failed pull says which stage broke rather than only that the
// board is stale.
func (r *azureReader) workItems(ctx context.Context, project, since string) ([]azureapi.WorkItem, error) {
	ids, err := r.boardIDs(ctx, project, since)
	if err != nil {
		return nil, err
	}
	items, err := r.client.WorkItems(ctx, project, ids)
	if err != nil {
		return nil, fmt.Errorf("read %d work items: %w", len(ids), err)
	}
	return items, nil
}

// boardIDs runs the scoped WIQL query behind every read the reader makes.
func (r *azureReader) boardIDs(ctx context.Context, project, since string) ([]int, error) {
	scope, err := r.client.ResolveScope(ctx, project, r.areaPath, r.teams)
	if err != nil {
		return nil, fmt.Errorf("resolve board scope: %w", err)
	}
	ids, err := r.client.SyncIDs(ctx, project, scope, since)
	if err != nil {
		return nil, fmt.Errorf("query board: %w", err)
	}
	return ids, nil
}

// Issue reads one work item through the organization-scoped route rather than the
// project's own, so a ticket belonging to another team project comes back with
// InProject false instead of the 404 a project-scoped read would answer with. A
// ticket inside the project but outside the board's scope answers the same way: the
// hub mirrors only the scoped slice and the loop only picks from it, so confirming
// one would offer work neither would ever run (ADR 0028 §3).
func (r *azureReader) Issue(ctx context.Context, id string) (IssueSummary, error) {
	n, err := workItemID(id)
	if err != nil {
		return IssueSummary{}, ErrIssueNotFound
	}
	item, err := r.client.WorkItem(ctx, "", n)
	if err != nil {
		if errors.Is(err, azureapi.ErrNotFound) {
			return IssueSummary{}, ErrIssueNotFound
		}
		return IssueSummary{}, err
	}
	onBoard, err := r.onBoard(ctx, item)
	if err != nil {
		return IssueSummary{}, err
	}
	return IssueSummary{
		BacklogItem: r.backlogItem(ctx, item),
		Project:     item.Project,
		InProject:   onBoard,
	}, nil
}

// onBoard reports whether the work item sits in the slice of the board this repo
// mirrors. Team-project membership is the cheap half of the answer; the rest is the
// board's own to give, so a scoped repo asks it rather than matching the item's Area
// Path here. An unscoped repo mirrors the whole team project and has nothing to ask.
func (r *azureReader) onBoard(ctx context.Context, item *azureapi.WorkItem) (bool, error) {
	project := strings.TrimSpace(r.project)
	if !inProject(item.Project, project) {
		return false, nil
	}
	scoped := strings.TrimSpace(r.areaPath) != "" || len(r.teams) > 0
	if project == "" || !scoped {
		return true, nil
	}
	scope, err := r.client.ResolveScope(ctx, project, r.areaPath, r.teams)
	if err != nil {
		return false, fmt.Errorf("resolve board scope: %w", err)
	}
	return r.client.Covers(ctx, project, scope, item.ID)
}

// mapLinearGroup maps a Linear workflow-state type onto a normalized status
// group. Linear's state types are triage | backlog | unstarted | started |
// completed | canceled.
func mapLinearGroup(stateType string) StatusGroup {
	switch strings.ToLower(strings.TrimSpace(stateType)) {
	case "triage", "backlog":
		return StatusGroupBacklog
	case "unstarted":
		return StatusGroupUnstarted
	case "started":
		return StatusGroupStarted
	case "completed":
		return StatusGroupDone
	case "canceled":
		return StatusGroupCanceled
	default:
		return StatusGroupUnknown
	}
}

// mapJiraGroup maps a Jira statusCategory key onto a normalized status group.
// Jira has no backlog or canceled category, so a To-Do issue groups as unstarted
// and a done-category issue closed with a won't-do/duplicate resolution groups as
// canceled rather than done.
func mapJiraGroup(category, resolution string) StatusGroup {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "new":
		return StatusGroupUnstarted
	case "indeterminate":
		return StatusGroupStarted
	case "done":
		if isCanceledResolution(resolution) {
			return StatusGroupCanceled
		}
		return StatusGroupDone
	default:
		return StatusGroupUnknown
	}
}

// mapAzureGroup maps an Azure DevOps state onto a normalized status group,
// classified against the states its work-item type declares. Proposed covers a
// raw intake column and a groomed ready-to-pick one alike — the category draws no
// line between them — so the state a todo write targets is the one that reads
// back as unstarted and every other Proposed state is backlog. A process with a
// single Proposed state therefore groups exactly as it always did, and so does
// one whose states could not be read. A Completed item closed with a discarded
// reason groups as canceled rather than done, the same discriminator
// mapAzureStatus applies.
func mapAzureGroup(states []azureapi.State, pin, state, reason string) StatusGroup {
	switch azureapi.CategoryOf(states, state) {
	case azureapi.CategoryProposed:
		todo, ok := azureUnstartedState(states, pin)
		if ok && azureapi.CategoryOf(states, todo) == azureapi.CategoryProposed && !strings.EqualFold(todo, state) {
			return StatusGroupBacklog
		}
		return StatusGroupUnstarted
	case azureapi.CategoryInProgress, azureapi.CategoryResolved:
		return StatusGroupStarted
	case azureapi.CategoryCompleted:
		if isCanceledReason(reason) {
			return StatusGroupCanceled
		}
		return StatusGroupDone
	case azureapi.CategoryRemoved:
		return StatusGroupCanceled
	default:
		return StatusGroupUnknown
	}
}

// containsLabel reports whether labels carries want (case-insensitively), so the
// board can flag ready-labelled tickets. An empty want is never a match.
func containsLabel(labels []string, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	for _, l := range labels {
		if strings.EqualFold(strings.TrimSpace(l), want) {
			return true
		}
	}
	return false
}
